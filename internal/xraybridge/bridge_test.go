package xraybridge

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

const testTimeout = 3 * time.Second

type carrierFunc func(context.Context, CarrierRequest, net.Conn) error

func (f carrierFunc) Handoff(ctx context.Context, request CarrierRequest, conn net.Conn) error {
	return f(ctx, request, conn)
}

type userEgressFunc func(context.Context, UserRequest) (net.Conn, error)

func (f userEgressFunc) Dial(ctx context.Context, request UserRequest) (net.Conn, error) {
	return f(ctx, request)
}

type closeWriteRecorder struct {
	calls atomic.Int64
}

func (*closeWriteRecorder) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (r *closeWriteRecorder) CloseWrite() error {
	r.calls.Add(1)
	return nil
}

type errorTracker struct {
	mu     sync.Mutex
	errors []error
}

func (t *errorTracker) SubmitError(err error) {
	t.mu.Lock()
	t.errors = append(t.errors, err)
	t.mu.Unlock()
}

func (t *errorTracker) snapshot() []error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]error(nil), t.errors...)
}

func TestHandlerContract(t *testing.T) {
	handler, err := New(Config{
		Tag: "xtier-bridge",
		Routes: map[string]RouteKind{
			"carrier-in": RouteCarrier,
			"user-in":    RouteUserEgress,
		},
		Carrier: carrierFunc(func(context.Context, CarrierRequest, net.Conn) error { return nil }),
		UserEgress: userEgressFunc(func(context.Context, UserRequest) (net.Conn, error) {
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := handler.Tag(); got != "xtier-bridge" {
		t.Fatalf("Tag = %q, want xtier-bridge", got)
	}
	if handler.SenderSettings() != nil {
		t.Fatal("SenderSettings must be nil")
	}
	if handler.ProxySettings() != nil {
		t.Fatal("ProxySettings must be nil")
	}
	if err := handler.Start(); err != nil {
		t.Fatal(err)
	}
	if err := handler.Start(); err != nil {
		t.Fatalf("second Start = %v, want nil", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := handler.Start(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestCarrierHandoffWrapsLinkBidirectionally(t *testing.T) {
	callbackDone := make(chan struct{})
	handler := mustStartHandler(t, Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"carrier-in": RouteCarrier},
		Carrier: carrierFunc(func(_ context.Context, request CarrierRequest, conn net.Conn) error {
			defer close(callbackDone)
			if request.InboundTag != "carrier-in" || request.AuthenticatedUser != "node-a" {
				t.Fatalf("carrier request = %+v", request)
			}
			payload := make([]byte, len("from-xray"))
			if _, err := io.ReadFull(conn, payload); err != nil {
				return err
			}
			if string(payload) != "from-xray" {
				t.Fatalf("carrier read %q", payload)
			}
			_, err := conn.Write([]byte("from-carrier"))
			return err
		}),
	})

	link, client := newLinkPair()
	tracker := new(errorTracker)
	ctx := requestContext(context.Background(), "carrier-in", "node-a", tcpTarget("carrier.internal", 443), tracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(ctx, link)
		close(dispatchDone)
	}()

	mustWrite(t, client, []byte("from-xray"))
	if got := string(mustRead(t, client, len("from-carrier"))); got != "from-carrier" {
		t.Fatalf("client read %q, want from-carrier", got)
	}
	waitClosed(t, callbackDone, "carrier callback")
	waitClosed(t, dispatchDone, "carrier dispatch")
	if got := tracker.snapshot(); len(got) != 0 {
		t.Fatalf("originator errors = %v, want none", got)
	}
}

func TestUserEgressCopiesBidirectionallyAndPreservesRequest(t *testing.T) {
	bridgeSide, originSide := net.Pipe()
	requestSeen := make(chan UserRequest, 1)
	handler := mustStartHandler(t, Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"socks-auth": RouteUserEgress},
		UserEgress: userEgressFunc(func(_ context.Context, request UserRequest) (net.Conn, error) {
			requestSeen <- request
			return bridgeSide, nil
		}),
	})

	target := tcpTarget("edge-only.example.test", 8443)
	username := " user name "
	link, client := newLinkPair()
	tracker := new(errorTracker)
	ctx := requestContext(context.Background(), "socks-auth", username, target, tracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(ctx, link)
		close(dispatchDone)
	}()

	var gotRequest UserRequest
	select {
	case gotRequest = <-requestSeen:
	case <-time.After(testTimeout):
		t.Fatal("user egress callback was not invoked")
	}
	if gotRequest.InboundTag != "socks-auth" {
		t.Fatalf("InboundTag = %q", gotRequest.InboundTag)
	}
	if gotRequest.Username != username {
		t.Fatalf("Username = %q, want exact %q", gotRequest.Username, username)
	}
	if gotRequest.Target.String() != target.String() {
		t.Fatalf("Target = %s, want %s", gotRequest.Target, target)
	}

	mustWrite(t, client, []byte("request-body"))
	if got := string(mustRead(t, originSide, len("request-body"))); got != "request-body" {
		t.Fatalf("origin read %q", got)
	}
	mustWrite(t, originSide, []byte("response-body"))
	if got := string(mustRead(t, client, len("response-body"))); got != "response-body" {
		t.Fatalf("client read %q", got)
	}

	_ = originSide.Close()
	_ = client.Close()
	waitClosed(t, dispatchDone, "user dispatch")
	if got := tracker.snapshot(); len(got) != 0 {
		t.Fatalf("originator errors = %v, want none", got)
	}
}

func TestUserEgressPropagatesOriginCloseBeforeClientClose(t *testing.T) {
	bridgeSide, originSide := net.Pipe()
	handler := mustStartHandler(t, Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"socks-auth": RouteUserEgress},
		UserEgress: userEgressFunc(func(context.Context, UserRequest) (net.Conn, error) {
			return bridgeSide, nil
		}),
	})

	link, client := newLinkPair()
	tracker := new(errorTracker)
	ctx := requestContext(context.Background(), "socks-auth", "user", tcpTarget("origin.internal", 443), tracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(ctx, link)
		close(dispatchDone)
	}()

	mustWrite(t, client, []byte("request"))
	if got := string(mustRead(t, originSide, len("request"))); got != "request" {
		t.Fatalf("origin read %q", got)
	}
	mustWrite(t, originSide, []byte("response"))
	if err := originSide.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, client, len("response"))); got != "response" {
		t.Fatalf("client read %q", got)
	}

	readDone := make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := client.Read(one[:])
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("client terminal read = %v, want EOF", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("origin close was not propagated to the Xray client")
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, dispatchDone, "user dispatch after client close")
	if got := tracker.snapshot(); len(got) != 0 {
		t.Fatalf("originator errors = %v, want none", got)
	}
}

func TestCloseXrayLinkWriterUnwrapsBufferToBytesWriter(t *testing.T) {
	recorder := new(closeWriteRecorder)
	writer := &buf.BufferToBytesWriter{Writer: recorder}
	handled, err := closeXrayLinkWriter(writer)
	if err != nil || !handled || recorder.calls.Load() != 1 {
		t.Fatalf("handled=%v err=%v calls=%d", handled, err, recorder.calls.Load())
	}
}

func TestDirectionalLinkConnReportsUnsupportedHalfCloseFallback(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	conn := newDirectionalLinkConn(left, &transport.Link{Writer: buf.Discard})
	err := conn.CloseWrite()
	if !errors.Is(err, ErrHalfCloseUnsupported) {
		t.Fatalf("CloseWrite error = %v, want ErrHalfCloseUnsupported", err)
	}
	if normalized := normalizeCopyError(err); !errors.Is(normalized, ErrHalfCloseUnsupported) {
		t.Fatalf("normalizeCopyError(%v) = %v", err, normalized)
	}
	if _, err := right.Write([]byte("must fail after bounded fallback")); err == nil {
		t.Fatal("fallback did not close the full connection")
	}
}

func TestDispatchRejectsInvalidRequestsFailClosed(t *testing.T) {
	var callbacks atomic.Int64
	handler := mustStartHandler(t, Config{
		Tag: "bridge",
		Routes: map[string]RouteKind{
			"carrier-in": RouteCarrier,
			"user-in":    RouteUserEgress,
		},
		Carrier: carrierFunc(func(context.Context, CarrierRequest, net.Conn) error {
			callbacks.Add(1)
			return nil
		}),
		UserEgress: userEgressFunc(func(context.Context, UserRequest) (net.Conn, error) {
			callbacks.Add(1)
			return nil, nil
		}),
	})

	tests := []struct {
		name string
		ctx  func(*errorTracker) context.Context
		want error
	}{
		{
			name: "unknown inbound tag",
			ctx: func(tracker *errorTracker) context.Context {
				return requestContext(context.Background(), "unknown", "user", tcpTarget("example.test", 80), tracker)
			},
			want: ErrUnknownInbound,
		},
		{
			name: "UDP target",
			ctx: func(tracker *errorTracker) context.Context {
				return requestContext(context.Background(), "user-in", "user", xnet.UDPDestination(xnet.DomainAddress("example.test"), 53), tracker)
			},
			want: ErrUnsupportedUDP,
		},
		{
			name: "missing user",
			ctx: func(tracker *errorTracker) context.Context {
				return requestContextWithUser(context.Background(), "user-in", nil, tcpTarget("example.test", 80), tracker)
			},
			want: ErrUnauthenticated,
		},
		{
			name: "empty user",
			ctx: func(tracker *errorTracker) context.Context {
				return requestContext(context.Background(), "carrier-in", " \t", tcpTarget("example.test", 80), tracker)
			},
			want: ErrUnauthenticated,
		},
		{
			name: "invalid target",
			ctx: func(tracker *errorTracker) context.Context {
				return requestContext(context.Background(), "user-in", "user", xnet.Destination{}, tracker)
			},
			want: ErrInvalidTarget,
		},
		{
			name: "missing outbound metadata",
			ctx: func(tracker *errorTracker) context.Context {
				ctx := session.ContextWithInbound(context.Background(), &session.Inbound{
					Tag:  "user-in",
					User: &protocol.MemoryUser{Email: "user"},
				})
				return session.TrackedConnectionError(ctx, tracker)
			},
			want: ErrInvalidTarget,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link, client := newLinkPair()
			tracker := new(errorTracker)
			handler.Dispatch(test.ctx(tracker), link)
			assertTrackedError(t, tracker, test.want)
			assertConnectionClosed(t, client)
		})
	}
	if got := callbacks.Load(); got != 0 {
		t.Fatalf("callbacks invoked %d times for rejected requests", got)
	}
}

func TestDispatchRejectsInvalidLink(t *testing.T) {
	handler := mustStartHandler(t, Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"carrier-in": RouteCarrier},
		Carrier: carrierFunc(func(context.Context, CarrierRequest, net.Conn) error {
			t.Fatal("carrier callback invoked for invalid link")
			return nil
		}),
	})
	tracker := new(errorTracker)
	ctx := requestContext(context.Background(), "carrier-in", "node", tcpTarget("example.test", 80), tracker)
	handler.Dispatch(ctx, nil)
	assertTrackedError(t, tracker, ErrInvalidLink)
}

func TestContextCancellationTerminatesActiveCarrier(t *testing.T) {
	started := make(chan struct{})
	handler := mustStartHandler(t, Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"carrier-in": RouteCarrier},
		Carrier: carrierFunc(func(_ context.Context, _ CarrierRequest, conn net.Conn) error {
			close(started)
			buffer := make([]byte, 1)
			_, err := conn.Read(buffer)
			return err
		}),
	})

	parent, cancel := context.WithCancel(context.Background())
	link, client := newLinkPair()
	tracker := new(errorTracker)
	ctx := requestContext(parent, "carrier-in", "node", tcpTarget("example.test", 80), tracker)
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(ctx, link)
		close(dispatchDone)
	}()
	waitClosed(t, started, "carrier start")
	cancel()
	waitClosed(t, dispatchDone, "canceled dispatch")
	assertConnectionClosed(t, client)
	if got := tracker.snapshot(); len(got) != 0 {
		t.Fatalf("cancellation reported as outbound failure: %v", got)
	}
}

func TestCloseIsConcurrentWaitsForCallbacksAndBlocksDispatch(t *testing.T) {
	const activeCount = 12
	started := make(chan struct{}, activeCount)
	unblocked := make(chan struct{}, activeCount)
	releaseCallbacks := make(chan struct{})
	var callbackCount atomic.Int64
	handler := mustStartHandler(t, Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"carrier-in": RouteCarrier},
		Carrier: carrierFunc(func(_ context.Context, _ CarrierRequest, conn net.Conn) error {
			callbackCount.Add(1)
			started <- struct{}{}
			buffer := make([]byte, 1)
			_, err := conn.Read(buffer)
			unblocked <- struct{}{}
			<-releaseCallbacks
			return err
		}),
	})

	dispatchDone := make(chan struct{}, activeCount)
	clients := make([]net.Conn, 0, activeCount)
	for index := 0; index < activeCount; index++ {
		link, client := newLinkPair()
		clients = append(clients, client)
		ctx := requestContext(context.Background(), "carrier-in", "node", tcpTarget("example.test", 80), new(errorTracker))
		go func() {
			handler.Dispatch(ctx, link)
			dispatchDone <- struct{}{}
		}()
	}
	for index := 0; index < activeCount; index++ {
		waitSignal(t, started, "carrier start")
	}

	const closerCount = 8
	closeResults := make(chan error, closerCount)
	for index := 0; index < closerCount; index++ {
		go func() { closeResults <- handler.Close() }()
	}
	for index := 0; index < activeCount; index++ {
		waitSignal(t, unblocked, "carrier unblock")
	}
	select {
	case err := <-closeResults:
		t.Fatalf("Close returned before callback completion: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCallbacks)

	for index := 0; index < activeCount; index++ {
		waitSignal(t, dispatchDone, "dispatch completion")
	}
	for index := 0; index < closerCount; index++ {
		select {
		case err := <-closeResults:
			if err != nil {
				t.Fatalf("Close = %v", err)
			}
		case <-time.After(testTimeout):
			t.Fatal("concurrent Close did not return")
		}
	}

	for _, client := range clients {
		_ = client.Close()
	}
	if got := callbackCount.Load(); got != activeCount {
		t.Fatalf("callback count = %d, want %d", got, activeCount)
	}

	link, client := newLinkPair()
	tracker := new(errorTracker)
	ctx := requestContext(context.Background(), "carrier-in", "node", tcpTarget("example.test", 80), tracker)
	handler.Dispatch(ctx, link)
	assertTrackedError(t, tracker, ErrClosed)
	assertConnectionClosed(t, client)
	if got := callbackCount.Load(); got != activeCount {
		t.Fatalf("post-close dispatch invoked callback; count = %d", got)
	}
}

func TestCloseWinsDialReturnRaceAndClosesLateConnection(t *testing.T) {
	dialStarted := make(chan struct{})
	lateConnectionReturned := make(chan struct{})
	bridgeSide, remoteSide := net.Pipe()
	handler := mustStartHandler(t, Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"user-in": RouteUserEgress},
		UserEgress: userEgressFunc(func(ctx context.Context, _ UserRequest) (net.Conn, error) {
			close(dialStarted)
			<-ctx.Done()
			close(lateConnectionReturned)
			return bridgeSide, nil
		}),
	})

	link, client := newLinkPair()
	ctx := requestContext(context.Background(), "user-in", "user", tcpTarget("example.test", 80), new(errorTracker))
	dispatchDone := make(chan struct{})
	go func() {
		handler.Dispatch(ctx, link)
		close(dispatchDone)
	}()
	waitClosed(t, dialStarted, "dial start")
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, lateConnectionReturned, "late dial return")
	waitClosed(t, dispatchDone, "late dial dispatch")
	assertConnectionClosed(t, remoteSide)
	assertConnectionClosed(t, client)
}

func TestDispatchBeforeStartFailsClosed(t *testing.T) {
	handler, err := New(Config{
		Tag:    "bridge",
		Routes: map[string]RouteKind{"carrier-in": RouteCarrier},
		Carrier: carrierFunc(func(context.Context, CarrierRequest, net.Conn) error {
			t.Fatal("callback invoked before Start")
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	link, client := newLinkPair()
	tracker := new(errorTracker)
	ctx := requestContext(context.Background(), "carrier-in", "node", tcpTarget("example.test", 80), tracker)
	handler.Dispatch(ctx, link)
	assertTrackedError(t, tracker, ErrNotRunning)
	assertConnectionClosed(t, client)
}

func mustStartHandler(t *testing.T, config Config) *Handler {
	t.Helper()
	handler, err := New(config)
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

func requestContext(parent context.Context, inboundTag, username string, target xnet.Destination, tracker *errorTracker) context.Context {
	return requestContextWithUser(parent, inboundTag, &protocol.MemoryUser{Email: username}, target, tracker)
}

func requestContextWithUser(parent context.Context, inboundTag string, user *protocol.MemoryUser, target xnet.Destination, tracker *errorTracker) context.Context {
	ctx := session.ContextWithInbound(parent, &session.Inbound{Tag: inboundTag, User: user})
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
	return session.TrackedConnectionError(ctx, tracker)
}

func tcpTarget(domain string, port xnet.Port) xnet.Destination {
	return xnet.TCPDestination(xnet.DomainAddress(domain), port)
}

func newLinkPair() (*transport.Link, net.Conn) {
	uplinkReader, uplinkWriter := pipe.New()
	downlinkReader, downlinkWriter := pipe.New()
	link := &transport.Link{Reader: uplinkReader, Writer: downlinkWriter}
	client := cnc.NewConnection(
		cnc.ConnectionInputMulti(uplinkWriter),
		cnc.ConnectionOutputMulti(downlinkReader),
	)
	return link, client
}

func mustWrite(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Write timed out")
	}
}

func mustRead(t *testing.T, conn net.Conn, size int) []byte {
	t.Helper()
	type result struct {
		payload []byte
		err     error
	}
	results := make(chan result, 1)
	go func() {
		payload := make([]byte, size)
		_, err := io.ReadFull(conn, payload)
		results <- result{payload: payload, err: err}
	}()
	select {
	case got := <-results:
		if got.err != nil {
			t.Fatalf("ReadFull: %v", got.err)
		}
		return got.payload
	case <-time.After(testTimeout):
		t.Fatal("ReadFull timed out")
		return nil
	}
}

func assertTrackedError(t *testing.T, tracker *errorTracker, want error) {
	t.Helper()
	errs := tracker.snapshot()
	if len(errs) != 1 || !errors.Is(errs[0], want) {
		t.Fatalf("tracked errors = %v, want one matching %v", errs, want)
	}
}

func assertConnectionClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	result := make(chan error, 1)
	go func() {
		_, err := conn.Write([]byte{1})
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("connection write succeeded after fail-closed dispatch")
		}
	case <-time.After(testTimeout):
		t.Fatal("connection remained open after fail-closed dispatch")
	}
}

func waitClosed(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitSignal(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	waitClosed(t, channel, description)
}
