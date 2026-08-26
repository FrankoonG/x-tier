// Package xrayegress implements X-Tier's constrained terminal TCP handler.
// It is installed in Xray as a fixed forced-tag outbound and preserves TCP
// half-close, which the pinned freedom handler does not propagate.
package xrayegress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	featureoutbound "github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"
)

var (
	ErrNotRunning      = errors.New("xrayegress: handler is not running")
	ErrClosed          = errors.New("xrayegress: handler is closed")
	ErrExternalInbound = errors.New("xrayegress: direct inbound dispatch is forbidden")
	ErrInvalidTarget   = errors.New("xrayegress: invalid TCP target")
	ErrPeerMismatch    = errors.New("xrayegress: connected peer does not match frozen target")
	ErrNotReady        = errors.New("xrayegress: terminal connection is not ready")
)

var readyFrame = [...]byte{'X', 'T', 'E', 'G', 1, 0, 0, 0}

type Handler struct {
	tag  string
	dial func(context.Context, string, string) (net.Conn, error)

	mu         sync.Mutex
	state      handlerState
	runContext context.Context
	runCancel  context.CancelFunc
	closeDone  chan struct{}
	active     map[net.Conn]struct{}
	wg         sync.WaitGroup
}

type handlerState uint8

const (
	handlerCreated handlerState = iota
	handlerRunning
	handlerClosing
	handlerClosed
)

var _ featureoutbound.Handler = (*Handler)(nil)

func New(tag string) (*Handler, error) {
	if strings.TrimSpace(tag) == "" {
		return nil, errors.New("xrayegress: tag is required")
	}
	dialer := new(net.Dialer)
	return &Handler{
		tag:       tag,
		dial:      dialer.DialContext,
		closeDone: make(chan struct{}),
		active:    make(map[net.Conn]struct{}),
	}, nil
}

func (h *Handler) Tag() string                        { return h.tag }
func (*Handler) SenderSettings() *serial.TypedMessage { return nil }
func (*Handler) ProxySettings() *serial.TypedMessage  { return nil }

func (h *Handler) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == handlerClosing || h.state == handlerClosed {
		return ErrClosed
	}
	if h.state == handlerRunning {
		return nil
	}
	h.runContext, h.runCancel = context.WithCancel(context.Background())
	h.state = handlerRunning
	return nil
}

func (h *Handler) Close() error {
	h.mu.Lock()
	switch h.state {
	case handlerClosing, handlerClosed:
		done := h.closeDone
		h.mu.Unlock()
		<-done
		return nil
	}
	h.state = handlerClosing
	if h.runCancel != nil {
		h.runCancel()
	}
	connections := make([]net.Conn, 0, len(h.active))
	for conn := range h.active {
		connections = append(connections, conn)
	}
	done := h.closeDone
	h.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	h.wg.Wait()
	h.mu.Lock()
	h.state = handlerClosed
	close(done)
	h.mu.Unlock()
	return nil
}

func (h *Handler) Dispatch(ctx context.Context, link *transport.Link) {
	if ctx == nil {
		ctx = context.Background()
	}
	if link == nil || link.Reader == nil || link.Writer == nil {
		h.reject(ctx, link, ErrInvalidTarget)
		return
	}
	if session.InboundFromContext(ctx) != nil {
		h.reject(ctx, link, ErrExternalInbound)
		return
	}
	target, err := targetFromContext(ctx)
	if err != nil {
		h.reject(ctx, link, err)
		return
	}
	dispatchCtx, finish, err := h.beginDispatch()
	if err != nil {
		h.reject(ctx, link, err)
		return
	}
	defer finish()
	dialCtx, cancelDial := context.WithCancel(dispatchCtx)
	parentCancel := startJoinedContextCallback(ctx, cancelDial)
	defer func() {
		parentCancel.Stop()
		cancelDial()
	}()
	expected, err := targetAddrPort(target)
	if err != nil {
		h.reject(ctx, link, err)
		return
	}
	network := "tcp4"
	if expected.Addr().Is6() {
		network = "tcp6"
	}
	conn, err := h.dial(dialCtx, network, expected.String())
	if err != nil {
		h.reject(ctx, link, reportDialFailure(err))
		return
	}
	if !connectedPeerMatches(conn, expected) {
		_ = conn.Close()
		h.reject(ctx, link, ErrPeerMismatch)
		return
	}
	if err := h.track(conn); err != nil {
		_ = conn.Close()
		h.reject(ctx, link, err)
		return
	}
	defer h.untrack(conn)
	defer conn.Close()
	if err := commitDial(ctx, dispatchCtx, parentCancel); err != nil {
		h.reject(ctx, link, errors.Join(ErrNotReady, err))
		return
	}
	cancelDial()
	readyCancel := startJoinedContextCallback(dispatchCtx, func() {
		common.Interrupt(link.Writer)
	})
	err = writeReady(link)
	readyCancel.Stop()
	if err != nil {
		h.reject(ctx, link, fmt.Errorf("%w: %v", ErrNotReady, err))
		return
	}

	if err := copyHalfDuplex(dispatchCtx, link, conn); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
		session.SubmitOutboundErrorToOriginator(ctx, err)
	}
}

// ConfirmReady consumes the process-internal readiness frame emitted only
// after the fixed egress handler has connected and verified the OS peer. On
// failure it closes conn; ownership transfers to the caller only on success.
func ConfirmReady(ctx context.Context, conn net.Conn) (result error) {
	if conn == nil {
		return ErrNotReady
	}
	if ctx == nil {
		_ = conn.Close()
		return ErrNotReady
	}
	var closeOnce sync.Once
	closeConn := func() {
		closeOnce.Do(func() { _ = conn.Close() })
	}
	cancelRead := startJoinedContextCallback(ctx, func() {
		if err := conn.SetReadDeadline(time.Now()); err != nil {
			closeConn()
		}
	})
	defer func() {
		cancelRead.Stop()
		if result != nil {
			closeConn()
		}
	}()
	var frame [len(readyFrame)]byte
	if _, err := io.ReadFull(conn, frame[:]); err != nil {
		if !cancelRead.Stop() {
			return errors.Join(ErrNotReady, cancellationCause(ctx), err)
		}
		return errors.Join(ErrNotReady, err)
	}
	if !bytes.Equal(frame[:], readyFrame[:]) {
		if !cancelRead.Stop() {
			return errors.Join(ErrNotReady, cancellationCause(ctx))
		}
		return ErrNotReady
	}
	if !cancelRead.Stop() {
		return errors.Join(ErrNotReady, cancellationCause(ctx))
	}
	if cause := context.Cause(ctx); cause != nil {
		return errors.Join(ErrNotReady, cause)
	}
	return nil
}

func commitDial(parent, dispatch context.Context, parentCancel *joinedContextCallback) error {
	if cause := context.Cause(parent); cause != nil {
		parentCancel.Stop()
		return cause
	}
	if cause := context.Cause(dispatch); cause != nil {
		parentCancel.Stop()
		return cause
	}
	if !parentCancel.Stop() {
		return cancellationCause(parent)
	}
	if cause := context.Cause(dispatch); cause != nil {
		return cause
	}
	return context.Cause(parent)
}

type joinedContextCallback struct {
	stop    func() bool
	done    chan struct{}
	once    sync.Once
	stopped bool
}

func startJoinedContextCallback(ctx context.Context, callback func()) *joinedContextCallback {
	joined := &joinedContextCallback{done: make(chan struct{})}
	joined.stop = context.AfterFunc(ctx, func() {
		defer close(joined.done)
		callback()
	})
	return joined
}

func (c *joinedContextCallback) Stop() bool {
	if c == nil {
		return true
	}
	c.once.Do(func() {
		c.stopped = c.stop()
		if !c.stopped {
			<-c.done
		}
	})
	return c.stopped
}

func cancellationCause(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return context.Canceled
}

type redactedDialError struct {
}

func (*redactedDialError) Error() string { return "xrayegress: terminal dial failed" }

func reportDialFailure(err error) error {
	opaque := error(&redactedDialError{})
	switch {
	case errors.Is(err, context.Canceled):
		return errors.Join(opaque, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.Join(opaque, context.DeadlineExceeded)
	case errors.Is(err, net.ErrClosed):
		return errors.Join(opaque, net.ErrClosed)
	default:
		return opaque
	}
}

func writeReady(link *transport.Link) error {
	if link == nil || link.Writer == nil {
		return ErrInvalidTarget
	}
	frame := append([]byte(nil), readyFrame[:]...)
	return link.Writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(frame)})
}

func targetAddrPort(target xnet.Destination) (netip.AddrPort, error) {
	if target.Address == nil || target.Port == 0 {
		return netip.AddrPort{}, ErrInvalidTarget
	}
	address, ok := netip.AddrFromSlice(target.Address.IP())
	if !ok || address.Is4In6() {
		return netip.AddrPort{}, ErrInvalidTarget
	}
	return netip.AddrPortFrom(address, uint16(target.Port)), nil
}

func connectedPeerMatches(conn net.Conn, expected netip.AddrPort) bool {
	if conn == nil || !expected.IsValid() || conn.RemoteAddr() == nil {
		return false
	}
	if address, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return address.AddrPort() == expected
	}
	actual, err := netip.ParseAddrPort(conn.RemoteAddr().String())
	return err == nil && actual == expected
}

func (h *Handler) beginDispatch() (context.Context, func(), error) {
	h.mu.Lock()
	if h.state == handlerClosing || h.state == handlerClosed {
		h.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if h.state != handlerRunning {
		h.mu.Unlock()
		return nil, nil, ErrNotRunning
	}
	runContext := h.runContext
	h.wg.Add(1)
	h.mu.Unlock()

	ctx, cancel := context.WithCancel(runContext)
	return ctx, func() {
		cancel()
		h.wg.Done()
	}, nil

}

func (h *Handler) track(conn net.Conn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == handlerClosing || h.state == handlerClosed {
		return ErrClosed
	}
	if h.state != handlerRunning {
		return ErrNotRunning
	}
	h.active[conn] = struct{}{}
	return nil
}

func (h *Handler) untrack(conn net.Conn) {
	h.mu.Lock()
	delete(h.active, conn)
	h.mu.Unlock()
}

func (h *Handler) reject(ctx context.Context, link *transport.Link, err error) {
	session.SubmitOutboundErrorToOriginator(ctx, err)
	if link == nil {
		return
	}
	if link.Reader != nil {
		common.Interrupt(link.Reader)
	}
	if link.Writer != nil {
		_ = common.Close(link.Writer)
	}
}

func targetFromContext(ctx context.Context) (xnet.Destination, error) {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 || outbounds[len(outbounds)-1] == nil {
		return xnet.Destination{}, ErrInvalidTarget
	}
	target := outbounds[len(outbounds)-1].Target
	if !target.IsValid() || target.Network != xnet.Network_TCP || target.Port == 0 || target.Address == nil {
		return xnet.Destination{}, ErrInvalidTarget
	}
	address, err := canonicalIPLiteral(target.Address)
	if err != nil {
		return xnet.Destination{}, err
	}
	target.Address = address
	return target, nil
}

func canonicalIPLiteral(address xnet.Address) (xnet.Address, error) {
	family := address.Family()
	if !family.IsIP() {
		return nil, fmt.Errorf("%w: IP literal required", ErrInvalidTarget)
	}
	if strings.Contains(address.String(), "%") {
		return nil, fmt.Errorf("%w: scoped IP literals are forbidden", ErrInvalidTarget)
	}

	ip, ok := netip.AddrFromSlice(address.IP())
	if !ok || ip.Is4In6() {
		return nil, fmt.Errorf("%w: non-native IP representation", ErrInvalidTarget)
	}
	if (family.IsIPv4() && !ip.Is4()) || (family.IsIPv6() && !ip.Is6()) {
		return nil, fmt.Errorf("%w: IP family mismatch", ErrInvalidTarget)
	}
	if ip.IsUnspecified() || ip.IsMulticast() || (ip.Is6() && ip.IsLinkLocalUnicast()) || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return nil, fmt.Errorf("%w: non-dialable IP literal", ErrInvalidTarget)
	}

	return xnet.IPAddress(ip.AsSlice()), nil
}

func copyHalfDuplex(ctx context.Context, link *transport.Link, conn net.Conn) error {
	results := make(chan error, 2)
	go func() {
		err := buf.Copy(link.Reader, buf.NewWriter(conn))
		if err == nil {
			if closer, ok := conn.(interface{ CloseWrite() error }); ok {
				err = closer.CloseWrite()
			}
		}
		results <- err
	}()
	go func() {
		err := buf.Copy(buf.NewReader(conn), link.Writer)
		if err == nil {
			err = common.Close(link.Writer)
		}
		results <- err
	}()

	var result, cancelErr error
	for received := 0; received < 2; {
		if cancelErr != nil {
			err := <-results
			received++
			result = errors.Join(result, normalize(err))
			continue
		}
		select {
		case err := <-results:
			received++
			result = errors.Join(result, normalize(err))
			if err != nil {
				_ = conn.Close()
				common.Interrupt(link.Reader)
				_ = common.Close(link.Writer)
			}
		case <-ctx.Done():
			cancelErr = ctx.Err()
			_ = conn.Close()
			common.Interrupt(link.Reader)
			_ = common.Close(link.Writer)
		}
	}
	return errors.Join(result, cancelErr)
}

func normalize(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
