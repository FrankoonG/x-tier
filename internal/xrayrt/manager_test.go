package xrayrt

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/freedom"
)

func TestApplyFailureKeepsCurrentGeneration(t *testing.T) {
	buildErr := errors.New("invalid config")
	backend := &fakeBackend{build: func(_ context.Context, id uint64, _ GenerationConfig) (Generation, error) {
		handle := newFakeGeneration(id)
		if id == 2 {
			return handle, buildErr
		}
		return handle, nil
	}}
	manager := NewManager(backend, nil)
	t.Cleanup(func() { _ = manager.Close() })
	config := testGenerationConfig(t)

	if id, err := manager.Apply(context.Background(), config); err != nil || id != 1 {
		t.Fatalf("first apply = (%d, %v), want (1, nil)", id, err)
	}
	if _, err := manager.Apply(context.Background(), config); !errors.Is(err, buildErr) {
		t.Fatalf("failed apply error = %v, want %v", err, buildErr)
	}

	assertCurrent(t, manager.Status(), 1, 0)
	if got := backend.removedIDs(); !reflect.DeepEqual(got, []uint64{2}) {
		t.Fatalf("removed generations = %v, want [2]", got)
	}
	if id, err := manager.Apply(context.Background(), config); err != nil || id != 3 {
		t.Fatalf("retry apply = (%d, %v), want (3, nil)", id, err)
	}
}

func TestApplyDrainsOldGenerationUntilLeaseCloses(t *testing.T) {
	backend := newFakeBackend()
	var gotTags []string
	var peers []net.Conn
	manager := NewManager(backend, func(_ context.Context, tag, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "example.test:443" {
			t.Fatalf("dial destination = %s %s", network, address)
		}
		gotTags = append(gotTags, tag)
		client, peer := net.Pipe()
		peers = append(peers, peer)
		return client, nil
	})
	t.Cleanup(func() {
		for _, peer := range peers {
			_ = peer.Close()
		}
		_ = manager.Close()
	})
	config := testGenerationConfig(t)

	if _, err := manager.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	conn, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	assertCurrent(t, manager.Status(), 2, 0)
	assertDraining(t, manager.Status(), GenerationStatus{Generation: 1, RefCount: 1, Draining: true})
	if len(backend.removedIDs()) != 0 {
		t.Fatalf("leased generation was removed: %v", backend.removedIDs())
	}
	if !reflect.DeepEqual(gotTags, []string{"gen-1/edge"}) {
		t.Fatalf("qualified dial tags = %v", gotTags)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := backend.removedIDs(); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("removed generations = %v, want [1]", got)
	}
	if len(manager.Status().Draining) != 0 {
		t.Fatalf("draining generations remain: %+v", manager.Status().Draining)
	}
}

func TestDialFailureReleasesLease(t *testing.T) {
	backend := newFakeBackend()
	dialErr := errors.New("dial failed")
	manager := NewManager(backend, func(context.Context, string, string, string) (net.Conn, error) {
		return nil, dialErr
	})
	t.Cleanup(func() { _ = manager.Close() })
	config := testGenerationConfig(t)

	if _, err := manager.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:80"); !errors.Is(err, dialErr) {
		t.Fatalf("dial error = %v, want %v", err, dialErr)
	}
	assertCurrent(t, manager.Status(), 1, 0)
}

func TestCloseDrainsLeasedCurrentGeneration(t *testing.T) {
	backend := newFakeBackend()
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	manager := NewManager(backend, func(context.Context, string, string, string) (net.Conn, error) {
		return client, nil
	})
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	conn, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:80")
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	assertDraining(t, manager.Status(), GenerationStatus{Generation: 1, RefCount: 1, Draining: true})
	if _, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:80"); !errors.Is(err, ErrClosed) {
		t.Fatalf("dial after close error = %v, want ErrClosed", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := backend.removedIDs(); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("removed generations = %v, want [1]", got)
	}
}

func TestCloseNowRemovesLeasedGenerationExactlyOnce(t *testing.T) {
	backend := newFakeBackend()
	client, peer := net.Pipe()
	defer peer.Close()
	manager := NewManager(backend, func(context.Context, string, string, string) (net.Conn, error) {
		return client, nil
	})
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	conn, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseNow(); err != nil {
		t.Fatal(err)
	}
	if got := backend.removedIDs(); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("removed generations = %v, want [1]", got)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := backend.removedIDs(); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("leased Close removed generation twice: %v", got)
	}
	if err := manager.CloseNow(); err != nil {
		t.Fatalf("second CloseNow: %v", err)
	}
}

func TestDialUDPIsUnsupportedAndDoesNotAcquireLease(t *testing.T) {
	backend := newFakeBackend()
	manager := NewManager(backend, nil)
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}

	if conn, err := manager.DialUDP(context.Background(), "edge"); conn != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("DialUDP = (%v, %v), want (nil, ErrUnsupported)", conn, err)
	}
	assertCurrent(t, manager.Status(), 1, 0)
}

func testGenerationConfig(t *testing.T) GenerationConfig {
	t.Helper()
	config, err := NewGenerationConfig([]*core.OutboundHandlerConfig{{
		Tag: "edge",
		ProxySettings: serial.ToTypedMessage(&freedom.Config{
			FinalRules: []*freedom.FinalRuleConfig{{Action: freedom.RuleAction_Allow}},
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

type fakeBackend struct {
	mu          sync.Mutex
	build       func(context.Context, uint64, GenerationConfig) (Generation, error)
	remove      func(Generation) error
	generations map[uint64]*fakeGeneration
	removed     []uint64
}

func newFakeBackend() *fakeBackend { return &fakeBackend{} }

func (b *fakeBackend) Build(ctx context.Context, id uint64, config GenerationConfig) (Generation, error) {
	var generation Generation
	var err error
	if b.build != nil {
		generation, err = b.build(ctx, id, config)
	} else {
		generation = newFakeGeneration(id)
	}
	if fake, ok := generation.(*fakeGeneration); ok {
		b.mu.Lock()
		if b.generations == nil {
			b.generations = make(map[uint64]*fakeGeneration)
		}
		b.generations[id] = fake
		b.mu.Unlock()
	}
	return generation, err
}

func (b *fakeBackend) Remove(generation Generation) error {
	if b.remove != nil {
		return b.remove(generation)
	}
	fake := generation.(*fakeGeneration)
	b.mu.Lock()
	b.removed = append(b.removed, fake.id)
	b.mu.Unlock()
	return nil
}

func TestStreamDialRejectsPacketNetworksWithoutCallingDialer(t *testing.T) {
	backend := newFakeBackend()
	called := false
	manager := NewManager(backend, func(context.Context, string, string, string) (net.Conn, error) {
		called = true
		return nil, nil
	})
	t.Cleanup(func() { _ = manager.CloseNow() })
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"udp", "udp4", "unix", ""} {
		if conn, err := manager.Dial(context.Background(), "edge", network, "example.test:53"); conn != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Dial(%q) = (%v, %v), want ErrUnsupported", network, conn, err)
		}
	}
	if called {
		t.Fatal("unsupported packet network reached stream dialer")
	}
}

func TestApplyCommitSurvivesRetirementCleanupErrorAndRetriesWithoutNewGeneration(t *testing.T) {
	cleanupErr := errors.New("cleanup failed once")
	backend := newFakeBackend()
	var attempts int
	backend.remove = func(generation Generation) error {
		fake := generation.(*fakeGeneration)
		backend.mu.Lock()
		backend.removed = append(backend.removed, fake.id)
		backend.mu.Unlock()
		if fake.id == 1 && attempts == 0 {
			attempts++
			return cleanupErr
		}
		return nil
	}
	manager := NewManager(backend, nil)
	config := testGenerationConfig(t)
	if _, err := manager.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if id, err := manager.Apply(context.Background(), config); err != nil || id != 2 {
		t.Fatalf("committed apply = (%d, %v), want (2, nil)", id, err)
	}
	assertCurrent(t, manager.Status(), 2, 0)
	assertDraining(t, manager.Status(), GenerationStatus{Generation: 1, Draining: true, CleanupError: cleanupErr.Error()})
	if err := manager.RetryCleanup(); err != nil {
		t.Fatalf("retry cleanup = %v", err)
	}
	if len(manager.Status().Draining) != 0 {
		t.Fatalf("cleanup retry remains visible: %+v", manager.Status())
	}
	assertCurrent(t, manager.Status(), 2, 0)
	if err := manager.CloseNow(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseNowCancelsBuildAndDoesNotWaitForApplyMutex(t *testing.T) {
	buildStarted := make(chan struct{})
	backend := &fakeBackend{build: func(ctx context.Context, id uint64, _ GenerationConfig) (Generation, error) {
		close(buildStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	manager := NewManager(backend, nil)
	applyDone := make(chan error, 1)
	go func() {
		_, err := manager.Apply(context.Background(), testGenerationConfig(t))
		applyDone <- err
	}()
	<-buildStarted
	started := time.Now()
	if err := manager.CloseNow(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("CloseNow took %s", elapsed)
	}
	if err := <-applyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply error = %v, want cancellation", err)
	}
}

func TestCloseNowClosesLeasedConnection(t *testing.T) {
	backend := newFakeBackend()
	client, peer := net.Pipe()
	defer peer.Close()
	manager := NewManager(backend, func(context.Context, string, string, string) (net.Conn, error) {
		return client, nil
	})
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	conn, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseNow(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("closed")); err == nil {
		t.Fatal("leased connection remained writable after CloseNow")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("idempotent leased close: %v", err)
	}
}

func TestCloseNowWaitsForDialBeforeLeaseRegistration(t *testing.T) {
	backend := newFakeBackend()
	dialStarted := make(chan struct{})
	dialRelease := make(chan struct{})
	client, peer := net.Pipe()
	defer peer.Close()
	manager := NewManager(backend, func(context.Context, string, string, string) (net.Conn, error) {
		close(dialStarted)
		<-dialRelease
		return client, nil
	})
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	dialDone := make(chan error, 1)
	go func() {
		_, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:80")
		dialDone <- err
	}()
	<-dialStarted

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := manager.CloseNowContext(ctx); !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("CloseNowContext = %v, want ErrShutdownIncomplete", err)
	}
	if got := backend.removedIDs(); len(got) != 0 {
		t.Fatalf("generation removed while dial was pending: %v", got)
	}
	close(dialRelease)
	if err := <-dialDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("pending Dial error = %v, want ErrClosed", err)
	}
	if err := manager.CloseNow(); err != nil {
		t.Fatal(err)
	}
	if got := backend.removedIDs(); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("removed generations = %v, want [1]", got)
	}
}

func TestCloseNowWaitsForRemovalAlreadyInProgress(t *testing.T) {
	removeStarted := make(chan struct{})
	removeRelease := make(chan struct{})
	backend := newFakeBackend()
	backend.remove = func(generation Generation) error {
		backend.mu.Lock()
		backend.removed = append(backend.removed, generation.(*fakeGeneration).id)
		backend.mu.Unlock()
		close(removeStarted)
		<-removeRelease
		return nil
	}
	client, peer := net.Pipe()
	defer peer.Close()
	manager := NewManager(backend, func(context.Context, string, string, string) (net.Conn, error) {
		return client, nil
	})
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	conn, err := manager.Dial(context.Background(), "edge", "tcp", "example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- conn.Close() }()
	<-removeStarted

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := manager.CloseNowContext(ctx); !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("CloseNowContext = %v, want ErrShutdownIncomplete", err)
	}
	close(removeRelease)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseNow(); err != nil {
		t.Fatal(err)
	}
	if got := backend.removedIDs(); !reflect.DeepEqual(got, []uint64{1}) {
		t.Fatalf("removed generations = %v, want [1]", got)
	}
}

func TestCloseNowContextDeadlineIncludesShutdownGateWait(t *testing.T) {
	removeStarted := make(chan struct{})
	removeRelease := make(chan struct{})
	backend := newFakeBackend()
	backend.remove = func(Generation) error {
		close(removeStarted)
		<-removeRelease
		return nil
	}
	manager := NewManager(backend, nil)
	if _, err := manager.Apply(context.Background(), testGenerationConfig(t)); err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	<-removeStarted

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := manager.CloseNowContext(ctx); !errors.Is(err, ErrShutdownIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseNowContext = %v, want incomplete deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline gate wait took %s", elapsed)
	}
	close(removeRelease)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseNow(); err != nil {
		t.Fatal(err)
	}
}

func (b *fakeBackend) removedIDs() []uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]uint64(nil), b.removed...)
}

type fakeGeneration struct{ id uint64 }

func newFakeGeneration(id uint64) *fakeGeneration { return &fakeGeneration{id: id} }

func (g *fakeGeneration) OutboundTag(tag string) (string, error) {
	return "gen-" + fmtUint(g.id) + "/" + tag, nil
}

func fmtUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

func assertCurrent(t *testing.T, status Status, generation uint64, refs int64) {
	t.Helper()
	want := GenerationStatus{Generation: generation, RefCount: refs}
	if status.Current == nil || *status.Current != want {
		t.Fatalf("current = %+v, want %+v", status.Current, want)
	}
}

func assertDraining(t *testing.T, status Status, want GenerationStatus) {
	t.Helper()
	if len(status.Draining) != 1 || status.Draining[0] != want {
		t.Fatalf("draining = %+v, want [%+v]", status.Draining, want)
	}
}
