package xrayrt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/xtls/xray-core/common"
	xlog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

// NewXrayStreamDialer binds stream dialing to one long-lived Xray instance.
// The manager supplies a generation-qualified outbound tag for every call.
func NewXrayStreamDialer(instance *core.Instance) StreamDialer {
	return func(ctx context.Context, outboundTag, network, address string) (net.Conn, error) {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if err := validateForcedOutboundTag(outboundTag); err != nil {
			return nil, err
		}
		if network != "tcp" {
			return nil, ErrUnsupported
		}
		if instance == nil {
			return nil, errors.New("xrayrt: nil Xray instance")
		}
		if !instance.IsRunning() {
			return nil, ErrClosed
		}
		destination, err := xnet.ParseDestination(network + ":" + address)
		if err != nil {
			return nil, fmt.Errorf("xrayrt: parse destination: %w", err)
		}
		return dialDirectional(withForcedOutboundTag(ctx, outboundTag), instance, destination)
	}
}

// dialDirectional is the core.Dial equivalent that retains the two pipe ends.
// Xray's public core.Dial erases them behind net.Conn, which makes TCP
// half-close impossible. The context key is isolated here and guarded by
// core.FromContext so a future Xray change fails closed in conformance tests.
func dialDirectional(ctx context.Context, instance *core.Instance, destination xnet.Destination) (net.Conn, error) {
	if core.FromContext(ctx) != instance {
		ctx = context.WithValue(ctx, core.XrayKey(1), instance)
		if core.FromContext(ctx) != instance {
			return nil, errors.New("xrayrt: Xray instance context is unavailable")
		}
	}
	feature := instance.GetFeature(routing.DispatcherType())
	dispatcher, ok := feature.(routing.Dispatcher)
	if !ok || dispatcher == nil {
		return nil, errors.New("xrayrt: routing dispatcher is not registered")
	}
	link, err := dispatcher.Dispatch(ctx, destination)
	if err != nil {
		return nil, err
	}
	conn := cnc.NewConnection(
		cnc.ConnectionInputMulti(link.Writer),
		cnc.ConnectionOutputMulti(link.Reader),
	)
	return &directionalConn{Conn: conn, link: link}, nil
}

type directionalConn struct {
	net.Conn
	link      *transport.Link
	writeOnce sync.Once
	readOnce  sync.Once
	writeErr  error
}

func (c *directionalConn) CloseWrite() error {
	c.writeOnce.Do(func() { c.writeErr = common.Close(c.link.Writer) })
	return c.writeErr
}

func (c *directionalConn) CloseRead() error {
	c.readOnce.Do(func() { common.Interrupt(c.link.Reader) })
	return nil
}

func validateForcedOutboundTag(tag string) error {
	if tag == "" || len(tag) > 256 || strings.TrimSpace(tag) != tag {
		return errors.New("xrayrt: forced outbound tag is invalid")
	}
	for index := range tag {
		character := tag[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' ||
			character == '.' || character == ':' || character == '@' || character == '/' {
			continue
		}
		return fmt.Errorf("xrayrt: forced outbound tag contains invalid byte at %d", index)
	}
	return nil
}

func withForcedOutboundTag(ctx context.Context, outboundTag string) context.Context {
	// Dispatcher mutates outbound metadata and AccessMessage.Detour. A bridge
	// dial may inherit both from the inbound handler, whose logger can still be
	// reading them, so every nested dispatch needs independent mutable state.
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
	if existing := xlog.AccessMessageFromContext(ctx); existing != nil {
		access := *existing
		access.Detour = ""
		ctx = xlog.ContextWithAccessMessage(ctx, &access)
	}
	content := &session.Content{}
	if existing := session.ContentFromContext(ctx); existing != nil {
		*content = *existing
		if existing.Attributes != nil {
			content.Attributes = make(map[string]string, len(existing.Attributes)+1)
			for key, value := range existing.Attributes {
				content.Attributes[key] = value
			}
		}
	}
	ctx = session.ContextWithContent(ctx, content)
	return session.SetForcedOutboundTagToContext(ctx, outboundTag)
}
