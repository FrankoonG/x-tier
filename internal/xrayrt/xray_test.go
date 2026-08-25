package xrayrt

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/proxyman"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	xlog "github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/blackhole"
	"github.com/xtls/xray-core/proxy/freedom"
)

func TestWithForcedOutboundTagPreservesCallerContextWithoutMutation(t *testing.T) {
	type contextKey struct{}
	callerContent := &session.Content{
		Protocol:   "test-protocol",
		Attributes: map[string]string{"existing": "value", "forcedOutboundTag": "old"},
	}
	callerOutbound := &session.Outbound{Tag: "caller-outbound"}
	callerAccess := &xlog.AccessMessage{Detour: "caller-detour", Email: "caller@example.test"}
	caller := session.ContextWithContent(context.WithValue(context.Background(), contextKey{}, "value"), callerContent)
	caller = session.ContextWithOutbounds(caller, []*session.Outbound{callerOutbound})
	caller = xlog.ContextWithAccessMessage(caller, callerAccess)

	forced := withForcedOutboundTag(caller, "gen-2/edge")
	if got := forced.Value(contextKey{}); got != "value" {
		t.Fatalf("context value = %v, want value", got)
	}
	gotContent := session.ContentFromContext(forced)
	if gotContent == callerContent {
		t.Fatal("forced context reused caller Content pointer")
	}
	if gotContent.Protocol != "test-protocol" || gotContent.Attribute("existing") != "value" {
		t.Fatalf("forced content did not preserve metadata: %+v", gotContent)
	}
	if got := session.GetForcedOutboundTagFromContext(forced); got != "gen-2/edge" {
		t.Fatalf("forced outbound tag = %q, want gen-2/edge", got)
	}
	if got := session.GetForcedOutboundTagFromContext(caller); got != "old" {
		t.Fatalf("caller outbound tag mutated to %q", got)
	}
	forcedOutbounds := session.OutboundsFromContext(forced)
	if len(forcedOutbounds) != 1 || forcedOutbounds[0] == callerOutbound || forcedOutbounds[0].Tag != "" {
		t.Fatalf("forced outbound metadata was not isolated: %+v", forcedOutbounds)
	}
	forcedAccess := xlog.AccessMessageFromContext(forced)
	if forcedAccess == nil || forcedAccess == callerAccess || forcedAccess.Detour != "" || forcedAccess.Email != callerAccess.Email {
		t.Fatalf("forced access message was not isolated: %+v", forcedAccess)
	}
	forcedOutbounds[0].Tag = "nested"
	forcedAccess.Detour = "nested"
	if callerOutbound.Tag != "caller-outbound" || callerAccess.Detour != "caller-detour" {
		t.Fatalf("nested metadata mutated caller: outbound=%+v access=%+v", callerOutbound, callerAccess)
	}
}

func TestXrayStreamDialerRejectsNilInstanceAndPropagatesContextError(t *testing.T) {
	dial := NewXrayStreamDialer(nil)
	if _, err := dial(context.Background(), "gen-1/edge", "tcp", "example.test:443"); err == nil {
		t.Fatal("nil instance dial succeeded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dial(ctx, "gen-1/edge", "tcp", "example.test:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dial error = %v", err)
	}
}

func TestXrayStreamDialerRejectsEmptyTagAndPacketNetwork(t *testing.T) {
	instance := newRunningTestInstance(t)
	dial := NewXrayStreamDialer(instance)
	if _, err := dial(context.Background(), "", "tcp", "example.test:443"); err == nil {
		t.Fatal("empty forced tag was accepted")
	}
	if _, err := dial(context.Background(), "gen-1/edge", "udp", "example.test:53"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("UDP stream dial error = %v, want ErrUnsupported", err)
	}
}

func TestXrayStreamDialerUsesForcedOutboundOnSingleInstance(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_, err = io.Copy(conn, conn)
		serverDone <- err
	}()

	instance, err := core.New(&core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				Tag:           "blocked-default",
				ProxySettings: serial.ToTypedMessage(&blackhole.Config{}),
			},
			{
				Tag: "gen-7/direct",
				ProxySettings: serial.ToTypedMessage(&freedom.Config{
					FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}},
				}),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := NewXrayStreamDialer(instance)(ctx, "gen-7/direct", "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("forced outbound dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}

	want := []byte("x-tier single-instance stream")
	if _, err := conn.Write(want); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("roundtrip payload = %q, want %q", got, want)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
