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
	"github.com/FrankoonG/x-tier/internal/egresspolicy"
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

const rendrRevocationTimeout = 2 * time.Second

const maxActiveCarriersPerPeer = rendradapter.MaxAcceptedSessions / 4

var ErrCarrierAdmissionLimit = errors.New("dataplane: carrier admission limit reached")

var ErrRendrRevocationIncomplete = errors.New("dataplane: rendr session revocation incomplete")

var ErrRendrAuthorizationUpdateFailStopped = errors.New("dataplane: rendr authorization update fail-stopped")

type rendrRuntime interface {
	SetDialers(rendradapter.StreamDialer, rendradapter.EgressDialer) error
	Dial(context.Context, string, rendradapter.Destination) (net.Conn, error)
	InjectCarrier(context.Context, rendradapter.OriginClaim, net.Conn) error
	Status() rendradapter.RuntimeStatus
	BeginClose()
	RevokeContext(context.Context) error
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
	users               map[string]string
	carrierPeers        map[string]string
	egressAuthorization *egressAuthorizationSnapshot
}

type Status struct {
	State                       string
	AppliedRevision             int64
	AttemptedRevision           int64
	AppliedDigest               [32]byte
	AttemptedDigest             [32]byte
	EgressAuthorizationRevision int64
	EgressAuthorizationDigest   [32]byte
	EgressAuthorizationSources  int
	LastError                   string
	LastErrorCode               string
	ObservedAt                  time.Time
	ObservationFresh            bool
	FailStopped                 bool
	Rendr                       rendradapter.RuntimeStatus
	Xray                        xrayrt.Status
	Listeners                   []ListenerStatus
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

	lifecycleMu          sync.RWMutex
	rendr                rendrRuntime
	rendrFactory         rendrRuntimeFactory
	closing              bool
	rendrDone            chan struct{}
	factoryDone          chan struct{}
	factoryWG            sync.WaitGroup
	operationDone        chan struct{}
	operationWG          sync.WaitGroup
	retirements          []*rendrRetirement
	retirementCloseError error
	lastRendrRestart     time.Time
	xray                 *xrayrt.Runtime
	bridge               *xraybridge.Handler
	routes               atomic.Pointer[routeTable]
	egressLookup         egresspolicy.LookupNetIP

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
	egressAuthorization, err := compileEgressAuthorization(cfg)
	if err != nil {
		return nil, fmt.Errorf("dataplane: compile initial egress authorization: %w", err)
	}

	planeCtx, cancel := context.WithCancel(ctx)
	p := &Plane{
		ctx:    planeCtx,
		cancel: cancel,
		rendrFactory: func(ctx context.Context) (rendrRuntime, error) {
			return rendradapter.NewRuntime(ctx)
		},
		rendrDone:     make(chan struct{}),
		factoryDone:   make(chan struct{}),
		operationDone: make(chan struct{}),
		status: Status{
			State:             "starting",
			AppliedRevision:   -1,
			AttemptedRevision: cfg.Revision,
			ObservedAt:        time.Now().UTC(),
		},
		listeners: make(map[string]string),
	}
	p.routes.Store(newEmptyRouteTable())
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
	if err := p.bindRendrDialers(runtime, egressAuthorization); err != nil {
		return nil, errors.Join(
			fmt.Errorf("dataplane: bind Rendr dialers: %w", err),
			closeStartedPlane(p),
		)
	}
	p.routes.Store(routesFrom(compiled, egressAuthorization))
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

func closeRendrRuntimeForRevocation(ctx context.Context, runtime rendrRuntime) error {
	if runtime == nil {
		return nil
	}
	runtime.BeginClose()
	if ctx == nil {
		return errors.Join(ErrRendrRevocationIncomplete, errors.New("dataplane: nil revocation context"))
	}
	waitCtx, cancel := context.WithTimeout(ctx, rendrRevocationTimeout)
	err := runtime.RevokeContext(waitCtx)
	cancel()
	if errors.Is(err, rendradapter.ErrShutdownIncomplete) {
		forceErr, completed := forceRendrRuntime(runtime, rendrForceCloseAttemptTimeout)
		if !completed {
			forceErr = errors.Join(forceErr, rendradapter.ErrShutdownIncomplete)
		}
		return errors.Join(ErrRendrRevocationIncomplete, err, forceErr)
	}
	return err
}

func (p *Plane) currentRendr() rendrRuntime {
	p.lifecycleMu.RLock()
	defer p.lifecycleMu.RUnlock()
	return p.rendr
}

func (p *Plane) ensureRendrRunning(ctx context.Context) error {
	if ctx == nil {
		return errors.New("dataplane: nil rendr recovery context")
	}
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
	if status.State != "failed" && status.State != "stopping" && status.State != "stopped" {
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
	p.lifecycleMu.Unlock()

	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, rendrRevocationTimeout)
	defer cancelRecovery()
	replacement, err := p.callPreparedRendrRuntime(recoveryCtx, p.activeEgressAuthorization())
	if err != nil {
		return fmt.Errorf("dataplane: restart rendr runtime: %w", err)
	}

	p.lifecycleMu.Lock()
	if p.closing {
		p.lifecycleMu.Unlock()
		p.retireRendrRuntime(replacement)
		return net.ErrClosed
	}
	if p.rendr != current {
		p.lifecycleMu.Unlock()
		p.retireRendrRuntime(replacement)
		return nil
	}
	retirement := &rendrRetirement{runtime: current, done: make(chan struct{})}
	p.collectCompletedRetirementsLocked()
	p.rendr = replacement
	p.retirements = append(p.retirements, retirement)
	p.lifecycleMu.Unlock()
	p.revokeRuntimeCarriers(current)
	current.BeginClose()
	go func() {
		retirement.err = closeRendrRuntime(current)
		close(retirement.done)
	}()
	if err := closeRendrRuntimeForRevocation(recoveryCtx, current); err != nil {
		status := current.Status()
		return errors.Join(
			err,
			fmt.Errorf("dataplane: failed-runtime revocation barrier retained client=%d accepted=%d", status.ActiveClient, status.ActiveAccepted),
		)
	}
	return nil
}

type rendrFactoryResult struct {
	runtime rendrRuntime
	err     error
}

func (p *Plane) callPreparedRendrRuntime(
	waitCtx context.Context,
	egressAuthorization *egressAuthorizationSnapshot,
) (rendrRuntime, error) {
	if waitCtx == nil {
		return nil, errors.New("dataplane: nil rendr factory wait context")
	}
	p.lifecycleMu.Lock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		p.lifecycleMu.Unlock()
		return nil, net.ErrClosed
	}
	factory := p.rendrFactory
	factoryCtx := p.ctx
	if factory == nil {
		p.lifecycleMu.Unlock()
		return nil, errors.New("dataplane: rendr runtime factory unavailable")
	}
	p.factoryWG.Add(1)
	p.lifecycleMu.Unlock()

	result := make(chan rendrFactoryResult)
	abandoned := make(chan struct{})
	go func() {
		defer p.factoryWG.Done()
		runtime, err := factory(factoryCtx)
		if err == nil && runtime == nil {
			err = errors.New("dataplane: rendr runtime factory returned nil")
		}
		if err == nil {
			if bindErr := p.bindRendrDialers(runtime, egressAuthorization); bindErr != nil {
				err = fmt.Errorf("dataplane: bind prepared rendr runtime: %w", bindErr)
			}
		}
		if err == nil {
			if status := runtime.Status(); status.State != "running" {
				err = fmt.Errorf("dataplane: prepared rendr runtime is %s", status.State)
			}
		}
		if err != nil && runtime != nil {
			p.retireRendrRuntime(runtime)
			runtime = nil
		}
		select {
		case result <- rendrFactoryResult{runtime: runtime, err: err}:
		case <-abandoned:
			if runtime != nil {
				p.retireRendrRuntime(runtime)
			}
		}
	}()

	select {
	case outcome := <-result:
		return outcome.runtime, outcome.err
	case <-waitCtx.Done():
		close(abandoned)
		return nil, waitCtx.Err()
	case <-factoryCtx.Done():
		close(abandoned)
		return nil, net.ErrClosed
	}
}

func (p *Plane) beginOperation() bool {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		return false
	}
	p.operationWG.Add(1)
	return true
}

func (p *Plane) retireRendrRuntime(runtime rendrRuntime) {
	if runtime == nil {
		return
	}
	retirement := &rendrRetirement{runtime: runtime, done: make(chan struct{})}
	p.lifecycleMu.Lock()
	p.collectCompletedRetirementsLocked()
	p.retirements = append(p.retirements, retirement)
	p.lifecycleMu.Unlock()
	runtime.BeginClose()
	go func() {
		retirement.err = closeRendrRuntime(runtime)
		close(retirement.done)
	}()
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
	if !p.beginOperation() {
		return net.ErrClosed
	}
	defer p.operationWG.Done()
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
	wasFailStopped := p.failStopped
	candidateRequired := cfg.Revision != appliedRevision || wasFailStopped
	if !candidateRequired && digest != appliedDigest {
		return p.setFailed(cfg.Revision, publicerr.Errorf(
			"dataplane.revision_content_mismatch",
			"configured revision %d differs from the applied content",
			cfg.Revision,
		))
	}
	previousEgressAuthorization := p.activeEgressAuthorization()
	var compiled xrayconfig.Compiled
	var candidateEgressAuthorization *egressAuthorizationSnapshot
	authorizationRevocation := false
	if candidateRequired {
		compiled, err = xrayconfig.Compile(cfg)
		if err != nil {
			return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: compile: %w", err))
		}
		candidateEgressAuthorization, err = compileEgressAuthorization(cfg)
		if err != nil {
			return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: compile egress authorization: %w", err))
		}
		activeRoutes := p.routes.Load()
		candidateRoutes := routesFrom(compiled, candidateEgressAuthorization)
		authorizationRevocation = carrierRevocationRequired(activeRoutes, candidateRoutes) ||
			!sameEgressAuthorization(activeEgressAuthorization(activeRoutes), candidateEgressAuthorization)
	}
	if err := p.ensureRendrRunning(ctx); err != nil {
		if errors.Is(err, ErrRendrRevocationIncomplete) || authorizationRevocation {
			recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), applyRecoveryTimeout)
			routeErr := p.failStopRoutes(recoveryCtx)
			cancelRecovery()
			return p.failStopRevocationApply(cfg.Revision, errors.Join(
				ErrRendrAuthorizationUpdateFailStopped,
				publicerr.Wrap("runtime.rendr_recovery_failed", err),
				routeErr,
			))
		}
		return p.setFailed(cfg.Revision, publicerr.Wrap("runtime.rendr_recovery_failed", err))
	}
	if !candidateRequired {
		if err := p.xray.RetryCleanup(); err != nil {
			return p.setFailed(cfg.Revision, publicerr.Wrap("runtime.xray_cleanup_failed", err))
		}
		return p.commitAppliedCurrent(cfg.Revision, digest)
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
		if err := p.publishOutboundAndRoutes(ctx, &compiled, candidateEgressAuthorization); err != nil {
			if errors.Is(err, ErrRendrAuthorizationUpdateFailStopped) {
				return p.failStopRevocationApply(cfg.Revision, err)
			}
			return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: apply Xray generation: %w", err))
		}
		return p.commitApplied(&compiled, cfg.Revision, digest)
	}

	// Stop admitting managed traffic while handlers, routes, and outbound
	// generations are changed. Existing streams retain their generation lease.
	if err := p.xray.ReplaceInbounds(ctx, nil); err != nil {
		return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: quiesce Xray inbounds: %w", err))
	}
	if err := p.publishOutboundAndRoutes(ctx, &compiled, candidateEgressAuthorization); err != nil {
		if errors.Is(err, ErrRendrAuthorizationUpdateFailStopped) {
			return p.failStopRevocationApply(cfg.Revision, err)
		}
		recoveryCtx, cancel := context.WithTimeout(context.Background(), applyRecoveryTimeout)
		defer cancel()
		if wasFailStopped {
			routeStopErr := p.failStopRoutes(recoveryCtx)
			failStopErr := p.xray.ReplaceInbounds(recoveryCtx, nil)
			if failStopErr != nil {
				failStopErr = fmt.Errorf("dataplane: preserve fail-stopped Xray inbounds: %w", failStopErr)
			}
			return p.setFailed(cfg.Revision, errors.Join(
				fmt.Errorf("dataplane: apply Xray generation: %w", err),
				routeStopErr,
				failStopErr,
			))
		}
		restoreErr := p.xray.ReplaceInbounds(recoveryCtx, previous.Inbounds)
		if restoreErr != nil {
			failStopErr := errors.Join(
				fmt.Errorf("dataplane: restore previous Xray inbounds: %w", restoreErr),
				p.failStopRoutes(recoveryCtx),
			)
			return p.failStopRevocationApply(cfg.Revision, errors.Join(
				fmt.Errorf("dataplane: apply Xray generation: %w", err),
				failStopErr,
			))
		}
		return p.setFailed(cfg.Revision, fmt.Errorf("dataplane: apply Xray generation: %w", err))
	}
	// Authorization removal is intentionally irreversible at the session
	// layer. A later inbound-install rollback restores configuration and new
	// admission, but it never resurrects sessions owned by the revoked runtime.
	if err := p.xray.ReplaceInbounds(ctx, compiled.Inbounds); err != nil {
		inboundErr := fmt.Errorf("dataplane: replace Xray inbounds: %w", err)
		recoveryCtx, cancel := context.WithTimeout(context.Background(), applyRecoveryTimeout)
		defer cancel()
		if wasFailStopped {
			routeStopErr := p.failStopRoutes(recoveryCtx)
			failStopErr := p.xray.ReplaceInbounds(recoveryCtx, nil)
			if failStopErr != nil {
				failStopErr = fmt.Errorf("dataplane: preserve fail-stopped Xray inbounds: %w", failStopErr)
			}
			return p.setFailed(cfg.Revision, errors.Join(inboundErr, routeStopErr, failStopErr))
		}
		rollbackErr := p.publishOutboundAndRoutes(recoveryCtx, previous, previousEgressAuthorization)
		if rollbackErr != nil {
			routeStopErr := p.failStopRoutes(recoveryCtx)
			failStopErr := p.xray.ReplaceInbounds(recoveryCtx, nil)
			if failStopErr != nil {
				failStopErr = fmt.Errorf("dataplane: fail-stop managed inbounds: %w", failStopErr)
			}
			return p.failStopRevocationApply(cfg.Revision, errors.Join(
				inboundErr,
				rollbackErr,
				routeStopErr,
				failStopErr,
			))
		}
		restoreErr := p.xray.ReplaceInbounds(recoveryCtx, previous.Inbounds)
		if restoreErr != nil {
			return p.failStopRevocationApply(cfg.Revision, errors.Join(
				inboundErr,
				fmt.Errorf("dataplane: restore previous Xray inbounds: %w", restoreErr),
				p.failStopRoutes(recoveryCtx),
			))
		}
		return p.setFailed(cfg.Revision, inboundErr)
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
	runtime := p.currentRendr()
	p.applyMu.RUnlock()
	target := rendradapter.Destination{
		Host: request.Target.Address.String(),
		Port: uint16(request.Target.Port),
	}
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
	select {
	case <-p.operationDone:
		retirements, historicalRetirementError = p.rendrRetirementSnapshot()
		rendrError = errors.Join(rendrError, normalizeRendrCloseError(historicalRetirementError))
	default:
		rendrError = errors.Join(rendrError, xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete)
	}
	select {
	case <-p.factoryDone:
		retirements, historicalRetirementError = p.rendrRetirementSnapshot()
		rendrError = errors.Join(rendrError, normalizeRendrCloseError(historicalRetirementError))
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
	if errors.Is(p.closeErr, xrayrt.ErrShutdownIncomplete) {
		p.setState("failed")
		return p.closeErr
	}
	p.closed = true
	p.setState("stopped")
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
	if p.factoryDone == nil {
		p.factoryDone = make(chan struct{})
	}
	if p.operationDone == nil {
		p.operationDone = make(chan struct{})
	}
	p.rendrOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		p.revokeAllCarriers()
		p.setState("stopping")
		go func() {
			p.operationWG.Wait()
			close(p.operationDone)
		}()
		go func() {
			p.factoryWG.Wait()
			close(p.factoryDone)
		}()
		go func() {
			if runtime != nil {
				p.rendrError = runtime.CloseContext(context.Background())
			}
			close(p.rendrDone)
		}()
	})
}

func (p *Plane) waitRendrShutdown(ctx context.Context) error {
	select {
	case <-p.operationDone:
	case <-ctx.Done():
		return errors.Join(xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete, ctx.Err())
	}
	select {
	case <-p.rendrDone:
	case <-ctx.Done():
		return errors.Join(xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete, ctx.Err())
	}
	select {
	case <-p.factoryDone:
	case <-ctx.Done():
		return errors.Join(xrayrt.ErrShutdownIncomplete, rendradapter.ErrShutdownIncomplete, ctx.Err())
	}
	retirements, historicalRetirementError := p.rendrRetirementSnapshot()
	result := errors.Join(normalizeRendrCloseError(historicalRetirementError), normalizeRendrCloseError(p.rendrError))
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
	retirementError := p.retirementCloseError
	for _, retirement := range p.retirements {
		select {
		case <-retirement.done:
			retirementError = errors.Join(retirementError, retirement.err)
		default:
		}
	}
	return append([]*rendrRetirement(nil), p.retirements...), retirementError
}

func (p *Plane) rendrRetirementHealthError() error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	p.collectCompletedRetirementsLocked()
	var healthError error
	for _, retirement := range p.retirements {
		select {
		case <-retirement.done:
			healthError = errors.Join(healthError, retirement.err)
		default:
		}
	}
	return healthError
}

func (p *Plane) collectCompletedRetirementsLocked() {
	active := p.retirements[:0]
	for _, retirement := range p.retirements {
		select {
		case <-retirement.done:
			if retirement.err == nil {
				continue
			}
			if retirement.runtime == nil {
				p.retirementCloseError = errors.Join(p.retirementCloseError, retirement.err)
				continue
			}
			if retirement.runtime.Status().State == "stopped" {
				p.retirementCloseError = errors.Join(
					p.retirementCloseError,
					resolvedRendrRetirementError(retirement.err),
				)
				continue
			}
			active = append(active, retirement)
		default:
			active = append(active, retirement)
		}
	}
	p.retirements = active
}

func resolvedRendrRetirementError(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var result error
		for _, child := range joined.Unwrap() {
			result = errors.Join(result, resolvedRendrRetirementError(child))
		}
		return result
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return resolvedRendrRetirementError(wrapped.Unwrap())
	}
	if errors.Is(err, rendradapter.ErrShutdownIncomplete) ||
		errors.Is(err, xrayrt.ErrShutdownIncomplete) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
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

func (p *Plane) bindRendrDialers(runtime rendrRuntime, authorization *egressAuthorizationSnapshot) error {
	if runtime == nil || authorization == nil {
		return errors.New("dataplane: incomplete rendr dialer authorization")
	}
	return runtime.SetDialers(p.dialCarrier, func(ctx context.Context, request rendradapter.EgressRequest) (net.Conn, error) {
		return p.dialEgressAuthorized(ctx, authorization, request)
	})
}

func (p *Plane) dialEgress(ctx context.Context, request rendradapter.EgressRequest) (net.Conn, error) {
	return p.dialEgressAuthorized(ctx, p.activeEgressAuthorization(), request)
}

func (p *Plane) dialEgressAuthorized(
	ctx context.Context,
	authorization *egressAuthorizationSnapshot,
	request rendradapter.EgressRequest,
) (net.Conn, error) {
	if p == nil || p.xray == nil || request.Validate() != nil {
		return nil, errors.New("dataplane: egress flow claim is not authorized")
	}
	policy, authorized := p.egressPolicyFor(authorization, request.Claim.Origin)
	if !authorized {
		return nil, errors.New("dataplane: egress flow claim is not authorized")
	}
	candidates, err := egresspolicy.Resolve(
		ctx,
		p.egressLookup,
		policy,
		request.Destination.Host,
		request.Destination.Port,
	)
	if err != nil {
		return nil, err
	}
	var dialErr error
	for _, candidate := range candidates {
		if !p.egressClaimAuthorized(authorization, request.Claim.Origin) {
			return nil, errors.New("dataplane: egress flow claim was revoked")
		}
		address := net.JoinHostPort(candidate.String(), fmt.Sprint(request.Destination.Port))
		conn, err := p.xray.DialFixed(ctx, xrayconfig.EgressOutboundTag, address)
		if err != nil {
			dialErr = errors.Join(dialErr, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if err := xrayegress.ConfirmReady(ctx, conn); err != nil {
			dialErr = errors.Join(dialErr, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if !p.egressClaimAuthorized(authorization, request.Claim.Origin) {
			_ = conn.Close()
			return nil, errors.New("dataplane: egress flow claim was revoked")
		}
		return conn, nil
	}
	if dialErr == nil {
		dialErr = errors.New("dataplane: no permitted egress candidate")
	}
	return nil, dialErr
}

func (p *Plane) egressPolicyFor(
	authorization *egressAuthorizationSnapshot,
	claim rendradapter.OriginClaim,
) (egresspolicy.Policy, bool) {
	if p == nil || claim.Validate() != nil || claim.InboundTag != xrayconfig.NodeVLESSTag {
		return egresspolicy.Policy{}, false
	}
	routes := p.routes.Load()
	if routes == nil || authorization == nil || routes.egressAuthorization != authorization ||
		routes.carrierPeers[claim.PrincipalHandle] != claim.ClaimedPeerNodeID {
		return egresspolicy.Policy{}, false
	}
	policy, allowed := authorization.policies[claim.ClaimedPeerNodeID]
	return policy, allowed
}

func (p *Plane) egressClaimAuthorized(
	authorization *egressAuthorizationSnapshot,
	claim rendradapter.OriginClaim,
) bool {
	_, authorized := p.egressPolicyFor(authorization, claim)
	return authorized
}

func (p *Plane) publishOutboundAndRoutes(
	ctx context.Context,
	compiled *xrayconfig.Compiled,
	egressAuthorization *egressAuthorizationSnapshot,
) error {
	if compiled == nil || egressAuthorization == nil {
		return errors.New("dataplane: no compiled configuration to publish")
	}
	previousRoutes := p.routes.Load()
	previousEgressAuthorization := activeEgressAuthorization(previousRoutes)
	if sameEgressAuthorization(previousEgressAuthorization, egressAuthorization) {
		egressAuthorization = previousEgressAuthorization
	}
	routes := routesFrom(*compiled, egressAuthorization)
	rotate := carrierRevocationRequired(previousRoutes, routes) ||
		previousEgressAuthorization != egressAuthorization
	operationCtx := ctx
	var cancelOperation context.CancelFunc
	if rotate {
		operationCtx, cancelOperation = context.WithTimeout(ctx, rendrRevocationTimeout)
		defer cancelOperation()
	}
	var current, replacement rendrRuntime
	var err error
	if rotate {
		current, replacement, err = p.prepareRendrRotation(operationCtx, egressAuthorization)
		if err != nil {
			stopErr := p.failStopRoutes(operationCtx)
			return errors.Join(
				ErrRendrAuthorizationUpdateFailStopped,
				fmt.Errorf("dataplane: prepare rendr authorization rotation: %w", err),
				stopErr,
			)
		}
	}
	p.applyMu.Lock()
	_, err = p.xray.Apply(operationCtx, compiled.Outbound)
	if err != nil {
		p.applyMu.Unlock()
		p.retireRendrRuntime(replacement)
		if rotate {
			stopErr := p.failStopRoutes(operationCtx)
			return errors.Join(ErrRendrAuthorizationUpdateFailStopped, err, stopErr)
		}
		return err
	}
	p.routes.Store(routes)
	if rotate {
		installed, installErr := p.installRendrRotation(operationCtx, current, replacement)
		if installErr != nil {
			if !installed {
				failStoppedRoutes := newEmptyRouteTable()
				p.routes.Store(failStoppedRoutes)
				p.revokeUnauthorizedCarriers(failStoppedRoutes)
				p.revokeRuntimeCarriers(current)
				if current != nil {
					current.BeginClose()
				}
				active := p.currentRendr()
				if active != nil && active != current {
					p.revokeRuntimeCarriers(active)
					active.BeginClose()
				}
				p.applyMu.Unlock()
				if current != nil && current != active {
					p.retireRendrRuntime(current)
				}
				p.retireRendrRuntime(replacement)
				var revocationErr error
				if current != nil {
					revocationErr = errors.Join(
						revocationErr,
						closeRendrRuntimeForRevocation(operationCtx, current),
					)
				}
				if active != nil && active != current {
					revocationErr = errors.Join(
						revocationErr,
						closeRendrRuntimeForRevocation(operationCtx, active),
					)
				}
				return errors.Join(ErrRendrAuthorizationUpdateFailStopped, installErr, revocationErr)
			}
			failStoppedRoutes := newEmptyRouteTable()
			p.routes.Store(failStoppedRoutes)
			p.revokeUnauthorizedCarriers(failStoppedRoutes)
			p.revokeRuntimeCarriers(replacement)
			replacement.BeginClose()
			p.applyMu.Unlock()
			return errors.Join(ErrRendrAuthorizationUpdateFailStopped, installErr)
		}
		p.applyMu.Unlock()
		return nil
	}
	p.revokeUnauthorizedCarriers(routes)
	p.applyMu.Unlock()
	return nil
}

func (p *Plane) failStopRoutes(ctx context.Context) error {
	p.applyMu.Lock()
	routes := newEmptyRouteTable()
	p.routes.Store(routes)
	p.revokeUnauthorizedCarriers(routes)
	current := p.currentRendr()
	p.revokeRuntimeCarriers(current)
	if current != nil {
		current.BeginClose()
	}
	p.applyMu.Unlock()
	return closeRendrRuntimeForRevocation(ctx, current)
}

func (p *Plane) failStopRevocationApply(revision int64, cause error) error {
	p.failStopped = true
	recoveryCtx, cancel := context.WithTimeout(context.Background(), applyRecoveryTimeout)
	defer cancel()
	inboundErr := p.xray.ReplaceInbounds(recoveryCtx, nil)
	if inboundErr != nil {
		inboundErr = fmt.Errorf("dataplane: fail-stop managed Xray inbounds: %w", inboundErr)
	}
	errorCode := "runtime.authorization_update_fail_stopped"
	if errors.Is(cause, ErrRendrRevocationIncomplete) || inboundErr != nil {
		errorCode = "runtime.fail_stop_incomplete"
	}
	failure := publicerr.Wrap(errorCode, cause)
	markErr := p.markFailStopped(errorCode)
	return errors.Join(failure, inboundErr, markErr)
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
	if !p.beginOperation() {
		return net.ErrClosed
	}
	defer p.operationWG.Done()
	p.reconcileMu.Lock()
	defer p.reconcileMu.Unlock()
	p.lifecycleMu.RLock()
	closing := p.closing || p.ctx == nil || p.ctx.Err() != nil
	p.lifecycleMu.RUnlock()
	if closing {
		return net.ErrClosed
	}
	routeErr := p.failStopRoutes(ctx)
	p.failStopped = true
	inboundErr := p.xray.ReplaceInbounds(ctx, nil)
	statusCode := errorCode
	if routeErr != nil || inboundErr != nil {
		statusCode = "runtime.fail_stop_incomplete"
	}
	markErr := p.markFailStopped(statusCode)
	if routeErr != nil || inboundErr != nil {
		var wrappedInbound error
		if inboundErr != nil {
			wrappedInbound = fmt.Errorf("dataplane: remove managed Xray inbounds: %w", inboundErr)
		}
		return errors.Join(routeErr, wrappedInbound, markErr)
	}
	return markErr
}

func (p *Plane) prepareRendrRotation(
	ctx context.Context,
	egressAuthorization *egressAuthorizationSnapshot,
) (rendrRuntime, rendrRuntime, error) {
	p.lifecycleMu.RLock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		p.lifecycleMu.RUnlock()
		return nil, nil, net.ErrClosed
	}
	current := p.rendr
	p.lifecycleMu.RUnlock()
	if current == nil {
		return nil, nil, errors.New("dataplane: rendr runtime unavailable")
	}
	replacement, err := p.callPreparedRendrRuntime(ctx, egressAuthorization)
	if err != nil {
		return current, nil, fmt.Errorf("dataplane: rotate rendr runtime: %w", err)
	}
	return current, replacement, nil
}

func (p *Plane) installRendrRotation(ctx context.Context, current, replacement rendrRuntime) (bool, error) {
	if current == nil || replacement == nil {
		return false, errors.New("dataplane: incomplete rendr rotation")
	}
	p.lifecycleMu.Lock()
	if p.closing || p.ctx == nil || p.ctx.Err() != nil {
		p.lifecycleMu.Unlock()
		return false, net.ErrClosed
	}
	if p.rendr != current {
		p.lifecycleMu.Unlock()
		return false, errors.New("dataplane: rendr runtime changed during rotation")
	}
	retirement := &rendrRetirement{runtime: current, done: make(chan struct{})}
	p.collectCompletedRetirementsLocked()
	p.rendr = replacement
	p.retirements = append(p.retirements, retirement)
	p.lifecycleMu.Unlock()

	p.revokeRuntimeCarriers(current)
	current.BeginClose()
	go func() {
		retirement.err = closeRendrRuntime(current)
		close(retirement.done)
	}()
	waitCtx, cancel := context.WithTimeout(ctx, rendrRevocationTimeout)
	defer cancel()
	if err := closeRendrRuntimeForRevocation(waitCtx, current); err != nil {
		status := current.Status()
		return true, errors.Join(
			err,
			fmt.Errorf("dataplane: revocation barrier retained client=%d accepted=%d", status.ActiveClient, status.ActiveAccepted),
		)
	}
	return true, nil
}

func (p *Plane) observeRuntime() {
	retirementErr := p.rendrRetirementHealthError()
	runtime := p.currentRendr()
	rendrStatus := rendradapter.RuntimeStatus{State: "unavailable", ObservedAt: time.Now().UTC()}
	if runtime != nil {
		rendrStatus = runtime.Status()
	}
	xrayStatus := p.xray.Status()
	managedTags := p.xray.ManagedInboundTags()
	routes := p.routes.Load()
	authorization := activeEgressAuthorization(routes)
	p.stateMu.Lock()
	p.status.Rendr = rendrStatus
	p.status.Xray = xrayStatus
	p.status.Listeners = listenerStatuses(p.listeners, managedTags, routes)
	p.status.EgressAuthorizationRevision = authorization.sourceRevision
	p.status.EgressAuthorizationDigest = authorization.digest
	p.status.EgressAuthorizationSources = len(authorization.policies)
	if retirementErr != nil && p.status.State == "running" {
		p.status.State = "degraded"
		p.status.LastErrorCode = "runtime.rendr_retirement_failed"
		p.status.LastError = publicerr.MessageCode(p.status.LastErrorCode)
	} else if retirementErr == nil &&
		p.status.State == "degraded" &&
		!p.status.FailStopped &&
		p.status.LastErrorCode == "runtime.rendr_retirement_failed" {
		p.status.State = "running"
		p.status.LastErrorCode = ""
		p.status.LastError = ""
	}
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

func routesFrom(
	compiled xrayconfig.Compiled,
	egressAuthorization *egressAuthorizationSnapshot,
) *routeTable {
	routes := &routeTable{
		users:               make(map[string]string),
		carrierPeers:        make(map[string]string, len(compiled.CarrierPeers)),
		egressAuthorization: egressAuthorization,
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

func newEmptyRouteTable() *routeTable {
	return &routeTable{
		users:               map[string]string{},
		carrierPeers:        map[string]string{},
		egressAuthorization: newDenyEgressAuthorization(-1),
	}
}

func activeEgressAuthorization(routes *routeTable) *egressAuthorizationSnapshot {
	if routes == nil || routes.egressAuthorization == nil {
		return newDenyEgressAuthorization(-1)
	}
	return routes.egressAuthorization
}

func (p *Plane) activeEgressAuthorization() *egressAuthorizationSnapshot {
	if p == nil {
		return newDenyEgressAuthorization(-1)
	}
	return activeEgressAuthorization(p.routes.Load())
}

func carrierRevocationRequired(previous, next *routeTable) bool {
	if previous == nil || len(previous.carrierPeers) == 0 {
		return false
	}
	if next == nil {
		return true
	}
	for account, peer := range previous.carrierPeers {
		if next.carrierPeers[account] != peer {
			return true
		}
	}
	return false
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
	claim := rendradapter.OriginClaim{
		Assurance:         rendradapter.OriginAssuranceXrayBearer,
		ClaimedPeerNodeID: peerNodeID,
		InboundTag:        request.InboundTag,
		PrincipalHandle:   request.AuthenticatedUser,
	}
	if err := runtime.InjectCarrier(ctx, claim, tracked); err != nil {
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
		conn.Interrupt()
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
	done      chan struct{}
	startOnce sync.Once
	errMu     sync.Mutex
	closeErr  error
}

func newCloseObservedConn(conn net.Conn) *closeObservedConn {
	return &closeObservedConn{Conn: conn, done: make(chan struct{})}
}

func (c *closeObservedConn) Close() error {
	c.startClose()
	<-c.done
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.closeErr
}

// Interrupt starts transport retirement without allowing a broken Close
// implementation to delay authorization revocation. Handoff still waits on
// done, preserving its connection-ownership contract.
func (c *closeObservedConn) Interrupt() {
	c.startClose()
}

func (c *closeObservedConn) startClose() {
	c.startOnce.Do(func() { go c.closeUnderlying() })
}

func (c *closeObservedConn) closeUnderlying() {
	err := c.Conn.Close()
	c.errMu.Lock()
	c.closeErr = err
	c.errMu.Unlock()
	close(c.done)
}

func (c *closeObservedConn) CloseWrite() error {
	if closer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return rendradapter.ErrHalfCloseUnsupported
}
