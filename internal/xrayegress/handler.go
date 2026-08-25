// Package xrayegress implements X-Tier's constrained terminal TCP handler.
// It is installed in Xray as a fixed forced-tag outbound and preserves TCP
// half-close, which the pinned freedom handler does not propagate.
package xrayegress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

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
)

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
	dispatchCtx, finish, err := h.beginDispatch(ctx)
	if err != nil {
		h.reject(ctx, link, err)
		return
	}
	defer finish()
	conn, err := h.dial(dispatchCtx, "tcp", target.NetAddr())
	if err != nil {
		h.reject(ctx, link, fmt.Errorf("xrayegress: dial %s: %w", target, err))
		return
	}
	if err := h.track(conn); err != nil {
		_ = conn.Close()
		h.reject(ctx, link, err)
		return
	}
	defer h.untrack(conn)
	defer conn.Close()

	if err := copyHalfDuplex(dispatchCtx, link, conn); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
		session.SubmitOutboundErrorToOriginator(ctx, err)
	}
}

func (h *Handler) beginDispatch(parent context.Context) (context.Context, func(), error) {
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

	ctx, cancel := context.WithCancel(parent)
	stopRunCancel := context.AfterFunc(runContext, cancel)
	return ctx, func() {
		stopRunCancel()
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
	if !target.IsValid() || target.Network != xnet.Network_TCP {
		return xnet.Destination{}, ErrInvalidTarget
	}
	return target, nil
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
