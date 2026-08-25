package xrayrt

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	featureinbound "github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/proxy/socks"
)

func TestRuntimeStartsBlockedAndOwnsGenerationManager(t *testing.T) {
	runtime, err := StartRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status := runtime.Status()
	if status.Closed || status.Current != nil || len(status.Draining) != 0 {
		t.Fatalf("initial runtime status = %+v", status)
	}
	if _, err := runtime.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	status = runtime.Status()
	if status.Current == nil || status.Current.Generation != 1 {
		t.Fatalf("applied runtime status = %+v", status)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := runtime.Apply(context.Background(), testGenerationConfig(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("apply after close error = %v", err)
	}
}

func TestStartRuntimeRejectsInvalidContext(t *testing.T) {
	if _, err := StartRuntime(nil); err == nil {
		t.Fatal("nil context was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StartRuntime(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}

func TestEquivalentInboundsUsesReplacementCanonicalForm(t *testing.T) {
	left := []*core.InboundHandlerConfig{testSOCKSInbound("managed", "127.0.0.1:1080")}
	equal, err := EquivalentInbounds(left, cloneInboundConfigs(left))
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("cloned inbound set was not equivalent")
	}
	different := []*core.InboundHandlerConfig{testSOCKSInbound("managed", "127.0.0.1:1081")}
	equal, err = EquivalentInbounds(left, different)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("different listener address was considered equivalent")
	}
}

func TestRuntimeDoesNotCloseCoreWhileBuildIgnoresCancellation(t *testing.T) {
	instance := newRunningTestInstance(t)
	buildStarted := make(chan struct{})
	buildRelease := make(chan struct{})
	backend := &fakeBackend{build: func(context.Context, uint64, GenerationConfig) (Generation, error) {
		close(buildStarted)
		<-buildRelease
		return newFakeGeneration(1), nil
	}}
	runtime := &Runtime{instance: instance, manager: NewManager(backend, nil)}
	applyDone := make(chan error, 1)
	go func() {
		_, err := runtime.Apply(context.Background(), testGenerationConfig(t))
		applyDone <- err
	}()
	<-buildStarted

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := runtime.CloseContext(ctx); !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("CloseContext = %v, want ErrShutdownIncomplete", err)
	}
	if !instance.IsRunning() {
		t.Fatal("runtime closed Xray core while generation build was still active")
	}
	close(buildRelease)
	if err := <-applyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply error = %v, want context cancellation", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if instance.IsRunning() {
		t.Fatal("runtime remained running after quiescent retry")
	}
}

func TestRuntimeForceCloseStopsCoreAfterPersistentGenerationCleanupFailure(t *testing.T) {
	instance := newRunningTestInstance(t)
	backend := newFakeBackend()
	backend.remove = func(Generation) error {
		return errors.New("persistent generation cleanup failure")
	}
	runtime := &Runtime{instance: instance, manager: NewManager(backend, nil)}
	if _, err := runtime.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err := runtime.CloseContext(ctx)
	cancel()
	if !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("graceful CloseContext = %v, want ErrShutdownIncomplete", err)
	}
	if !instance.IsRunning() {
		t.Fatal("graceful cleanup failure closed the Xray core")
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	err = runtime.ForceCloseContext(ctx)
	cancel()
	if !errors.Is(err, ErrShutdownForced) {
		t.Fatalf("ForceCloseContext = %v, want ErrShutdownForced", err)
	}
	if instance.IsRunning() {
		t.Fatal("forced shutdown left the Xray core running")
	}
	if err := runtime.ForceClose(); !errors.Is(err, ErrShutdownForced) {
		t.Fatalf("second ForceClose = %v, want retained forced result", err)
	}
}

func TestRuntimeCloseContextDeadlineIncludesCloseGateWait(t *testing.T) {
	removeStarted := make(chan struct{})
	removeRelease := make(chan struct{})
	backend := newFakeBackend()
	backend.remove = func(Generation) error {
		close(removeStarted)
		<-removeRelease
		return nil
	}
	runtime := &Runtime{manager: NewManager(backend, nil)}
	if _, err := runtime.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- runtime.Close() }()
	<-removeStarted

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := runtime.CloseContext(ctx); !errors.Is(err, ErrShutdownIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext = %v, want incomplete deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline gate wait took %s", elapsed)
	}
	close(removeRelease)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceInboundsPartialRemovalRestoresCompleteOldSet(t *testing.T) {
	oldA := reserveRuntimeAddress(t)
	oldB := reserveRuntimeAddress(t)
	newA := reserveRuntimeAddress(t)
	newB := reserveRuntimeAddress(t)
	oldConfigs := []*core.InboundHandlerConfig{
		testSOCKSInbound("old-a", oldA),
		testSOCKSInbound("old-b", oldB),
	}
	runtime, err := StartRuntime(context.Background(), StartOptions{Inbounds: oldConfigs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	faults := &faultInboundManager{Manager: runtime.inbound, failRemoveTag: "old-b"}
	runtime.inbound = faults

	err = runtime.ReplaceInbounds(context.Background(), []*core.InboundHandlerConfig{
		testSOCKSInbound("old-a", newA),
		testSOCKSInbound("old-b", newB),
	})
	if err == nil || errors.Is(err, ErrInboundFailStopped) {
		t.Fatalf("ReplaceInbounds = %v, want recoverable removal failure", err)
	}
	assertRuntimePortState(t, oldA, true)
	assertRuntimePortState(t, oldB, true)
	assertRuntimePortState(t, newA, false)
	assertRuntimePortState(t, newB, false)
}

func TestReplaceInboundsRestoreFailureFailStopsAndCanRecover(t *testing.T) {
	oldAddress := reserveRuntimeAddress(t)
	newAddress := reserveRuntimeAddress(t)
	oldConfigs := []*core.InboundHandlerConfig{testSOCKSInbound("managed", oldAddress)}
	runtime, err := StartRuntime(context.Background(), StartOptions{Inbounds: oldConfigs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	faults := &faultInboundManager{Manager: runtime.inbound, failAdds: 2}
	runtime.inbound = faults
	newConfigs := []*core.InboundHandlerConfig{testSOCKSInbound("managed", newAddress)}

	err = runtime.ReplaceInbounds(context.Background(), newConfigs)
	if !errors.Is(err, ErrInboundFailStopped) {
		t.Fatalf("ReplaceInbounds = %v, want ErrInboundFailStopped", err)
	}
	if len(runtime.inboundConfigs) != 0 || len(runtime.inboundWire) != 0 {
		t.Fatalf("fail-stopped runtime retained authoritative inbounds: configs=%d wire=%d", len(runtime.inboundConfigs), len(runtime.inboundWire))
	}
	assertRuntimePortState(t, oldAddress, false)
	assertRuntimePortState(t, newAddress, false)

	if err := runtime.ReplaceInbounds(context.Background(), newConfigs); err != nil {
		t.Fatalf("recover fail-stopped runtime: %v", err)
	}
	assertRuntimePortState(t, oldAddress, false)
	assertRuntimePortState(t, newAddress, true)
}

func TestIdenticalInboundReloadRepairsMissingHandler(t *testing.T) {
	address := reserveRuntimeAddress(t)
	configs := []*core.InboundHandlerConfig{testSOCKSInbound("managed", address)}
	runtime, err := StartRuntime(context.Background(), StartOptions{Inbounds: configs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	assertRuntimePortState(t, address, true)

	if err := runtime.inbound.RemoveHandler(context.Background(), "managed"); err != nil {
		t.Fatal(err)
	}
	if tags := runtime.ManagedInboundTags(); len(tags) != 0 {
		t.Fatalf("removed handler remained observable: %v", tags)
	}
	assertRuntimePortState(t, address, false)

	if err := runtime.ReplaceInbounds(context.Background(), configs); err != nil {
		t.Fatal(err)
	}
	if tags := runtime.ManagedInboundTags(); len(tags) != 1 || tags[0] != "managed" {
		t.Fatalf("repaired handler tags = %v", tags)
	}
	assertRuntimePortState(t, address, true)
}

type faultInboundManager struct {
	featureinbound.Manager
	mu            sync.Mutex
	failRemoveTag string
	removeFailed  bool
	failAdds      int
}

func (m *faultInboundManager) RemoveHandler(ctx context.Context, tag string) error {
	m.mu.Lock()
	if tag == m.failRemoveTag && !m.removeFailed {
		m.removeFailed = true
		m.mu.Unlock()
		return errors.New("injected inbound removal failure")
	}
	m.mu.Unlock()
	return m.Manager.RemoveHandler(ctx, tag)
}

func (m *faultInboundManager) AddHandler(ctx context.Context, handler featureinbound.Handler) error {
	m.mu.Lock()
	if m.failAdds > 0 {
		m.failAdds--
		m.mu.Unlock()
		return errors.New("injected inbound installation failure")
	}
	m.mu.Unlock()
	return m.Manager.AddHandler(ctx, handler)
}

func testSOCKSInbound(tag, address string) *core.InboundHandlerConfig {
	host, portText, _ := net.SplitHostPort(address)
	port, _ := net.LookupPort("tcp", portText)
	return &core.InboundHandlerConfig{
		Tag: tag,
		ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
			PortList: &xnet.PortList{Range: []*xnet.PortRange{xnet.SinglePortRange(xnet.Port(port))}},
			Listen:   xnet.NewIPOrDomain(xnet.IPAddress(net.ParseIP(host))),
		}),
		ProxySettings: serial.ToTypedMessage(&socks.ServerConfig{AuthType: socks.AuthType_NO_AUTH}),
	}
}

func reserveRuntimeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func assertRuntimePortState(t *testing.T, address string, wantOpen bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
		}
		if (err == nil) == wantOpen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %s open=%v, want %v (last error %v)", address, err == nil, wantOpen, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
