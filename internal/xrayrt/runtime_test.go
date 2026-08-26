package xrayrt

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	addresses := reserveDistinctRuntimeAddresses(t, 4)
	oldA, oldB, newA, newB := addresses[0], addresses[1], addresses[2], addresses[3]
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
	if tags := runtime.ManagedInboundTags(); len(tags) != 2 || tags[0] != "old-a" || tags[1] != "old-b" {
		t.Fatalf("restored handler tags = %v, want [old-a old-b]", tags)
	}
	equivalent, err := EquivalentInbounds(runtime.inboundConfigs, oldConfigs)
	if err != nil || !equivalent {
		t.Fatalf("restored inbound configs equivalent=%v err=%v", equivalent, err)
	}
	expectedWire, err := canonicalInboundWire(oldConfigs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(runtime.inboundWire, expectedWire) {
		t.Fatal("restored inbound wire differs from the complete old set")
	}
	assertRuntimeSOCKSState(t, oldA, true)
	assertRuntimeSOCKSState(t, oldB, true)
	assertRuntimeSOCKSState(t, newA, false)
	assertRuntimeSOCKSState(t, newB, false)
}

func TestReplaceInboundsRestoreFailureFailStopsAndCanRecover(t *testing.T) {
	addresses := reserveDistinctRuntimeAddresses(t, 2)
	oldAddress, newAddress := addresses[0], addresses[1]
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
	assertRuntimeSOCKSState(t, oldAddress, false)
	assertRuntimeSOCKSState(t, newAddress, false)

	if err := runtime.ReplaceInbounds(context.Background(), newConfigs); err != nil {
		t.Fatalf("recover fail-stopped runtime: %v", err)
	}
	assertRuntimeSOCKSState(t, oldAddress, false)
	assertRuntimeSOCKSState(t, newAddress, true)
}

func TestIdenticalInboundReloadRepairsMissingHandler(t *testing.T) {
	address := reserveDistinctRuntimeAddresses(t, 1)[0]
	configs := []*core.InboundHandlerConfig{testSOCKSInbound("managed", address)}
	runtime, err := StartRuntime(context.Background(), StartOptions{Inbounds: configs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	assertRuntimeSOCKSState(t, address, true)

	if err := runtime.inbound.RemoveHandler(context.Background(), "managed"); err != nil {
		t.Fatal(err)
	}
	if tags := runtime.ManagedInboundTags(); len(tags) != 0 {
		t.Fatalf("removed handler remained observable: %v", tags)
	}
	assertRuntimeSOCKSState(t, address, false)

	if err := runtime.ReplaceInbounds(context.Background(), configs); err != nil {
		t.Fatal(err)
	}
	if tags := runtime.ManagedInboundTags(); len(tags) != 1 || tags[0] != "managed" {
		t.Fatalf("repaired handler tags = %v", tags)
	}
	assertRuntimeSOCKSState(t, address, true)
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

func reserveDistinctRuntimeAddresses(t *testing.T, count int) []string {
	t.Helper()
	if count <= 0 {
		t.Fatalf("reservation count = %d, want positive", count)
	}
	listeners := make([]net.Listener, 0, count)
	addresses := make([]string, 0, count)
	for range count {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		addresses = append(addresses, listener.Addr().String())
	}
	var closeErr error
	for _, listener := range listeners {
		closeErr = errors.Join(closeErr, listener.Close())
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return addresses
}

func assertRuntimeSOCKSState(t *testing.T, address string, wantOpen bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for {
		open, err := probeRuntimeSOCKS(address)
		lastErr = err
		if open == wantOpen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("SOCKS inbound %s open=%v, want %v (last error %v)", address, open, wantOpen, lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func probeRuntimeSOCKS(address string) (bool, error) {
	conn, err := net.DialTimeout("tcp4", address, 50*time.Millisecond)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		return false, err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return false, err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return false, err
	}
	return response[0] == 0x05 && response[1] == 0x00, nil
}
