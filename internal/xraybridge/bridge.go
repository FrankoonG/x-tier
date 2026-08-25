// Package xraybridge adapts Xray outbound links to caller-owned stream
// runtimes. It owns neither an Xray instance nor the stream runtime behind the
// injected interfaces.
package xraybridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	featureoutbound "github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/transport"
)

var (
	ErrNotRunning           = errors.New("xraybridge: handler is not running")
	ErrClosed               = errors.New("xraybridge: handler is closed")
	ErrUnknownInbound       = errors.New("xraybridge: unknown inbound tag")
	ErrUnauthenticated      = errors.New("xraybridge: authenticated user is required")
	ErrInvalidTarget        = errors.New("xraybridge: invalid target")
	ErrUnsupportedUDP       = errors.New("xraybridge: UDP is unsupported")
	ErrInvalidLink          = errors.New("xraybridge: invalid transport link")
	ErrNilConnection        = errors.New("xraybridge: callback returned a nil connection")
	ErrInvalidRouteKind     = errors.New("xraybridge: invalid route kind")
	ErrHalfCloseUnsupported = errors.New("xraybridge: directional half-close is unsupported")
)

// RouteKind selects what an authenticated Xray inbound may do.
type RouteKind uint8

const (
	RouteCarrier RouteKind = iota + 1
	RouteUserEgress
)

// UserRequest is the authenticated request passed to the user egress.
type UserRequest struct {
	InboundTag string
	Username   string
	Target     xnet.Destination
}

// CarrierRequest identifies the VLESS account authenticated by Xray. The
// caller must authorize it against the currently applied peer configuration
// before handing the stream to its carrier runtime.
type CarrierRequest struct {
	InboundTag        string
	AuthenticatedUser string
}

// Carrier accepts an authenticated carrier stream. Handoff must not return
// until it has finished using conn, and it must honor ctx cancellation.
type Carrier interface {
	Handoff(ctx context.Context, request CarrierRequest, conn net.Conn) error
}

// UserEgress opens the caller-owned stream used for an authenticated user
// request. The returned connection is owned and closed by Handler.
type UserEgress interface {
	Dial(ctx context.Context, request UserRequest) (net.Conn, error)
}

// Config contains only routing and injected stream ownership boundaries.
type Config struct {
	Tag        string
	Routes     map[string]RouteKind
	Carrier    Carrier
	UserEgress UserEgress
}

// Handler is an Xray feature outbound handler. It is safe for concurrent use.
type Handler struct {
	tag        string
	routes     map[string]RouteKind
	carrier    Carrier
	userEgress UserEgress

	mu        sync.Mutex
	state     handlerState
	active    map[*activeDispatch]struct{}
	wg        sync.WaitGroup
	closeDone chan struct{}
}

type handlerState uint8

const (
	stateNew handlerState = iota
	stateRunning
	stateClosing
	stateClosed
)

type activeDispatch struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	closed bool
	conns  []net.Conn
}

var _ featureoutbound.Handler = (*Handler)(nil)

// New constructs a stopped handler. Xray's outbound manager calls Start when
// it installs the handler into an already-running core.
func New(config Config) (*Handler, error) {
	if strings.TrimSpace(config.Tag) == "" {
		return nil, errors.New("xraybridge: outbound tag is required")
	}
	if len(config.Routes) == 0 {
		return nil, errors.New("xraybridge: at least one inbound route is required")
	}

	routes := make(map[string]RouteKind, len(config.Routes))
	needsCarrier := false
	needsUserEgress := false
	for tag, kind := range config.Routes {
		if strings.TrimSpace(tag) == "" {
			return nil, errors.New("xraybridge: inbound route tag is required")
		}
		switch kind {
		case RouteCarrier:
			needsCarrier = true
		case RouteUserEgress:
			needsUserEgress = true
		default:
			return nil, fmt.Errorf("%w for inbound %q: %d", ErrInvalidRouteKind, tag, kind)
		}
		routes[tag] = kind
	}
	if needsCarrier && config.Carrier == nil {
		return nil, errors.New("xraybridge: carrier callback is required")
	}
	if needsUserEgress && config.UserEgress == nil {
		return nil, errors.New("xraybridge: user egress callback is required")
	}

	return &Handler{
		tag:        config.Tag,
		routes:     routes,
		carrier:    config.Carrier,
		userEgress: config.UserEgress,
		active:     make(map[*activeDispatch]struct{}),
		closeDone:  make(chan struct{}),
	}, nil
}

// Tag implements outbound.Handler.
func (h *Handler) Tag() string {
	return h.tag
}

// Start implements common.Runnable.
func (h *Handler) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch h.state {
	case stateNew:
		h.state = stateRunning
		return nil
	case stateRunning:
		return nil
	case stateClosing, stateClosed:
		return ErrClosed
	default:
		return ErrNotRunning
	}
}

// Close prevents new dispatches, cancels and closes every active stream, then
// waits for all callbacks and copy loops to return. Concurrent calls share the
// same completion barrier.
func (h *Handler) Close() error {
	h.mu.Lock()
	switch h.state {
	case stateClosed:
		h.mu.Unlock()
		return nil
	case stateClosing:
		done := h.closeDone
		h.mu.Unlock()
		<-done
		return nil
	case stateNew, stateRunning:
		h.state = stateClosing
		active := make([]*activeDispatch, 0, len(h.active))
		for dispatch := range h.active {
			active = append(active, dispatch)
		}
		h.mu.Unlock()

		for _, dispatch := range active {
			dispatch.stop()
		}
		h.wg.Wait()

		h.mu.Lock()
		h.state = stateClosed
		close(h.closeDone)
		h.mu.Unlock()
		return nil
	default:
		h.mu.Unlock()
		return ErrClosed
	}
}

// SenderSettings implements outbound.Handler. The bridge does not dial through
// Xray sender settings.
func (*Handler) SenderSettings() *serial.TypedMessage {
	return nil
}

// ProxySettings implements outbound.Handler. The bridge is installed directly
// rather than constructed from an Xray proxy protobuf.
func (*Handler) ProxySettings() *serial.TypedMessage {
	return nil
}

// Dispatch implements outbound.Handler. Every rejected request interrupts both
// sides of link and reports the reason to Xray's request originator.
func (h *Handler) Dispatch(ctx context.Context, link *transport.Link) {
	if ctx == nil {
		ctx = context.Background()
	}
	if link == nil || link.Reader == nil || link.Writer == nil {
		h.reject(ctx, link, ErrInvalidLink)
		return
	}

	route, request, err := h.requestFromContext(ctx)
	if err != nil {
		h.reject(ctx, link, err)
		return
	}

	rawLinkConn := cnc.NewConnection(
		cnc.ConnectionInputMulti(link.Writer),
		cnc.ConnectionOutputMulti(link.Reader),
	)
	linkConn := newDirectionalLinkConn(rawLinkConn, link)
	dispatchCtx, dispatch, stopWatch, err := h.begin(ctx, linkConn)
	if err != nil {
		_ = linkConn.Close()
		h.reject(ctx, link, err)
		return
	}
	defer h.finish(dispatch, stopWatch)

	switch route {
	case RouteCarrier:
		err = h.carrier.Handoff(dispatchCtx, CarrierRequest{
			InboundTag: request.InboundTag, AuthenticatedUser: request.Username,
		}, linkConn)
	case RouteUserEgress:
		err = h.dispatchUser(dispatchCtx, dispatch, linkConn, request)
	default:
		err = ErrInvalidRouteKind
	}
	if err != nil && !expectedTermination(dispatchCtx, err) {
		h.reject(ctx, link, err)
	}
}

func (h *Handler) requestFromContext(ctx context.Context) (RouteKind, UserRequest, error) {
	inbound := session.InboundFromContext(ctx)
	if inbound == nil {
		return 0, UserRequest{}, ErrUnknownInbound
	}
	route, ok := h.routes[inbound.Tag]
	if !ok {
		return 0, UserRequest{}, fmt.Errorf("%w: %q", ErrUnknownInbound, inbound.Tag)
	}
	if inbound.User == nil || strings.TrimSpace(inbound.User.Email) == "" {
		return 0, UserRequest{}, ErrUnauthenticated
	}

	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 || outbounds[len(outbounds)-1] == nil {
		return 0, UserRequest{}, ErrInvalidTarget
	}
	target := outbounds[len(outbounds)-1].Target
	if !target.IsValid() {
		return 0, UserRequest{}, ErrInvalidTarget
	}
	if target.Network == xnet.Network_UDP {
		return 0, UserRequest{}, ErrUnsupportedUDP
	}
	if target.Network != xnet.Network_TCP {
		return 0, UserRequest{}, fmt.Errorf("%w: network %s", ErrInvalidTarget, target.Network)
	}

	return route, UserRequest{
		InboundTag: inbound.Tag,
		Username:   inbound.User.Email,
		Target:     target,
	}, nil
}

func (h *Handler) begin(parent context.Context, linkConn net.Conn) (context.Context, *activeDispatch, func() bool, error) {
	ctx, cancel := context.WithCancel(parent)
	dispatch := &activeDispatch{
		cancel: cancel,
		conns:  []net.Conn{linkConn},
	}

	h.mu.Lock()
	switch h.state {
	case stateRunning:
		h.active[dispatch] = struct{}{}
		h.wg.Add(1)
		h.mu.Unlock()
		stopWatch := context.AfterFunc(ctx, dispatch.closeConnections)
		return ctx, dispatch, stopWatch, nil
	case stateClosing, stateClosed:
		h.mu.Unlock()
		dispatch.stop()
		return nil, nil, nil, ErrClosed
	default:
		h.mu.Unlock()
		dispatch.stop()
		return nil, nil, nil, ErrNotRunning
	}
}

func (h *Handler) finish(dispatch *activeDispatch, stopWatch func() bool) {
	if stopWatch != nil {
		stopWatch()
	}
	dispatch.stop()

	h.mu.Lock()
	delete(h.active, dispatch)
	h.mu.Unlock()
	h.wg.Done()
}

func (h *Handler) dispatchUser(ctx context.Context, dispatch *activeDispatch, linkConn net.Conn, request UserRequest) error {
	peerConn, err := h.userEgress.Dial(ctx, request)
	if err != nil {
		return fmt.Errorf("xraybridge: dial user egress: %w", err)
	}
	if peerConn == nil {
		return ErrNilConnection
	}
	if err := dispatch.addConnection(peerConn); err != nil {
		return err
	}
	return copyBidirectional(ctx, linkConn, peerConn)
}

func (h *Handler) reject(ctx context.Context, link *transport.Link, err error) {
	session.SubmitOutboundErrorToOriginator(ctx, err)
	interruptLink(link)
}

func interruptLink(link *transport.Link) {
	if link == nil {
		return
	}
	if link.Reader != nil {
		_ = common.Interrupt(link.Reader)
	}
	if link.Writer != nil {
		_ = common.Interrupt(link.Writer)
	}
}

// directionalLinkConn preserves Xray's directional stream lifecycle. The
// public cnc.Connection erases CloseWrite/CloseRead even though transport.Link
// exposes the two directions independently.
type directionalLinkConn struct {
	net.Conn
	link      *transport.Link
	writeOnce sync.Once
	readOnce  sync.Once
	writeErr  error
}

func newDirectionalLinkConn(conn net.Conn, link *transport.Link) *directionalLinkConn {
	return &directionalLinkConn{Conn: conn, link: link}
}

func (c *directionalLinkConn) CloseWrite() error {
	c.writeOnce.Do(func() {
		if handled, err := closeXrayLinkWriter(c.link.Writer); handled {
			c.writeErr = err
			return
		}
		// Some pinned Xray inbounds wrap the socket in a buf.Writer that
		// erases Close. A full link close is the only bounded fallback; a
		// successful no-op would strand the peer waiting for a FIN forever.
		// Keep the fallback observable so a future Xray writer shape cannot
		// turn a truncated stream into a reported clean EOF.
		c.writeErr = errors.Join(ErrHalfCloseUnsupported, c.Conn.Close())
	})
	return c.writeErr
}

func closeXrayLinkWriter(writer buf.Writer) (bool, error) {
	switch current := writer.(type) {
	case *dispatcher.SizeStatWriter:
		return closeXrayLinkWriter(current.Writer)
	case *buf.BufferToBytesWriter:
		return closeXrayIOWriter(current.Writer)
	case *buf.SequentialWriter:
		return closeXrayIOWriter(current.Writer)
	case interface{ CloseWrite() error }:
		return true, current.CloseWrite()
	case io.Closer:
		return true, current.Close()
	default:
		return false, nil
	}
}

func closeXrayIOWriter(writer io.Writer) (bool, error) {
	if closer, ok := writer.(interface{ CloseWrite() error }); ok {
		return true, closer.CloseWrite()
	}
	if closer, ok := writer.(io.Closer); ok {
		return true, closer.Close()
	}
	return false, nil
}

func (c *directionalLinkConn) CloseRead() error {
	c.readOnce.Do(func() { common.Interrupt(c.link.Reader) })
	return nil
}

func (d *activeDispatch) addConnection(conn net.Conn) error {
	if conn == nil {
		return ErrNilConnection
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = conn.Close()
		return ErrClosed
	}
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return nil
}

func (d *activeDispatch) stop() {
	d.cancel()
	d.closeConnections()
}

func (d *activeDispatch) closeConnections() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	conns := append([]net.Conn(nil), d.conns...)
	d.conns = nil
	d.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

func copyBidirectional(ctx context.Context, left, right net.Conn) error {
	results := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if closeWriter, ok := dst.(interface{ CloseWrite() error }); ok {
				err = closeWriter.CloseWrite()
			}
		}
		results <- err
	}

	go copyOne(right, left)
	go copyOne(left, right)

	first := <-results
	if first != nil {
		_ = left.Close()
		_ = right.Close()
	}
	second := <-results

	if ctx.Err() != nil {
		return ctx.Err()
	}
	first = normalizeCopyError(first)
	second = normalizeCopyError(second)
	return errors.Join(first, second)
}

func normalizeCopyError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrClosedPipe), errors.Is(err, net.ErrClosed):
		return nil
	default:
		return err
	}
}

func expectedTermination(ctx context.Context, err error) bool {
	return ctx.Err() != nil || normalizeCopyError(err) == nil
}
