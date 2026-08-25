package rendradapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/FrankoonG/rendr"
)

func TestRuntimeCarriesTargetAndBidirectionalBytes(t *testing.T) {
	echo := startEcho(t)
	server := newTestRuntime(t)
	client := newTestRuntime(t)
	wireRuntimes(t, client, server, func(ctx context.Context, host string, port uint16) (net.Conn, error) {
		if got := net.JoinHostPort(host, itoa(port)); got != echo.Addr().String() {
			t.Fatalf("egress target = %s, want %s", got, echo.Addr())
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
	})

	conn, err := client.Dial(context.Background(), "peer-b", destinationFromAddr(t, echo.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	want := sha256.Sum256(payload)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(got); sum != want {
		t.Fatalf("payload hash = %x, want %x", sum, want)
	}
	if status := client.Status(); status.TotalClient != 1 || status.ActiveClient != 1 || status.PacketSupported ||
		status.StreamCarrier != "unknown" || status.MobilityMode != "redial_attach" || status.EndpointOwned {
		t.Fatalf("client status = %+v", status)
	}
}

func TestRuntimeReportsSafeTerminalDialFailure(t *testing.T) {
	server := newTestRuntime(t)
	client := newTestRuntime(t)
	const (
		targetHost = "only-b.test"
		secret     = "credential=do-not-disclose"
	)
	want := errors.New("dial " + targetHost + " denied: " + secret)
	wireRuntimes(t, client, server, func(context.Context, string, uint16) (net.Conn, error) {
		return nil, want
	})
	_, err := client.Dial(context.Background(), "peer-b", Destination{Host: targetHost, Port: 443})
	if !errors.Is(err, errTerminalUnavailable) {
		t.Fatalf("Dial error = %v, want terminal unavailable", err)
	}
	for _, sensitive := range []string{targetHost, secret, want.Error()} {
		if contains(err.Error(), sensitive) {
			t.Fatalf("Dial error leaked %q: %v", sensitive, err)
		}
	}

	clientStatus := client.Status()
	if clientStatus.LastError != RuntimeStatusErrorEgressUnavailable {
		t.Fatalf("client LastError = %q, want %q", clientStatus.LastError, RuntimeStatusErrorEgressUnavailable)
	}
	serverStatus := waitRuntimeStatus(t, server, func(status RuntimeStatus) bool {
		return status.LastError != ""
	})
	if serverStatus.LastError != RuntimeStatusErrorEgressUnavailable {
		t.Fatalf("server LastError = %q, want %q", serverStatus.LastError, RuntimeStatusErrorEgressUnavailable)
	}
	for _, status := range []RuntimeStatus{clientStatus, serverStatus} {
		if contains(status.LastError, targetHost) || contains(status.LastError, secret) {
			t.Fatalf("status leaked sensitive terminal failure: %+v", status)
		}
	}
}

func TestRuntimeReportsFailedWhenAcceptLoopTerminates(t *testing.T) {
	runtime := newTestRuntime(t)
	if err := runtime.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	status := waitRuntimeStatus(t, runtime, func(status RuntimeStatus) bool {
		return status.State == "failed"
	})
	if status.LastError != RuntimeStatusErrorCarrier {
		t.Fatalf("failed accept status=%+v", status)
	}
}

func TestAcceptedSessionOpenHandshakeHasDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	left, right := net.Pipe()
	defer right.Close()
	runtime := &Runtime{ctx: ctx, handshakeTimeout: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- runtime.serveAccepted(fakeRendrConn{Conn: left}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled open handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("stalled open handshake was not bounded")
	}
}

func TestOpenSessionUsesRuntimeHandshakeTimeout(t *testing.T) {
	const timeout = 40 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	left, right := net.Pipe()
	defer right.Close()
	runtime := &Runtime{ctx: ctx, handshakeTimeout: timeout}

	readOpenDone := make(chan error, 1)
	go func() {
		_, err := readOpen(right)
		readOpenDone <- err
	}()
	started := time.Now()
	err := runtime.openSession(context.Background(), left, Destination{Host: "timeout.test", Port: 443})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("openSession error = %v, want context deadline exceeded", err)
	}
	if category := categoryOf(err); category != runtimeErrorHandshakeTimeout {
		t.Fatalf("openSession category = %v, want handshake timeout", category)
	}
	if elapsed < timeout/2 || elapsed > 500*time.Millisecond {
		t.Fatalf("openSession elapsed = %s, configured timeout = %s", elapsed, timeout)
	}
	select {
	case err := <-readOpenDone:
		if err != nil {
			t.Fatalf("readOpen: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("open frame reader did not finish")
	}
}

type fakeRendrConn struct{ net.Conn }

func (fakeRendrConn) Paths() []rendr.PathInfo { return nil }
func (fakeRendrConn) FlowID() [16]byte        { return [16]byte{} }
func (fakeRendrConn) Status() rendr.Status    { return rendr.Status{} }

func TestRuntimeDialCancellationInterruptsOpenHandshake(t *testing.T) {
	server := newTestRuntime(t)
	client := newTestRuntime(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	wireRuntimes(t, client, server, func(context.Context, string, uint16) (net.Conn, error) {
		close(entered)
		<-release
		return nil, errors.New("blocked terminal dial released")
	})

	ctx, cancel := context.WithCancel(context.Background())
	dialed := make(chan error, 1)
	go func() {
		_, err := client.Dial(ctx, "peer-b", Destination{Host: "blocked.test", Port: 443})
		dialed <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal dial was not reached")
	}
	cancel()
	select {
	case err := <-dialed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Dial error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Dial did not stop after its context was canceled")
	}
	if status := client.Status(); status.ActiveClient != 0 || status.TotalClient != 0 {
		t.Fatalf("client status after canceled open = %+v", status)
	}
}

func TestRuntimeCloseClosesActiveClientSessions(t *testing.T) {
	echo := startEcho(t)
	server := newTestRuntime(t)
	client := newTestRuntime(t)
	wireRuntimes(t, client, server, func(ctx context.Context, _ string, _ uint16) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
	})

	conn, err := client.Dial(context.Background(), "peer-b", destinationFromAddr(t, echo.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	if status := client.Status(); status.ActiveClient != 1 {
		t.Fatalf("active clients before Close = %d, want 1", status.ActiveClient)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if status := client.Status(); status.State != "stopped" || status.ActiveClient != 0 {
		t.Fatalf("client status after Close = %+v", status)
	}
	if _, err := conn.Write([]byte("closed")); err == nil {
		t.Fatal("write on a session owned by a closed runtime succeeded")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second session Close: %v", err)
	}
}

func TestRuntimeCloseContextBoundsBrokenConnectionClose(t *testing.T) {
	runtime, err := NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	defer right.Close()
	release := make(chan struct{})
	entered := make(chan struct{})
	blocking := &blockingCloseConn{Conn: left, release: release, entered: entered}
	tracked := &trackedConn{Conn: blocking}
	tracked.release = func() { runtime.releaseClient(tracked) }
	runtime.clientsMu.Lock()
	runtime.clients[tracked] = struct{}{}
	runtime.clientsWG.Add(1)
	runtime.activeClient.Add(1)
	runtime.clientsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	started := time.Now()
	err = runtime.CloseContext(ctx)
	cancel()
	if !errors.Is(err, ErrShutdownIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext error = %v, want incomplete deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("CloseContext exceeded its deadline by too much: %s", elapsed)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("broken connection Close was not attempted")
	}
	if err := runtime.ForceClose(); !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("ForceClose error = %v, want incomplete shutdown", err)
	}
	close(release)
	select {
	case <-runtime.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not finish after broken Close was released")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("completed Close: %v", err)
	}
}

func TestOpenProtocolRejectsInvalidAndOversizedFrames(t *testing.T) {
	if err := (Destination{Host: "", Port: 80}).Validate(); err == nil {
		t.Fatal("empty host accepted")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	go func() {
		header := make([]byte, 12)
		copy(header[:4], openMagic[:])
		header[4] = openVersion
		header[5] = openNetworkTCP
		header[10] = 1
		header[11] = 254
		_, _ = left.Write(header)
	}()
	if _, err := readOpen(right); err == nil {
		t.Fatal("oversized target was accepted")
	}
}

func TestAckUsesFixedSafeClassifications(t *testing.T) {
	cases := []struct {
		name    string
		code    ackCode
		message string
		wantErr error
	}{
		{name: "success", code: ackOK, message: ""},
		{name: "invalid request", code: ackInvalidRequest, message: "invalid request", wantErr: errTerminalInvalidRequest},
		{name: "egress unavailable", code: ackEgressUnavailable, message: "egress unavailable", wantErr: errTerminalUnavailable},
		{name: "egress timeout", code: ackEgressTimeout, message: "egress timeout", wantErr: errTerminalTimeout},
		{name: "session failure", code: ackSessionFailure, message: "session failed", wantErr: errTerminalSessionFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var frame bytes.Buffer
			if err := writeAck(&frame, tc.code); err != nil {
				t.Fatal(err)
			}
			payload := frame.Bytes()
			if len(payload) != 8+len(tc.message) || ackCode(payload[4]) != tc.code || string(payload[8:]) != tc.message {
				t.Fatalf("ACK frame = %x, code/message = %d/%q", payload, tc.code, tc.message)
			}
			err := readAck(bytes.NewReader(payload))
			if tc.wantErr == nil && err != nil {
				t.Fatalf("readAck: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("readAck error = %v, want %v", err, tc.wantErr)
			}
		})
	}

	const untrusted = "target=private.example credential=super-secret"
	header := make([]byte, 8)
	copy(header[:4], ackMagic[:])
	header[4] = byte(ackEgressUnavailable)
	binary.BigEndian.PutUint16(header[6:8], uint16(len(untrusted)))
	frame := append(header, []byte(untrusted)...)
	err := readAck(bytes.NewReader(frame))
	if !errors.Is(err, errInvalidAck) {
		t.Fatalf("untrusted ACK error = %v, want invalid acknowledgement", err)
	}
	if contains(err.Error(), untrusted) || contains(err.Error(), "super-secret") {
		t.Fatalf("untrusted ACK text was reflected: %v", err)
	}
}

func TestRuntimeStatusErrorCategoriesAreStable(t *testing.T) {
	cases := []struct {
		category runtimeErrorCategory
		want     string
	}{
		{category: runtimeErrorCanceled, want: RuntimeStatusErrorCanceled},
		{category: runtimeErrorCarrier, want: RuntimeStatusErrorCarrier},
		{category: runtimeErrorHandshakeTimeout, want: RuntimeStatusErrorHandshakeTimeout},
		{category: runtimeErrorProtocol, want: RuntimeStatusErrorProtocol},
		{category: runtimeErrorEgressUnavailable, want: RuntimeStatusErrorEgressUnavailable},
		{category: runtimeErrorEgressTimeout, want: RuntimeStatusErrorEgressTimeout},
		{category: runtimeErrorStream, want: RuntimeStatusErrorStream},
		{category: runtimeErrorInternal, want: RuntimeStatusErrorInternal},
		{category: runtimeErrorCategory(255), want: RuntimeStatusErrorInternal},
	}
	seen := make(map[string]struct{}, len(cases))
	for _, tc := range cases {
		got := tc.category.statusMessage()
		if got != tc.want {
			t.Fatalf("category %d message = %q, want %q", tc.category, got, tc.want)
		}
		if tc.category != runtimeErrorCategory(255) {
			if _, duplicate := seen[got]; duplicate {
				t.Fatalf("duplicate status category message %q", got)
			}
			seen[got] = struct{}{}
		}
	}
}

func TestRendrDialErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want runtimeErrorCategory
	}{
		{name: "carrier", err: errors.New("dial failed"), want: runtimeErrorCarrier},
		{name: "protocol version", err: fmt.Errorf("peer rejected: %w", rendr.ErrPeerProtoVersion), want: runtimeErrorProtocol},
		{name: "peer protocol", err: rendr.ErrPeerProtocol, want: runtimeErrorProtocol},
		{name: "canceled", err: context.Canceled, want: runtimeErrorCanceled},
		{name: "deadline", err: context.DeadlineExceeded, want: runtimeErrorCanceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRendrDialError(tc.err); got != tc.want {
				t.Fatalf("category = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestActionableCloseErrorPreservesHardJoinedFailure(t *testing.T) {
	hard := errors.New("hard teardown failure")
	err := fmt.Errorf("outer close: %w", errors.Join(
		fmt.Errorf("graceful: %w", rendr.ErrGracefulCloseTimeout),
		fmt.Errorf("closed: %w", net.ErrClosed),
		fmt.Errorf("actionable: %w", hard),
	))
	filtered := actionableCloseError(err)
	if !errors.Is(filtered, hard) {
		t.Fatalf("filtered error = %v, want hard teardown failure", filtered)
	}
	if errors.Is(filtered, rendr.ErrGracefulCloseTimeout) || errors.Is(filtered, net.ErrClosed) {
		t.Fatalf("filtered error retained benign leaves: %v", filtered)
	}
	if got := actionableCloseError(errors.Join(rendr.ErrGracefulCloseTimeout, net.ErrClosed)); got != nil {
		t.Fatalf("all-benign close error = %v, want nil", got)
	}
}

func TestRuntimeStatusRedactsCarrierError(t *testing.T) {
	runtime := newTestRuntime(t)
	const secret = "peer-token=do-not-disclose"
	if err := runtime.SetDialers(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("carrier authentication failed: " + secret)
	}, func(context.Context, string, uint16) (net.Conn, error) {
		return nil, errors.New("unused")
	}); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.Dial(context.Background(), "peer-secret", Destination{Host: "private.test", Port: 443})
	if err == nil {
		t.Fatal("Dial succeeded")
	}
	status := runtime.Status()
	if status.LastError != RuntimeStatusErrorCarrier {
		t.Fatalf("LastError = %q, want %q", status.LastError, RuntimeStatusErrorCarrier)
	}
	if contains(status.LastError, secret) || contains(status.LastError, "peer-secret") || contains(status.LastError, "private.test") {
		t.Fatalf("LastError leaked connection details: %q", status.LastError)
	}
}

func TestProxyStreamHardErrorClosesBothAndWaits(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "arbitrary hard error", err: errors.New("injected hard read failure")},
		{name: "first closed error", err: net.ErrClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leftPipe, leftPeer := net.Pipe()
			rightPipe, rightPeer := net.Pipe()
			defer leftPeer.Close()
			defer rightPeer.Close()
			left := newReadErrorConn(leftPipe, tc.err)
			right := newObservedReadConn(rightPipe)

			done := make(chan error, 1)
			go func() { done <- proxyStream(context.Background(), left, right) }()
			select {
			case err := <-done:
				if !errors.Is(err, tc.err) {
					t.Fatalf("proxyStream error = %v, want %v", err, tc.err)
				}
			case <-time.After(time.Second):
				t.Fatal("proxyStream did not wake its blocked copy direction")
			}
			for name, signal := range map[string]<-chan struct{}{
				"left close":      left.closed,
				"right close":     right.closed,
				"right read exit": right.readExited,
			} {
				select {
				case <-signal:
				default:
					t.Fatalf("proxyStream returned before %s", name)
				}
			}
		})
	}
}

func TestProxyStreamPreservesBidirectionalHalfClose(t *testing.T) {
	left, leftPeer := tcpConnPair(t)
	right, rightPeer := tcpConnPair(t)
	deadline := time.Now().Add(2 * time.Second)
	for _, conn := range []*net.TCPConn{left, leftPeer, right, rightPeer} {
		if err := conn.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 1)
	go func() { done <- proxyStream(context.Background(), left, right) }()
	request := []byte("request-before-half-close")
	if _, err := leftPeer.Write(request); err != nil {
		t.Fatal(err)
	}
	if err := leftPeer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	gotRequest, err := io.ReadAll(rightPeer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotRequest, request) {
		t.Fatalf("request = %q, want %q", gotRequest, request)
	}

	response := []byte("response-after-request-eof")
	if _, err := rightPeer.Write(response); err != nil {
		t.Fatal(err)
	}
	if err := rightPeer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	gotResponse, err := io.ReadAll(leftPeer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotResponse, response) {
		t.Fatalf("response = %q, want %q", gotResponse, response)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("proxyStream: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxyStream did not finish after both half-closes")
	}
}

func TestProxyStreamRejectsEndpointWithoutHalfClose(t *testing.T) {
	left, leftPeer := net.Pipe()
	right, rightPeer := net.Pipe()
	defer leftPeer.Close()
	defer rightPeer.Close()
	done := make(chan error, 1)
	go func() { done <- proxyStream(context.Background(), left, right) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrHalfCloseUnsupported) {
			t.Fatalf("proxyStream error = %v, want ErrHalfCloseUnsupported", err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxyStream did not reject missing CloseWrite support")
	}
}

func TestRuntimeHardProxyErrorReleasesAcceptedSlot(t *testing.T) {
	server := newTestRuntime(t)
	client := newTestRuntime(t)
	hardErr := errors.New("write failure credential=do-not-disclose")
	upstream := newBlockingWriteErrorConn(hardErr)
	wireRuntimes(t, client, server, func(context.Context, string, uint16) (net.Conn, error) {
		return upstream, nil
	})
	waitRuntimeStatus(t, server, func(status RuntimeStatus) bool {
		return status.ActiveAccepted == 0 && len(server.acceptedSlots) == 1
	})
	baselineSlots := len(server.acceptedSlots)

	conn, err := client.Dial(context.Background(), "peer-b", Destination{Host: "slot.test", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("trigger hard proxy error")); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("trigger write: %v", err)
	}
	select {
	case <-upstream.writeEntered:
	case <-time.After(time.Second):
		t.Fatal("terminal write was not attempted")
	}
	status := waitRuntimeStatus(t, server, func(status RuntimeStatus) bool {
		return status.ActiveAccepted == 0 && status.LastError != ""
	})
	if status.LastError != RuntimeStatusErrorStream {
		t.Fatalf("LastError = %q, want %q", status.LastError, RuntimeStatusErrorStream)
	}
	if len(server.acceptedSlots) != baselineSlots {
		t.Fatalf("accepted slots in use = %d, want idle baseline %d", len(server.acceptedSlots), baselineSlots)
	}
	if contains(status.LastError, "credential") || contains(status.LastError, "slot.test") {
		t.Fatalf("LastError leaked proxy failure: %q", status.LastError)
	}
}

type readErrorConn struct {
	net.Conn
	err       error
	closeOnce sync.Once
	closed    chan struct{}
}

func newReadErrorConn(conn net.Conn, err error) *readErrorConn {
	return &readErrorConn{Conn: conn, err: err, closed: make(chan struct{})}
}

func (c *readErrorConn) Read([]byte) (int, error) { return 0, c.err }

func (c *readErrorConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (*readErrorConn) CloseWrite() error { return nil }

type observedReadConn struct {
	net.Conn
	closeOnce    sync.Once
	readExitOnce sync.Once
	closed       chan struct{}
	readExited   chan struct{}
}

func newObservedReadConn(conn net.Conn) *observedReadConn {
	return &observedReadConn{Conn: conn, closed: make(chan struct{}), readExited: make(chan struct{})}
}

func (c *observedReadConn) Read(payload []byte) (int, error) {
	n, err := c.Conn.Read(payload)
	c.readExitOnce.Do(func() { close(c.readExited) })
	return n, err
}

func (c *observedReadConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func (*observedReadConn) CloseWrite() error { return nil }

type blockingWriteErrorConn struct {
	err          error
	closeOnce    sync.Once
	writeOnce    sync.Once
	closed       chan struct{}
	writeEntered chan struct{}
}

type blockingCloseConn struct {
	net.Conn
	release   <-chan struct{}
	entered   chan struct{}
	enterOnce sync.Once
}

func (c *blockingCloseConn) Close() error {
	c.enterOnce.Do(func() { close(c.entered) })
	<-c.release
	return c.Conn.Close()
}

func newBlockingWriteErrorConn(err error) *blockingWriteErrorConn {
	return &blockingWriteErrorConn{
		err:          err,
		closed:       make(chan struct{}),
		writeEntered: make(chan struct{}),
	}
}

func (c *blockingWriteErrorConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *blockingWriteErrorConn) Write([]byte) (int, error) {
	c.writeOnce.Do(func() { close(c.writeEntered) })
	return 0, c.err
}

func (c *blockingWriteErrorConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (*blockingWriteErrorConn) CloseWrite() error { return nil }

func (*blockingWriteErrorConn) LocalAddr() net.Addr              { return testAddr("local") }
func (*blockingWriteErrorConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (*blockingWriteErrorConn) SetDeadline(time.Time) error      { return nil }
func (*blockingWriteErrorConn) SetReadDeadline(time.Time) error  { return nil }
func (*blockingWriteErrorConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (address testAddr) Network() string { return "test" }
func (address testAddr) String() string  { return string(address) }

func tcpConnPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan *net.TCPConn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	peer, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	var proxy *net.TCPConn
	select {
	case proxy = <-accepted:
	case err := <-acceptErr:
		_ = peer.Close()
		_ = listener.Close()
		t.Fatal(err)
	case <-time.After(time.Second):
		_ = peer.Close()
		_ = listener.Close()
		t.Fatal("TCP pair accept timed out")
	}
	if err := listener.Close(); err != nil {
		_ = proxy.Close()
		_ = peer.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proxy.Close()
		_ = peer.Close()
	})
	return proxy, peer
}

func waitRuntimeStatus(t *testing.T, runtime *Runtime, condition func(RuntimeStatus) bool) RuntimeStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := runtime.Status()
		if condition(status) {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime status condition not reached: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return runtime
}

func wireRuntimes(t *testing.T, client, server *Runtime, egress EgressDialer) {
	t.Helper()
	stream := func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		left, right := net.Pipe()
		accepted := make(chan error, 1)
		go func() { accepted <- server.InjectCarrier(ctx, right) }()
		select {
		case err := <-accepted:
			if err != nil {
				_ = left.Close()
				_ = right.Close()
				return nil, err
			}
			return left, nil
		case <-ctx.Done():
			_ = left.Close()
			_ = right.Close()
			return nil, ctx.Err()
		}
	}
	if err := client.SetDialers(stream, func(context.Context, string, uint16) (net.Conn, error) {
		return nil, errors.New("client egress must not be used")
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetDialers(stream, egress); err != nil {
		t.Fatal(err)
	}
}

func startEcho(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener
}

func destinationFromAddr(t *testing.T, address string) Destination {
	t.Helper()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	value, err := net.LookupPort("tcp", port)
	if err != nil {
		t.Fatal(err)
	}
	return Destination{Host: host, Port: uint16(value)}
}

func itoa(port uint16) string { return net.JoinHostPort("", "")[1:1] + fmtUint(port) }

func fmtUint(port uint16) string {
	var buffer [5]byte
	end := len(buffer)
	value := int(port)
	for value > 0 {
		end--
		buffer[end] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[end:])
}

func contains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}

func TestRuntimeCloseIsIdempotent(t *testing.T) {
	runtime, err := NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- runtime.Close() }()
	go func() { done <- runtime.Close() }()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close timed out")
		}
	}
}
