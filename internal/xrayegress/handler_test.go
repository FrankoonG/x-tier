package xrayegress

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

const handlerTestTimeout = 5 * time.Second

func TestHandlerPreservesRealTCPHalfCloseAndResponse(t *testing.T) {
	listener := mustTCPListener(t)
	request := []byte("request requiring a write FIN")
	response := []byte("response after peer observed EOF")
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(handlerTestTimeout)); err != nil {
			serverDone <- err
			return
		}
		got, err := io.ReadAll(conn)
		if err != nil {
			serverDone <- err
			return
		}
		if string(got) != string(request) {
			serverDone <- errors.New("server received an unexpected request")
			return
		}
		_, err = conn.Write(response)
		serverDone <- err
	}()

	handler := mustStartEgressHandler(t)
	link, peer := newEgressLinkPair()
	defer peer.Close()
	tracker := new(egressErrorTracker)
	port := xnet.Port(listener.Addr().(*net.TCPAddr).Port)
	ctx := egressContext(xnet.TCPDestination(xnet.DomainAddress("localhost"), port), tracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(ctx, link)
		close(dispatchDone)
	}()

	mustConnWrite(t, peer, request)
	if err := peer.CloseWrite(); err != nil {
		t.Fatalf("close Xray uplink: %v", err)
	}
	if got := mustConnReadFull(t, peer, len(response)); string(got) != string(response) {
		t.Fatalf("response = %q, want %q", got, response)
	}
	assertConnReadClosed(t, peer)
	waitForSignal(t, dispatchDone, "egress dispatch completion")
	if err := waitForError(t, serverDone, "TCP server completion"); err != nil {
		t.Fatalf("TCP server: %v", err)
	}
	assertNoEgressErrors(t, tracker)
}

func TestTargetFromContextPreservesLastDomainDestination(t *testing.T) {
	want := xnet.TCPDestination(xnet.DomainAddress("edge-only.example.test"), 18443)
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{
		{Target: xnet.TCPDestination(xnet.IPAddress(net.ParseIP("192.0.2.10")), 9)},
		{Target: want},
	})

	got, err := targetFromContext(ctx)
	if err != nil {
		t.Fatalf("targetFromContext: %v", err)
	}
	if got.Network != xnet.Network_TCP {
		t.Fatalf("network = %s, want TCP", got.Network)
	}
	if got.Address.Family() != xnet.AddressFamilyDomain {
		t.Fatalf("address family = %v, want domain", got.Address.Family())
	}
	if got.Address.Domain() != want.Address.Domain() {
		t.Fatalf("domain = %q, want %q", got.Address.Domain(), want.Address.Domain())
	}
	if got.Port != want.Port {
		t.Fatalf("port = %d, want %d", got.Port, want.Port)
	}
}

func TestDispatchRejectsInboundContextWithoutDialing(t *testing.T) {
	listener := mustTCPListener(t)
	handler := mustStartEgressHandler(t)
	link, peer := newEgressLinkPair()
	defer peer.Close()
	tracker := new(egressErrorTracker)
	port := xnet.Port(listener.Addr().(*net.TCPAddr).Port)
	ctx := egressContext(xnet.TCPDestination(xnet.IPAddress(net.ParseIP("127.0.0.1")), port), tracker)
	ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: "external-socks"})

	handler.Dispatch(ctx, link)
	assertOneEgressError(t, tracker, ErrExternalInbound)
	assertConnFailClosed(t, peer)

	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err == nil {
		_ = conn.Close()
		t.Fatal("handler dialed a target for an inbound-context dispatch")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Accept error = %v, want timeout proving no dial", err)
	}
}

func TestDispatchRejectsUDPAndMissingTargetFailClosed(t *testing.T) {
	handler := mustStartEgressHandler(t)
	tests := []struct {
		name string
		ctx  func(*egressErrorTracker) context.Context
	}{
		{
			name: "UDP",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.UDPDestination(xnet.DomainAddress("dns.example.test"), 53), tracker)
			},
		},
		{
			name: "missing outbound list",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return session.TrackedConnectionError(context.Background(), tracker)
			},
		},
		{
			name: "nil final outbound",
			ctx: func(tracker *egressErrorTracker) context.Context {
				ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{nil})
				return session.TrackedConnectionError(ctx, tracker)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link, peer := newEgressLinkPair()
			defer peer.Close()
			tracker := new(egressErrorTracker)
			handler.Dispatch(test.ctx(tracker), link)
			assertOneEgressError(t, tracker, ErrInvalidTarget)
			assertConnFailClosed(t, peer)
		})
	}
}

func TestConcurrentCloseTerminatesAllActiveConnections(t *testing.T) {
	const (
		activeConnections = 12
		concurrentClosers = 8
	)
	listener := mustTCPListener(t)
	accepted := make(chan net.Conn, activeConnections)
	acceptFailure := make(chan error, 1)
	go func() {
		for range activeConnections {
			conn, err := listener.Accept()
			if err != nil {
				acceptFailure <- err
				return
			}
			accepted <- conn
		}
		close(accepted)
	}()

	handler := mustStartEgressHandler(t)
	port := xnet.Port(listener.Addr().(*net.TCPAddr).Port)
	target := xnet.TCPDestination(xnet.IPAddress(net.ParseIP("127.0.0.1")), port)
	peers := make([]*egressLinkPeer, 0, activeConnections)
	dispatchDone := make(chan struct{}, activeConnections)
	for range activeConnections {
		link, peer := newEgressLinkPair()
		peers = append(peers, peer)
		go func() {
			handler.Dispatch(egressContext(target, new(egressErrorTracker)), link)
			dispatchDone <- struct{}{}
		}()
	}

	serverConnections := make([]net.Conn, 0, activeConnections)
	for range activeConnections {
		select {
		case conn := <-accepted:
			serverConnections = append(serverConnections, conn)
		case err := <-acceptFailure:
			t.Fatalf("accept active connection: %v", err)
		case <-time.After(handlerTestTimeout):
			t.Fatal("timed out accepting active connections")
		}
	}
	waitForActiveEgressConnections(t, handler, activeConnections)

	startClose := make(chan struct{})
	closeResults := make(chan error, concurrentClosers)
	for range concurrentClosers {
		go func() {
			<-startClose
			closeResults <- handler.Close()
		}()
	}
	close(startClose)
	for range concurrentClosers {
		if err := waitForError(t, closeResults, "concurrent Close"); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	for range activeConnections {
		waitForSignal(t, dispatchDone, "closed dispatch")
	}

	for index, conn := range serverConnections {
		if err := conn.SetReadDeadline(time.Now().Add(handlerTestTimeout)); err != nil {
			t.Fatalf("server connection %d deadline: %v", index, err)
		}
		buffer := make([]byte, 1)
		if _, err := conn.Read(buffer); err == nil {
			t.Fatalf("server connection %d remained open", index)
		}
		_ = conn.Close()
	}
	for index, peer := range peers {
		assertConnFailClosed(t, peer)
		if err := peer.Close(); err != nil {
			t.Errorf("close peer %d: %v", index, err)
		}
	}

	handler.mu.Lock()
	active := len(handler.active)
	handler.mu.Unlock()
	if active != 0 {
		t.Fatalf("active connections after Close = %d, want 0", active)
	}
	if err := handler.Start(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestConcurrentCloseWaitsForAndCancelsPendingDial(t *testing.T) {
	handler := mustStartEgressHandler(t)
	dialStarted := make(chan struct{})
	dialCanceled := make(chan struct{})
	allowDialReturn := make(chan struct{})
	handler.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialStarted)
		<-ctx.Done()
		close(dialCanceled)
		<-allowDialReturn
		return nil, ctx.Err()
	}

	link, peer := newEgressLinkPair()
	defer peer.Close()
	tracker := new(egressErrorTracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(egressContext(xnet.TCPDestination(xnet.DomainAddress("pending.example.test"), 443), tracker), link)
		close(dispatchDone)
	}()
	waitForSignal(t, dialStarted, "pending dial start")

	firstClose := make(chan error, 1)
	secondClose := make(chan error, 1)
	go func() { firstClose <- handler.Close() }()
	waitForSignal(t, dialCanceled, "pending dial cancellation")
	go func() { secondClose <- handler.Close() }()
	select {
	case err := <-secondClose:
		t.Fatalf("concurrent Close returned before pending dial drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(allowDialReturn)
	if err := waitForError(t, firstClose, "first Close"); err != nil {
		t.Fatal(err)
	}
	if err := waitForError(t, secondClose, "second Close"); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, dispatchDone, "canceled dispatch")
	assertOneEgressError(t, tracker, context.Canceled)
	assertConnFailClosed(t, peer)
}

func TestCloseWaitsForInterruptedCopyGoroutines(t *testing.T) {
	handler := mustStartEgressHandler(t)
	conn, peer := net.Pipe()
	defer peer.Close()
	handler.dial = func(context.Context, string, string) (net.Conn, error) {
		return conn, nil
	}

	reader := newDelayedInterruptReader()
	link := &transport.Link{Reader: reader, Writer: buf.Discard}
	tracker := new(egressErrorTracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(egressContext(xnet.TCPDestination(xnet.DomainAddress("copy.example.test"), 443), tracker), link)
		close(dispatchDone)
	}()
	waitForSignal(t, reader.started, "copy reader start")

	closeResult := make(chan error, 1)
	go func() { closeResult <- handler.Close() }()
	waitForSignal(t, reader.interrupted, "copy reader interruption")
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned before the interrupted copy goroutine exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(reader.release)
	waitForSignal(t, reader.exited, "copy reader exit")
	if err := waitForError(t, closeResult, "Close after copy exit"); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, dispatchDone, "dispatch after copy exit")
	assertNoEgressErrors(t, tracker)
}

type delayedInterruptReader struct {
	started     chan struct{}
	interrupted chan struct{}
	release     chan struct{}
	exited      chan struct{}
	startOnce   sync.Once
	stopOnce    sync.Once
}

func newDelayedInterruptReader() *delayedInterruptReader {
	return &delayedInterruptReader{
		started: make(chan struct{}), interrupted: make(chan struct{}),
		release: make(chan struct{}), exited: make(chan struct{}),
	}
}

func (r *delayedInterruptReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.interrupted
	<-r.release
	close(r.exited)
	return nil, io.ErrClosedPipe
}

func (r *delayedInterruptReader) Interrupt() {
	r.stopOnce.Do(func() { close(r.interrupted) })
}

type egressErrorTracker struct {
	mu     sync.Mutex
	errors []error
}

func (t *egressErrorTracker) SubmitError(err error) {
	t.mu.Lock()
	t.errors = append(t.errors, err)
	t.mu.Unlock()
}

func (t *egressErrorTracker) snapshot() []error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]error(nil), t.errors...)
}

type egressLinkPeer struct {
	net.Conn
	uplink *pipe.Writer
}

func (p *egressLinkPeer) CloseWrite() error {
	return p.uplink.Close()
}

func newEgressLinkPair() (*transport.Link, *egressLinkPeer) {
	uplinkReader, uplinkWriter := pipe.New()
	downlinkReader, downlinkWriter := pipe.New()
	link := &transport.Link{Reader: uplinkReader, Writer: downlinkWriter}
	peer := &egressLinkPeer{
		Conn: cnc.NewConnection(
			cnc.ConnectionInputMulti(uplinkWriter),
			cnc.ConnectionOutputMulti(downlinkReader),
		),
		uplink: uplinkWriter,
	}
	return link, peer
}

func mustStartEgressHandler(t *testing.T) *Handler {
	t.Helper()
	handler, err := New("test-egress")
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := handler.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return handler
}

func mustTCPListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func egressContext(target xnet.Destination, tracker *egressErrorTracker) context.Context {
	ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{{Target: target}})
	return session.TrackedConnectionError(ctx, tracker)
}

func mustConnWrite(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		result <- err
	}()
	if err := waitForError(t, result, "connection write"); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func mustConnReadFull(t *testing.T, conn net.Conn, size int) []byte {
	t.Helper()
	type readResult struct {
		payload []byte
		err     error
	}
	result := make(chan readResult, 1)
	go func() {
		payload := make([]byte, size)
		_, err := io.ReadFull(conn, payload)
		result <- readResult{payload: payload, err: err}
	}()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("ReadFull: %v", got.err)
		}
		return got.payload
	case <-time.After(handlerTestTimeout):
		t.Fatal("timed out reading connection")
		return nil
	}
}

func assertConnReadClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, err := conn.Read(buffer)
		result <- err
	}()
	if err := waitForError(t, result, "closed read"); err == nil {
		t.Fatal("connection read succeeded after peer close")
	}
}

func assertConnFailClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	writeResult := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte{1})
		writeResult <- err
	}()
	if err := waitForError(t, writeResult, "fail-closed write"); err == nil {
		t.Fatal("connection write succeeded after rejected or closed dispatch")
	}
	assertConnReadClosed(t, conn)
}

func assertOneEgressError(t *testing.T, tracker *egressErrorTracker, want error) {
	t.Helper()
	errs := tracker.snapshot()
	if len(errs) != 1 || !errors.Is(errs[0], want) {
		t.Fatalf("originator errors = %v, want one matching %v", errs, want)
	}
}

func assertNoEgressErrors(t *testing.T, tracker *egressErrorTracker) {
	t.Helper()
	if errs := tracker.snapshot(); len(errs) != 0 {
		t.Fatalf("originator errors = %v, want none", errs)
	}
}

func waitForActiveEgressConnections(t *testing.T, handler *Handler, want int) {
	t.Helper()
	deadline := time.Now().Add(handlerTestTimeout)
	for time.Now().Before(deadline) {
		handler.mu.Lock()
		got := len(handler.active)
		handler.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for handler to track all active connections")
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(handlerTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForError(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(handlerTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}
