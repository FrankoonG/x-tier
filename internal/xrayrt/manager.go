package xrayrt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
)

var (
	ErrClosed             = errors.New("xrayrt: manager is closed")
	ErrNoGeneration       = errors.New("xrayrt: no current generation")
	ErrUnsupported        = errors.New("xrayrt: packet dialing with a strict forced outbound is unsupported")
	ErrShutdownIncomplete = errors.New("xrayrt: shutdown is incomplete")
	ErrInboundFailStopped = errors.New("xrayrt: inbound replacement failed and all managed inbounds were stopped")
)

// Backend installs generation-qualified outbound handlers into one long-lived
// runtime. Build must not disturb previously installed generations on failure.
// Remove must unregister every handler and explicitly close it.
type Backend interface {
	Build(ctx context.Context, generation uint64, config GenerationConfig) (Generation, error)
	Remove(generation Generation) error
}

// Generation is an installed set of outbound handlers. OutboundTag resolves a
// caller-facing tag to the generation-qualified tag installed in the runtime.
type Generation interface {
	OutboundTag(tag string) (string, error)
}

// StreamDialer dials through a forced generation-qualified outbound tag.
type StreamDialer func(ctx context.Context, outboundTag, network, address string) (net.Conn, error)

type GenerationStatus struct {
	Generation   uint64 `json:"generation"`
	RefCount     int64  `json:"ref_count"`
	Draining     bool   `json:"draining"`
	CleanupError string `json:"cleanup_error,omitempty"`
}

type Status struct {
	Closed               bool               `json:"closed"`
	StrictStreamOutbound bool               `json:"strict_stream_outbound"`
	StrictPacketOutbound bool               `json:"strict_packet_outbound"`
	Current              *GenerationStatus  `json:"current,omitempty"`
	Draining             []GenerationStatus `json:"draining"`
}

type Manager struct {
	applyMu      sync.Mutex
	shutdownGate contextGate
	mu           sync.Mutex

	backend Backend
	dial    StreamDialer

	closed           bool
	nextID           uint64
	current          *generation
	draining         map[uint64]*generation
	active           map[*leasedConn]struct{}
	pending          map[*pendingDial]struct{}
	operations       int
	operationChanged chan struct{}

	buildID     uint64
	buildCancel context.CancelFunc
}

type generation struct {
	id       uint64
	handle   Generation
	refs     int64
	draining bool
	removing bool
	removed  bool
	cleanup  error
}

func NewManager(backend Backend, dialer StreamDialer) *Manager {
	return &Manager{
		backend:          backend,
		dial:             dialer,
		draining:         make(map[uint64]*generation),
		active:           make(map[*leasedConn]struct{}),
		pending:          make(map[*pendingDial]struct{}),
		operationChanged: make(chan struct{}),
	}
}

// Apply builds a generation before atomically publishing it. Failed builds
// leave the current generation untouched. Apply calls are serialized so an
// older, slower build can never supersede a newer request.
func (m *Manager) Apply(ctx context.Context, config GenerationConfig) (uint64, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if _, err := config.Info(); err != nil {
		return 0, err
	}
	if m.backend == nil {
		return 0, errors.New("xrayrt: backend is required")
	}

	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, ErrClosed
	}
	m.nextID++
	id := m.nextID
	buildCtx, cancel := context.WithCancel(ctx)
	m.buildID = id
	m.buildCancel = cancel
	m.operations++
	m.mu.Unlock()
	defer m.operationDone()
	defer func() {
		cancel()
		m.mu.Lock()
		if m.buildID == id {
			m.buildID = 0
			m.buildCancel = nil
		}
		m.mu.Unlock()
	}()

	handle, err := m.backend.Build(buildCtx, id, config.clone())
	if err != nil {
		if handle != nil {
			cleanupErr := m.destroy(handle)
			m.trackCleanupFailure(id, handle, cleanupErr)
			err = errors.Join(err, cleanupErr)
		}
		return 0, fmt.Errorf("xrayrt: build generation %d: %w", id, err)
	}
	if handle == nil {
		return 0, fmt.Errorf("xrayrt: build generation %d returned nil", id)
	}
	if err := buildCtx.Err(); err != nil {
		cleanupErr := m.destroy(handle)
		m.trackCleanupFailure(id, handle, cleanupErr)
		return 0, errors.Join(err, cleanupErr)
	}

	installed := &generation{id: id, handle: handle}
	var removeNow []*generation
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cleanupErr := m.destroy(handle)
		m.trackCleanupFailure(id, handle, cleanupErr)
		return 0, errors.Join(ErrClosed, cleanupErr)
	}
	retire := m.current
	m.current = installed
	if retire != nil {
		retire.draining = true
		m.draining[retire.id] = retire
	}
	removeNow = m.collectRemovalsLocked(removeNow)
	m.mu.Unlock()

	for _, generation := range removeNow {
		m.finishRemoval(generation, m.destroy(generation.handle))
	}
	return id, nil
}

// RetryCleanup retries removal of quiescent generations without publishing a
// new generation. It lets a reconciler heal retirement failures for an
// otherwise unchanged desired revision.
func (m *Manager) RetryCleanup() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	var removeNow []*generation
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	removeNow = m.collectRemovalsLocked(removeNow)
	if len(removeNow) > 0 {
		m.operations++
	}
	m.mu.Unlock()
	if len(removeNow) == 0 {
		return nil
	}
	defer m.operationDone()

	var result error
	for _, generation := range removeNow {
		removeErr := m.destroy(generation.handle)
		m.finishRemoval(generation, removeErr)
		result = errors.Join(result, removeErr)
	}
	return result
}

// Dial opens a stream through the current generation. The returned connection
// keeps that generation's handlers installed until Close releases the lease.
func (m *Manager) Dial(ctx context.Context, outboundTag, network, address string) (net.Conn, error) {
	if err := validateDial(ctx, outboundTag); err != nil {
		return nil, err
	}
	if network != "tcp" {
		return nil, ErrUnsupported
	}
	gen, pending, dialCtx, err := m.acquireDial(ctx)
	if err != nil {
		return nil, err
	}
	releasePending := func() error {
		pending.cancel()
		m.mu.Lock()
		delete(m.pending, pending)
		m.mu.Unlock()
		err := m.release(gen)
		m.operationDone()
		return err
	}
	qualifiedTag, err := gen.handle.OutboundTag(outboundTag)
	if err != nil {
		releaseErr := releasePending()
		return nil, errors.Join(fmt.Errorf("xrayrt: resolve outbound %q: %w", outboundTag, err), releaseErr)
	}
	if qualifiedTag == "" {
		releaseErr := releasePending()
		return nil, errors.Join(fmt.Errorf("xrayrt: resolve outbound %q returned an empty tag", outboundTag), releaseErr)
	}

	if m.dial == nil {
		return nil, errors.Join(errors.New("xrayrt: stream dialer is required"), releasePending())
	}
	conn, err := m.dial(dialCtx, qualifiedTag, network, address)
	if err != nil {
		return nil, errors.Join(err, releasePending())
	}
	if conn == nil {
		return nil, errors.Join(errors.New("xrayrt: stream dialer returned a nil connection"), releasePending())
	}
	lease := &leasedConn{Conn: conn, cancel: pending.cancel, done: make(chan struct{})}
	lease.release = func() error {
		err := m.releaseConnection(lease, gen)
		m.operationDone()
		return err
	}
	m.mu.Lock()
	delete(m.pending, pending)
	if m.closed {
		m.mu.Unlock()
		pending.cancel()
		return nil, errors.Join(ErrClosed, lease.Close())
	}
	m.active[lease] = struct{}{}
	m.mu.Unlock()
	return lease, nil
}

// DialUDP is deliberately unsupported until forced-tag packet semantics have
// an independent conformance contract. It never acquires a generation lease.
func (m *Manager) DialUDP(ctx context.Context, outboundTag string) (net.PacketConn, error) {
	if err := validateDial(ctx, outboundTag); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := Status{
		Closed:               m.closed,
		StrictStreamOutbound: true,
		Draining:             make([]GenerationStatus, 0, len(m.draining)),
	}
	if m.current != nil {
		current := generationStatus(m.current)
		status.Current = &current
	}
	for _, gen := range m.draining {
		status.Draining = append(status.Draining, generationStatus(gen))
	}
	sort.Slice(status.Draining, func(i, j int) bool {
		return status.Draining[i].Generation < status.Draining[j].Generation
	})
	return status
}

// Close prevents new work and drains all generations. Leased generations are
// removed and closed by the final connection that releases them.
func (m *Manager) Close() error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	if err := m.shutdownGate.lock(context.Background()); err != nil {
		return err
	}
	defer m.shutdownGate.unlock()

	var removeNow []*generation
	var pending []*pendingDial
	m.mu.Lock()
	m.closed = true
	for dial := range m.pending {
		pending = append(pending, dial)
	}
	if m.current != nil {
		gen := m.current
		m.current = nil
		gen.draining = true
		m.draining[gen.id] = gen
	}
	for _, gen := range m.draining {
		if gen.refs == 0 && !gen.removing && !gen.removed {
			gen.removing = true
			removeNow = append(removeNow, gen)
		}
	}
	m.mu.Unlock()
	for _, dial := range pending {
		dial.cancel()
	}

	var err error
	for _, gen := range removeNow {
		removeErr := m.destroy(gen.handle)
		m.finishRemoval(gen, removeErr)
		err = errors.Join(err, removeErr)
	}
	return err
}

// CloseNow stops new work and immediately removes every generation, including
// generations with live leases. It is reserved for process shutdown, where
// the owning Xray instance is about to close and graceful drain cannot outlive
// the process. A later leased connection Close only releases bookkeeping.
func (m *Manager) CloseNow() error {
	return m.CloseNowContext(context.Background())
}

// CloseNowContext force-closes live leases and waits until every build, dial,
// lease release, and handler removal that started before shutdown is quiescent.
// A context deadline never permits the caller to close the owning Xray
// instance: ErrShutdownIncomplete means the same manager must be retried later.
func (m *Manager) CloseNowContext(ctx context.Context) error {
	if ctx == nil {
		return errors.Join(ErrShutdownIncomplete, errors.New("xrayrt: nil shutdown context"))
	}
	if err := m.shutdownGate.lock(ctx); err != nil {
		return errors.Join(ErrShutdownIncomplete, err)
	}
	defer m.shutdownGate.unlock()

	var connections []*leasedConn
	var pending []*pendingDial
	m.mu.Lock()
	m.closed = true
	if m.buildCancel != nil {
		m.buildCancel()
	}
	if m.current != nil {
		m.current.draining = true
		m.draining[m.current.id] = m.current
		m.current = nil
	}
	for connection := range m.active {
		connections = append(connections, connection)
	}
	for dial := range m.pending {
		pending = append(pending, dial)
	}
	m.mu.Unlock()

	for _, dial := range pending {
		dial.cancel()
	}
	for _, connection := range connections {
		connection.beginClose()
	}
	if err := m.waitOperations(ctx); err != nil {
		return err
	}
	var err error
	for _, connection := range connections {
		err = errors.Join(err, connection.Close())
	}

	var removeNow []*generation
	m.mu.Lock()
	for _, gen := range m.draining {
		if gen.refs != 0 || gen.removing {
			m.mu.Unlock()
			return fmt.Errorf("%w: generation %d is not quiescent", ErrShutdownIncomplete, gen.id)
		}
	}
	removeNow = m.collectRemovalsLocked(removeNow)
	m.mu.Unlock()
	if len(removeNow) > 0 {
		m.mu.Lock()
		m.operations++
		m.mu.Unlock()
		go func() {
			defer m.operationDone()
			for _, gen := range removeNow {
				m.finishRemoval(gen, m.destroy(gen.handle))
			}
		}()
	}
	if waitErr := m.waitOperations(ctx); waitErr != nil {
		return errors.Join(err, waitErr)
	}
	var cleanupErr error
	m.mu.Lock()
	for _, gen := range m.draining {
		cleanupErr = errors.Join(cleanupErr, gen.cleanup)
	}
	m.mu.Unlock()
	if cleanupErr != nil {
		return errors.Join(err, ErrShutdownIncomplete, cleanupErr)
	}
	return err
}

func (m *Manager) acquireDial(ctx context.Context) (*generation, *pendingDial, context.Context, error) {
	dialCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		cancel()
		return nil, nil, nil, ErrClosed
	}
	if m.current == nil {
		cancel()
		return nil, nil, nil, ErrNoGeneration
	}
	m.current.refs++
	pending := &pendingDial{cancel: cancel}
	m.pending[pending] = struct{}{}
	m.operations++
	return m.current, pending, dialCtx, nil
}

func (m *Manager) release(gen *generation) error {
	remove := false
	m.mu.Lock()
	gen.refs--
	if gen.refs < 0 {
		m.mu.Unlock()
		panic("xrayrt: negative generation reference count")
	}
	if gen.draining && gen.refs == 0 && !gen.removing && !gen.removed {
		gen.removing = true
		remove = true
	}
	m.mu.Unlock()
	if remove {
		err := m.destroy(gen.handle)
		m.finishRemoval(gen, err)
		return err
	}
	return nil
}

func (m *Manager) releaseConnection(connection *leasedConn, gen *generation) error {
	remove := false
	m.mu.Lock()
	delete(m.active, connection)
	gen.refs--
	if gen.refs < 0 {
		m.mu.Unlock()
		panic("xrayrt: negative generation reference count")
	}
	if gen.draining && gen.refs == 0 && !gen.removing && !gen.removed {
		gen.removing = true
		remove = true
	}
	m.mu.Unlock()
	if !remove {
		return nil
	}
	err := m.destroy(gen.handle)
	m.finishRemoval(gen, err)
	return err
}

func (m *Manager) waitOperations(ctx context.Context) error {
	for {
		m.mu.Lock()
		if m.operations == 0 {
			m.mu.Unlock()
			return nil
		}
		changed := m.operationChanged
		m.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrShutdownIncomplete, ctx.Err())
		}
	}
}

func (m *Manager) operationDone() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.operations--
	if m.operations < 0 {
		panic("xrayrt: negative operation count")
	}
	close(m.operationChanged)
	m.operationChanged = make(chan struct{})
}

func (m *Manager) finishRemoval(gen *generation, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	gen.removing = false
	gen.cleanup = err
	if err == nil {
		gen.removed = true
		delete(m.draining, gen.id)
		return
	}
	m.draining[gen.id] = gen
}

func (m *Manager) trackCleanupFailure(id uint64, handle Generation, err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.draining[id] = &generation{id: id, handle: handle, draining: true, cleanup: err}
	m.mu.Unlock()
}

func (m *Manager) collectRemovalsLocked(result []*generation) []*generation {
	for _, gen := range m.draining {
		if gen.refs == 0 && !gen.removing && !gen.removed {
			gen.removing = true
			result = append(result, gen)
		}
	}
	return result
}

func (m *Manager) destroy(handle Generation) error {
	return m.backend.Remove(handle)
}

func generationStatus(gen *generation) GenerationStatus {
	return GenerationStatus{
		Generation:   gen.id,
		RefCount:     gen.refs,
		Draining:     gen.draining,
		CleanupError: errorString(gen.cleanup),
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func validateDial(ctx context.Context, outboundTag string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if outboundTag == "" {
		return errors.New("xrayrt: outbound tag is required")
	}
	return validateGenerationOutboundTag(outboundTag)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("xrayrt: nil context")
	}
	return ctx.Err()
}

type leasedConn struct {
	net.Conn
	once     sync.Once
	closeErr error
	cancel   context.CancelFunc
	release  func() error
	done     chan struct{}
}

func (c *leasedConn) Close() error {
	c.beginClose()
	<-c.done
	return c.closeErr
}

func (c *leasedConn) CloseWrite() error {
	closer, ok := c.Conn.(interface{ CloseWrite() error })
	if !ok {
		return ErrUnsupported
	}
	return closer.CloseWrite()
}

func (c *leasedConn) CloseRead() error {
	closer, ok := c.Conn.(interface{ CloseRead() error })
	if !ok {
		return ErrUnsupported
	}
	return closer.CloseRead()
}

func (c *leasedConn) beginClose() {
	c.once.Do(func() {
		go func() {
			c.cancel()
			c.closeErr = errors.Join(c.Conn.Close(), c.release())
			close(c.done)
		}()
	})
}

type pendingDial struct {
	cancel context.CancelFunc
}
