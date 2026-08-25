package xrayrt

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
)

func TestManagerFreedomOutboundOnSingleLongLivedInstance(t *testing.T) {
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

	instance := newRunningTestInstance(t)
	manager, err := NewInstanceManager(instance)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	config, err := NewGenerationConfig([]*core.OutboundHandlerConfig{freedomOutbound("direct", "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(ctx, config); err != nil {
		t.Fatalf("install generation: %v", err)
	}
	conn, err := manager.Dial(ctx, "direct", "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial freedom outbound: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	want := []byte("x-tier freedom roundtrip")
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
