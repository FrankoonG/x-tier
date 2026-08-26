package rendradapter

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/rendr"
)

const (
	xrayStreamFactory    = "xray-stream"
	carrierTarget        = "127.0.0.1:1"
	openVersion          = 1
	openNetworkTCP       = 1
	maxTargetHost        = 253
	maxOpenError         = 1024
	openHandshakeTimeout = 15 * time.Second
	ackWriteTimeout      = time.Second
	runtimeCloseTimeout  = 5 * time.Second
	MaxAcceptedSessions  = 512
	maxClaimField        = 1024
)

const (
	RuntimeStatusErrorCanceled          = "operation_canceled"
	RuntimeStatusErrorCarrier           = "carrier_failure"
	RuntimeStatusErrorHandshakeTimeout  = "handshake_timeout"
	RuntimeStatusErrorProtocol          = "protocol_failure"
	RuntimeStatusErrorEgressUnavailable = "egress_unavailable"
	RuntimeStatusErrorEgressTimeout     = "egress_timeout"
	RuntimeStatusErrorStream            = "stream_failure"
	RuntimeStatusErrorInternal          = "internal_failure"
)

type runtimeErrorCategory uint8

const (
	runtimeErrorInternal runtimeErrorCategory = iota
	runtimeErrorCanceled
	runtimeErrorCarrier
	runtimeErrorHandshakeTimeout
	runtimeErrorProtocol
	runtimeErrorEgressUnavailable
	runtimeErrorEgressTimeout
	runtimeErrorStream
)

type ackCode byte

const (
	ackOK ackCode = iota
	ackInvalidRequest
	ackEgressUnavailable
	ackEgressTimeout
	ackSessionFailure
)

var (
	openMagic = [4]byte{'X', 'T', 'O', '1'}
	ackMagic  = [4]byte{'X', 'T', 'A', '1'}

	ErrHalfCloseUnsupported   = errors.New("rendradapter: directional half-close is unsupported")
	ErrShutdownIncomplete     = errors.New("rendradapter: runtime shutdown incomplete")
	errInvalidAck             = errors.New("rendradapter: invalid egress acknowledgement")
	errTerminalInvalidRequest = errors.New("rendradapter: terminal rejected session request")
	errTerminalUnavailable    = errors.New("rendradapter: terminal egress unavailable")
	errTerminalTimeout        = errors.New("rendradapter: terminal egress timeout")
	errTerminalSessionFailure = errors.New("rendradapter: terminal session failed")
)

type categorizedError struct {
	category runtimeErrorCategory
	cause    error
}

func (e *categorizedError) Error() string { return e.cause.Error() }
func (e *categorizedError) Unwrap() error { return e.cause }

// StreamDialer opens one ordered carrier stream through a logical Xray peer
// outbound tag. innerTarget is X-Tier-owned and must not be user-controlled.
type StreamDialer func(ctx context.Context, peerTag, innerTarget string) (net.Conn, error)

// OriginClaim is the Xray bearer claim bound to the initial carrier path. It is
// not a NodeSession proof and must not be promoted to a cryptographic NodeID.
const OriginAssuranceXrayBearer = "xray_bearer"

type OriginClaim struct {
	Assurance         string
	ClaimedPeerNodeID string
	InboundTag        string
	PrincipalHandle   string
}

func (c OriginClaim) Validate() error {
	if c.Assurance != OriginAssuranceXrayBearer {
		return errors.New("rendradapter: unsupported origin assurance")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "claimed peer", value: c.ClaimedPeerNodeID},
		{name: "inbound tag", value: c.InboundTag},
		{name: "principal", value: c.PrincipalHandle},
	} {
		if field.value == "" || len(field.value) > maxClaimField || strings.TrimSpace(field.value) != field.value || strings.ContainsAny(field.value, "\x00\r\n") {
			return fmt.Errorf("rendradapter: invalid %s claim", field.name)
		}
	}
	return nil
}

type FlowClaim struct {
	Origin         OriginClaim
	FlowID         [16]byte
	PeerInstanceID PeerInstanceID
}

type PeerInstanceID [16]byte

func (c FlowClaim) Validate() error {
	if err := c.Origin.Validate(); err != nil {
		return err
	}
	if c.FlowID == ([16]byte{}) {
		return errors.New("rendradapter: invalid flow claim ID")
	}
	if c.PeerInstanceID == (PeerInstanceID{}) {
		return errors.New("rendradapter: invalid flow peer instance ID")
	}
	return nil
}

type EgressRequest struct {
	Claim       FlowClaim
	Destination Destination
}

func (r EgressRequest) Validate() error {
	if err := r.Claim.Validate(); err != nil {
		return err
	}
	return r.Destination.Validate()
}

// EgressDialer opens the final TCP connection on the accepting node. The
// destination remains a domain until this callback, so the terminal node owns
// resolution and authorization before handing a frozen literal to its dialer.
type EgressDialer func(ctx context.Context, request EgressRequest) (net.Conn, error)

type Destination struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

func (d Destination) Validate() error {
	if d.Host == "" || len(d.Host) > maxTargetHost || strings.ContainsAny(d.Host, "\x00\r\n") {
		return errors.New("rendradapter: invalid target host")
	}
	if d.Port == 0 {
		return errors.New("rendradapter: invalid target port")
	}
	return nil
}

func (d Destination) Address() string {
	return net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port)))
}

type RuntimeStatus struct {
	State           string `json:"state"`
	ActiveClient    int64  `json:"active_client_sessions"`
	ActiveAccepted  int64  `json:"active_accepted_sessions"`
	AcceptedFlowIDs int    `json:"accepted_flow_ids"`
	TotalClient     uint64 `json:"total_client_sessions"`
	TotalAccepted   uint64 `json:"total_accepted_sessions"`
	// LastError is empty or one of the RuntimeStatusError constants. It never
	// contains an underlying transport, target, or credential-bearing error.
	LastError        string    `json:"last_error,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
	StreamFactory    string    `json:"stream_factory"`
	StreamCarrier    string    `json:"stream_carrier"`
	MobilityMode     string    `json:"mobility_mode"`
	EndpointOwned    bool      `json:"endpoint_owned"`
	PacketSupported  bool      `json:"packet_supported"`
	InstanceID       string    `json:"instance_id,omitempty"`
	InstanceIDSource string    `json:"instance_id_source,omitempty"`
}

// Runtime owns exactly one rendr.Runtime for one xtierd process.
type Runtime struct {
	ctx      context.Context
	cancel   context.CancelFunc
	runtime  *rendr.Runtime
	source   *carrierListener
	listener *rendr.SessionListener

	dialMu       sync.RWMutex
	streamDialer StreamDialer
	egressDialer EgressDialer

	activeClient   atomic.Int64
	activeAccepted atomic.Int64
	totalClient    atomic.Uint64
	totalAccepted  atomic.Uint64

	errMu     sync.RWMutex
	lastError string

	clientsMu        sync.Mutex
	clients          map[*trackedConn]struct{}
	clientsWG        sync.WaitGroup
	dialWG           sync.WaitGroup
	admissionMu      sync.Mutex
	activeSessions   int64
	sessionsDrained  bool
	sessionsClosed   chan struct{}
	sessionCloseWG   sync.WaitGroup
	closing          atomic.Bool
	acceptFailed     atomic.Bool
	acceptedSlots    chan struct{}
	handshakeTimeout time.Duration
	claimMu          sync.Mutex
	pendingClaims    map[string]OriginClaim

	closeOnce sync.Once
	closed    chan struct{}
	wg        sync.WaitGroup
	closeErr  error
}

func NewRuntime(ctx context.Context) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("rendradapter: nil runtime context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeCtx, cancel := context.WithCancel(ctx)
	rr, err := rendr.NewRuntimeContext(runtimeCtx, rendr.DefaultRuntimeConfig())
	if err != nil {
		cancel()
		return nil, err
	}
	r := &Runtime{
		ctx:              runtimeCtx,
		cancel:           cancel,
		runtime:          rr,
		source:           newCarrierListener(),
		closed:           make(chan struct{}),
		clients:          make(map[*trackedConn]struct{}),
		sessionsClosed:   make(chan struct{}),
		acceptedSlots:    make(chan struct{}, MaxAcceptedSessions),
		handshakeTimeout: openHandshakeTimeout,
		pendingClaims:    make(map[string]OriginClaim),
	}
	if err := rr.RegisterStreamFactory(xrayStreamFactory, rendr.StreamFactory{
		Carrier: rendr.CarrierUnknown,
		Dial:    r.dialCarrier,
	}); err != nil {
		cancel()
		return nil, err
	}
	listener, err := rr.Listen(rendr.ListenConfig{Streams: []rendr.StreamSource{{
		Name:     xrayStreamFactory,
		Carrier:  rendr.CarrierUnknown,
		Listener: r.source,
	}}})
	if err != nil {
		cancel()
		_ = r.source.Close()
		return nil, err
	}
	r.listener = listener
	r.wg.Add(1)
	go r.acceptLoop()
	return r, nil
}

func (r *Runtime) SetDialers(stream StreamDialer, egress EgressDialer) error {
	if r == nil {
		return errors.New("rendradapter: nil runtime")
	}
	if stream == nil || egress == nil {
		return errors.New("rendradapter: both stream and egress dialers are required")
	}
	if r.closing.Load() {
		return net.ErrClosed
	}
	r.dialMu.Lock()
	defer r.dialMu.Unlock()
	if r.closing.Load() {
		return net.ErrClosed
	}
	r.streamDialer = stream
	r.egressDialer = egress
	return nil
}

// InjectCarrier transfers one authenticated, decrypted Xray stream and its
// local bearer claim into the Runtime.Listen source. Ownership transfers only
// after a successful return.
func (r *Runtime) InjectCarrier(ctx context.Context, claim OriginClaim, conn net.Conn) error {
	if r == nil || r.source == nil {
		return errors.New("rendradapter: runtime unavailable")
	}
	if err := claim.Validate(); err != nil {
		return err
	}
	if conn == nil {
		return errors.New("rendradapter: nil carrier connection")
	}
	if r.closing.Load() {
		return net.ErrClosed
	}
	token, err := r.registerCarrierClaim(claim)
	if err != nil {
		return err
	}
	wrapped := newAdmittedCarrierConn(conn, token, func() { r.releaseCarrierClaim(token) })
	if err := r.source.Inject(ctx, wrapped); err != nil {
		r.releaseCarrierClaim(token)
		return err
	}
	return nil
}

func (r *Runtime) registerCarrierClaim(claim OriginClaim) (string, error) {
	var raw [16]byte
	for range 16 {
		if _, err := rand.Read(raw[:]); err != nil {
			return "", fmt.Errorf("rendradapter: carrier claim token: %w", err)
		}
		token := hex.EncodeToString(raw[:])
		r.claimMu.Lock()
		if r.closing.Load() {
			r.claimMu.Unlock()
			return "", net.ErrClosed
		}
		if len(r.pendingClaims) >= MaxAcceptedSessions {
			r.claimMu.Unlock()
			return "", errors.New("rendradapter: carrier claim capacity reached")
		}
		if _, exists := r.pendingClaims[token]; !exists {
			r.pendingClaims[token] = claim
			r.claimMu.Unlock()
			return token, nil
		}
		r.claimMu.Unlock()
	}
	return "", errors.New("rendradapter: carrier claim token collision")
}

func (r *Runtime) releaseCarrierClaim(token string) {
	r.claimMu.Lock()
	delete(r.pendingClaims, token)
	r.claimMu.Unlock()
}

func (r *Runtime) consumeCarrierClaim(conn rendr.Conn) (OriginClaim, error) {
	if conn == nil {
		return OriginClaim{}, errors.New("rendradapter: accepted stream has no carrier claim")
	}
	token := ""
	for _, path := range conn.Paths() {
		candidate, ok := carrierAdmissionToken(path.RemoteAddr)
		if !ok {
			candidate, ok = carrierAdmissionToken(path.Spec.Address)
		}
		if !ok {
			continue
		}
		if token != "" && token != candidate {
			return OriginClaim{}, errors.New("rendradapter: accepted stream has ambiguous carrier claims")
		}
		token = candidate
	}
	if token == "" {
		return OriginClaim{}, errors.New("rendradapter: accepted stream has no carrier claim")
	}
	r.claimMu.Lock()
	claim, ok := r.pendingClaims[token]
	if ok {
		delete(r.pendingClaims, token)
	}
	r.claimMu.Unlock()
	if !ok {
		return OriginClaim{}, errors.New("rendradapter: accepted stream carrier claim is unavailable")
	}
	return claim, nil
}

func (r *Runtime) Dial(ctx context.Context, peerTag string, target Destination) (net.Conn, error) {
	if r == nil || r.runtime == nil {
		return nil, errors.New("rendradapter: runtime unavailable")
	}
	if ctx == nil {
		return nil, errors.New("rendradapter: nil dial context")
	}
	if peerTag == "" {
		return nil, errors.New("rendradapter: peer tag required")
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if !r.beginClientAttempt() {
		return nil, net.ErrClosed
	}
	applicationOwned := true
	defer func() {
		r.dialWG.Done()
		if applicationOwned {
			r.finishSession(&r.activeClient)
		}
	}()
	dialCtx, cancelDial := context.WithCancel(ctx)
	stopRuntimeCancel := context.AfterFunc(r.ctx, cancelDial)
	contextOwned := true
	defer func() {
		if contextOwned {
			stopRuntimeCancel()
			cancelDial()
		}
	}()

	conn, err := r.runtime.Dial(dialCtx, rendr.SessionConfig{Root: rendr.Path(peerTag, rendr.PathSpec{
		Transport: xrayStreamFactory,
		Address:   peerTag,
		Opts:      map[string]string{"name": peerTag},
	})})
	if err != nil {
		r.recordError(classifyRendrDialError(err))
		return nil, err
	}
	var interruptOnce sync.Once
	interrupt := func() {
		interruptOnce.Do(func() { r.scheduleSessionConnInterrupt(conn) })
	}
	if err := r.openSession(dialCtx, conn, target, interrupt); err != nil {
		interrupt()
		r.recordError(categoryOf(err))
		return nil, err
	}
	tracked := &trackedConn{Conn: conn}
	tracked.release = func() { r.releaseClient(tracked) }
	tracked.finishApplication = func() { r.finishSession(&r.activeClient) }
	tracked.interruptApplication = func() {
		stopRuntimeCancel()
		cancelDial()
		r.scheduleSessionTask(func() { _ = conn.SetDeadline(time.Now()) })
	}
	r.admissionMu.Lock()
	r.clientsMu.Lock()
	if r.closing.Load() {
		r.clientsMu.Unlock()
		r.admissionMu.Unlock()
		interrupt()
		return nil, net.ErrClosed
	}
	r.clients[tracked] = struct{}{}
	r.clientsWG.Add(1)
	r.totalClient.Add(1)
	applicationOwned = false
	contextOwned = false
	r.clientsMu.Unlock()
	r.admissionMu.Unlock()
	return tracked, nil
}

func (r *Runtime) beginClientAttempt() bool {
	r.admissionMu.Lock()
	defer r.admissionMu.Unlock()
	if r.closing.Load() {
		return false
	}
	r.dialWG.Add(1)
	r.activeSessions++
	r.activeClient.Add(1)
	return true
}

func (r *Runtime) openSession(ctx context.Context, conn net.Conn, target Destination, interrupts ...func()) error {
	if r.handshakeTimeout <= 0 {
		return categorized(runtimeErrorInternal, errors.New("rendradapter: invalid handshake timeout"))
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, r.handshakeTimeout)
	defer cancel()
	interrupt := func() { r.scheduleSessionConnInterrupt(conn) }
	if len(interrupts) > 0 && interrupts[0] != nil {
		interrupt = interrupts[0]
	}
	watcher := watchConnectionCancellation(handshakeCtx, r.ctx, interrupt)
	defer watcher.Stop()

	err := writeOpen(conn, target)
	if err == nil {
		err = readAck(conn)
	}
	if err == nil {
		err = watcher.Commit()
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return categorized(runtimeErrorCanceled, ctxErr)
	}
	if r.ctx.Err() != nil {
		return categorized(runtimeErrorCanceled, net.ErrClosed)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(handshakeCtx.Err(), context.DeadlineExceeded) {
		return categorized(runtimeErrorHandshakeTimeout, fmt.Errorf("rendradapter: opening terminal session: %w", errors.Join(err, handshakeCtx.Err())))
	}
	return categorized(classifyOpenError(err), err)
}

type cancellationWatcherState uint8

const (
	cancellationPending cancellationWatcherState = iota
	cancellationCommitted
	cancellationFailed
)

type connectionCancellationWatcher struct {
	ctx        context.Context
	runtimeCtx context.Context
	interrupt  func()
	stop       chan struct{}
	done       chan struct{}
	stopOnce   sync.Once

	mu    sync.Mutex
	state cancellationWatcherState
	cause error
}

func watchConnectionCancellation(ctx, runtimeCtx context.Context, interrupt func()) *connectionCancellationWatcher {
	if ctx == nil {
		ctx = context.Background()
	}
	watcher := &connectionCancellationWatcher{
		ctx: ctx, runtimeCtx: runtimeCtx, interrupt: interrupt,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	var runtimeDone <-chan struct{}
	if runtimeCtx != nil {
		runtimeDone = runtimeCtx.Done()
	}
	go func() {
		defer close(watcher.done)
		select {
		case <-ctx.Done():
			watcher.fail(context.Cause(ctx))
		case <-runtimeDone:
			watcher.fail(net.ErrClosed)
		case <-watcher.stop:
		}
	}()
	return watcher
}

func (w *connectionCancellationWatcher) Commit() error {
	if w == nil {
		return context.Canceled
	}
	interrupt := false
	w.mu.Lock()
	switch w.state {
	case cancellationFailed:
		// The watcher already owns the failure and interruption.
	case cancellationCommitted:
		w.mu.Unlock()
		w.Stop()
		return nil
	default:
		if cause := w.currentCause(); cause != nil {
			w.state = cancellationFailed
			w.cause = cause
			interrupt = true
		} else {
			w.state = cancellationCommitted
		}
	}
	cause := w.cause
	w.mu.Unlock()
	if interrupt && w.interrupt != nil {
		w.interrupt()
	}
	w.Stop()
	return cause
}

func (w *connectionCancellationWatcher) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

func (w *connectionCancellationWatcher) fail(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	interrupt := false
	w.mu.Lock()
	if w.state == cancellationPending {
		w.state = cancellationFailed
		w.cause = cause
		interrupt = true
	}
	w.mu.Unlock()
	if interrupt && w.interrupt != nil {
		w.interrupt()
	}
}

func (w *connectionCancellationWatcher) currentCause() error {
	if cause := context.Cause(w.ctx); cause != nil {
		return cause
	}
	if deadline, ok := w.ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	if w.runtimeCtx != nil && context.Cause(w.runtimeCtx) != nil {
		return net.ErrClosed
	}
	return nil
}

func (w *connectionCancellationWatcher) Cause() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cause
}

func (r *Runtime) releaseClient(conn *trackedConn) {
	released := false
	r.clientsMu.Lock()
	if _, ok := r.clients[conn]; ok {
		delete(r.clients, conn)
		released = true
	}
	r.clientsMu.Unlock()
	if released {
		r.clientsWG.Done()
	}
}

func (r *Runtime) Status() RuntimeStatus {
	if r == nil {
		return RuntimeStatus{State: "unavailable", ObservedAt: time.Now().UTC()}
	}
	state := "running"
	select {
	case <-r.closed:
		state = "stopped"
	default:
		if r.closing.Load() {
			state = "stopping"
		}
	}
	if state == "running" && r.acceptFailed.Load() {
		state = "failed"
	}
	r.errMu.RLock()
	lastError := r.lastError
	r.errMu.RUnlock()
	flows := 0
	if r.listener != nil {
		flows = len(r.listener.FlowIDs())
	}
	return RuntimeStatus{
		State:            state,
		ActiveClient:     r.activeClient.Load(),
		ActiveAccepted:   r.activeAccepted.Load(),
		AcceptedFlowIDs:  flows,
		TotalClient:      r.totalClient.Load(),
		TotalAccepted:    r.totalAccepted.Load(),
		LastError:        lastError,
		ObservedAt:       time.Now().UTC(),
		StreamFactory:    xrayStreamFactory,
		StreamCarrier:    "unknown",
		MobilityMode:     "redial_attach",
		EndpointOwned:    false,
		PacketSupported:  false,
		InstanceIDSource: "not_exposed_by_pinned_rendr_api",
	}
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeCloseTimeout)
	defer cancel()
	return r.CloseContext(ctx)
}

// CloseContext starts shutdown exactly once and bounds only the caller's wait.
// The shutdown goroutine remains responsible for joining every owned worker if
// a broken transport violates net.Conn's prompt-Close contract.
func (r *Runtime) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.Join(ErrShutdownIncomplete, errors.New("rendradapter: nil shutdown context"))
	}
	r.beginClose()
	select {
	case <-r.closed:
		return r.closeErr
	case <-ctx.Done():
		return errors.Join(ErrShutdownIncomplete, ctx.Err())
	}
}

// BeginClose synchronously publishes cancellation to every session owned by
// this runtime. Callers may then finish the bounded join through CloseContext.
func (r *Runtime) BeginClose() {
	if r != nil {
		r.beginClose()
	}
}

// RevokeContext waits only for application sessions owned by this runtime to
// stop carrying data. Listener and transport retirement continues under the
// ordinary CloseContext lifecycle and is intentionally not part of this
// authorization barrier.
func (r *Runtime) RevokeContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.Join(ErrShutdownIncomplete, errors.New("rendradapter: nil revocation context"))
	}
	r.beginClose()
	select {
	case <-r.sessionsClosed:
		return nil
	case <-ctx.Done():
		return errors.Join(ErrShutdownIncomplete, ctx.Err())
	}
}

// ForceClose repeats all non-blocking cancellation and close signals without
// waiting for a misbehaving transport. It cannot kill a Go goroutine, so an
// incomplete result remains explicit to the daemon shutdown boundary.
func (r *Runtime) ForceClose() error {
	if r == nil {
		return nil
	}
	r.beginClose()
	r.forceUnblock()
	select {
	case <-r.closed:
		return r.closeErr
	default:
		return ErrShutdownIncomplete
	}
}

func (r *Runtime) beginClose() {
	r.closeOnce.Do(func() {
		r.closing.Store(true)
		r.admissionMu.Lock()
		r.cancel()
		r.closeSessionsIfDrainedLocked()
		r.admissionMu.Unlock()
		r.revokeClientSessions()
		go r.shutdown()
	})
}

func (r *Runtime) revokeClientSessions() {
	r.clientsMu.Lock()
	clients := make([]*trackedConn, 0, len(r.clients))
	for conn := range r.clients {
		clients = append(clients, conn)
	}
	r.clientsMu.Unlock()
	for _, conn := range clients {
		conn.revokeApplicationIO()
	}
}

func (r *Runtime) finishSession(active *atomic.Int64) {
	r.admissionMu.Lock()
	active.Add(-1)
	r.activeSessions--
	r.closeSessionsIfDrainedLocked()
	r.admissionMu.Unlock()
}

func (r *Runtime) closeSessionsIfDrainedLocked() {
	if r.closing.Load() && r.activeSessions == 0 && !r.sessionsDrained {
		r.sessionsDrained = true
		close(r.sessionsClosed)
	}
}

func (r *Runtime) shutdown() {
	r.clientsMu.Lock()
	clients := make([]*trackedConn, 0, len(r.clients))
	for conn := range r.clients {
		clients = append(clients, conn)
	}
	r.clientsMu.Unlock()

	closeErrors := make(chan error, len(clients)+2)
	var closeWG sync.WaitGroup
	closeAsync := func(closeFn func() error) {
		if closeFn == nil {
			return
		}
		closeWG.Add(1)
		go func() {
			defer closeWG.Done()
			closeErrors <- closeFn()
		}()
	}
	for _, conn := range clients {
		closeAsync(conn.Close)
	}
	if r.listener != nil {
		closeAsync(r.listener.Close)
	}
	if r.source != nil {
		closeAsync(r.source.Close)
	}
	closeWG.Wait()
	close(closeErrors)
	for err := range closeErrors {
		r.closeErr = errors.Join(r.closeErr, actionableCloseError(err))
	}
	r.wg.Wait()
	r.dialWG.Wait()
	r.clientsWG.Wait()
	r.sessionCloseWG.Wait()
	close(r.closed)
}

func (r *Runtime) forceUnblock() {
	r.cancel()
	r.clientsMu.Lock()
	clients := make([]*trackedConn, 0, len(r.clients))
	for conn := range r.clients {
		clients = append(clients, conn)
	}
	r.clientsMu.Unlock()
	for _, conn := range clients {
		go func(conn *trackedConn) { _ = conn.Close() }(conn)
	}
	if r.listener != nil {
		go func() { _ = r.listener.Close() }()
	}
	if r.source != nil {
		go func() { _ = r.source.Close() }()
	}
}

func classifyRendrDialError(err error) runtimeErrorCategory {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return runtimeErrorCanceled
	case errors.Is(err, rendr.ErrPeerProtoVersion), errors.Is(err, rendr.ErrPeerProtocol):
		return runtimeErrorProtocol
	default:
		return runtimeErrorCarrier
	}
}

// actionableCloseError removes only benign leaves from a possibly joined or
// wrapped error tree. A graceful timeout must not hide an unrelated teardown
// failure returned alongside it.
func actionableCloseError(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var result error
		for _, child := range joined.Unwrap() {
			result = errors.Join(result, actionableCloseError(child))
		}
		return result
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return actionableCloseError(wrapped.Unwrap())
	}
	if errors.Is(err, rendr.ErrGracefulCloseTimeout) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (r *Runtime) dialCarrier(ctx context.Context, peerTag string) (net.Conn, error) {
	r.dialMu.RLock()
	dialer := r.streamDialer
	r.dialMu.RUnlock()
	if dialer == nil {
		return nil, errors.New("rendradapter: stream dialer unavailable")
	}
	return dialer(ctx, peerTag, carrierTarget)
}

func (r *Runtime) acceptLoop() {
	defer r.wg.Done()
	for {
		select {
		case r.acceptedSlots <- struct{}{}:
		case <-r.ctx.Done():
			return
		}
		conn, err := r.listener.AcceptStream(r.ctx)
		if err != nil {
			<-r.acceptedSlots
			if r.ctx.Err() == nil {
				r.recordError(runtimeErrorCarrier)
				r.acceptFailed.Store(true)
			}
			return
		}
		claim, claimErr := r.consumeCarrierClaim(conn)
		if claimErr != nil {
			<-r.acceptedSlots
			r.retireUnadmittedAccepted(conn)
			r.recordError(runtimeErrorProtocol)
			continue
		}
		r.admissionMu.Lock()
		if r.closing.Load() {
			r.admissionMu.Unlock()
			<-r.acceptedSlots
			r.retireUnadmittedAccepted(conn)
			return
		}
		r.activeAccepted.Add(1)
		r.totalAccepted.Add(1)
		r.wg.Add(1)
		r.activeSessions++
		r.admissionMu.Unlock()
		go func() {
			defer r.wg.Done()
			defer func() { <-r.acceptedSlots }()
			defer r.finishSession(&r.activeAccepted)
			if err := r.serveAccepted(conn, claim); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
				r.recordError(categoryOf(err))
			}
		}()
	}
}

func (r *Runtime) retireUnadmittedAccepted(conn net.Conn) {
	r.scheduleSessionConnClose(conn)
}

func (r *Runtime) serveAccepted(conn rendr.Conn, origin OriginClaim) error {
	var carrierCloseOnce sync.Once
	closeCarrier := func() {
		carrierCloseOnce.Do(func() { r.scheduleSessionConnClose(conn) })
	}
	defer closeCarrier()
	handshakeCtx, cancel := context.WithTimeout(r.ctx, r.handshakeTimeout)
	defer cancel()
	handshakeWatcher := watchConnectionCancellation(handshakeCtx, nil, func() {
		r.scheduleSessionTask(func() { _ = conn.SetDeadline(time.Now()) })
		closeCarrier()
	})
	defer handshakeWatcher.Stop()
	if deadline, ok := handshakeCtx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return categorized(runtimeErrorInternal, err)
		}
	}
	target, err := readOpen(conn)
	if err != nil {
		if errors.Is(handshakeCtx.Err(), context.DeadlineExceeded) {
			return categorized(runtimeErrorHandshakeTimeout, errors.Join(handshakeCtx.Err(), err))
		}
		if errors.Is(handshakeCtx.Err(), context.Canceled) {
			return categorized(runtimeErrorCanceled, errors.Join(handshakeCtx.Err(), err))
		}
		_ = writeAck(conn, ackInvalidRequest)
		return categorized(runtimeErrorProtocol, err)
	}
	r.dialMu.RLock()
	dialer := r.egressDialer
	r.dialMu.RUnlock()
	if dialer == nil {
		err := errors.New("rendradapter: egress dialer unavailable")
		_ = writeAck(conn, ackEgressUnavailable)
		return categorized(runtimeErrorEgressUnavailable, err)
	}
	status := conn.Status()
	request := EgressRequest{
		Claim: FlowClaim{
			Origin:         origin,
			FlowID:         conn.FlowID(),
			PeerInstanceID: PeerInstanceID(status.Peer.InstanceID),
		},
		Destination: target,
	}
	if err := request.Validate(); err != nil {
		_ = writeAck(conn, ackInvalidRequest)
		return categorized(runtimeErrorProtocol, err)
	}
	upstream, err := dialer(handshakeCtx, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(handshakeCtx.Err(), context.Canceled) {
			return categorized(runtimeErrorCanceled, errors.Join(err, handshakeCtx.Err()))
		}
		category := runtimeErrorEgressUnavailable
		code := ackEgressUnavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(handshakeCtx.Err(), context.DeadlineExceeded) {
			category = runtimeErrorEgressTimeout
			code = ackEgressTimeout
		}
		_ = writeAck(conn, code)
		return categorized(category, err)
	}
	var upstreamCloseOnce sync.Once
	closeUpstream := func() {
		upstreamCloseOnce.Do(func() { r.scheduleSessionConnClose(upstream) })
	}
	defer closeUpstream()
	if err := handshakeWatcher.Commit(); err != nil {
		category := runtimeErrorCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			category = runtimeErrorHandshakeTimeout
		}
		return categorized(category, err)
	}
	cancel()
	if err := r.writeSuccessAck(conn); err != nil {
		category := runtimeErrorCarrier
		if errors.Is(err, context.DeadlineExceeded) {
			category = runtimeErrorHandshakeTimeout
		} else if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			category = runtimeErrorCanceled
		}
		return categorized(category, err)
	}
	return categorized(runtimeErrorStream, proxyStreamWithInterrupt(r.ctx, conn, upstream, func() {
		closeCarrier()
		closeUpstream()
	}, r.scheduleSessionTask))
}

func (r *Runtime) writeSuccessAck(conn net.Conn) error {
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	ackCtx, cancel := context.WithTimeout(r.ctx, ackWriteTimeout)
	defer cancel()
	watcher := watchConnectionCancellation(ackCtx, nil, func() {
		r.scheduleSessionConnClose(conn)
	})
	err := writeAck(conn, ackOK)
	watcher.Stop()
	if err == nil {
		// A complete protocol frame is the success linearization point. A
		// concurrent runtime cancellation may close the new stream, but cannot
		// retroactively turn an acknowledgement already delivered into failure.
		return nil
	}
	cause := watcher.Cause()
	if cause == nil {
		cause = context.Cause(ackCtx)
	}
	return errors.Join(cause, err)
}

func (r *Runtime) scheduleSessionConnClose(conn net.Conn) {
	if conn == nil {
		return
	}
	r.scheduleSessionTask(func() { _ = conn.Close() })
}

func (r *Runtime) scheduleSessionConnInterrupt(conn net.Conn) {
	if conn == nil {
		return
	}
	r.scheduleSessionTask(func() { _ = conn.SetDeadline(time.Now()) })
	r.scheduleSessionConnClose(conn)
}

func (r *Runtime) scheduleSessionTask(task func()) {
	if task == nil {
		return
	}
	r.sessionCloseWG.Add(1)
	go func() {
		defer r.sessionCloseWG.Done()
		task()
	}()
}

func (r *Runtime) recordError(category runtimeErrorCategory) {
	r.errMu.Lock()
	r.lastError = category.statusMessage()
	r.errMu.Unlock()
}

func categorized(category runtimeErrorCategory, cause error) error {
	if cause == nil {
		return nil
	}
	return &categorizedError{category: category, cause: cause}
}

func categoryOf(err error) runtimeErrorCategory {
	var classified *categorizedError
	if errors.As(err, &classified) {
		return classified.category
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return runtimeErrorCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return runtimeErrorHandshakeTimeout
	}
	return runtimeErrorInternal
}

func classifyOpenError(err error) runtimeErrorCategory {
	switch {
	case err == nil:
		return runtimeErrorInternal
	case errors.Is(err, errTerminalInvalidRequest), errors.Is(err, errInvalidAck):
		return runtimeErrorProtocol
	case errors.Is(err, errTerminalUnavailable):
		return runtimeErrorEgressUnavailable
	case errors.Is(err, errTerminalTimeout):
		return runtimeErrorEgressTimeout
	case errors.Is(err, errTerminalSessionFailure):
		return runtimeErrorInternal
	case errors.Is(err, context.DeadlineExceeded):
		return runtimeErrorHandshakeTimeout
	default:
		return runtimeErrorCarrier
	}
}

func (category runtimeErrorCategory) statusMessage() string {
	switch category {
	case runtimeErrorCanceled:
		return RuntimeStatusErrorCanceled
	case runtimeErrorCarrier:
		return RuntimeStatusErrorCarrier
	case runtimeErrorHandshakeTimeout:
		return RuntimeStatusErrorHandshakeTimeout
	case runtimeErrorProtocol:
		return RuntimeStatusErrorProtocol
	case runtimeErrorEgressUnavailable:
		return RuntimeStatusErrorEgressUnavailable
	case runtimeErrorEgressTimeout:
		return RuntimeStatusErrorEgressTimeout
	case runtimeErrorStream:
		return RuntimeStatusErrorStream
	default:
		return RuntimeStatusErrorInternal
	}
}

type trackedConn struct {
	net.Conn
	closeOnce sync.Once
	release   func()
	closeErr  error

	applicationMu        sync.Mutex
	applicationRevoked   bool
	applicationActive    int
	applicationFinished  bool
	finishApplication    func()
	interruptApplication func()
}

func (c *trackedConn) Close() error {
	c.revokeApplicationIO()
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		if c.release != nil {
			c.release()
		}
	})
	return c.closeErr
}

func (c *trackedConn) Read(payload []byte) (int, error) {
	if !c.beginApplicationIO() {
		return 0, net.ErrClosed
	}
	defer c.finishApplicationIO()
	return c.Conn.Read(payload)
}

func (c *trackedConn) Write(payload []byte) (int, error) {
	if !c.beginApplicationIO() {
		return 0, net.ErrClosed
	}
	defer c.finishApplicationIO()
	return c.Conn.Write(payload)
}

func (c *trackedConn) CloseWrite() error {
	c.applicationMu.Lock()
	revoked := c.applicationRevoked
	c.applicationMu.Unlock()
	if revoked {
		return net.ErrClosed
	}
	if closer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return ErrHalfCloseUnsupported
}

func (c *trackedConn) beginApplicationIO() bool {
	c.applicationMu.Lock()
	defer c.applicationMu.Unlock()
	if c.applicationRevoked {
		return false
	}
	c.applicationActive++
	return true
}

func (c *trackedConn) finishApplicationIO() {
	var finish func()
	c.applicationMu.Lock()
	c.applicationActive--
	if c.applicationRevoked && c.applicationActive == 0 && !c.applicationFinished {
		c.applicationFinished = true
		finish = c.finishApplication
	}
	c.applicationMu.Unlock()
	if finish != nil {
		finish()
	}
}

func (c *trackedConn) revokeApplicationIO() {
	var finish func()
	c.applicationMu.Lock()
	if !c.applicationRevoked {
		c.applicationRevoked = true
		// Scheduling the interrupt while holding applicationMu prevents the
		// runtime shutdown waiter from racing ahead of the WaitGroup Add.
		if c.interruptApplication != nil {
			c.interruptApplication()
		}
	}
	if c.applicationActive == 0 && !c.applicationFinished {
		c.applicationFinished = true
		finish = c.finishApplication
	}
	c.applicationMu.Unlock()
	if finish != nil {
		finish()
	}
}

func writeOpen(w io.Writer, target Destination) error {
	if err := target.Validate(); err != nil {
		return err
	}
	header := make([]byte, 12)
	copy(header[:4], openMagic[:])
	header[4] = openVersion
	header[5] = openNetworkTCP
	binary.BigEndian.PutUint16(header[8:10], target.Port)
	binary.BigEndian.PutUint16(header[10:12], uint16(len(target.Host)))
	if err := writeFull(w, header); err != nil {
		return err
	}
	return writeFull(w, []byte(target.Host))
}

func readOpen(r io.Reader) (Destination, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(r, header); err != nil {
		return Destination{}, err
	}
	if string(header[:4]) != string(openMagic[:]) || header[4] != openVersion {
		return Destination{}, errors.New("rendradapter: invalid open header")
	}
	if header[5] != openNetworkTCP || header[6] != 0 || header[7] != 0 {
		return Destination{}, errors.New("rendradapter: unsupported open network")
	}
	length := int(binary.BigEndian.Uint16(header[10:12]))
	if length < 1 || length > maxTargetHost {
		return Destination{}, errors.New("rendradapter: target host length out of range")
	}
	host := make([]byte, length)
	if _, err := io.ReadFull(r, host); err != nil {
		return Destination{}, err
	}
	target := Destination{Host: string(host), Port: binary.BigEndian.Uint16(header[8:10])}
	return target, target.Validate()
}

func writeAck(w io.Writer, code ackCode) error {
	// The caller supplies only a protocol-owned code. Arbitrary cause.Error()
	// text can therefore never become part of the acknowledgement frame.
	message, valid := ackMessage(code)
	if !valid {
		return errInvalidAck
	}
	header := make([]byte, 8)
	copy(header[:4], ackMagic[:])
	header[4] = byte(code)
	binary.BigEndian.PutUint16(header[6:8], uint16(len(message)))
	if err := writeFull(w, header); err != nil {
		return err
	}
	if message == "" {
		return nil
	}
	return writeFull(w, []byte(message))
}

func writeFull(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := w.Write(payload)
		if written < 0 || written > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[written:]
		if len(payload) == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readAck(r io.Reader) error {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return err
	}
	if string(header[:4]) != string(ackMagic[:]) || header[5] != 0 {
		return errInvalidAck
	}
	length := int(binary.BigEndian.Uint16(header[6:8]))
	if length > maxOpenError {
		return errInvalidAck
	}
	message := make([]byte, length)
	if _, err := io.ReadFull(r, message); err != nil {
		return err
	}
	code := ackCode(header[4])
	wantMessage, valid := ackMessage(code)
	if !valid || string(message) != wantMessage {
		return errInvalidAck
	}
	switch code {
	case ackOK:
		return nil
	case ackInvalidRequest:
		return errTerminalInvalidRequest
	case ackEgressUnavailable:
		return errTerminalUnavailable
	case ackEgressTimeout:
		return errTerminalTimeout
	case ackSessionFailure:
		return errTerminalSessionFailure
	default:
		return errInvalidAck
	}
}

func ackMessage(code ackCode) (string, bool) {
	switch code {
	case ackOK:
		return "", true
	case ackInvalidRequest:
		return "invalid request", true
	case ackEgressUnavailable:
		return "egress unavailable", true
	case ackEgressTimeout:
		return "egress timeout", true
	case ackSessionFailure:
		return "session failed", true
	default:
		return "", false
	}
}

func proxyStream(ctx context.Context, left, right net.Conn) error {
	schedule := func(task func()) { go task() }
	return proxyStreamWithInterrupt(ctx, left, right, func() {
		schedule(func() { _ = left.Close() })
		schedule(func() { _ = right.Close() })
	}, schedule)
}

func proxyStreamWithInterrupt(
	ctx context.Context,
	left, right net.Conn,
	closeBoth func(),
	schedule func(func()),
) error {
	if schedule == nil {
		schedule = func(task func()) { go task() }
	}
	leftHalf, leftOK := left.(interface{ CloseWrite() error })
	rightHalf, rightOK := right.(interface{ CloseWrite() error })
	if !leftOK || !rightOK {
		if closeBoth != nil {
			closeBoth()
		} else {
			schedule(func() { _ = left.Close() })
			schedule(func() { _ = right.Close() })
		}
		return fmt.Errorf("%w: left=%t right=%t", ErrHalfCloseUnsupported, leftOK, rightOK)
	}
	done := make(chan error, 2)
	var interruptOnce sync.Once
	var interrupted atomic.Bool
	interruptedCh := make(chan struct{})
	interruptBoth := func() bool {
		initiated := false
		interruptOnce.Do(func() {
			initiated = true
			interrupted.Store(true)
			close(interruptedCh)
			if closeBoth != nil {
				closeBoth()
			}
			now := time.Now()
			_ = left.SetDeadline(now)
			_ = right.SetDeadline(now)
		})
		return initiated
	}
	copyOne := func(dst, src net.Conn, half interface{ CloseWrite() error }) {
		_, err := io.Copy(dst, src)
		if err != nil {
			initiated := interruptBoth()
			if !initiated && isExpectedProxyClose(err) {
				err = nil
			}
			done <- err
			return
		}
		if interrupted.Load() {
			done <- nil
			return
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			interruptBoth()
			done <- ctxErr
			return
		}
		halfClosed := make(chan error, 1)
		schedule(func() { halfClosed <- half.CloseWrite() })
		select {
		case err = <-halfClosed:
		case <-interruptedCh:
			done <- nil
			return
		case <-ctx.Done():
			interruptBoth()
			done <- ctx.Err()
			return
		}
		if isExpectedProxyClose(err) {
			err = nil
		} else if err != nil {
			interruptBoth()
		}
		done <- err
	}
	go copyOne(left, right, leftHalf)
	go copyOne(right, left, rightHalf)

	ctxDone := ctx.Done()
	var contextErr error
	var resultErr error
	for completed := 0; completed < 2; {
		select {
		case err := <-done:
			completed++
			resultErr = errors.Join(resultErr, err)
		case <-ctxDone:
			contextErr = ctx.Err()
			ctxDone = nil
			interruptBoth()
		}
	}
	return errors.Join(resultErr, contextErr)
}

func isExpectedProxyClose(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}
