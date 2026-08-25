package dataplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/rendradapter"
	"github.com/FrankoonG/x-tier/internal/xraybridge"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
	"github.com/FrankoonG/x-tier/internal/xrayegress"
	"github.com/FrankoonG/x-tier/internal/xrayrt"
	featureoutbound "github.com/xtls/xray-core/features/outbound"
)

const bridgeOutboundTag = "xtier-runtime-bridge"

const applyRecoveryTimeout = 5 * time.Second

const planeShutdownTimeout = 5 * time.Second

const rendrRestartMinimumInterval = time.Second

const rendrForceCloseAttemptTimeout = 250 * time.Millisecond

const maxActiveCarriersPerPeer = rendradapter.MaxAcceptedSessions / 4

var ErrCarrierAdmissionLimit = errors.New("dataplane: carrier admission limit reached")

type rendrRuntime interface {
	SetDialers(rendradapter.StreamDialer, rendradapter.EgressDialer) error
	Dial(context.Context, string, rendradapter.Destination) (net.Conn, error)
	InjectCarrier(context.Context, net.Conn) error
	Status() rendradapter.RuntimeStatus
	CloseContext(context.Context) error
	ForceClose() error
}

type rendrRuntimeFactory func(context.Context) (rendrRuntime, error)

type rendrRetirement struct {
	runtime rendrRuntime
	done    chan struct{}
	err     error
}

type routeTable struct {
	users        map[string]string
	carrierPeers map[string]string
}

type Status struct {
	State             string
	AppliedRevision   int64
	AttemptedRevision int64
	AppliedDigest     [32]byte
	AttemptedDigest   [32]byte
	LastError         string
	LastErrorCode     string
	ObservedAt        time.Time
	ObservationFresh  bool
	FailStopped       bool
	Rendr             rendradapter.RuntimeStatus
	Xray              xrayrt.Status
	Listeners         []ListenerStatus
}

type ListenerStatus struct {
	Tag    string
	Listen string
	State  string
}

// Plane owns one Xray instance and one Rendr runtime for one daemon process.
type Plane struct {
	ctx    context.Context
	cancel context.CancelFunc

	lifecycleMu      sync.RWMutex
	rendr            rendrRuntime
	rendrFactory     rendrRuntimeFactory
	closing          bool
	rendrDone        chan struct{}
	retirements      []*rendrRetirement
	retirementError  error
	lastRendrRestart time.Time
	xray             *xrayrt.Runtime
	bridge           *xraybridge.Handler
	routes           atomic.Pointer[routeTable]

	reconcileMu    sync.Mutex
	applyMu        sync.RWMutex
	carrierMu      sync.Mutex
	stateMu        sync.RWMutex
	status         Status
	listeners      map[string]string
	current        *xrayconfig.Compiled
	failStopped    bool
	activeCarriers map[*closeObservedConn]carrierAuthorization
	carrierCounts  map[string]int

	closeMu    sync.Mutex
	rendrOnce  sync.Once
	rendrError error
	closed     bool
	closeErr   error
}

func Start(ctx context.Context, cfg configstore.Config) (*Plane, error) {
	if ctx == nil {
		return nil, errors.New("dataplane: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	compiled, err := xrayconfig.Compile(cfg)
	if err != nil {
		return nil, fmt.Errorf("dataplane: compile revision %d: %w", cfg.Revision, err)
	}
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		return nil, fmt.Errorf("dataplane: digest initial configuration: %w", err)
	}

	planeCtx, cancel := context.WithCancel(ctx)
	p := &Plane{
		ctx:    planeCtx,
		cancel: cancel,
		rendrFactory: func(ctx context.Context) (rendrRuntime, error) {
			return rendradapter.NewRuntime(ctx)
		},
		rendrDone: make(chan struct{}),
		status: Status{
			State:             "starting",
			AppliedRevision:   -1,
			AttemptedRevision: cfg.Revision,
			ObservedAt:        time.Now().UTC(),
		},
		listeners: make(map[string]string),
	}
	p.routes.Store(&routeTable{users: map[string]string{}, carrierPeers: map[string]string{}})
	p.activeCarriers = make(map[*closeObservedConn]carrierAuthorization)
	p.carrierCounts = make(map[string]int)

	runtime, err := p.rendrFactory(planeCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dataplane: start rendr: %w", err)
	}
	p.rendr = runtime

	bridge, err := xraybridge.New(xraybridge.Config{
		Tag: bridgeOutboundTag,
		Routes: map[string]xraybridge.RouteKind{
			xrayconfig.NodeVLESSTag: xraybridge.RouteCarrier,
			xrayconfig.UserSOCKSTag: xraybridge.RouteUserEgress,
		},
		Carrier:    p,
		UserEgress: p,
	})
	if err != nil {
		_ = closeRendrRuntime(runtime)
		cancel()
		return nil, fmt.Errorf("dataplane: create Xray bridge: %w", err)
	}
	p.bridge = bridge
	egress, err := xrayegress.New(xrayconfig.EgressOutboundTag)
	if err != nil {
		_ = closeRendrRuntime(runtime)
		cancel()
		return nil, fmt.Errorf("dataplane: create Xray egress: %w", err)
	}

	xrayRuntime, err := xrayrt.StartRuntime(planeCtx, xrayrt.StartOptions{
		DefaultOutbound: bridge,
		FixedOutbounds:  []featureoutbound.Handler{egress},
	})
	if err != nil {
		_ = closeRendrRuntime(runtime)
		cancel()
		return nil, fmt.Errorf("dataplane: start Xray: %w", err)
	}
	p.xray = xrayRuntime
	if _, err := xrayRuntime.Apply(planeCtx, compiled.Outbound); err != nil {
		return nil, errors.Join(
			fmt.Errorf("dataplane: apply initial Xray generation: %w", err),
			closeStartedPlane(p),
		)
	}
	if err := runtime.SetDialers(p.dialCarrier, p.dialEgress); err != nil {
		return nil, errors.Join(
			fmt.Errorf("dataplane: bind Rendr dialers: %w", err),
			closeStartedPlane(p),
		)
	}
	p.routes.Store(routesFrom(compiled))
	if err := xrayRuntime.ReplaceInbounds(planeCtx, compiled.Inbounds); err != nil {
		return nil, errors.Join(
			fmt.Errorf("dataplane: install initial Xray inbounds: %w", err),
			closeStartedPlane(p),
		)
	}
	p.current = &compiled
	p.setApplied(cfg.Revision, digest, compiled.Listeners)
	return p, nil
}

func closeStartedPlane(plane *Plane) error {
	ctx, cancel := context.WithTimeout(context.Background(), planeShutdownTimeout)
	err := plane.CloseContext(ctx)
	cancel()
	if errors.Is(err, xrayrt.ErrShutdownIncomplete) {
		return errors.Join(err, plane.ForceClose())
	}
	return err
}

func closeRendrRuntime(runtime rendrRuntime) error {
	if runtime == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), planeShutdownTimeout)
	err := runtime.CloseContext(ctx)
	cancel()
	if errors.Is(err, rendradapter.ErrShutdownIncomplete) {
		err = errors.Join(err, runtime.ForceClose())
	}
	return err
}

func (p *Plane) currentRendr() rendrRuntime {
	p.lifecycleMu.RLock()
	defer p.lifecycleMu.RUnlock()
	return p.rendr
}

func (p *Plane) ensureRendrRunning() error {
	if p.ctx == nil || p.ctx.Err() != nil {
		return net.ErrClosed
	}
	current := p.currentRendr()
	if current == nil {
		return errors.New("dataplane: rendr runtime unavailable")
	}
	status := current.Status()
	if status.State == "running" {
		return nil
	}
	if status.State != "failed" {
		return fmt.Errorf("dataplane: rendr runtime is %s", status.State)
	}

	now := time.Now()
	p.lifecycleMu.Lock()
	if p.closing || p.ctx.Err() != nil {
		p.lifecycleMu.Unlock()
		return net.ErrClosed
	}
	if p.rendr != current {
		p.lifecycleMu.Unlock()
		return nil
	}
	p.collectCompletedRetirementsLocked()
	if !p.lastRendrRestart.IsZero() && now.Sub(p.lastRendrRestart) < rendrRestartMinimumInterval {
		next := p.lastRendrRestart.Add(rendrRestartMinimumInterval)
		p.lifecycleMu.Unlock()
		return fmt.Errorf("dataplane: rendr restart backoff active until %s", next.UTC().Format(time.RFC3339Nano))
	}
	p.lastRendrRestart = now
	factory := p.rendrFactory
	p.lifecycleMu.Unlock()

	replacement, err := factory(p.ctx)
	if err != nil {
		return fmt.Errorf("dataplane: restart rendr runtime: %w", err)
	}
	if err := replacement.SetDialers(p.dialCarrier, p.dialEgress); err != nil {
		_ = closeRendrRuntime(replacement)
		return fmt.Errorf("dataplane: bind restarted rendr runtime: %w", err)
	}
	if replacementStatus := replacement.Status(); replacementStatus.State != "running" {
		_ = closeRendrRuntime(replacement)
		return fmt.Errorf("dataplane: restarted rendr runtime is %s", replacementStatus.State)
	}

	p.lifecycleMu.Lock()
	if p.closing {
		p.lifecycleMu.Unlock()
		_ = closeRendrRuntime(replacement)
		return net.ErrClosed
	}
	if p.rendr != current {
		p.lifecycleMu.Unlock()
		_ = closeRendrRuntime(replacement)
		return nil
	}
	retirement := &rendrRetirement{runtime: current, done: make(chan struct{})}
	p.collectCompletedRetirementsLocked()
	p.rendr = replacement
	p.retirements = append(p.retirements, retirement)
	p.lifecycleMu.Unlock()
	p.revokeRuntimeCarriers(current)
	go func() {
		retirement.err = closeRendrRuntime(current)
		close(retirement.done)
	}()
	return nil
}

// Apply reconciles a complete configuration. Managed listeners are quiesced
// before the outbound generation and route table move together, so a request
// can observe either the previous pair or the candidate pair, never a mix.
func (p *Plane) Apply(ctx context.Context, cfg configstore.Config) error {
	if p == nil {
		return errors.New("dataplane: nil plane")
	}
	if ctx == nil {
		return errors.New("dataplane: nil apply context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()
	if p.ctx == nil || p.ctx.Err() != nil {
		return net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: digest configuration: %w", err))
	}
	if err := p.setAttemptIfOpen(ctx, cfg.Revision, digest); err != nil {
		return err
	}
	p.stateMu.RLock()
	appliedRevision := p.status.AppliedRevision
	appliedDigest := p.status.AppliedDigest
	p.stateMu.RUnlock()
	if cfg.Revision < appliedRevision {
		return p.setFailed(cfg.Revision, publicerr.Errorf(
			"dataplane.revision_regression",
			"configured revision %d is older than applied revision %d",
			cfg.Revision,
			appliedRevision,
		))
	}
	if err := p.ensureRendrRunning(); err != nil {
		return p.setFailed(cfg.Revision, publicerr.Wrap("runtime.rendr_recovery_failed", err))
	}
	wasFailStopped := p.failStopped
	if cfg.Revision == appliedRevision && !wasFailStopped {
		if digest != appliedDigest {
			return p.setFailed(cfg.Revision, publicerr.Errorf(
				"dataplane.revision_content_mismatch",
				"configured revision %d differs from the applied content",
				cfg.Revision,
			))
		}
		if err := p.xray.RetryCleanup(); err != nil {
			return p.setFailed(cfg.Revision, publicerr.Wrap("runtime.xray_cleanup_failed", err))
		}
		return p.commitAppliedCurrent(cfg.Revision, digest)
	}

	compiled, err := xrayconfig.Compile(cfg)
	if err != nil {
		return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: compile: %w", err))
	}
	previous := p.current
	if previous == nil {
		return p.setFailed(cfg.Revision, errors.New("dataplane: previous applied configuration unavailable"))
	}
	inboundsUnchanged, err := xrayrt.EquivalentInbounds(previous.Inbounds, compiled.Inbounds)
	if err != nil {
		return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: compare Xray inbounds: %w", err))
	}
	if inboundsUnchanged && !wasFailStopped {
		if err := p.publishOutboundAndRoutes(ctx, &compiled); err != nil {
			return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: apply Xray generation: %w", err))
		}
		return p.commitApplied(&compiled, cfg.Revision, digest)
	}

	// Stop admitting managed traffic while handlers, routes, and outbound
	// generations are changed. Existing streams retain their generation lease.
	if err := p.xray.ReplaceInbounds(ctx, nil); err != nil {
		return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: quiesce Xray inbounds: %w", err))
	}
	if err := p.publishOutboundAndRoutes(ctx, &compiled); err != nil {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), applyRecoveryTimeout)
		defer cancel()
		if wasFailStopped {
			p.failStopRoutes()
			failStopErr := p.xray.ReplaceInbounds(recoveryCtx, nil)
			if failStopErr != nil {
				failStopErr = fmt.Errorf("dataplane: preserve fail-stopped Xray inbounds: %w", failStopErr)
			}
			return p.setFailed(cfg.Revision, errors.Join(
				fmt.Errorf("dataplane: apply Xray generation: %w", err),
				failStopErr,
			))
		}
		restoreErr := p.xray.ReplaceInbounds(recoveryCtx, previous.Inbounds)
		if restoreErr != nil {
			p.failStopRoutes()
			restoreErr = fmt.Errorf("dataplane: restore previous Xray inbounds: %w", restoreErr)
		}
		return p.setFailed(cfg.Revision, errors.Join(
			fmt.Errorf("dataplane: apply Xray generation: %w", err),
			restoreErr,
		))
	}
	if err := p.xray.ReplaceInbounds(ctx, compiled.Inbounds); err != nil {
		inboundErr := fmt.Errorf("dataplane: replace Xray inbounds: %w", err)
		recoveryCtx, cancel := context.WithTimeout(context.Background(), applyRecoveryTimeout)
		defer cancel()
		if wasFailStopped {
			p.failStopRoutes()
			failStopErr := p.xray.ReplaceInbounds(recoveryCtx, nil)
			if failStopErr != nil {
				failStopErr = fmt.Errorf("dataplane: preserve fail-stopped Xray inbounds: %w", failStopErr)
			}
			return p.setFailed(cfg.Revision, errors.Join(inboundErr, failStopErr))
		}
		rollbackErr := p.publishOutboundAndRoutes(recoveryCtx, previous)
		if rollbackErr != nil {
			p.failStopRoutes()
			failStopErr := p.xray.ReplaceInbounds(recoveryCtx, nil)
			if failStopErr != nil {
				failStopErr = fmt.Errorf("dataplane: fail-stop managed inbounds: %w", failStopErr)
			}
			return p.setFailed(cfg.Revision, errors.Join(
				inboundErr,
				rollbackErr,
				failStopErr,
			))
		}
		restoreErr := p.xray.ReplaceInbounds(recoveryCtx, previous.Inbounds)
		if restoreErr != nil {
			p.failStopRoutes()
			restoreErr = fmt.Errorf("dataplane: restore previous Xray inbounds: %w", restoreErr)
		}
		return p.setFailed(cfg.Revision, errors.Join(inboundErr, restoreErr))
	}
	return p.commitApplied(&compiled, cfg.Revision, digest)
}

func (p *Plane) Dial(ctx context.Context, request xraybridge.UserRequest) (net.Conn, error) {
	p.applyMu.RLock()
	routes := p.routes.Load()
	peerTag := ""
	if routes != nil {
		peerTag = routes.users[request.InboundTag]
	}
	if peerTag == "" {
		p.applyMu.RUnlock()
		return nil, fmt.Errorf("dataplane: inbound %q has no applied exit peer", request.InboundTag)
	}
	p.applyMu.RUnlock()
	target := rendradapter.Destination{
		Host: request.Target.Address.String(),
		Port: uint16(request.Target.Port),
	}
	runtime := p.currentRendr()
	if runtime == nil {
		return nil, errors.New("dataplane: rendr runtime unavailable")
	}
	return runtime.Dial(ctx, peerTag, target)
}

func (p *Plane) Status() Status {
	if p == nil {
		return Status{State: "unavailable", AppliedRevision: -1, AttemptedRevision: -1, ObservedAt: time.Now().UTC()}
	}
	fresh := p.reconcileMu.TryLock()
	if fresh {
		p.observeRuntime()
		p.reconcileMu.Unlock()
	}
	p.stateMu.RLock()
	status := p.status
	status.ObservationFresh = fresh
	status.Listeners = append([]ListenerStatus(nil), p.status.Listeners...)
	p.stateMu.RUnlock()
	return status
}

func (p *Plane) Close() error {
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), planeShutdownTimeout)
	defer cancel()
	return p.CloseContext(ctx)
}

func (p *Plane) CloseContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return errors.Join(xrayrt.ErrShutdownIncomplete, errors.New("dataplane: nil shutdown context"))
	}
	p.closeMu.Lock()
	if p.closed {
		err := p.closeErr
		p.closeMu.Unlock()
		return err
	}
	p.beginClose()
	p.closeMu.Unlock()

	xrayError := p.xray.CloseContext(ctx)
	rendrError := p.waitRendrShutdown(ctx)
	result := errors.Join(rendrError, xrayError)
	if errors.Is(result, xrayrt.ErrShutdownIncomplete) {
		return result
	}
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closeErr = result
	p.closed = true
	p.setState("stopped")
	return p.closeErr
}

func (p *Plane) ForceClose() error {
	if p == nil {
		return nil
	}
	p.closeMu.Lock()
	if p.closed {
		err := p.closeErr
		p.closeMu.Unlock()
		return err
	}
	p.beginClose()
	p.closeMu.Unlock()

	runtime := p.currentRendr()
	retirements, historicalRetirementError := p.rendrRetirementSnapshot()
	rendrError := normalizeRendrCloseError(historicalRetirementError)
	if runtime != nil {
		forceErr, completed := forceRendrRuntime(runtime, rendrForceCloseAttemptTimeout)
		if !completed {
			forceErr = errors.Join(forceErr, xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete)
		}
		rendrError = errors.Join(rendrError, normalizeRendrCloseError(forceErr))
	}
	select {
	case <-p.rendrDone:
		rendrError = errors.Join(rendrError, normalizeRendrCloseError(p.rendrError))
	default:
		rendrError = errors.Join(rendrError, xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete)
	}
	for _, retirement := range retirements {
		select {
		case <-retirement.done:
			rendrError = errors.Join(rendrError, normalizeRendrCloseError(retirement.err))
		default:
			forceErr, completed := forceRendrRuntime(retirement.runtime, rendrForceCloseAttemptTimeout)
			if !completed {
				forceErr = errors.Join(forceErr, context.DeadlineExceeded)
			}
			rendrError = errors.Join(
				rendrError,
				normalizeRendrCloseError(forceErr),
				xrayrt.ErrShutdownIncomplete,
				rendradapter.ErrShutdownIncomplete,
			)
		}
	}
	xrayError := p.xray.ForceClose()
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closeErr = errors.Join(rendrError, xrayError)
	p.closed = true
	if errors.Is(p.closeErr, xrayrt.ErrShutdownIncomplete) {
		p.setState("failed")
	} else {
		p.setState("stopped")
	}
	return p.closeErr
}

func (p *Plane) beginClose() {
	p.lifecycleMu.Lock()
	p.closing = true
	runtime := p.rendr
	p.lifecycleMu.Unlock()
	if p.rendrDone == nil {
		p.rendrDone = make(chan struct{})
	}
	p.rendrOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		p.revokeAllCarriers()
		p.setState("stopping")
		go func() {
			if runtime != nil {
				p.rendrError = runtime.CloseContext(context.Background())
			}
			close(p.rendrDone)
		}()
	})
}

func (p *Plane) waitRendrShutdown(ctx context.Context) error {
	retirements, historicalRetirementError := p.rendrRetirementSnapshot()
	result := normalizeRendrCloseError(historicalRetirementError)
	select {
	case <-p.rendrDone:
		result = errors.Join(result, normalizeRendrCloseError(p.rendrError))
	case <-ctx.Done():
		return errors.Join(result, xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete, ctx.Err())
	}
	for _, retirement := range retirements {
		select {
		case <-retirement.done:
			result = errors.Join(result, normalizeRendrCloseError(retirement.err))
		case <-ctx.Done():
			return errors.Join(result, xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete, ctx.Err())
		}
	}
	return result
}

func (p *Plane) rendrRetirementSnapshot() ([]*rendrRetirement, error) {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	p.collectCompletedRetirementsLocked()
	return append([]*rendrRetirement(nil), p.retirements...), p.retirementError
}

func (p *Plane) collectCompletedRetirementsLocked() {
	active := p.retirements[:0]
	for _, retirement := range p.retirements {
		select {
		case <-retirement.done:
			p.retirementError = errors.Join(p.retirementError, retirement.err)
		default:
			active = append(active, retirement)
		}
	}
	p.retirements = active
}

func forceRendrRuntime(runtime rendrRuntime, timeout time.Duration) (error, bool) {
	if runtime == nil {
		return nil, true
	}
	result := make(chan error, 1)
	go func() { result <- runtime.ForceClose() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return err, true
	case <-timer.C:
		return context.DeadlineExceeded, false
	}
}

func normalizeRendrCloseError(err error) error {
	if errors.Is(err, rendradapter.ErrShutdownIncomplete) {
		return errors.Join(xrayrt.ErrShutdownIncomplete, err)
	}
	return err
}

func (p *Plane) dialCarrier(ctx context.Context, peerTag, innerTarget string) (net.Conn, error) {
	return p.xray.Dial(ctx, peerTag, innerTarget)
}

func (p *Plane) dialEgress(ctx context.Context, host string, port uint16) (net.Conn, error) {
	return p.xray.DialFixed(ctx, xrayconfig.EgressOutboundTag, net.JoinHostPort(host, fmt.Sprint(port)))
}

func (p *Plane) publishOutboundAndRoutes(ctx context.Context, compiled *xrayconfig.Compiled) error {
	if compiled == nil {
		return errors.New("dataplane: no compiled configuration to publish")
	}
	p.applyMu.Lock()
	defer p.applyMu.Unlock()
	_, err := p.xray.Apply(ctx, compiled.Outbound)
	if err != nil {
		return err
	}
	routes := routesFrom(*compiled)
	p.routes.Store(routes)
	p.revokeUnauthorizedCarriers(routes)
	return nil
}

func (p *Plane) failStopRoutes() {
	p.applyMu.Lock()
	defer p.applyMu.Unlock()
	routes := &routeTable{users: map[string]string{}, carrierPeers: map[string]string{}}
	p.routes.Store(routes)
	p.revokeUnauthorizedCarriers(routes)
}

// FailStop revokes all managed admissions when the authoritative configuration
// can no longer be read. A later successful Apply restores the same revision.
func (p *Plane) FailStop(ctx context.Context, errorCode string) error {
	if p == nil {
		return errors.New("dataplane: nil plane")
	}
	if ctx == nil {
		return errors.New("dataplane: nil fail-stop context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if errorCode == "" {
		errorCode = "runtime.fail_stopped"
	}
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()
	p.lifecycleMu.RLock()
	closing := p.closing || p.ctx == nil || p.ctx.Err() != nil
	p.lifecycleMu.RUnlock()
	if closing {
		return net.ErrClosed
	}
	p.failStopRoutes()
	p.failStopped = true
	inboundErr := p.xray.ReplaceInbounds(ctx, nil)
	statusCode := errorCode
	if inboundErr != nil {
		statusCode = "runtime.fail_stop_incomplete"
	}
	markErr := p.markFailStopped(statusCode)
	if inboundErr != nil {
		return errors.Join(fmt.Errorf("dataplane: remove managed Xray inbounds: %w", inboundErr), markErr)
	}
	return markErr
}

func (p *Plane) observeRuntime() {
	_, _ = p.rendrRetirementSnapshot()
	runtime := p.currentRendr()
	rendrStatus := rendradapter.RuntimeStatus{State: "unavailable", ObservedAt: time.Now().UTC()}
	if runtime != nil {
		rendrStatus = runtime.Status()
	}
	xrayStatus := p.xray.Status()
	managedTags := p.xray.ManagedInboundTags()
	routes := p.routes.Load()
	p.stateMu.Lock()
	p.status.Rendr = rendrStatus
	p.status.Xray = xrayStatus
	p.status.Listeners = listenerStatuses(p.listeners, managedTags, routes)
	p.stateMu.Unlock()
}

func (p *Plane) setAttemptIfOpen(ctx context.Context, revision int64, digest [32]byte) error {
	p.lifecycleMu.RLock()
	defer p.lifecycleMu.RUnlock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		return net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.stateMu.Lock()
	p.status.State = "applying"
	p.status.AttemptedRevision = revision
	p.status.AttemptedDigest = digest
	p.status.LastError = ""
	p.status.LastErrorCode = ""
	p.status.ObservedAt = time.Now().UTC()
	p.stateMu.Unlock()
	return nil
}

func (p *Plane) setApplied(revision int64, digest [32]byte, listeners map[string]string) {
	p.stateMu.Lock()
	p.status.State = "running"
	p.status.AppliedRevision = revision
	p.status.AttemptedRevision = revision
	p.status.AppliedDigest = digest
	p.status.AttemptedDigest = digest
	p.status.LastError = ""
	p.status.LastErrorCode = ""
	p.status.FailStopped = false
	p.status.ObservedAt = time.Now().UTC()
	p.listeners = cloneListeners(listeners)
	p.stateMu.Unlock()
}

// commitApplied runs after the candidate routes and listeners are published.
// Caller cancellation can no longer roll that mutation back, so lifecycle
// closure is the only condition that may reject the bookkeeping commit.
func (p *Plane) commitApplied(compiled *xrayconfig.Compiled, revision int64, digest [32]byte) error {
	if compiled == nil {
		return errors.New("dataplane: no compiled configuration to commit")
	}
	p.lifecycleMu.RLock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		p.lifecycleMu.RUnlock()
		return net.ErrClosed
	}
	p.current = compiled
	p.failStopped = false
	p.setApplied(revision, digest, compiled.Listeners)
	p.lifecycleMu.RUnlock()
	for _, generation := range p.xray.Status().Draining {
		if generation.CleanupError != "" {
			return p.setFailed(revision, publicerr.Errorf(
				"runtime.xray_cleanup_failed",
				"generation %d retirement remains pending",
				generation.Generation,
			))
		}
	}
	return nil
}

func (p *Plane) commitAppliedCurrent(revision int64, digest [32]byte) error {
	p.lifecycleMu.RLock()
	defer p.lifecycleMu.RUnlock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		return net.ErrClosed
	}
	p.stateMu.Lock()
	p.status.State = "running"
	p.status.AppliedRevision = revision
	p.status.AttemptedRevision = revision
	p.status.AppliedDigest = digest
	p.status.AttemptedDigest = digest
	p.status.LastError = ""
	p.status.LastErrorCode = ""
	p.status.FailStopped = false
	p.status.ObservedAt = time.Now().UTC()
	p.stateMu.Unlock()
	return nil
}

func (p *Plane) setFailed(revision int64, err error) error {
	p.lifecycleMu.RLock()
	defer p.lifecycleMu.RUnlock()
	if p.closing {
		return err
	}
	p.stateMu.Lock()
	p.status.State = "degraded"
	p.status.AttemptedRevision = revision
	p.status.LastError = sanitizeRuntimeError(err)
	p.status.LastErrorCode = publicerr.Code(err, "runtime.apply_failed")
	p.status.ObservedAt = time.Now().UTC()
	p.stateMu.Unlock()
	return err
}

func (p *Plane) markFailStopped(errorCode string) error {
	p.lifecycleMu.RLock()
	defer p.lifecycleMu.RUnlock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		return net.ErrClosed
	}
	p.stateMu.Lock()
	p.status.State = "degraded"
	p.status.LastError = publicerr.MessageCode(errorCode)
	p.status.LastErrorCode = publicerr.NormalizeCode(errorCode, "runtime.apply_failed")
	p.status.FailStopped = true
	p.status.ObservedAt = time.Now().UTC()
	p.stateMu.Unlock()
	return nil
}

func sanitizeRuntimeError(err error) string {
	return publicerr.Message(err, "runtime.apply_failed")
}

func (p *Plane) setState(state string) {
	p.stateMu.Lock()
	p.status.State = state
	p.status.ObservedAt = time.Now().UTC()
	p.stateMu.Unlock()
}

func routesFrom(compiled xrayconfig.Compiled) *routeTable {
	routes := &routeTable{
		users: make(map[string]string), carrierPeers: make(map[string]string, len(compiled.CarrierPeers)),
	}
	for tag, route := range compiled.Routes {
		if route.Kind == xrayconfig.RouteUser {
			routes.users[tag] = route.PeerTag
		}
	}
	for accountHandle, peerNodeID := range compiled.CarrierPeers {
		routes.carrierPeers[accountHandle] = peerNodeID
	}
	return routes
}

func cloneListeners(listeners map[string]string) map[string]string {
	cloned := make(map[string]string, len(listeners))
	for tag, address := range listeners {
		cloned[tag] = address
	}
	return cloned
}

func listenerStatuses(configured map[string]string, activeTags []string, routes *routeTable) []ListenerStatus {
	active := make(map[string]bool, len(activeTags))
	for _, tag := range activeTags {
		active[tag] = true
	}
	tags := make([]string, 0, len(configured)+len(active))
	for tag := range configured {
		tags = append(tags, tag)
	}
	for tag := range active {
		if _, ok := configured[tag]; !ok {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	result := make([]ListenerStatus, 0, len(tags))
	for _, tag := range tags {
		address, configuredHere := configured[tag]
		state := "missing"
		switch {
		case !configuredHere:
			state = "unexpected"
		case tag == xrayconfig.UserSOCKSTag && (routes == nil || routes.users[tag] == ""):
			state = "unavailable"
		case tag == xrayconfig.NodeVLESSTag && (routes == nil || len(routes.carrierPeers) == 0):
			state = "unavailable"
		case active[tag]:
			state = "bound"
		}
		result = append(result, ListenerStatus{Tag: tag, Listen: address, State: state})
	}
	return result
}

type carrierAuthorization struct {
	accountHandle string
	peerNodeID    string
	runtime       rendrRuntime
}

func (p *Plane) Handoff(ctx context.Context, request xraybridge.CarrierRequest, conn net.Conn) error {
	if p == nil || conn == nil || request.InboundTag != xrayconfig.NodeVLESSTag || request.AuthenticatedUser == "" {
		return errors.New("dataplane: carrier principal is not authorized")
	}
	p.applyMu.RLock()
	routes := p.routes.Load()
	peerNodeID := ""
	if routes != nil {
		peerNodeID = routes.carrierPeers[request.AuthenticatedUser]
	}
	if peerNodeID == "" {
		p.applyMu.RUnlock()
		return errors.New("dataplane: carrier principal is not authorized")
	}
	tracked := newCloseObservedConn(conn)
	p.lifecycleMu.RLock()
	runtime := p.rendr
	closing := p.closing
	p.carrierMu.Lock()
	if len(p.activeCarriers) >= rendradapter.MaxAcceptedSessions || p.carrierCounts[peerNodeID] >= maxActiveCarriersPerPeer {
		p.carrierMu.Unlock()
		p.lifecycleMu.RUnlock()
		p.applyMu.RUnlock()
		return ErrCarrierAdmissionLimit
	}
	if closing || runtime == nil {
		p.carrierMu.Unlock()
		p.lifecycleMu.RUnlock()
		p.applyMu.RUnlock()
		return errors.New("dataplane: rendr runtime unavailable")
	}
	p.activeCarriers[tracked] = carrierAuthorization{
		accountHandle: request.AuthenticatedUser, peerNodeID: peerNodeID, runtime: runtime,
	}
	p.carrierCounts[peerNodeID]++
	p.carrierMu.Unlock()
	p.lifecycleMu.RUnlock()
	p.applyMu.RUnlock()
	defer func() {
		p.carrierMu.Lock()
		delete(p.activeCarriers, tracked)
		p.carrierCounts[peerNodeID]--
		if p.carrierCounts[peerNodeID] == 0 {
			delete(p.carrierCounts, peerNodeID)
		}
		p.carrierMu.Unlock()
	}()
	if err := runtime.InjectCarrier(ctx, tracked); err != nil {
		_ = tracked.Close()
		return err
	}
	select {
	case <-tracked.done:
		return nil
	case <-ctx.Done():
		_ = tracked.Close()
		return ctx.Err()
	}
}

func (p *Plane) revokeRuntimeCarriers(runtime rendrRuntime) {
	if runtime == nil {
		return
	}
	p.revokeCarriers(func(authorization carrierAuthorization) bool {
		return authorization.runtime == runtime
	})
}

func (p *Plane) revokeAllCarriers() {
	p.revokeCarriers(func(carrierAuthorization) bool { return true })
}

func (p *Plane) revokeCarriers(shouldRevoke func(carrierAuthorization) bool) {
	p.carrierMu.Lock()
	revoke := make([]*closeObservedConn, 0)
	for conn, authorization := range p.activeCarriers {
		if shouldRevoke(authorization) {
			revoke = append(revoke, conn)
		}
	}
	p.carrierMu.Unlock()
	for _, conn := range revoke {
		_ = conn.Close()
	}
}

func (p *Plane) revokeUnauthorizedCarriers(routes *routeTable) {
	p.revokeCarriers(func(authorization carrierAuthorization) bool {
		peerNodeID, allowed := routes.carrierPeers[authorization.accountHandle]
		return !allowed || peerNodeID != authorization.peerNodeID
	})
}

type closeObservedConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func newCloseObservedConn(conn net.Conn) *closeObservedConn {
	return &closeObservedConn{Conn: conn, done: make(chan struct{})}
}

func (c *closeObservedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

func (c *closeObservedConn) CloseWrite() error {
	if closer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return rendradapter.ErrHalfCloseUnsupported
}
