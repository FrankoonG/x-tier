package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/controlserver"
	"github.com/FrankoonG/x-tier/internal/dataplane"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/webbridge"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
	"github.com/FrankoonG/x-tier/internal/xrayrt"
)

type Options struct {
	ConfigPath                 string
	ControlAddr                string
	WebAddr                    string
	WebRoot                    string
	WebInsecurePrivateNetwork  bool
	ConfigReadFailureTolerance time.Duration
}

const (
	lastKnownGoodPersistError       = controlapi.LastKnownGoodPersistFailed
	lastKnownGoodRevisionAheadError = controlapi.LastKnownGoodRevisionAheadOfApplied
	configContentInvalidError       = "config.content_invalid"
	configReadFailedError           = "config.read_failed"
	configReadFailClosedError       = "config.read_failed_fail_closed"
	legacyRecoveryAmbiguousError    = "config.legacy_recovery_ambiguous"
)

const (
	maxReconcileBackoff               = 30 * time.Second
	defaultRuntimePlaneShutdownLimit  = 10 * time.Second
	defaultConfigReadFailureTolerance = 2 * time.Minute
	configFailStopTimeout             = 15 * time.Second
)

var (
	errRuntimePlaneShutdownTimedOut = errors.New("daemon: runtime plane graceful shutdown timed out")
	errReconcileShutdownTimedOut    = errors.New("daemon: reconciler shutdown timed out")
	errOperationShutdownTimedOut    = errors.New("daemon: active runtime operations shutdown timed out")
	errLastKnownGoodRevisionAhead   = errors.New("daemon: last-known-good revision is ahead of the applied revision")
)

type runtimePlane interface {
	Close() error
	Status() dataplane.Status
	Apply(context.Context, configstore.Config) error
}

type runtimePlaneStarter func(
	context.Context,
	string,
	*statestore.Store,
	configstore.Config,
) (runtimePlane, int64, *controlapi.StartupRollbackStatus, error)

type contextClosingRuntimePlane interface {
	CloseContext(context.Context) error
}

type forceClosingRuntimePlane interface {
	ForceClose() error
}

type failStoppingRuntimePlane interface {
	FailStop(context.Context, string) error
}

type Daemon struct {
	configPath        string
	runtimeConfigPath string
	startedAt         time.Time
	bootID            string

	ctx           context.Context
	cancel        context.CancelFunc
	serverMu      sync.RWMutex
	server        *controlserver.Server
	web           *webbridge.Server
	plane         runtimePlane
	lease         *instanceLease
	stateStore    *statestore.Store
	reconcileDone chan struct{}

	stateMu        sync.RWMutex
	state          controlapi.DaemonState
	configuration  controlapi.ConfigurationStatus
	observedConfig configObservation

	applyPersistMu       sync.Mutex
	operationMu          sync.Mutex
	operations           sync.WaitGroup
	operationsClosing    bool
	checkpointPersistMu  sync.Mutex
	reconcileFailureMu   sync.RWMutex
	reconcileFailure     reconcileFailureState
	configReadFailureMu  sync.RWMutex
	configContentFailure reconcileFailureState
	configIOFailure      reconcileFailureState
	configIOElapsed      time.Duration
	configIOLastAt       time.Time
	configIOActive       bool
	configReadFailClosed bool

	closeOnce                  sync.Once
	retryDelay                 time.Duration
	shutdownLimit              time.Duration
	configReadFailureTolerance time.Duration
	done                       chan struct{}
	waitMu                     sync.Mutex
	waitErr                    error
}

type reconcileFailureState struct {
	revision int64
	count    int
	firstAt  time.Time
	nextAt   time.Time
}

type configObservation struct {
	revision int64
	digest   [32]byte
}

func Start(ctx context.Context, opts Options) (*Daemon, error) {
	return start(ctx, opts, startRuntimePlane)
}

func start(ctx context.Context, opts Options, startPlane runtimePlaneStarter) (*Daemon, error) {
	if ctx == nil {
		return nil, fmt.Errorf("daemon.context_nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("daemon.context_done: %w", err)
	}
	if opts.ConfigPath == "" {
		return nil, fmt.Errorf("daemon.config_path_required")
	}
	configPath, err := configstore.CanonicalPath(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("daemon.config_path_invalid: %w", err)
	}
	lease, err := acquireInstanceLease(configPath)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = lease.Close()
		}
	}()
	runtimeConfigPath := lease.ConfigPath()
	stateStore := lease.Store()
	cfg, migrated, configRollback, err := loadInitialConfig(stateStore, runtimeConfigPath, configPath)
	if err != nil {
		return nil, fmt.Errorf("daemon.config_load: %w", err)
	}
	if err := migrateLegacyState(stateStore, cfg.Node.NodeID); err != nil {
		return nil, fmt.Errorf("daemon.state_migration: %w", err)
	}
	bootID, err := newBootID()
	if err != nil {
		return nil, fmt.Errorf("daemon.boot_id: %w", err)
	}

	daemonCtx, cancel := context.WithCancel(ctx)
	d := &Daemon{
		configPath:        configPath,
		runtimeConfigPath: runtimeConfigPath,
		startedAt:         time.Now().UTC(),
		bootID:            bootID,
		ctx:               daemonCtx,
		cancel:            cancel,
		lease:             lease,
		stateStore:        stateStore,
		state:             controlapi.DaemonStateStarting,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion:         cfg.SchemaVersion,
			MigratedAtStartup:     migrated,
			LastKnownGoodRevision: -1,
		},
		done:                       make(chan struct{}),
		reconcileDone:              make(chan struct{}),
		retryDelay:                 100 * time.Millisecond,
		shutdownLimit:              defaultRuntimePlaneShutdownLimit,
		configReadFailureTolerance: normalizedConfigReadFailureTolerance(opts.ConfigReadFailureTolerance),
	}
	runtimePlane, persistedRevision, startupRollback, err := startPlane(daemonCtx, runtimeConfigPath, stateStore, cfg)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("daemon.dataplane_start: %w", err)
	}
	if configRollback != nil {
		// Invalid active content is the operator-actionable startup event. A
		// simultaneous runtime recovery failure remains visible through the
		// plane's reconcile status; it must not remove the control-plane repair
		// route by turning degraded startup into a crash loop.
		startupRollback = configRollback
	}
	d.plane = runtimePlane
	configuredDigest, err := configstore.ContentDigest(cfg)
	if err != nil {
		cancel()
		return nil, errors.Join(
			fmt.Errorf("daemon.config_digest: %w", err),
			closeRuntimePlane(runtimePlane, d.retryDelay, d.shutdownLimit),
		)
	}
	d.recordObservedConfig(cfg.Revision, configuredDigest)
	if status := runtimePlane.Status(); status.LastError != "" {
		d.recordReconcileFailure(cfg.Revision, time.Now())
	}
	d.recordLastKnownGood(persistedRevision, "")
	d.setStartupRollback(startupRollback)
	server, err := controlserver.StartOwnedStore(
		daemonCtx,
		opts.ControlAddr,
		stateStore,
		configPath,
		d,
	)
	if err != nil {
		cancel()
		return nil, errors.Join(
			fmt.Errorf("daemon.control_start: %w", err),
			closeRuntimePlane(runtimePlane, d.retryDelay, d.shutdownLimit),
		)
	}
	d.serverMu.Lock()
	d.server = server
	d.serverMu.Unlock()
	if opts.WebAddr != "" {
		if _, err := controlapi.CreateStoreToken(stateStore, statestore.WebToken); err != nil {
			controlErr := closeAndWaitControlServer(server)
			cancel()
			return nil, errors.Join(
				fmt.Errorf("daemon.web_credential_create: %w", err),
				controlErr,
				closeRuntimePlane(runtimePlane, d.retryDelay, d.shutdownLimit),
			)
		}
		web, err := webbridge.Start(daemonCtx, webbridge.Config{
			Addr:                        opts.WebAddr,
			ControlAddr:                 server.Addr(),
			StateStore:                  stateStore,
			StaticDir:                   opts.WebRoot,
			AllowInsecurePrivateNetwork: opts.WebInsecurePrivateNetwork,
		})
		if err != nil {
			controlErr := closeAndWaitControlServer(server)
			cancel()
			return nil, errors.Join(
				fmt.Errorf("daemon.web_start: %w", err),
				controlErr,
				closeRuntimePlane(runtimePlane, d.retryDelay, d.shutdownLimit),
			)
		}
		d.serverMu.Lock()
		d.web = web
		d.serverMu.Unlock()
	}
	d.refreshOperationalStateFrom(cfg.Revision, configuredDigest, runtimePlane.Status())
	go d.reconcileLoop(cfg.Revision)
	go d.serve()
	started = true
	return d, nil
}

func (d *Daemon) Addr() string {
	if d == nil {
		return ""
	}
	d.serverMu.RLock()
	defer d.serverMu.RUnlock()
	if d.server == nil {
		return ""
	}
	return d.server.Addr()
}

func (d *Daemon) ConfigPath() string {
	if d == nil {
		return ""
	}
	return d.configPath
}

func (d *Daemon) loadConfig() (configstore.Config, error) {
	return loadExistingConfig(d.stateStore, d.runtimeConfigPath)
}

func (d *Daemon) loadLastKnownGood() (configstore.Config, error) {
	return loadLastKnownGood(d.stateStore, d.runtimeConfigPath)
}

func (d *Daemon) saveLastKnownGood(cfg configstore.Config) error {
	return saveLastKnownGood(d.stateStore, d.runtimeConfigPath, cfg)
}

func (d *Daemon) WebAddr() string {
	if d == nil {
		return ""
	}
	d.serverMu.RLock()
	defer d.serverMu.RUnlock()
	if d.web == nil {
		return ""
	}
	return d.web.Addr()
}

func (d *Daemon) Status(ctx context.Context) (controlapi.DaemonStatus, error) {
	if d == nil {
		return controlapi.DaemonStatus{}, fmt.Errorf("daemon.nil")
	}
	if ctx == nil {
		return controlapi.DaemonStatus{}, fmt.Errorf("daemon.status_context_nil")
	}
	if err := ctx.Err(); err != nil {
		return controlapi.DaemonStatus{}, err
	}
	if err := d.beginOperation(); err != nil {
		return controlapi.DaemonStatus{}, err
	}
	defer d.endOperation()
	cfg, loadErr := d.loadConfig()
	configuredRevision, configuredDigest := d.observedConfigSnapshot()
	if loadErr == nil {
		configuredDigest, loadErr = configstore.ContentDigest(cfg)
		if loadErr == nil {
			configuredRevision = cfg.Revision
			d.recordObservedConfig(configuredRevision, configuredDigest)
		}
	}
	planeStatus := d.plane.Status()
	baseState, configuration := d.stateAndConfigurationStatus()
	status := controlapi.DaemonStatus{
		APIVersion:  controlapi.APIVersion,
		BootID:      d.bootID,
		State:       operationalStateFrom(baseState, configuredRevision, configuredDigest, planeStatus, configuration),
		Revision:    configuredRevision,
		Reconcile:   d.reconcileStatusFrom(configuredRevision, configuredDigest, planeStatus),
		ConfigPath:  d.configPath,
		ControlAddr: d.Addr(),
		WebAddr:     d.WebAddr(),
		StartedAt:   d.startedAt,
		Idempotency: controlapi.IdempotencyStatus{
			Scope:             controlapi.IdempotencyScopeProcessMemory,
			RestartPersistent: false,
			Provisional:       true,
		},
		Configuration: configuration,
		Rendr:         rendrStatusFrom(planeStatus),
		Xray:          xrayStatusFrom(planeStatus),
	}
	if loadErr != nil {
		status.State = controlapi.DaemonStateDegraded
		status.Reconcile.State = controlapi.ReconcileStateFailed
		failureCode := configReadFailedError
		if configstore.IsContentError(loadErr) {
			failureCode = configContentInvalidError
		} else if d.configReadFailClosedSnapshot() {
			failureCode = configReadFailClosedError
		}
		status.Reconcile.LastError = publicerr.MessageCode(failureCode)
		status.Reconcile.LastErrorCode = failureCode
		status.Reconcile.ObservationFresh = false
		if failure, ok := d.configReadFailureSnapshot(); ok {
			first, next := failure.firstAt, failure.nextAt
			status.Reconcile.ConsecutiveFailures = failure.count
			status.Reconcile.FirstFailureAt = &first
			status.Reconcile.NextRetryAt = &next
		}
	}
	return status, nil
}

func (d *Daemon) Reload(ctx context.Context, expectedRevision int64, dryRun bool) (controlapi.ReconcileStatus, error) {
	if d == nil {
		return controlapi.ReconcileStatus{}, publicerr.Errorf("service.reload_unavailable", "runtime plane is unavailable")
	}
	if ctx == nil {
		return controlapi.ReconcileStatus{}, publicerr.Errorf("service.reload_context_nil", "reload context is nil")
	}
	if err := d.beginOperation(); err != nil {
		return controlapi.ReconcileStatus{}, err
	}
	defer d.endOperation()
	if d.plane == nil {
		return controlapi.ReconcileStatus{}, publicerr.Errorf("service.reload_unavailable", "runtime plane is unavailable")
	}
	applyCtx, cancel := linkedRuntimeContext(ctx, d.ctx)
	defer cancel()
	d.applyPersistMu.Lock()
	defer d.applyPersistMu.Unlock()
	if err := applyCtx.Err(); err != nil {
		return controlapi.ReconcileStatus{}, publicerr.Wrap("service.reload_canceled", err)
	}
	cfg, err := d.loadConfig()
	if err != nil {
		d.setOperationalState(true)
		if configstore.IsContentError(err) {
			d.recordConfigContentFailure(time.Now())
			return controlapi.ReconcileStatus{}, publicerr.Wrap(configContentInvalidError, err)
		}
		d.handleConfigReadFailure(time.Now())
		return controlapi.ReconcileStatus{}, publicerr.Wrap("service.reload_config", err)
	}
	if err := applyCtx.Err(); err != nil {
		return controlapi.ReconcileStatus{}, publicerr.Wrap("service.reload_canceled", err)
	}
	configuredDigest, err := configstore.ContentDigest(cfg)
	if err != nil {
		d.setOperationalState(true)
		d.recordConfigContentFailure(time.Now())
		return d.reconcileStatusFrom(cfg.Revision, [32]byte{}, d.plane.Status()), publicerr.Wrap(configContentInvalidError, err)
	}
	d.clearConfigReadFailureLedger()
	d.recordObservedConfig(cfg.Revision, configuredDigest)
	if err := configstore.ValidateRevision(cfg, expectedRevision); err != nil {
		return d.reconcileStatusFrom(cfg.Revision, configuredDigest, d.plane.Status()), err
	}
	if dryRun {
		if err := applyCtx.Err(); err != nil {
			return d.reconcileStatusFrom(cfg.Revision, configuredDigest, d.plane.Status()), publicerr.Wrap("service.reload_canceled", err)
		}
		if _, err := xrayconfig.Compile(cfg); err != nil {
			return d.reconcileStatusFrom(cfg.Revision, configuredDigest, d.plane.Status()), publicerr.Wrap("service.reload_validate", err)
		}
		return d.reconcileStatusFrom(cfg.Revision, configuredDigest, d.plane.Status()), nil
	}
	before := d.plane.Status()
	if appliedRuntimeHealthy(before, cfg.Revision, configuredDigest) {
		d.clearConfigReadFailureAfterRuntimeRecovery()
		d.clearStartupRollback()
		d.clearReconcileFailure()
		if err := d.persistLastKnownGood(cfg); err != nil {
			d.setOperationalState(true)
			return d.reconcileStatusFrom(cfg.Revision, configuredDigest, before), reloadCheckpointError(err)
		}
		d.refreshOperationalStateFrom(cfg.Revision, configuredDigest, before)
		return d.reconcileStatusFrom(cfg.Revision, configuredDigest, before), nil
	}
	d.setOperationalState(true)
	if err := d.plane.Apply(applyCtx, cfg); err != nil {
		d.setOperationalState(true)
		d.recordReconcileFailure(cfg.Revision, time.Now())
		return d.reconcileStatusFrom(cfg.Revision, configuredDigest, d.plane.Status()), publicerr.Wrap("service.reload_apply", err)
	}
	if err := applyCtx.Err(); err != nil {
		return d.reconcileStatusFrom(cfg.Revision, configuredDigest, d.plane.Status()), publicerr.Wrap("service.reload_canceled", err)
	}
	applied := d.plane.Status()
	if !appliedRuntimeHealthy(applied, cfg.Revision, configuredDigest) {
		d.setOperationalState(true)
		d.recordReconcileFailure(cfg.Revision, time.Now())
		if publishedConfigurationMatches(applied, cfg.Revision, configuredDigest) {
			return d.reconcileStatusFrom(cfg.Revision, configuredDigest, applied), publicerr.Errorf(
				"service.reload_applied_unhealthy",
				"runtime published the requested configuration but did not become healthy",
			)
		}
		return d.reconcileStatusFrom(cfg.Revision, configuredDigest, applied), publicerr.Errorf(
			"service.reload_not_applied",
			"runtime did not report the requested configuration as healthy and applied",
		)
	}
	d.clearConfigReadFailureAfterRuntimeRecovery()
	d.clearStartupRollback()
	d.clearReconcileFailure()
	if err := d.persistLastKnownGood(cfg); err != nil {
		d.setOperationalState(true)
		return d.reconcileStatusFrom(cfg.Revision, configuredDigest, applied), reloadCheckpointError(err)
	}
	d.refreshOperationalStateFrom(cfg.Revision, configuredDigest, applied)
	return d.reconcileStatusFrom(cfg.Revision, configuredDigest, applied), nil
}

// ReconcileCommittedRevision is the control-plane commit barrier. It keeps
// every acknowledged config revision observable by the runtime instead of
// relying on the periodic reconciler, which may legitimately coalesce file
// observations between ticks.
func (d *Daemon) ReconcileCommittedRevision(ctx context.Context, revision int64) (controlapi.ReconcileStatus, error) {
	return d.Reload(ctx, revision, false)
}

func (d *Daemon) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(d.cancel)
	return d.Wait()
}

func (d *Daemon) Done() <-chan struct{} {
	if d == nil || d.done == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return d.done
}

func (d *Daemon) Wait() error {
	if d == nil || d.done == nil {
		return nil
	}
	<-d.done
	d.waitMu.Lock()
	defer d.waitMu.Unlock()
	return d.waitErr
}

func (d *Daemon) serve() {
	d.serverMu.RLock()
	server := d.server
	web := d.web
	d.serverMu.RUnlock()
	var webDone <-chan struct{}
	if web != nil {
		webDone = web.Done()
	}
	select {
	case <-d.ctx.Done():
		d.stopAcceptingOperations()
		d.setState(controlapi.DaemonStateStopping)
		var err error
		if web != nil {
			err = errors.Join(err, closeAndWaitWebServer(web))
		}
		err = errors.Join(err, closeAndWaitControlServer(server))
		d.finish(err)
	case <-server.Done():
		d.stopAcceptingOperations()
		err := server.Wait()
		d.cancel()
		if web != nil {
			err = errors.Join(err, closeAndWaitWebServer(web))
		}
		d.finish(err)
	case <-webDone:
		d.stopAcceptingOperations()
		err := web.Wait()
		d.cancel()
		err = errors.Join(err, closeAndWaitControlServer(server))
		d.finish(err)
	}
}

func closeAndWaitControlServer(server *controlserver.Server) error {
	if server == nil {
		return nil
	}
	return errors.Join(server.Close(), server.Wait())
}

func closeAndWaitWebServer(server *webbridge.Server) error {
	if server == nil {
		return nil
	}
	return errors.Join(server.Close(), server.Wait())
}

func (d *Daemon) finish(err error) {
	d.stopAcceptingOperations()
	shutdownLimit := d.shutdownLimit
	if shutdownLimit <= 0 {
		shutdownLimit = defaultRuntimePlaneShutdownLimit
	}
	shutdownDeadline := time.Now().Add(shutdownLimit)
	operationDone := d.operationsDone()
	operationBudget := shutdownLimit / 4
	if operationBudget <= 0 {
		operationBudget = time.Nanosecond
	}
	operationErr := waitForOperations(operationDone, operationBudget)
	err = errors.Join(err, operationErr)
	if operationErr != nil {
		// A direct provider call may still own the state store or runtime plane.
		// Keep both alive after reporting the missed graceful deadline.
		<-operationDone
		shutdownDeadline = time.Now().Add(shutdownLimit)
	}
	if d.reconcileDone != nil {
		reconcileBudget := shutdownLimit / 4
		if reconcileBudget <= 0 {
			reconcileBudget = time.Nanosecond
		}
		reconcileErr := waitForReconciler(d.reconcileDone, reconcileBudget)
		err = errors.Join(err, reconcileErr)
		if reconcileErr != nil {
			// The reconciler may still be using the state store or runtime plane.
			// Report the missed deadline, but do not release owned resources under it.
			<-d.reconcileDone
			shutdownDeadline = time.Now().Add(shutdownLimit)
		}
	}
	if d.plane != nil {
		remaining := time.Until(shutdownDeadline)
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		err = errors.Join(err, closeRuntimePlane(d.plane, d.retryDelay, remaining))
	}
	err = errors.Join(err, d.lease.Close())
	d.setState(controlapi.DaemonStateStopped)
	d.waitMu.Lock()
	d.waitErr = errors.Join(d.waitErr, err)
	d.waitMu.Unlock()
	close(d.done)
}

func (d *Daemon) beginOperation() error {
	d.operationMu.Lock()
	defer d.operationMu.Unlock()
	if d.operationsClosing {
		return publicerr.Errorf("service.stopping", "daemon is stopping")
	}
	d.operations.Add(1)
	return nil
}

func (d *Daemon) endOperation() {
	d.operations.Done()
}

func (d *Daemon) stopAcceptingOperations() {
	d.operationMu.Lock()
	d.operationsClosing = true
	d.operationMu.Unlock()
}

func (d *Daemon) operationsDone() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		d.operations.Wait()
		close(done)
	}()
	return done
}

func waitForOperations(done <-chan struct{}, shutdownLimit time.Duration) error {
	if done == nil {
		return nil
	}
	if shutdownLimit <= 0 {
		shutdownLimit = defaultRuntimePlaneShutdownLimit
	}
	timer := time.NewTimer(shutdownLimit)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.Join(errOperationShutdownTimedOut, xrayrt.ErrShutdownIncomplete, context.DeadlineExceeded)
	}
}

func waitForReconciler(done <-chan struct{}, shutdownLimit time.Duration) error {
	if done == nil {
		return nil
	}
	if shutdownLimit <= 0 {
		shutdownLimit = defaultRuntimePlaneShutdownLimit
	}
	timer := time.NewTimer(shutdownLimit)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.Join(errReconcileShutdownTimedOut, xrayrt.ErrShutdownIncomplete, context.DeadlineExceeded)
	}
}

func (d *Daemon) xrayStatus() controlapi.XrayStatus {
	if d.plane == nil {
		return controlapi.XrayStatus{
			State: controlapi.RuntimeStateUnavailable, Draining: []controlapi.XrayGenerationStatus{}, Inbounds: []controlapi.XrayInboundStatus{},
		}
	}
	return xrayStatusFrom(d.plane.Status())
}

func xrayStatusFrom(planeStatus dataplane.Status) controlapi.XrayStatus {
	status := planeStatus.Xray
	state := controlapi.RuntimeStateUnavailable
	switch {
	case planeStatus.FailStopped || planeStatus.State == "failed":
		state = controlapi.RuntimeStateFailed
	case status.Closed || planeStatus.State == "stopped":
		state = controlapi.RuntimeStateStopped
	case planeStatus.State == "stopping" || (status.Current == nil && len(status.Draining) > 0):
		state = controlapi.RuntimeStateStopping
	case status.Current != nil:
		state = controlapi.RuntimeStateRunning
	case planeStatus.State == "starting" || planeStatus.State == "applying":
		state = controlapi.RuntimeStateStarting
	}
	result := controlapi.XrayStatus{
		State:                       state,
		FailStopped:                 planeStatus.FailStopped,
		Draining:                    make([]controlapi.XrayGenerationStatus, 0, len(status.Draining)),
		Inbounds:                    make([]controlapi.XrayInboundStatus, 0, len(planeStatus.Listeners)),
		StrictStreamOutbound:        status.StrictStreamOutbound,
		StrictPacketOutbound:        status.StrictPacketOutbound,
		EgressAuthorizationRevision: planeStatus.EgressAuthorizationRevision,
		EgressAuthorizationDigest:   hex.EncodeToString(planeStatus.EgressAuthorizationDigest[:]),
		EgressAuthorizationSources:  planeStatus.EgressAuthorizationSources,
		EgressAuthorizationDenials:  planeStatus.EgressAuthorizationDenials,
	}
	for _, inbound := range planeStatus.Listeners {
		result.Inbounds = append(result.Inbounds, controlapi.XrayInboundStatus{
			Tag: inbound.Tag, Listen: inbound.Listen, State: inbound.State,
		})
	}
	if status.Current != nil {
		current := controlapi.XrayGenerationStatus{
			Generation:   status.Current.Generation,
			RefCount:     status.Current.RefCount,
			Draining:     status.Current.Draining,
			CleanupError: publicXrayCleanupError(status.Current.CleanupError),
		}
		result.Current = &current
	}
	for _, generation := range status.Draining {
		result.Draining = append(result.Draining, controlapi.XrayGenerationStatus{
			Generation:   generation.Generation,
			RefCount:     generation.RefCount,
			Draining:     generation.Draining,
			CleanupError: publicXrayCleanupError(generation.CleanupError),
		})
	}
	return result
}

func (d *Daemon) rendrStatus() controlapi.RuntimeStatus {
	if d.plane == nil {
		return controlapi.RuntimeStatus{State: controlapi.RuntimeStateUnavailable}
	}
	return rendrStatusFrom(d.plane.Status())
}

func rendrStatusFrom(planeStatus dataplane.Status) controlapi.RuntimeStatus {
	status := planeStatus.Rendr
	state := controlapi.RuntimeState(status.State)
	switch state {
	case controlapi.RuntimeStateRunning, controlapi.RuntimeStateStopping, controlapi.RuntimeStateStopped, controlapi.RuntimeStateFailed:
	default:
		state = controlapi.RuntimeStateUnavailable
	}
	return controlapi.RuntimeStatus{
		State:            state,
		InstanceID:       status.InstanceID,
		InstanceIDSource: status.InstanceIDSource,
		ActiveClient:     status.ActiveClient,
		ActiveAccepted:   status.ActiveAccepted,
		AcceptedFlowIDs:  status.AcceptedFlowIDs,
		TotalClient:      status.TotalClient,
		TotalAccepted:    status.TotalAccepted,
		LastError:        status.LastError,
		ObservedAt:       status.ObservedAt,
		StreamFactory:    status.StreamFactory,
		StreamCarrier:    status.StreamCarrier,
		MobilityMode:     status.MobilityMode,
		EndpointOwned:    status.EndpointOwned,
		PacketSupported:  status.PacketSupported,
	}
}

func (d *Daemon) reconcileStatusFrom(configuredRevision int64, configuredDigest [32]byte, status dataplane.Status) controlapi.ReconcileStatus {
	state := controlapi.ReconcileStatePending
	cleanupPending := xrayCleanupPending(status.Xray)
	listenersHealthy := runtimeListenersHealthy(status.Listeners)
	if appliedRuntimeHealthy(status, configuredRevision, configuredDigest) {
		state = controlapi.ReconcileStateApplied
	} else if status.AttemptedRevision == configuredRevision && (status.LastError != "" || cleanupPending || !listenersHealthy) {
		state = controlapi.ReconcileStateFailed
	}
	result := controlapi.ReconcileStatus{
		State:                  state,
		AppliedRevision:        status.AppliedRevision,
		AttemptedRevision:      status.AttemptedRevision,
		ConfigurationPublished: publishedConfigurationMatches(status, configuredRevision, configuredDigest),
		LastError:              status.LastError,
		LastErrorCode:          status.LastErrorCode,
		ObservedAt:             status.ObservedAt,
		ObservationFresh:       status.ObservationFresh,
	}
	if cleanupPending && result.LastError == "" {
		result.LastErrorCode = "runtime.xray_cleanup_failed"
		result.LastError = publicerr.MessageCode(result.LastErrorCode)
	}
	if !listenersHealthy && result.LastError == "" {
		result.LastErrorCode = "runtime.listener_unavailable"
		result.LastError = publicerr.MessageCode(result.LastErrorCode)
	}
	if failure, ok := d.reconcileFailureSnapshot(configuredRevision); ok {
		first, next := failure.firstAt, failure.nextAt
		result.ConsecutiveFailures = failure.count
		result.FirstFailureAt = &first
		result.NextRetryAt = &next
	}
	return result
}

func (d *Daemon) reconcileLoop(initialRevision int64) {
	defer close(d.reconcileDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	nextPersistRetry := time.Time{}
	persistFailures := 0
	persistRevision := initialRevision
	var persistDigest [32]byte
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
		}
		d.applyPersistMu.Lock()
		if err := d.ctx.Err(); err != nil {
			d.applyPersistMu.Unlock()
			return
		}
		if failure, ok := d.configReadFailureSnapshot(); ok && time.Now().Before(failure.nextAt) {
			d.applyPersistMu.Unlock()
			continue
		}
		cfg, err := d.loadConfig()
		if err != nil {
			d.setOperationalState(true)
			if configstore.IsContentError(err) {
				d.recordConfigContentFailure(time.Now())
			} else {
				d.handleConfigReadFailure(time.Now())
			}
			d.applyPersistMu.Unlock()
			continue
		}
		if err := d.ctx.Err(); err != nil {
			d.applyPersistMu.Unlock()
			return
		}
		configuredDigest, err := configstore.ContentDigest(cfg)
		if err != nil {
			d.setOperationalState(true)
			d.recordConfigContentFailure(time.Now())
			d.applyPersistMu.Unlock()
			continue
		}
		if err := d.ctx.Err(); err != nil {
			d.applyPersistMu.Unlock()
			return
		}
		d.clearConfigReadFailureLedger()
		d.recordObservedConfig(cfg.Revision, configuredDigest)
		if cfg.Revision != persistRevision || configuredDigest != persistDigest {
			persistRevision = cfg.Revision
			persistDigest = configuredDigest
			persistFailures = 0
			nextPersistRetry = time.Time{}
		}
		status := d.plane.Status()
		d.refreshOperationalStateFrom(cfg.Revision, configuredDigest, status)
		cleanupPending := xrayCleanupPending(status.Xray)
		rendrHealthy := status.Rendr.State == "running"
		listenersHealthy := runtimeListenersHealthy(status.Listeners)
		if appliedConfigurationMatches(status, cfg.Revision, configuredDigest) && status.LastError == "" && !cleanupPending && rendrHealthy && listenersHealthy {
			d.clearConfigReadFailureAfterRuntimeRecovery()
			d.clearStartupRollback()
			d.clearReconcileFailure()
			checkpoint := d.configurationStatus()
			if shouldPersistLastKnownGood(checkpoint, cfg.Revision) && time.Now().After(nextPersistRetry) {
				if err := d.persistLastKnownGood(cfg); err != nil {
					if errors.Is(err, errLastKnownGoodRevisionAhead) {
						persistFailures = 0
						nextPersistRetry = time.Time{}
					} else {
						persistFailures++
						nextPersistRetry = time.Now().Add(reconcileBackoff(persistFailures, cfg.Revision))
					}
				} else {
					persistFailures = 0
					nextPersistRetry = time.Time{}
				}
			}
			d.refreshOperationalStateFrom(cfg.Revision, configuredDigest, status)
			d.applyPersistMu.Unlock()
			continue
		}
		now := time.Now()
		if failure, ok := d.reconcileFailureSnapshot(cfg.Revision); ok && now.Before(failure.nextAt) {
			d.applyPersistMu.Unlock()
			continue
		}
		needsApply := !appliedConfigurationMatches(status, cfg.Revision, configuredDigest) ||
			status.LastError != "" || cleanupPending || !rendrHealthy || !listenersHealthy
		if !needsApply {
			d.applyPersistMu.Unlock()
			continue
		}
		d.setOperationalState(true)
		applyCtx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
		err = d.plane.Apply(applyCtx, cfg)
		cancel()
		if contextErr := d.ctx.Err(); contextErr != nil {
			d.applyPersistMu.Unlock()
			return
		}
		if err != nil {
			d.setOperationalState(true)
			d.recordReconcileFailure(cfg.Revision, time.Now())
		} else {
			appliedStatus := d.plane.Status()
			if !appliedRuntimeHealthy(appliedStatus, cfg.Revision, configuredDigest) {
				d.setOperationalState(true)
				d.recordReconcileFailure(cfg.Revision, time.Now())
			} else {
				d.clearConfigReadFailureAfterRuntimeRecovery()
				d.clearStartupRollback()
				d.clearReconcileFailure()
				if persistErr := d.persistLastKnownGood(cfg); persistErr == nil || errors.Is(persistErr, errLastKnownGoodRevisionAhead) {
					persistFailures = 0
					nextPersistRetry = time.Time{}
				} else {
					persistFailures++
					nextPersistRetry = time.Now().Add(reconcileBackoff(persistFailures, cfg.Revision))
				}
			}
			d.refreshOperationalStateFrom(cfg.Revision, configuredDigest, appliedStatus)
		}
		d.applyPersistMu.Unlock()
	}
}

func (d *Daemon) recordReconcileFailure(revision int64, now time.Time) {
	d.reconcileFailureMu.Lock()
	defer d.reconcileFailureMu.Unlock()
	if d.reconcileFailure.count == 0 || d.reconcileFailure.revision != revision {
		d.reconcileFailure = reconcileFailureState{revision: revision, firstAt: now}
	}
	d.reconcileFailure.count++
	d.reconcileFailure.nextAt = now.Add(reconcileBackoff(d.reconcileFailure.count, revision))
}

func (d *Daemon) clearReconcileFailure() {
	d.reconcileFailureMu.Lock()
	d.reconcileFailure = reconcileFailureState{}
	d.reconcileFailureMu.Unlock()
}

func (d *Daemon) reconcileFailureSnapshot(revision int64) (reconcileFailureState, bool) {
	d.reconcileFailureMu.RLock()
	defer d.reconcileFailureMu.RUnlock()
	failure := d.reconcileFailure
	return failure, failure.count > 0 && failure.revision == revision
}

func (d *Daemon) recordConfigContentFailure(now time.Time) {
	revision, _ := d.observedConfigSnapshot()
	d.configReadFailureMu.Lock()
	defer d.configReadFailureMu.Unlock()
	d.advanceConfigIOElapsedLocked(now)
	d.configIOActive = false
	d.configIOLastAt = time.Time{}
	d.configIOFailure = reconcileFailureState{}
	recordFailureState(&d.configContentFailure, revision, now)
}

func (d *Daemon) handleConfigReadFailure(now time.Time) {
	revision, _ := d.observedConfigSnapshot()
	d.configReadFailureMu.Lock()
	d.configContentFailure = reconcileFailureState{}
	if d.configIOActive {
		d.advanceConfigIOElapsedLocked(now)
	}
	d.configIOActive = true
	d.configIOLastAt = now
	recordFailureState(&d.configIOFailure, revision, now)
	ioElapsed := d.configIOElapsed
	if d.configReadFailClosed {
		d.configReadFailureMu.Unlock()
		return
	}
	d.configReadFailureMu.Unlock()
	if ioElapsed < normalizedConfigReadFailureTolerance(d.configReadFailureTolerance) {
		return
	}

	stopper, ok := d.plane.(failStoppingRuntimePlane)
	if !ok {
		d.configReadFailureMu.Lock()
		d.configReadFailClosed = false
		d.configReadFailureMu.Unlock()
		return
	}
	parent := d.ctx
	if parent == nil {
		parent = context.Background()
	}
	stopCtx, cancel := context.WithTimeout(parent, configFailStopTimeout)
	defer cancel()
	if err := stopper.FailStop(stopCtx, configReadFailClosedError); err != nil {
		return
	}
	d.configReadFailureMu.Lock()
	if d.configIOFailure.count > 0 && d.configIOActive {
		d.configReadFailClosed = true
	}
	d.configReadFailureMu.Unlock()
}

func recordFailureState(failure *reconcileFailureState, revision int64, now time.Time) {
	if failure.count > 0 && now.Before(failure.nextAt) {
		return
	}
	if failure.count == 0 || failure.revision != revision {
		*failure = reconcileFailureState{revision: revision, firstAt: now}
	}
	failure.count++
	failure.nextAt = now.Add(reconcileBackoff(failure.count, revision))
}

func (d *Daemon) advanceConfigIOElapsedLocked(now time.Time) {
	if !d.configIOActive || d.configIOLastAt.IsZero() {
		return
	}
	d.addConfigIOElapsedLocked(now.Sub(d.configIOLastAt))
	d.configIOLastAt = now
}

func (d *Daemon) addConfigIOElapsedLocked(elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	tolerance := normalizedConfigReadFailureTolerance(d.configReadFailureTolerance)
	remaining := tolerance - d.configIOElapsed
	if remaining <= 0 || elapsed >= remaining {
		d.configIOElapsed = tolerance
		return
	}
	d.configIOElapsed += elapsed
}

func (d *Daemon) clearConfigReadFailureLedger() {
	d.configReadFailureMu.Lock()
	d.configContentFailure = reconcileFailureState{}
	d.configIOFailure = reconcileFailureState{}
	d.configIOElapsed = 0
	d.configIOLastAt = time.Time{}
	d.configIOActive = false
	d.configReadFailureMu.Unlock()
}

func (d *Daemon) clearConfigReadFailureAfterRuntimeRecovery() {
	d.configReadFailureMu.Lock()
	d.configContentFailure = reconcileFailureState{}
	d.configIOFailure = reconcileFailureState{}
	d.configIOElapsed = 0
	d.configIOLastAt = time.Time{}
	d.configIOActive = false
	d.configReadFailClosed = false
	d.configReadFailureMu.Unlock()
}

func (d *Daemon) configReadFailureSnapshot() (reconcileFailureState, bool) {
	d.configReadFailureMu.RLock()
	defer d.configReadFailureMu.RUnlock()
	if d.configIOFailure.count > 0 {
		return d.configIOFailure, true
	}
	return d.configContentFailure, d.configContentFailure.count > 0
}

func (d *Daemon) configReadFailClosedSnapshot() bool {
	d.configReadFailureMu.RLock()
	defer d.configReadFailureMu.RUnlock()
	return d.configReadFailClosed
}

func (d *Daemon) recordObservedConfig(revision int64, digest [32]byte) {
	d.stateMu.Lock()
	d.observedConfig = configObservation{revision: revision, digest: digest}
	d.stateMu.Unlock()
}

func (d *Daemon) observedConfigSnapshot() (int64, [32]byte) {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	return d.observedConfig.revision, d.observedConfig.digest
}

func reconcileBackoff(failures int, revision int64) time.Duration {
	if failures < 1 {
		return 0
	}
	shift := failures - 1
	if shift > 5 {
		shift = 5
	}
	base := time.Second * time.Duration(1<<shift)
	if base > maxReconcileBackoff {
		base = maxReconcileBackoff
	}
	span := base / 5
	if span == 0 || base == maxReconcileBackoff {
		return base
	}
	seed := uint64(revision) ^ (uint64(failures) * 0x9e3779b97f4a7c15)
	jitter := time.Duration(seed % uint64(span+1))
	return base + jitter
}

func normalizedConfigReadFailureTolerance(tolerance time.Duration) time.Duration {
	if tolerance <= 0 {
		return defaultConfigReadFailureTolerance
	}
	return tolerance
}

func linkedRuntimeContext(parent, lifetime context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if lifetime == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(lifetime, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func reloadCheckpointError(err error) error {
	if errors.Is(err, errLastKnownGoodRevisionAhead) {
		return publicerr.Wrap(lastKnownGoodRevisionAheadError, err)
	}
	return publicerr.Wrap("service.reload_last_good_persist", err)
}

func shouldPersistLastKnownGood(checkpoint controlapi.ConfigurationStatus, appliedRevision int64) bool {
	return checkpoint.LastKnownGoodRevision < appliedRevision ||
		(checkpoint.LastKnownGoodRevision == appliedRevision && checkpoint.LastKnownGoodError != "")
}

func (d *Daemon) persistLastKnownGood(cfg configstore.Config) error {
	d.checkpointPersistMu.Lock()
	defer d.checkpointPersistMu.Unlock()
	checkpoint := d.configurationStatus()
	if checkpoint.LastKnownGoodRevision >= cfg.Revision {
		existing, err := d.loadLastKnownGood()
		if err != nil || existing.Revision != checkpoint.LastKnownGoodRevision {
			d.recordLastKnownGood(-1, lastKnownGoodPersistError)
			if err != nil {
				return err
			}
			return fmt.Errorf(
				"last-known-good revision changed: status=%d file=%d",
				checkpoint.LastKnownGoodRevision,
				existing.Revision,
			)
		}
		if existing.Revision > cfg.Revision {
			d.recordLastKnownGood(existing.Revision, lastKnownGoodRevisionAheadError)
			return fmt.Errorf(
				"%w: checkpoint=%d applied=%d",
				errLastKnownGoodRevisionAhead,
				existing.Revision,
				cfg.Revision,
			)
		}
		existingDigest, digestErr := configstore.ContentDigest(existing)
		if digestErr != nil {
			d.recordLastKnownGood(-1, lastKnownGoodPersistError)
			return digestErr
		}
		candidateDigest, digestErr := configstore.ContentDigest(cfg)
		if digestErr != nil {
			d.recordLastKnownGood(-1, lastKnownGoodPersistError)
			return digestErr
		}
		if existingDigest != candidateDigest {
			d.recordLastKnownGood(-1, lastKnownGoodPersistError)
			return fmt.Errorf("lastgood.revision_content_mismatch: revision %d has different checkpoint content", cfg.Revision)
		}
		d.recordLastKnownGood(existing.Revision, "")
		return nil
	}
	if err := d.saveLastKnownGood(cfg); err != nil {
		d.recordLastKnownGood(-1, lastKnownGoodPersistError)
		return err
	}
	d.recordLastKnownGood(cfg.Revision, "")
	return nil
}

func (d *Daemon) recordLastKnownGood(revision int64, errorCode string) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if revision >= 0 {
		d.configuration.LastKnownGoodRevision = revision
	}
	d.configuration.LastKnownGoodError = errorCode
}

func (d *Daemon) setStartupRollback(status *controlapi.StartupRollbackStatus) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if status == nil {
		d.configuration.StartupRollback = nil
		return
	}
	copy := *status
	d.configuration.StartupRollback = &copy
}

func (d *Daemon) clearStartupRollback() {
	d.setStartupRollback(nil)
}

func (d *Daemon) configurationStatus() controlapi.ConfigurationStatus {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	return d.configuration
}

func (d *Daemon) stateAndConfigurationStatus() (controlapi.DaemonState, controlapi.ConfigurationStatus) {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	return d.state, d.configuration
}

func (d *Daemon) refreshOperationalStateFrom(configuredRevision int64, configuredDigest [32]byte, status dataplane.Status) {
	checkpoint := d.configurationStatus()
	d.setState(operationalStateFrom(d.currentState(), configuredRevision, configuredDigest, status, checkpoint))
}

func operationalStateFrom(base controlapi.DaemonState, configuredRevision int64, configuredDigest [32]byte, status dataplane.Status, checkpoint controlapi.ConfigurationStatus) controlapi.DaemonState {
	switch base {
	case controlapi.DaemonStateStarting, controlapi.DaemonStateRunning, controlapi.DaemonStateDegraded:
	default:
		return base
	}
	degraded :=
		!appliedConfigurationMatches(status, configuredRevision, configuredDigest) ||
			status.LastError != "" ||
			status.Rendr.State != "running" ||
			!runtimeListenersHealthy(status.Listeners) ||
			xrayCleanupPending(status.Xray) ||
			checkpoint.LastKnownGoodRevision != status.AppliedRevision ||
			checkpoint.StartupRollback != nil ||
			checkpoint.LastKnownGoodError != ""
	if degraded {
		return controlapi.DaemonStateDegraded
	}
	return controlapi.DaemonStateRunning
}

func xrayCleanupPending(status xrayrt.Status) bool {
	if status.Current != nil && status.Current.CleanupError != "" {
		return true
	}
	for _, generation := range status.Draining {
		if generation.CleanupError != "" {
			return true
		}
	}
	return false
}

func publicXrayCleanupError(raw string) string {
	if raw == "" {
		return ""
	}
	return publicerr.MessageCode("runtime.xray_cleanup_failed")
}

func closeRuntimePlane(plane runtimePlane, retryDelay, shutdownLimit time.Duration) error {
	if plane == nil {
		return nil
	}
	if retryDelay <= 0 {
		retryDelay = time.Millisecond
	}
	if shutdownLimit <= 0 {
		shutdownLimit = defaultRuntimePlaneShutdownLimit
	}
	deadline := time.Now().Add(shutdownLimit)
	forceBudget := shutdownLimit / 4
	if forceBudget <= 0 {
		forceBudget = time.Nanosecond
	}
	gracefulDeadline := deadline.Add(-forceBudget)
	var lastErr error
	for {
		err, completed := callRuntimeCloseBefore(plane, gracefulDeadline)
		if !completed {
			lastErr = errors.Join(lastErr, xrayrt.ErrShutdownIncomplete, context.DeadlineExceeded)
			break
		}
		if !errors.Is(err, xrayrt.ErrShutdownIncomplete) {
			return err
		}
		lastErr = err
		remaining := time.Until(gracefulDeadline)
		if remaining <= 0 {
			break
		}
		if retryDelay > remaining {
			time.Sleep(remaining)
		} else {
			time.Sleep(retryDelay)
		}
	}
	var forceErr error
	if closer, ok := plane.(forceClosingRuntimePlane); ok {
		forceResult := make(chan error, 1)
		go func() { forceResult <- closer.ForceClose() }()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		forceTimer := time.NewTimer(remaining)
		defer forceTimer.Stop()
		select {
		case forceErr = <-forceResult:
		case <-forceTimer.C:
			forceErr = errors.Join(xrayrt.ErrShutdownIncomplete, context.DeadlineExceeded)
		}
	}
	return errors.Join(errRuntimePlaneShutdownTimedOut, lastErr, forceErr)
}

func callRuntimeCloseBefore(plane runtimePlane, deadline time.Time) (error, bool) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded, false
	}
	result := make(chan error, 1)
	go func() {
		if closer, ok := plane.(contextClosingRuntimePlane); ok {
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			result <- closer.CloseContext(ctx)
			return
		}
		result <- plane.Close()
	}()
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case err := <-result:
		return err, true
	case <-timer.C:
		return context.DeadlineExceeded, false
	}
}

func appliedConfigurationMatches(status dataplane.Status, revision int64, digest [32]byte) bool {
	if status.State != "running" || status.FailStopped {
		return false
	}
	return publishedConfigurationMatches(status, revision, digest)
}

func publishedConfigurationMatches(status dataplane.Status, revision int64, digest [32]byte) bool {
	if status.AppliedRevision != revision {
		return false
	}
	var unknown [32]byte
	return digest != unknown && status.AppliedDigest != unknown && status.AppliedDigest == digest
}

func runtimeStatusHealthy(status dataplane.Status) bool {
	return status.State == "running" && !status.FailStopped && status.LastError == "" &&
		status.Rendr.State == "running" && !xrayCleanupPending(status.Xray) &&
		runtimeListenersHealthy(status.Listeners)
}

func runtimeListenersHealthy(listeners []dataplane.ListenerStatus) bool {
	for _, listener := range listeners {
		if listener.State == "bound" {
			continue
		}
		// A node carrier with no currently authorized inbound peer has nothing
		// safe to bind and is intentionally unavailable. A configured user
		// listener, by contrast, must never disappear without degrading runtime.
		if listener.Tag == xrayconfig.NodeVLESSTag && listener.State == "unavailable" {
			continue
		}
		return false
	}
	return true
}

func appliedRuntimeHealthy(status dataplane.Status, revision int64, digest [32]byte) bool {
	return appliedConfigurationMatches(status, revision, digest) && runtimeStatusHealthy(status)
}

func migrateLegacyState(store *statestore.Store, assertedIdentity string) error {
	if store == nil {
		return fmt.Errorf("state store is required")
	}
	return configstore.WithStoreLock(store, func() error {
		_, err := store.MigrateLegacy(statestore.LegacyMigrationOptions{
			Identity: assertedIdentity,
			ValidateIdentitySeed: func(expected string, payload []byte) error {
				seed, err := identity.UnmarshalSeedEnvelope(payload)
				if err != nil {
					return err
				}
				backing, err := identity.FromSeed(seed)
				if err != nil {
					return err
				}
				if backing.NodeID().String() != expected {
					return fmt.Errorf("seed identity does not match configured node")
				}
				return nil
			},
			IsConfigDocument: func(payload []byte) bool {
				_, err := configstore.DecodeCheckpointDocument(payload)
				return err == nil
			},
		})
		return err
	})
}

func startRuntimePlane(ctx context.Context, configPath string, store *statestore.Store, configured configstore.Config) (runtimePlane, int64, *controlapi.StartupRollbackStatus, error) {
	lastGood, loadErr := loadLastKnownGood(store, configPath)
	hasLastGood := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return nil, -1, nil, fmt.Errorf("load last-known-good: %w", loadErr)
	}
	var precheckErr error
	if hasLastGood && lastGood.Revision > configured.Revision {
		precheckErr = publicerr.Errorf(
			"dataplane.revision_regression",
			"configured revision %d is older than last-known-good revision %d",
			configured.Revision,
			lastGood.Revision,
		)
	} else if hasLastGood && lastGood.Revision == configured.Revision {
		configuredDigest, err := configstore.ContentDigest(configured)
		if err != nil {
			return nil, -1, nil, fmt.Errorf("digest configured revision %d: %w", configured.Revision, err)
		}
		lastGoodDigest, err := configstore.ContentDigest(lastGood)
		if err != nil {
			return nil, -1, nil, fmt.Errorf("digest last-known-good revision %d: %w", lastGood.Revision, err)
		}
		if configuredDigest != lastGoodDigest {
			precheckErr = publicerr.Errorf(
				"dataplane.revision_content_mismatch",
				"configured revision %d differs from last-known-good content",
				configured.Revision,
			)
		}
	}
	startErr := precheckErr
	var plane *dataplane.Plane
	if startErr == nil {
		plane, startErr = dataplane.Start(ctx, configured)
	}
	if startErr == nil {
		if err := saveLastKnownGoodMonotonicStore(store, configPath, configured); err != nil {
			return nil, -1, nil, errors.Join(
				fmt.Errorf("persist initial last-known-good revision %d: %w", configured.Revision, err),
				closeRuntimePlane(plane, 100*time.Millisecond, defaultRuntimePlaneShutdownLimit),
			)
		}
		return plane, configured.Revision, nil, nil
	}

	if !hasLastGood {
		return nil, -1, nil, errors.Join(startErr, fmt.Errorf("load last-known-good: %w", loadErr))
	}
	plane, recoveryErr := dataplane.Start(ctx, lastGood)
	if recoveryErr != nil {
		return nil, -1, nil, errors.Join(startErr, fmt.Errorf("start last-known-good revision %d: %w", lastGood.Revision, recoveryErr))
	}

	applyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	applyErr := plane.Apply(applyCtx, configured)
	cancel()
	if applyErr == nil {
		if precheckErr != nil {
			return nil, -1, nil, errors.Join(
				precheckErr,
				errors.New("dataplane startup precheck was accepted during recovery"),
				closeRuntimePlane(plane, 100*time.Millisecond, defaultRuntimePlaneShutdownLimit),
			)
		}
		if err := saveLastKnownGoodMonotonicStore(store, configPath, configured); err != nil {
			return nil, -1, nil, errors.Join(
				fmt.Errorf("persist recovered configured revision %d: %w", configured.Revision, err),
				closeRuntimePlane(plane, 100*time.Millisecond, defaultRuntimePlaneShutdownLimit),
			)
		}
		return plane, configured.Revision, nil, nil
	}
	rollbackCause := applyErr
	if precheckErr != nil {
		rollbackCause = precheckErr
	}
	appliedRevision := plane.Status().AppliedRevision
	return plane, lastGood.Revision, &controlapi.StartupRollbackStatus{
		ConfiguredRevision: configured.Revision,
		AppliedRevision:    appliedRevision,
		ErrorCode:          publicerr.Code(rollbackCause, "dataplane.startup_apply_failed"),
	}, nil
}

func loadInitialConfig(store *statestore.Store, runtimeConfigPath, ownershipKey string) (configstore.Config, bool, *controlapi.StartupRollbackStatus, error) {
	if _, err := loadExistingConfig(store, runtimeConfigPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, checkpointErr := loadLastKnownGood(store, runtimeConfigPath); checkpointErr == nil {
				return configstore.Config{}, false, nil, publicerr.Errorf(
					"config.missing_with_last_good",
					"configured file is missing while a last-known-good checkpoint exists",
				)
			} else if !errors.Is(checkpointErr, os.ErrNotExist) {
				return configstore.Config{}, false, nil, fmt.Errorf("load last-known-good before initialization: %w", checkpointErr)
			}
			ambiguous, inspectErr := hasAmbiguousLegacyRecovery(store)
			if inspectErr != nil {
				return configstore.Config{}, false, nil, inspectErr
			}
			if ambiguous {
				return configstore.Config{}, false, nil, publicerr.Errorf(
					legacyRecoveryAmbiguousError,
					"configured file is missing while an ownership-ambiguous legacy recovery file exists",
				)
			}
		} else {
			if !configstore.IsContentError(err) {
				return configstore.Config{}, false, nil, err
			}
			// The dedicated migration read below can safely quarantine the
			// narrow set of historical credential states rejected by ordinary
			// runtime readers. Other content failures still fall back to LKG.
		}
	}
	var cfg configstore.Config
	var migrated bool
	var err error
	if store != nil {
		cfg, migrated, err = configstore.LoadStoreOrMigrate(store, true)
	} else {
		cfg, migrated, err = configstore.LoadPinnedOrMigrate(runtimeConfigPath, ownershipKey)
	}
	if err != nil {
		if configstore.IsContentError(err) {
			return recoverInitialConfigFromLastKnownGood(store, runtimeConfigPath, ownershipKey, err)
		}
		return configstore.Config{}, false, nil, err
	}
	return cfg, migrated, nil, nil
}

func recoverInitialConfigFromLastKnownGood(
	store *statestore.Store,
	runtimeConfigPath string,
	ownershipKey string,
	cause error,
) (configstore.Config, bool, *controlapi.StartupRollbackStatus, error) {
	var checkpoint configstore.Config
	var err error
	if store != nil {
		checkpoint, err = configstore.LoadStoreLastKnownGoodForRecovery(store)
	} else {
		checkpoint, err = configstore.LoadPinnedLastKnownGoodForRecovery(runtimeConfigPath, ownershipKey)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			ambiguous, inspectErr := hasAmbiguousLegacyRecovery(store)
			if inspectErr != nil {
				return configstore.Config{}, false, nil, errors.Join(cause, inspectErr)
			}
			if ambiguous {
				return configstore.Config{}, false, nil, publicerr.Wrap(
					legacyRecoveryAmbiguousError,
					errors.Join(cause, errors.New("ownership-ambiguous legacy recovery file requires explicit operator action")),
				)
			}
		}
		return configstore.Config{}, false, nil, errors.Join(
			cause,
			fmt.Errorf("load last-known-good after invalid configured content: %w", err),
		)
	}
	return checkpoint, false, &controlapi.StartupRollbackStatus{
		ConfiguredRevision: -1,
		AppliedRevision:    checkpoint.Revision,
		ErrorCode:          "config.startup_content_invalid",
	}, nil
}

func hasAmbiguousLegacyRecovery(store *statestore.Store) (bool, error) {
	if store == nil {
		return false, nil
	}
	ambiguous, err := store.HasLegacyRecoveryCandidates()
	if err != nil {
		return false, fmt.Errorf("inspect legacy recovery candidates: %w", err)
	}
	return ambiguous, nil
}

func saveLastKnownGoodMonotonic(configPath string, configured configstore.Config) error {
	return saveLastKnownGoodMonotonicStore(nil, configPath, configured)
}

func saveLastKnownGoodMonotonicStore(store *statestore.Store, configPath string, configured configstore.Config) error {
	existing, err := loadLastKnownGood(store, configPath)
	if errors.Is(err, os.ErrNotExist) {
		return saveLastKnownGood(store, configPath, configured)
	}
	if err != nil {
		return err
	}
	if existing.Revision > configured.Revision {
		return fmt.Errorf(
			"lastgood.revision_regression: configured revision %d is older than checkpoint revision %d",
			configured.Revision,
			existing.Revision,
		)
	}
	if existing.Revision == configured.Revision {
		existingDigest, digestErr := configstore.ContentDigest(existing)
		if digestErr != nil {
			return digestErr
		}
		configuredDigest, digestErr := configstore.ContentDigest(configured)
		if digestErr != nil {
			return digestErr
		}
		if existingDigest != configuredDigest {
			return fmt.Errorf("lastgood.revision_content_mismatch: revision %d has different checkpoint content", configured.Revision)
		}
		return nil
	}
	return saveLastKnownGood(store, configPath, configured)
}

func loadExistingConfig(store *statestore.Store, configPath string) (configstore.Config, error) {
	if store != nil {
		return configstore.LoadStoreExisting(store)
	}
	return configstore.LoadExisting(configPath)
}

func loadLastKnownGood(store *statestore.Store, configPath string) (configstore.Config, error) {
	if store != nil {
		return configstore.LoadStoreLastKnownGood(store)
	}
	return configstore.LoadLastKnownGood(configPath)
}

func saveLastKnownGood(store *statestore.Store, configPath string, cfg configstore.Config) error {
	if store != nil {
		return configstore.SaveStoreLastKnownGood(store, cfg)
	}
	return configstore.SaveLastKnownGood(configPath, cfg)
}

func newBootID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (d *Daemon) currentState() controlapi.DaemonState {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	return d.state
}

func (d *Daemon) setState(state controlapi.DaemonState) {
	d.stateMu.Lock()
	d.state = state
	d.stateMu.Unlock()
}

func (d *Daemon) setOperationalState(degraded bool) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	switch d.state {
	case controlapi.DaemonStateStarting, controlapi.DaemonStateRunning, controlapi.DaemonStateDegraded:
		if degraded {
			d.state = controlapi.DaemonStateDegraded
		} else {
			d.state = controlapi.DaemonStateRunning
		}
	}
}
