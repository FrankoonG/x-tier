package xrayegress

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
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
	var dialNetwork, dialAddress string
	dialer := new(net.Dialer)
	handler.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialNetwork, dialAddress = network, address
		return dialer.DialContext(ctx, network, address)
	}
	link, peer := newEgressLinkPair()
	defer peer.Close()
	tracker := new(egressErrorTracker)
	port := xnet.Port(listener.Addr().(*net.TCPAddr).Port)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	ctx := egressContextFrom(parentCtx, xnet.TCPDestination(xnet.IPAddress(listener.Addr().(*net.TCPAddr).IP), port), tracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(ctx, link)
		close(dispatchDone)
	}()

	readyCtx, cancelReady := context.WithTimeout(context.Background(), handlerTestTimeout)
	if err := ConfirmReady(readyCtx, peer); err != nil {
		cancelReady()
		t.Fatalf("confirm egress ready: %v", err)
	}
	cancelReady()
	cancelParent()
	mustConnWrite(t, peer, request)
	if err := peer.CloseWrite(); err != nil {
		t.Fatalf("close Xray uplink: %v", err)
	}
	if got := mustConnReadFull(t, peer, len(response)); string(got) != string(response) {
		t.Fatalf("response = %q, want %q", got, response)
	}
	assertConnReadClosed(t, peer)
	waitForSignal(t, dispatchDone, "egress dispatch completion")
	if dialNetwork != "tcp4" || dialAddress != listener.Addr().String() {
		t.Fatalf("dial target = %q %q, want tcp4 %q", dialNetwork, dialAddress, listener.Addr())
	}
	if err := waitForError(t, serverDone, "TCP server completion"); err != nil {
		t.Fatalf("TCP server: %v", err)
	}
	assertNoEgressErrors(t, tracker)
}

func TestConfirmReadyRejectsInvalidAndTruncatedFrames(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
	}{
		{name: "invalid", frame: []byte("BADFRAME")},
		{name: "truncated", frame: readyFrame[:3]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, right := net.Pipe()
			defer left.Close()
			writeDone := make(chan struct{})
			go func() {
				_, _ = right.Write(test.frame)
				_ = right.Close()
				close(writeDone)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), handlerTestTimeout)
			err := ConfirmReady(ctx, left)
			cancel()
			if !errors.Is(err, ErrNotReady) {
				t.Fatalf("ConfirmReady error = %v, want ErrNotReady", err)
			}
			waitForSignal(t, writeDone, "readiness writer")
		})
	}
}

func TestConfirmReadyNilContextClosesConnection(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	if err := ConfirmReady(nil, left); !errors.Is(err, ErrNotReady) {
		t.Fatalf("ConfirmReady error = %v, want ErrNotReady", err)
	}
	readResult := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := right.Read(one[:])
		readResult <- err
	}()
	if err := waitForError(t, readResult, "nil-context peer close"); err == nil {
		t.Fatal("connection remained open after nil-context readiness failure")
	}
}

func TestConfirmReadyCancellationCannotRaceSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := newReadyCancellationRaceConn()
	result := make(chan error, 1)
	go func() { result <- ConfirmReady(ctx, conn) }()
	waitForSignal(t, conn.readStarted, "readiness read")
	cancel()
	waitForSignal(t, conn.readDeadlineStarted, "readiness cancellation deadline")
	close(conn.releaseRead)
	select {
	case err := <-result:
		t.Fatalf("ConfirmReady returned before its cancellation callback exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(conn.releaseReadDeadline)
	err := waitForError(t, result, "canceled readiness result")
	if !errors.Is(err, ErrNotReady) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ConfirmReady error = %v, want ErrNotReady and context.Canceled", err)
	}
}

func TestConfirmReadyCancellationClosesWhenReadDeadlinesAreUnsupported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn := newUnsupportedReadyDeadlineConn()
	result := make(chan error, 1)
	go func() { result <- ConfirmReady(ctx, conn) }()
	waitForSignal(t, conn.readStarted, "unsupported-deadline readiness read")
	cancel()
	waitForSignal(t, conn.closeStarted, "unsupported-deadline readiness close")
	err := waitForError(t, result, "unsupported-deadline readiness result")
	if !errors.Is(err, ErrNotReady) || !errors.Is(err, context.Canceled) {
		t.Fatalf("ConfirmReady error = %v, want ErrNotReady and context.Canceled", err)
	}
}

func TestTargetFromContextCanonicalizesLastIPLiteral(t *testing.T) {
	tests := []struct {
		name     string
		address  xnet.Address
		wantAddr string
		family   xnet.AddressFamily
	}{
		{
			name:     "IPv4",
			address:  testIPAddress{ip: net.IP{192, 0, 2, 10}, family: xnet.AddressFamilyIPv4, text: "192.000.002.010"},
			wantAddr: "192.0.2.10:18443",
			family:   xnet.AddressFamilyIPv4,
		},
		{
			name:     "IPv6",
			address:  testIPAddress{ip: net.ParseIP("2001:0db8:0:0:0:0:0:10"), family: xnet.AddressFamilyIPv6, text: "[2001:0DB8:0:0:0:0:0:10]"},
			wantAddr: "[2001:db8::10]:18443",
			family:   xnet.AddressFamilyIPv6,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := session.ContextWithOutbounds(context.Background(), []*session.Outbound{
				{Target: xnet.TCPDestination(xnet.IPAddress(net.IP{198, 51, 100, 1}), 9)},
				{Target: xnet.TCPDestination(test.address, 18443)},
			})

			got, err := targetFromContext(ctx)
			if err != nil {
				t.Fatalf("targetFromContext: %v", err)
			}
			if got.Network != xnet.Network_TCP {
				t.Fatalf("network = %s, want TCP", got.Network)
			}
			if got.Address.Family() != test.family {
				t.Fatalf("address family = %v, want %v", got.Address.Family(), test.family)
			}
			if got.NetAddr() != test.wantAddr {
				t.Fatalf("NetAddr = %q, want %q", got.NetAddr(), test.wantAddr)
			}
		})
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

func TestDispatchRejectsNonDialableTargetsBeforeDial(t *testing.T) {
	tests := []struct {
		name string
		ctx  func(*egressErrorTracker) context.Context
	}{
		{
			name: "domain",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.DomainAddress("origin.example.test"), 443), tracker)
			},
		},
		{
			name: "numeric literal encoded as domain",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.DomainAddress("192.0.2.10"), 443), tracker)
			},
		},
		{
			name: "IPv6 zone",
			ctx: func(tracker *egressErrorTracker) context.Context {
				address := testIPAddress{ip: net.ParseIP("fe80::1"), family: xnet.AddressFamilyIPv6, text: "[fe80::1%eth0]"}
				return egressContext(xnet.TCPDestination(address, 443), tracker)
			},
		},
		{
			name: "IPv6 link-local without zone",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.IPAddress(net.ParseIP("fe80::1")), 443), tracker)
			},
		},
		{
			name: "IPv4-mapped IPv6",
			ctx: func(tracker *egressErrorTracker) context.Context {
				address := testIPAddress{ip: net.ParseIP("::ffff:192.0.2.10"), family: xnet.AddressFamilyIPv6, text: "[::ffff:192.0.2.10]"}
				return egressContext(xnet.TCPDestination(address, 443), tracker)
			},
		},
		{
			name: "unspecified IPv4",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.AnyIP, 443), tracker)
			},
		},
		{
			name: "unspecified IPv6",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.AnyIPv6, 443), tracker)
			},
		},
		{
			name: "multicast IPv4",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.IPAddress(net.IP{224, 0, 0, 1}), 443), tracker)
			},
		},
		{
			name: "multicast IPv6",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.IPAddress(net.ParseIP("ff02::1")), 443), tracker)
			},
		},
		{
			name: "IPv4 broadcast",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.IPAddress(net.IPv4bcast), 443), tracker)
			},
		},
		{
			name: "zero port",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.TCPDestination(xnet.IPAddress(net.IP{192, 0, 2, 10}), 0), tracker)
			},
		},
		{
			name: "nil address",
			ctx: func(tracker *egressErrorTracker) context.Context {
				target := xnet.Destination{Network: xnet.Network_TCP, Port: 443}
				return egressContext(target, tracker)
			},
		},
		{
			name: "invalid IP bytes",
			ctx: func(tracker *egressErrorTracker) context.Context {
				address := testIPAddress{ip: net.IP{192, 0, 2}, family: xnet.AddressFamilyIPv4, text: "192.0.2"}
				return egressContext(xnet.TCPDestination(address, 443), tracker)
			},
		},
		{
			name: "IP family mismatch",
			ctx: func(tracker *egressErrorTracker) context.Context {
				address := testIPAddress{ip: net.ParseIP("2001:db8::1"), family: xnet.AddressFamilyIPv4, text: "2001:db8::1"}
				return egressContext(xnet.TCPDestination(address, 443), tracker)
			},
		},
		{
			name: "UDP",
			ctx: func(tracker *egressErrorTracker) context.Context {
				return egressContext(xnet.UDPDestination(xnet.IPAddress(net.IP{192, 0, 2, 53}), 53), tracker)
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
			handler := mustStartEgressHandler(t)
			dialCalls := 0
			handler.dial = func(context.Context, string, string) (net.Conn, error) {
				dialCalls++
				return nil, errors.New("unexpected dial")
			}
			link, peer := newEgressLinkPair()
			defer peer.Close()
			tracker := new(egressErrorTracker)
			handler.Dispatch(test.ctx(tracker), link)
			assertOneEgressError(t, tracker, ErrInvalidTarget)
			assertConnFailClosed(t, peer)
			if dialCalls != 0 {
				t.Fatalf("dial calls = %d, want 0", dialCalls)
			}
		})
	}
}

func TestDispatchRejectsConnectedPeerMismatchBeforeReadiness(t *testing.T) {
	handler := mustStartEgressHandler(t)
	left, right := net.Pipe()
	defer right.Close()
	handler.dial = func(context.Context, string, string) (net.Conn, error) {
		return &remoteAddrConn{
			Conn:   left,
			remote: staticAddr("198.51.100.9:443"),
		}, nil
	}
	link, peer := newEgressLinkPair()
	defer peer.Close()
	tracker := new(egressErrorTracker)
	target := xnet.TCPDestination(xnet.IPAddress(net.IP{192, 0, 2, 9}), 443)

	handler.Dispatch(egressContext(target, tracker), link)
	assertOneEgressError(t, tracker, ErrPeerMismatch)
	assertConnFailClosed(t, peer)

	readResult := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := right.Read(one[:])
		readResult <- err
	}()
	if err := waitForError(t, readResult, "mismatched connection close"); err == nil {
		t.Fatal("mismatched egress connection remained open")
	}
}

func TestDispatchRedactsDialFailureBeforeReportingIt(t *testing.T) {
	handler := mustStartEgressHandler(t)
	const secret = "credential=never-report-this"
	dialFailure := errors.New("upstream rejected " + secret)
	handler.dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, dialFailure
	}
	link, peer := newEgressLinkPair()
	defer peer.Close()
	tracker := new(egressErrorTracker)
	target := xnet.TCPDestination(xnet.IPAddress(net.IP{192, 0, 2, 77}), 443)

	handler.Dispatch(egressContext(target, tracker), link)
	errs := tracker.snapshot()
	if len(errs) != 1 || errors.Is(errs[0], dialFailure) {
		t.Fatalf("originator errors = %v, want one opaque dial failure", errs)
	}
	for _, sensitive := range []string{secret, "192.0.2.77", dialFailure.Error()} {
		if strings.Contains(errs[0].Error(), sensitive) {
			t.Fatalf("reported dial error leaked %q: %v", sensitive, errs[0])
		}
	}
	assertConnFailClosed(t, peer)
}

func TestCloseInterruptsBlockedReadinessWriter(t *testing.T) {
	handler := mustStartEgressHandler(t)
	conn, peerConn := net.Pipe()
	defer peerConn.Close()
	handler.dial = func(context.Context, string, string) (net.Conn, error) {
		return &remoteAddrConn{Conn: conn, remote: staticAddr("192.0.2.22:443")}, nil
	}
	link, peer := newEgressLinkPair()
	defer peer.Close()
	writer := newBlockingInterruptWriter()
	link.Writer = writer
	tracker := new(egressErrorTracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(egressContext(xnet.TCPDestination(xnet.IPAddress(net.IP{192, 0, 2, 22}), 443), tracker), link)
		close(dispatchDone)
	}()
	waitForSignal(t, writer.started, "readiness writer start")
	closeDone := make(chan error, 1)
	go func() { closeDone <- handler.Close() }()
	waitForSignal(t, writer.interrupted, "readiness writer interrupt")
	if err := waitForError(t, closeDone, "handler close after readiness interrupt"); err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, dispatchDone, "dispatch after readiness interrupt")
	assertOneEgressError(t, tracker, ErrNotReady)
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
	for index, peer := range peers {
		readyCtx, cancelReady := context.WithTimeout(context.Background(), handlerTestTimeout)
		err := ConfirmReady(readyCtx, peer)
		cancelReady()
		if err != nil {
			t.Fatalf("confirm active egress %d ready: %v", index, err)
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
		handler.Dispatch(egressContext(xnet.TCPDestination(xnet.IPAddress(net.IP{192, 0, 2, 20}), 443), tracker), link)
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
		return &remoteAddrConn{Conn: conn, remote: staticAddr("192.0.2.21:443")}, nil
	}

	reader := newDelayedInterruptReader()
	link := &transport.Link{Reader: reader, Writer: buf.Discard}
	tracker := new(egressErrorTracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(egressContext(xnet.TCPDestination(xnet.IPAddress(net.IP{192, 0, 2, 21}), 443), tracker), link)
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

type testIPAddress struct {
	ip     net.IP
	family xnet.AddressFamily
	text   string
}

type remoteAddrConn struct {
	net.Conn
	remote net.Addr
}

func (c *remoteAddrConn) RemoteAddr() net.Addr { return c.remote }

type staticAddr string

func (a staticAddr) Network() string { return "tcp" }
func (a staticAddr) String() string  { return string(a) }

type readyCancellationRaceConn struct {
	readStarted         chan struct{}
	releaseRead         chan struct{}
	readDeadlineStarted chan struct{}
	releaseReadDeadline chan struct{}
	readOnce            sync.Once
	readDeadlineOnce    sync.Once
}

func newReadyCancellationRaceConn() *readyCancellationRaceConn {
	return &readyCancellationRaceConn{
		readStarted:         make(chan struct{}),
		releaseRead:         make(chan struct{}),
		readDeadlineStarted: make(chan struct{}),
		releaseReadDeadline: make(chan struct{}),
	}
}

func (c *readyCancellationRaceConn) Read(buffer []byte) (int, error) {
	c.readOnce.Do(func() { close(c.readStarted) })
	<-c.releaseRead
	return copy(buffer, readyFrame[:]), nil
}

func (*readyCancellationRaceConn) Write(buffer []byte) (int, error) { return len(buffer), nil }
func (*readyCancellationRaceConn) Close() error                     { return nil }
func (*readyCancellationRaceConn) LocalAddr() net.Addr              { return staticAddr("local") }
func (*readyCancellationRaceConn) RemoteAddr() net.Addr             { return staticAddr("remote") }
func (*readyCancellationRaceConn) SetDeadline(time.Time) error      { return nil }
func (c *readyCancellationRaceConn) SetReadDeadline(time.Time) error {
	c.readDeadlineOnce.Do(func() {
		close(c.readDeadlineStarted)
		<-c.releaseReadDeadline
	})
	return nil
}
func (*readyCancellationRaceConn) SetWriteDeadline(time.Time) error { return nil }

type unsupportedReadyDeadlineConn struct {
	readStarted  chan struct{}
	closeStarted chan struct{}
	closed       chan struct{}
	readOnce     sync.Once
	closeOnce    sync.Once
}

func newUnsupportedReadyDeadlineConn() *unsupportedReadyDeadlineConn {
	return &unsupportedReadyDeadlineConn{
		readStarted:  make(chan struct{}),
		closeStarted: make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (c *unsupportedReadyDeadlineConn) Read([]byte) (int, error) {
	c.readOnce.Do(func() { close(c.readStarted) })
	<-c.closed
	return 0, net.ErrClosed
}

func (*unsupportedReadyDeadlineConn) Write(payload []byte) (int, error) { return len(payload), nil }
func (c *unsupportedReadyDeadlineConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closeStarted)
		close(c.closed)
	})
	return nil
}
func (*unsupportedReadyDeadlineConn) LocalAddr() net.Addr  { return staticAddr("local") }
func (*unsupportedReadyDeadlineConn) RemoteAddr() net.Addr { return staticAddr("remote") }
func (*unsupportedReadyDeadlineConn) SetDeadline(time.Time) error {
	return errors.New("deadlines unsupported")
}
func (*unsupportedReadyDeadlineConn) SetReadDeadline(time.Time) error {
	return errors.New("read deadlines unsupported")
}
func (*unsupportedReadyDeadlineConn) SetWriteDeadline(time.Time) error {
	return errors.New("write deadlines unsupported")
}

type blockingInterruptWriter struct {
	started     chan struct{}
	interrupted chan struct{}
	startOnce   sync.Once
	stopOnce    sync.Once
}

func newBlockingInterruptWriter() *blockingInterruptWriter {
	return &blockingInterruptWriter{started: make(chan struct{}), interrupted: make(chan struct{})}
}

func (w *blockingInterruptWriter) WriteMultiBuffer(payload buf.MultiBuffer) error {
	buf.ReleaseMulti(payload)
	w.startOnce.Do(func() { close(w.started) })
	<-w.interrupted
	return io.ErrClosedPipe
}

func (w *blockingInterruptWriter) Interrupt() {
	w.stopOnce.Do(func() { close(w.interrupted) })
}

func (a testIPAddress) IP() net.IP                 { return append(net.IP(nil), a.ip...) }
func (testIPAddress) Domain() string               { panic("Domain called on test IP address") }
func (a testIPAddress) Family() xnet.AddressFamily { return a.family }
func (a testIPAddress) String() string             { return a.text }

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
	return egressContextFrom(context.Background(), target, tracker)
}

func egressContextFrom(parent context.Context, target xnet.Destination, tracker *egressErrorTracker) context.Context {
	ctx := session.ContextWithOutbounds(parent, []*session.Outbound{{Target: target}})
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
