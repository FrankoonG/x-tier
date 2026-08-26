package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/dataplane"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/rendradapter"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/webbridge"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
	"github.com/FrankoonG/x-tier/internal/xrayrt"
)

func daemonControlToken(d *Daemon) (string, error) {
	return controlapi.ReadStoreToken(d.stateStore, statestore.ControlToken)
}

func daemonStatus(d *Daemon) (controlapi.DaemonStatus, error) {
	token, err := daemonControlToken(d)
	if err != nil {
		return controlapi.DaemonStatus{}, err
	}
	return controlapi.GetStatusToken(d.Addr(), token)
}

func daemonExecute(d *Daemon, request controlapi.Request) (controlapi.Response, error) {
	token, err := daemonControlToken(d)
	if err != nil {
		return controlapi.Response{}, err
	}
	return controlapi.ExecuteToken(d.Addr(), token, request)
}

func loadStoreLastKnownGood(configPath string) (cfg configstore.Config, err error) {
	canonical, err := configstore.CanonicalPath(configPath)
	if err != nil {
		return configstore.Config{}, err
	}
	store, err := statestore.Open(canonical)
	if err != nil {
		return configstore.Config{}, err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	return configstore.LoadStoreLastKnownGood(store)
}

func saveStoreLastKnownGood(t *testing.T, configPath string, cfg configstore.Config) {
	t.Helper()
	canonical, err := configstore.CanonicalPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := statestore.Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := configstore.SaveStoreLastKnownGood(store, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestControlPlaneEndToEnd(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{
		NodeID:          "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DisplayName:     "A",
		Role:            "thin",
		RendrCapable:    true,
		RendrInstanceID: "configured-id-is-not-a-runtime-id",
	}
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d, err := Start(ctx, Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	resp, err := http.Get(controlapi.URL(d.Addr()) + controlapi.StatusPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status code=%d", resp.StatusCode)
	}

	status, err := daemonStatus(d)
	if err != nil {
		t.Fatal(err)
	}
	if status.APIVersion != controlapi.APIVersion || status.State != controlapi.DaemonStateRunning {
		t.Fatalf("unexpected daemon status: %+v", status)
	}
	if status.ConfigPath != d.ConfigPath() || status.ControlAddr != d.Addr() || status.Revision != 0 {
		t.Fatalf("status does not describe daemon ownership: %+v", status)
	}
	if status.Idempotency.Scope != controlapi.IdempotencyScopeProcessMemory ||
		status.Idempotency.RestartPersistent || !status.Idempotency.Provisional {
		t.Fatalf("status overstates request idempotency: %+v", status.Idempotency)
	}
	if status.BootID == "" || status.Reconcile.State != controlapi.ReconcileStateApplied ||
		status.Reconcile.AppliedRevision != 0 {
		t.Fatalf("runtime reconciliation is not applied: %+v", status)
	}
	if status.Configuration.SchemaVersion != configstore.CurrentSchemaVersion ||
		status.Configuration.MigratedAtStartup || status.Configuration.LastKnownGoodRevision != 0 ||
		status.Configuration.LastKnownGoodError != "" {
		t.Fatalf("unexpected configuration status: %+v", status.Configuration)
	}
	if status.Rendr.State != controlapi.RuntimeStateRunning || status.Rendr.InstanceID != "" ||
		status.Rendr.InstanceIDSource == "" || status.Rendr.StreamFactory != "xray-stream" ||
		status.Rendr.StreamCarrier != "unknown" || status.Rendr.MobilityMode != "redial_attach" ||
		status.Rendr.EndpointOwned || status.Rendr.PacketSupported {
		t.Fatalf("rendr status fabricated an instance: %+v", status.Rendr)
	}
	if status.Xray.State != controlapi.RuntimeStateRunning || status.Xray.Current == nil ||
		status.Xray.Current.Generation != 1 ||
		!status.Xray.StrictStreamOutbound || status.Xray.StrictPacketOutbound {
		t.Fatalf("unexpected Xray runtime status: %+v", status.Xray)
	}

	request := controlapi.Request{
		Args:      []string{"local", "identity", "rename", "daemon-a"},
		JSON:      true,
		Revision:  0,
		RequestID: "40000000000000000000000000000000",
	}
	first, err := daemonExecute(d, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := daemonExecute(d, request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.ExitCode != 0 {
		t.Fatalf("request replay changed response: first=%+v second=%+v", first, second)
	}
	after, err := configstore.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 1 || after.Node.DisplayName != "daemon-a" {
		t.Fatalf("same-process replay executed again: revision=%d name=%q", after.Revision, after.Node.DisplayName)
	}

	status, err = daemonStatus(d)
	if err != nil {
		t.Fatal(err)
	}
	if status.Revision != 1 {
		t.Fatalf("status revision=%d, want 1", status.Revision)
	}
	deadline := time.Now().Add(5 * time.Second)
	for status.Reconcile.AppliedRevision != 1 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		status, err = daemonStatus(d)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.Reconcile.State != controlapi.ReconcileStateApplied || status.Reconcile.AppliedRevision != 1 {
		t.Fatalf("runtime did not converge to revision 1: %+v", status.Reconcile)
	}

	addr := d.Addr()
	cancel()
	select {
	case <-d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop after context cancellation")
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("control listener remained reachable after daemon shutdown")
	}
}

func TestStartPersistsInitialDefaultConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	cfg, err := configstore.LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 0 || cfg.SchemaVersion != configstore.CurrentSchemaVersion {
		t.Fatalf("persisted initial config = %+v", cfg)
	}
}

func TestRestartAcceptsEvolvedObjectCheckpoint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	saveStoreLastKnownGood(t, configPath, cfg)
	first, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := controlapi.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	response, err := daemonExecute(first, controlapi.Request{
		Args: []string{"local", "identity", "rename", "evolved"}, JSON: true,
		Revision: 0, RequestID: requestID,
	})
	if err != nil || response.ExitCode != 0 {
		_ = first.Close()
		t.Fatalf("mutate migrated daemon response=%+v err=%v", response, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	checkpointAdvanced := false
	for time.Now().Before(deadline) {
		status, statusErr := first.Status(context.Background())
		if statusErr != nil {
			_ = first.Close()
			t.Fatal(statusErr)
		}
		if status.Configuration.LastKnownGoodRevision == 1 {
			checkpointAdvanced = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !checkpointAdvanced {
		_ = first.Close()
		t.Fatal("migrated checkpoint did not advance to revision 1")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("restart after checkpoint evolution: %v", err)
	}
	defer second.Close()
	status, err := second.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Revision != 1 || status.Configuration.LastKnownGoodRevision != 1 {
		t.Fatalf("restarted status=%+v", status)
	}
}

func TestDaemonDoesNotRecreateMissingConfigWhenLastKnownGoodExists(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	cfg.Revision = 7
	saveStoreLastKnownGood(t, configPath, cfg)
	if _, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "config.missing_with_last_good") {
		t.Fatalf("missing configured file error=%v", err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing configured file was recreated: %v", err)
	}
}

func TestDaemonRejectsMissingConfigWithAmbiguousLegacyLastKnownGood(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := os.WriteFile(configPath+".last-good", []byte(`{"revision":7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if d != nil {
		defer d.Close()
	}
	if publicerr.Code(err, "operation.failed") != legacyRecoveryAmbiguousError {
		t.Fatalf("ambiguous legacy recovery error=%v code=%q", err, publicerr.Code(err, "operation.failed"))
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing configured file was recreated: %v", statErr)
	}
}

func TestDaemonRejectsInvalidConfigWithAmbiguousLegacyLastKnownGood(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	invalid := []byte(`{"schema_version":`)
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath+".last-good", []byte(`{"revision":7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if d != nil {
		defer d.Close()
	}
	if publicerr.Code(err, "operation.failed") != legacyRecoveryAmbiguousError {
		t.Fatalf("ambiguous legacy recovery error=%v code=%q", err, publicerr.Code(err, "operation.failed"))
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, invalid) {
		t.Fatalf("invalid configured content was modified: %q", after)
	}
}

func TestDaemonRejectsMissingConfigWithAmbiguousLegacyTimestampBackup(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	backup := configPath + ".bak.20260826T010203.123456789"
	if err := os.WriteFile(backup, []byte("legacy backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if d != nil {
		defer d.Close()
	}
	if publicerr.Code(err, "operation.failed") != legacyRecoveryAmbiguousError {
		t.Fatalf("ambiguous legacy backup error=%v code=%q", err, publicerr.Code(err, "operation.failed"))
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing configured file was recreated: %v", statErr)
	}
}

func TestDaemonAllowsDefaultInitializationBesideNonBackupNeighbor(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := os.WriteFile(configPath+".bak.notes", []byte("neighbor config"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := configstore.LoadExisting(configPath); err != nil {
		t.Fatalf("default config was not initialized: %v", err)
	}
}

func TestDaemonStatusSurvivesMissingRuntimeConfigWithReadBackoff(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	cfg, err := configstore.LoadExisting(d.runtimeConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(d.runtimeConfigPath); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var failed controlapi.DaemonStatus
	for time.Now().Before(deadline) {
		failed, err = d.Status(context.Background())
		if err != nil {
			t.Fatalf("status became unavailable after config removal: %v", err)
		}
		if failed.Reconcile.ConsecutiveFailures > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if failed.State != controlapi.DaemonStateDegraded || failed.Revision != cfg.Revision ||
		failed.Reconcile.State != controlapi.ReconcileStateFailed ||
		failed.Reconcile.LastError != "operation failed (config.read_failed)" ||
		failed.Reconcile.ObservationFresh || failed.Reconcile.ConsecutiveFailures != 1 ||
		failed.Reconcile.FirstFailureAt == nil || failed.Reconcile.NextRetryAt == nil {
		t.Fatalf("missing config status = %+v", failed)
	}
	firstFailure := *failed.Reconcile.FirstFailureAt
	firstRetry := *failed.Reconcile.NextRetryAt
	time.Sleep(350 * time.Millisecond)
	stillFailed, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stillFailed.Reconcile.ConsecutiveFailures != 1 || stillFailed.Reconcile.FirstFailureAt == nil ||
		stillFailed.Reconcile.NextRetryAt == nil || !stillFailed.Reconcile.FirstFailureAt.Equal(firstFailure) ||
		!stillFailed.Reconcile.NextRetryAt.Equal(firstRetry) {
		t.Fatalf("config reader ignored backoff: before=%+v after=%+v", failed.Reconcile, stillFailed.Reconcile)
	}

	if err := configstore.Save(d.runtimeConfigPath, cfg); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	var recovered controlapi.DaemonStatus
	for time.Now().Before(deadline) {
		recovered, err = d.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if recovered.State == controlapi.DaemonStateRunning && recovered.Reconcile.ConsecutiveFailures == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if recovered.State != controlapi.DaemonStateRunning || recovered.Revision != cfg.Revision ||
		recovered.Reconcile.State != controlapi.ReconcileStateApplied ||
		recovered.Reconcile.ConsecutiveFailures != 0 || recovered.Reconcile.LastError != "" ||
		!recovered.Reconcile.ObservationFresh {
		t.Fatalf("restored config did not recover status: %+v", recovered)
	}
}

func TestReconcilerFailStopsAfterSustainedConfigReadFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.json")
	ctx, cancel := context.WithCancel(context.Background())
	plane := &failStopTrackingPlane{called: make(chan string, 1)}
	now := time.Now()
	d := &Daemon{
		ctx: ctx, cancel: cancel, runtimeConfigPath: configPath, plane: plane,
		reconcileDone: make(chan struct{}), state: controlapi.DaemonStateRunning,
		done: make(chan struct{}), configReadFailureTolerance: 20 * time.Millisecond,
		configIOFailure: reconcileFailureState{
			revision: 0, count: 1, firstAt: now.Add(-time.Second), nextAt: now.Add(-time.Millisecond),
		},
		configIOLastAt: now.Add(-time.Second),
		configIOActive: true,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: 0,
		},
	}
	go d.reconcileLoop(0)
	select {
	case code := <-plane.called:
		if code != configReadFailClosedError {
			t.Fatalf("fail-stop code=%q", code)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("sustained config read failure did not fail-stop the runtime")
	}
	status, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Reconcile.LastError != "operation failed (config.read_failed_fail_closed)" ||
		status.Reconcile.State != controlapi.ReconcileStateFailed {
		t.Fatalf("fail-stopped status=%+v", status.Reconcile)
	}
	cancel()
	<-d.reconcileDone
}

func TestConfigReadFailStopRetriesAfterIncompleteAttempt(t *testing.T) {
	now := time.Now()
	plane := &failStopTrackingPlane{called: make(chan string, 2), failures: 1}
	d := &Daemon{
		ctx: context.Background(), plane: plane, configReadFailureTolerance: time.Millisecond,
		configIOFailure: reconcileFailureState{
			revision: 0, count: 1, firstAt: now.Add(-time.Second), nextAt: now.Add(time.Second),
		},
		configIOLastAt: now.Add(-time.Second),
		configIOActive: true,
	}
	d.handleConfigReadFailure(now)
	if d.configReadFailClosedSnapshot() {
		t.Fatal("incomplete fail-stop was reported as complete")
	}
	d.handleConfigReadFailure(now.Add(time.Millisecond))
	if !d.configReadFailClosedSnapshot() || plane.callCount() != 2 {
		t.Fatalf("fail-stop retry state: complete=%t calls=%d", d.configReadFailClosedSnapshot(), plane.callCount())
	}
}

func TestContentFailureDoesNotConsumeIOFailureTolerance(t *testing.T) {
	now := time.Now()
	plane := &failStopTrackingPlane{called: make(chan string, 1)}
	d := &Daemon{
		ctx: context.Background(), plane: plane, configReadFailureTolerance: time.Second,
	}

	d.recordConfigContentFailure(now.Add(-time.Hour))
	d.handleConfigReadFailure(now)
	if plane.callCount() != 0 {
		t.Fatal("first I/O failure inherited the content-error tolerance window")
	}
	d.configReadFailureMu.RLock()
	contentFailure := d.configContentFailure
	ioFailure := d.configIOFailure
	d.configReadFailureMu.RUnlock()
	if contentFailure.count != 0 || ioFailure.count != 1 || !ioFailure.firstAt.Equal(now) {
		t.Fatalf("failure-class transition did not start a fresh I/O ledger: content=%+v io=%+v", contentFailure, ioFailure)
	}

	d.handleConfigReadFailure(now.Add(999 * time.Millisecond))
	if plane.callCount() != 0 {
		t.Fatal("I/O failure fail-stopped before its own tolerance elapsed")
	}
	d.handleConfigReadFailure(now.Add(time.Second))
	if plane.callCount() != 1 || !d.configReadFailClosedSnapshot() {
		t.Fatalf("sustained I/O failure did not fail-stop after its own tolerance: calls=%d latched=%t", plane.callCount(), d.configReadFailClosedSnapshot())
	}
}

func TestAlternatingContentAndIOFailuresEventuallyFailStop(t *testing.T) {
	now := time.Now()
	plane := &failStopTrackingPlane{called: make(chan string, 1)}
	d := &Daemon{
		ctx: context.Background(), plane: plane, configReadFailureTolerance: time.Second,
	}

	d.handleConfigReadFailure(now)
	d.recordConfigContentFailure(now.Add(300 * time.Millisecond))
	d.handleConfigReadFailure(now.Add(400 * time.Millisecond))
	d.recordConfigContentFailure(now.Add(700 * time.Millisecond))
	d.handleConfigReadFailure(now.Add(800 * time.Millisecond))
	d.recordConfigContentFailure(now.Add(1100 * time.Millisecond))
	d.handleConfigReadFailure(now.Add(1200 * time.Millisecond))
	if plane.callCount() != 0 {
		t.Fatal("alternating failures fail-stopped before cumulative I/O exposure reached tolerance")
	}
	d.handleConfigReadFailure(now.Add(1300 * time.Millisecond))
	if plane.callCount() != 1 || !d.configReadFailClosedSnapshot() {
		t.Fatalf("alternating failures evaded fail-stop: calls=%d latched=%t", plane.callCount(), d.configReadFailClosedSnapshot())
	}
}

func TestValidRuntimeRecoveryResetsIOFailureTolerance(t *testing.T) {
	now := time.Now()
	plane := &failStopTrackingPlane{called: make(chan string, 1)}
	d := &Daemon{
		ctx: context.Background(), plane: plane, configReadFailureTolerance: time.Second,
	}

	d.handleConfigReadFailure(now)
	d.handleConfigReadFailure(now.Add(900 * time.Millisecond))
	if plane.callCount() != 0 {
		t.Fatal("initial I/O exposure fail-stopped before tolerance")
	}
	d.clearConfigReadFailureAfterRuntimeRecovery()

	recoveredAt := now.Add(time.Hour)
	d.handleConfigReadFailure(recoveredAt)
	d.handleConfigReadFailure(recoveredAt.Add(999 * time.Millisecond))
	if plane.callCount() != 0 {
		t.Fatal("new I/O failure inherited exposure from before valid runtime recovery")
	}
	d.handleConfigReadFailure(recoveredAt.Add(time.Second))
	if plane.callCount() != 1 || !d.configReadFailClosedSnapshot() {
		t.Fatalf("new I/O failure did not receive exactly one full tolerance window: calls=%d latched=%t", plane.callCount(), d.configReadFailClosedSnapshot())
	}
}

func TestReloadContentInvalidNeverFailStopsOrDropsBoundUserListener(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plane := &failStopTrackingPlane{
		called: make(chan string, 1),
		status: dataplane.Status{
			State: "running", AppliedRevision: 7, AttemptedRevision: 7,
			Listeners: []dataplane.ListenerStatus{{
				Tag: "xtier-user-socks", Listen: "127.0.0.1:1080", State: "bound",
			}},
		},
	}
	plane.status.Rendr.State = "running"
	d := &Daemon{
		ctx: context.Background(), runtimeConfigPath: configPath, plane: plane,
		state: controlapi.DaemonStateDegraded, configReadFailureTolerance: 20 * time.Millisecond,
	}
	d.recordConfigContentFailure(time.Now().Add(-time.Second))

	for _, dryRun := range []bool{true, false} {
		if _, err := d.Reload(context.Background(), 7, dryRun); err == nil ||
			publicerr.Message(err, "fallback") != "operation failed (config.content_invalid)" {
			t.Fatalf("content-invalid reload dry_run=%t error=%v", dryRun, err)
		}
		status := plane.Status()
		if plane.callCount() != 0 || d.configReadFailClosedSnapshot() || len(status.Listeners) != 1 ||
			status.Listeners[0].Tag != "xtier-user-socks" || status.Listeners[0].State != "bound" {
			t.Fatalf("content-invalid reload changed the serving plane: dry_run=%t calls=%d latched=%t status=%+v", dryRun, plane.callCount(), d.configReadFailClosedSnapshot(), status)
		}
	}
}

func TestStatusPollingCannotResetOrUnlatchConfigReadFailStop(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	plane := &failStopTrackingPlane{called: make(chan string, 1)}
	d := &Daemon{
		ctx: context.Background(), configPath: configPath, runtimeConfigPath: configPath,
		plane: plane, state: controlapi.DaemonStateDegraded,
		configReadFailureTolerance: time.Millisecond,
		configIOFailure: reconcileFailureState{
			revision: cfg.Revision, count: 1, firstAt: now.Add(-time.Second), nextAt: now.Add(time.Second),
		},
		configIOLastAt: now.Add(-time.Second),
		configIOActive: true,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: cfg.Revision,
		},
	}
	if _, err := d.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if failure, ok := d.configReadFailureSnapshot(); !ok || failure.count != 1 || !failure.firstAt.Equal(now.Add(-time.Second)) {
		t.Fatalf("status poll reset config-read failure ledger: %+v ok=%t", failure, ok)
	}
	d.handleConfigReadFailure(now)
	if !d.configReadFailClosedSnapshot() || plane.callCount() != 1 {
		t.Fatalf("status poll deferred fail-stop: latched=%t calls=%d", d.configReadFailClosedSnapshot(), plane.callCount())
	}
	if _, err := d.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !d.configReadFailClosedSnapshot() {
		t.Fatal("status poll unlatched an already fail-stopped runtime")
	}
	d.clearConfigReadFailureAfterRuntimeRecovery()
	if d.configReadFailClosedSnapshot() {
		t.Fatal("successful runtime recovery did not clear fail-stop latch")
	}
}

func TestStatusJSONOmitsUnobservedRuntimeInstanceIDs(t *testing.T) {
	status := controlapi.DaemonStatus{
		APIVersion: controlapi.APIVersion,
		State:      controlapi.DaemonStateRunning,
		Rendr:      controlapi.RuntimeStatus{State: controlapi.RuntimeStateUnavailable},
		Xray: controlapi.XrayStatus{
			State:                controlapi.RuntimeStateRunning,
			Draining:             []controlapi.XrayGenerationStatus{},
			StrictStreamOutbound: true,
		},
	}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("instance_id")) {
		t.Fatalf("unobserved instance id was serialized: %s", body)
	}
}

func TestStartRejectsNonLoopbackAddress(t *testing.T) {
	_, err := Start(context.Background(), Options{
		ConfigPath:  filepath.Join(t.TempDir(), "xtier.json"),
		ControlAddr: "0.0.0.0:0",
	})
	if err == nil {
		t.Fatal("non-loopback control address was accepted")
	}
}

func TestDaemonServesBuiltWebAppThroughAuthenticatedBridge(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	webRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(webRoot, "index.html"), []byte("<!doctype html><title>X-Tier real</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{
		ConfigPath:  configPath,
		ControlAddr: "127.0.0.1:0",
		WebAddr:     "127.0.0.1:0",
		WebRoot:     webRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if d.WebAddr() == "" || d.WebAddr() == d.Addr() {
		t.Fatalf("invalid web/control addresses: web=%q control=%q", d.WebAddr(), d.Addr())
	}
	response, err := http.Get("http://" + d.WebAddr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous web app response=%d", response.StatusCode)
	}
	webCredential, err := controlapi.ReadStoreToken(d.stateStore, statestore.WebToken)
	if err != nil {
		t.Fatal(err)
	}
	controlToken, err := daemonControlToken(d)
	if err != nil {
		t.Fatal(err)
	}
	if webCredential == controlToken {
		t.Fatal("Web credential reused the control token")
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+d.WebAddr()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(webbridge.BasicUsername, webCredential)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("X-Tier real")) {
		t.Fatalf("web app response=%d %q", response.StatusCode, body)
	}
	request, err = http.NewRequest(http.MethodGet, "http://"+d.WebAddr()+controlapi.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(webbridge.BasicUsername, webCredential)
	request.Header.Set("Origin", "http://"+d.WebAddr())
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("X-XTier-CSRF-Token") == "" {
		t.Fatalf("browser status response=%d headers=%v", response.StatusCode, response.Header)
	}
	status, err := daemonStatus(d)
	if err != nil {
		t.Fatal(err)
	}
	if status.WebAddr != d.WebAddr() {
		t.Fatalf("status web address=%q, want %q", status.WebAddr, d.WebAddr())
	}
}

func TestLocalReloadAppliesAndRejectsStaleRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	token, err := daemonControlToken(d)
	if err != nil {
		t.Fatal(err)
	}
	before, err := controlapi.GetStatusToken(d.Addr(), token)
	if err != nil {
		t.Fatal(err)
	}
	missingRevision := controlapi.Request{
		Args: []string{"local", "reload"}, JSON: true, Revision: -1,
		RequestID: "80000000000000000000000000000000",
	}
	response, err := controlapi.ExecuteToken(d.Addr(), token, missingRevision)
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode == 0 || !strings.Contains(response.Stdout, "config.revision_required") {
		t.Fatalf("reload without revision response=%+v", response)
	}
	unmodified, err := controlapi.GetStatusToken(d.Addr(), token)
	if err != nil {
		t.Fatal(err)
	}
	if before.Xray.Current == nil || unmodified.Xray.Current == nil || unmodified.Xray.Current.Generation != before.Xray.Current.Generation {
		t.Fatalf("reload without revision changed generation: before=%+v after=%+v", before.Xray.Current, unmodified.Xray.Current)
	}
	request := controlapi.Request{
		Args: []string{"local", "reload"}, JSON: true, Revision: 0,
		RequestID: "81000000000000000000000000000000",
	}
	response, err = controlapi.ExecuteToken(d.Addr(), token, request)
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 0 || !strings.Contains(response.Stdout, `"reconciliation_state":"applied"`) {
		t.Fatalf("reload response=%+v", response)
	}
	after, err := controlapi.GetStatusToken(d.Addr(), token)
	if err != nil {
		t.Fatal(err)
	}
	if before.Xray.Current == nil || after.Xray.Current == nil || after.Xray.Current.Generation != before.Xray.Current.Generation {
		t.Fatalf("unchanged reload published a new generation: before=%+v after=%+v", before.Xray.Current, after.Xray.Current)
	}
	cfg, err := configstore.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Revision = 1
	cfg.Node.DisplayName = "reload revision one"
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	request.Revision = 1
	request.RequestID = "81500000000000000000000000000000"
	response, err = controlapi.ExecuteToken(d.Addr(), token, request)
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 0 || !strings.Contains(response.Stdout, `"reconciliation_state":"applied"`) {
		t.Fatalf("changed reload response=%+v", response)
	}
	changed, err := controlapi.GetStatusToken(d.Addr(), token)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision != 1 || changed.Xray.Current == nil || changed.Xray.Current.Generation <= after.Xray.Current.Generation {
		t.Fatalf("changed reload did not publish a new generation: before=%+v after=%+v", after.Xray.Current, changed.Xray.Current)
	}
	request.RequestID = "81700000000000000000000000000000"
	response, err = controlapi.ExecuteToken(d.Addr(), token, request)
	if err != nil || response.ExitCode != 0 {
		t.Fatalf("idempotent reload retry response=%+v err=%v", response, err)
	}
	retried, err := controlapi.GetStatusToken(d.Addr(), token)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Xray.Current == nil || retried.Xray.Current.Generation != changed.Xray.Current.Generation {
		t.Fatalf("same-revision reload retry changed generation: before=%+v after=%+v", changed.Xray.Current, retried.Xray.Current)
	}
	request.Revision = 99
	request.RequestID = "82000000000000000000000000000000"
	response, err = controlapi.ExecuteToken(d.Addr(), token, request)
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode == 0 || !strings.Contains(response.Stdout, "config.revision_conflict") {
		t.Fatalf("stale reload response=%+v", response)
	}
}

func TestReloadReadsConfigAfterAcquiringApplyLock(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	cfg.Revision = 1
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := configstore.SaveLastKnownGood(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plane := &driftRuntimePlane{
		status: dataplane.Status{
			State:             "running",
			AppliedRevision:   cfg.Revision,
			AttemptedRevision: cfg.Revision,
			AppliedDigest:     digest,
			AttemptedDigest:   digest,
		},
		applied: make(chan int64, 1),
	}
	d := &Daemon{
		configPath: configPath, runtimeConfigPath: configPath, plane: plane,
		state: controlapi.DaemonStateRunning,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: cfg.Revision,
		},
	}

	d.applyPersistMu.Lock()
	locked := true
	defer func() {
		if locked {
			d.applyPersistMu.Unlock()
		}
	}()
	reloadDone := make(chan error, 1)
	go func() {
		_, reloadErr := d.Reload(context.Background(), cfg.Revision, false)
		reloadDone <- reloadErr
	}()
	select {
	case reloadErr := <-reloadDone:
		t.Fatalf("reload bypassed the held apply lock: %v", reloadErr)
	case <-time.After(250 * time.Millisecond):
	}

	newer := cfg
	newer.Revision++
	newer.Node.DisplayName = "concurrent mutation"
	if err := configstore.Save(configPath, newer); err != nil {
		t.Fatal(err)
	}
	d.applyPersistMu.Unlock()
	locked = false
	if reloadErr := <-reloadDone; reloadErr == nil || !strings.Contains(reloadErr.Error(), "config.revision_conflict") {
		t.Fatalf("reload did not re-read the concurrent revision under lock: %v", reloadErr)
	}
	select {
	case revision := <-plane.applied:
		t.Fatalf("reload applied stale revision %d", revision)
	default:
	}
	if status := plane.Status(); status.LastError != "" || status.AppliedRevision != cfg.Revision {
		t.Fatalf("revision conflict polluted runtime status: %+v", status)
	}
}

func TestStartRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Start(ctx, Options{
		ConfigPath:  filepath.Join(t.TempDir(), "xtier.json"),
		ControlAddr: "127.0.0.1:0",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context cancellation", err)
	}
}

func TestOneDaemonOwnsEachConfigAndLeaseIsReleased(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	first, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "daemon.already_running") {
		t.Fatalf("second daemon error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("restart after lease release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOneDaemonOwnsConfigAcrossParentDirectoryAlias(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	aliasDir := filepath.Join(dir, "alias")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	realPath := filepath.Join(realDir, "xtier.json")
	if err := configstore.Save(realPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	first, err := Start(context.Background(), Options{ConfigPath: realPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	aliasPath := filepath.Join(aliasDir, "xtier.json")
	if _, err := Start(context.Background(), Options{ConfigPath: aliasPath, ControlAddr: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "daemon.already_running") {
		t.Fatalf("second daemon through parent alias error = %v", err)
	}
	canonical, err := configstore.CanonicalPath(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConfigPath() != canonical {
		t.Fatalf("daemon config path = %q, want canonical %q", first.ConfigPath(), canonical)
	}
}

func TestDaemonLeaseRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "xtier.json")
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "must-not-change")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := configstore.CanonicalPath(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := statestore.Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := store.DiagnosticPath(statestore.DaemonLock)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lockPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"}); err == nil {
		t.Fatal("daemon accepted symlink lease")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "original" {
		t.Fatalf("symlink target was modified: %q", contents)
	}
}

func TestSameDirectoryDaemonsUseIndependentPrivateState(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "a"),
		filepath.Join(dir, "a.control-token"),
		filepath.Join(dir, "a.web-token"),
		filepath.Join(dir, "a.last-good"),
		filepath.Join(dir, "a.bak.notes"),
		filepath.Join(dir, "a.lock"),
		filepath.Join(dir, "a.daemon.lock"),
	}
	for index, path := range paths {
		cfg := configstore.DefaultConfig()
		cfg.Node.DisplayName = fmt.Sprintf("neighbor-%d", index)
		if err := configstore.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
	}

	daemons := make([]*Daemon, 0, len(paths))
	tokens := make([]string, 0, len(paths))
	keys := make(map[string]struct{}, len(paths))
	privatePaths := make(map[string]struct{})
	for index, path := range paths {
		d, err := Start(context.Background(), Options{
			ConfigPath: path, ControlAddr: "127.0.0.1:0", WebAddr: "127.0.0.1:0",
		})
		if err != nil {
			t.Fatalf("start neighbor %d (%s): %v", index, path, err)
		}
		t.Cleanup(func() { _ = d.Close() })
		daemons = append(daemons, d)
		key := d.stateStore.ConfigKey()
		if _, exists := keys[key]; exists {
			t.Fatalf("same-directory daemons share config key %q", key)
		}
		keys[key] = struct{}{}
		for _, object := range []statestore.Object{
			statestore.ConfigLock, statestore.DaemonLock, statestore.ControlToken,
			statestore.WebToken, statestore.IdentitySeed, statestore.LastKnownGood,
		} {
			privatePath, err := d.stateStore.DiagnosticPath(object)
			if err != nil {
				t.Fatal(err)
			}
			privatePath = filepath.Clean(privatePath)
			if _, exists := privatePaths[privatePath]; exists {
				t.Fatalf("same-directory daemons share private path %q", privatePath)
			}
			privatePaths[privatePath] = struct{}{}
		}
		token, err := daemonControlToken(d)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
		loaded, err := configstore.LoadExisting(path)
		if err != nil || loaded.Node.DisplayName != fmt.Sprintf("neighbor-%d", index) {
			t.Fatalf("neighbor %d was modified: config=%+v err=%v", index, loaded, err)
		}
	}
	for daemonIndex, d := range daemons {
		for tokenIndex, token := range tokens {
			_, err := controlapi.GetStatusToken(d.Addr(), token)
			if daemonIndex == tokenIndex && err != nil {
				t.Fatalf("daemon %d rejected its own token: %v", daemonIndex, err)
			}
			if daemonIndex != tokenIndex && err == nil {
				t.Fatalf("daemon %d accepted daemon %d token", daemonIndex, tokenIndex)
			}
		}
	}
}

func TestDaemonRetainsLeaseUntilXrayShutdownCompletes(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireInstanceLease(configPath)
	if err != nil {
		t.Fatal(err)
	}
	firstAttempt := make(chan struct{})
	releaseRetry := make(chan struct{})
	runtime := &retryXrayRuntime{failures: 5, firstAttempt: firstAttempt, releaseRetry: releaseRetry}
	d := &Daemon{
		configPath:        configPath,
		runtimeConfigPath: lease.ConfigPath(),
		plane:             runtime,
		lease:             lease,
		done:              make(chan struct{}),
		retryDelay:        time.Millisecond,
	}
	go d.finish(nil)
	<-firstAttempt
	if _, err := acquireInstanceLease(configPath); err == nil || !strings.Contains(err.Error(), "daemon.already_running") {
		t.Fatalf("lease released during incomplete Xray shutdown: %v", err)
	}
	close(releaseRetry)
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not finish after Xray retry succeeded")
	}
	runtime.mu.Lock()
	attempts := runtime.attempts
	runtime.mu.Unlock()
	if attempts < 6 {
		t.Fatalf("Xray shutdown attempts=%d, want at least 6", attempts)
	}
	second, err := acquireInstanceLease(configPath)
	if err != nil {
		t.Fatalf("lease not released after Xray shutdown: %v", err)
	}
	_ = second.Close()
}

func TestCloseRuntimePlaneRetriesUntilXrayShutdownCompletes(t *testing.T) {
	firstAttempt := make(chan struct{})
	releaseRetry := make(chan struct{})
	close(releaseRetry)
	runtime := &retryXrayRuntime{
		failures: 3, firstAttempt: firstAttempt, releaseRetry: releaseRetry,
	}
	if err := closeRuntimePlane(runtime, time.Microsecond, time.Second); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	attempts := runtime.attempts
	runtime.mu.Unlock()
	if attempts != 4 {
		t.Fatalf("shutdown attempts=%d, want 4", attempts)
	}
}

func TestCloseRuntimePlaneBoundsRetriesAndForcesShutdown(t *testing.T) {
	runtime := &forceRequiredRuntime{}
	started := time.Now()
	err := closeRuntimePlane(runtime, time.Millisecond, 10*time.Millisecond)
	if !errors.Is(err, errRuntimePlaneShutdownTimedOut) || !errors.Is(err, xrayrt.ErrShutdownIncomplete) {
		t.Fatalf("bounded shutdown error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded shutdown took %s", elapsed)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.attempts < 1 || !runtime.forced {
		t.Fatalf("shutdown attempts=%d forced=%t", runtime.attempts, runtime.forced)
	}
}

func TestCloseRuntimePlaneBoundsImplementationsThatIgnoreDeadlines(t *testing.T) {
	release := make(chan struct{})
	runtime := &deadlineIgnoringRuntime{
		release: release, closeEntered: make(chan struct{}), forceEntered: make(chan struct{}),
	}
	started := time.Now()
	err := closeRuntimePlane(runtime, time.Millisecond, 200*time.Millisecond)
	if !errors.Is(err, errRuntimePlaneShutdownTimedOut) || !errors.Is(err, xrayrt.ErrShutdownIncomplete) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded shutdown error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 350*time.Millisecond {
		t.Fatalf("deadline-ignoring runtime exceeded the single shutdown budget: %s", elapsed)
	}
	select {
	case <-runtime.closeEntered:
	default:
		t.Fatal("graceful close was not attempted")
	}
	select {
	case <-runtime.forceEntered:
	default:
		t.Fatal("forced close was not attempted")
	}
	close(release)
}

func TestDaemonFinishReportsBlockedReconcilerWithoutReleasingOwnedResources(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lease, err := acquireInstanceLease(configPath)
	if err != nil {
		t.Fatal(err)
	}
	plane := &contextIgnoringApplyPlane{
		entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}),
	}
	d := &Daemon{
		ctx: ctx, cancel: cancel, runtimeConfigPath: lease.ConfigPath(), plane: plane,
		lease: lease, stateStore: lease.Store(),
		reconcileDone: make(chan struct{}), done: make(chan struct{}),
		state: controlapi.DaemonStateDegraded,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: -1,
		},
		retryDelay: time.Millisecond, shutdownLimit: 25 * time.Millisecond,
	}
	go d.reconcileLoop(cfg.Revision)
	select {
	case <-plane.entered:
	case <-time.After(time.Second):
		t.Fatal("reconciler did not enter context-ignoring Apply")
	}
	cancel()
	go d.finish(nil)
	select {
	case <-d.done:
		t.Fatal("daemon released owned resources while the reconciler was still running")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-plane.closed:
		t.Fatal("runtime plane closed underneath the reconciler")
	default:
	}
	if _, err := configstore.LoadStoreExisting(lease.Store()); err != nil {
		t.Fatalf("state store closed underneath the reconciler: %v", err)
	}
	if _, err := acquireInstanceLease(configPath); err == nil || !strings.Contains(err.Error(), "daemon.already_running") {
		t.Fatalf("instance lease released underneath the reconciler: %v", err)
	}
	close(plane.release)
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("daemon did not finish after the reconciler was released")
	}
	if err := d.Wait(); !errors.Is(err, errReconcileShutdownTimedOut) ||
		!errors.Is(err, xrayrt.ErrShutdownIncomplete) {
		t.Fatalf("finish error=%v, want explicit incomplete reconciler shutdown", err)
	}
	select {
	case <-plane.closed:
	default:
		t.Fatal("runtime plane was not closed after the reconciler exited")
	}
	second, err := acquireInstanceLease(configPath)
	if err != nil {
		t.Fatalf("instance lease not released after shutdown: %v", err)
	}
	_ = second.Close()
}

func TestDaemonFinishWaitsForDirectReloadBeforeReleasingOwnedResources(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lease, err := acquireInstanceLease(configPath)
	if err != nil {
		t.Fatal(err)
	}
	plane := &contextIgnoringApplyPlane{
		entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}),
	}
	d := &Daemon{
		ctx: ctx, cancel: cancel, configPath: configPath, runtimeConfigPath: lease.ConfigPath(),
		plane: plane, lease: lease, stateStore: lease.Store(), done: make(chan struct{}),
		state: controlapi.DaemonStateRunning,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: -1,
		},
		retryDelay: time.Millisecond, shutdownLimit: 25 * time.Millisecond,
	}
	reloadDone := make(chan error, 1)
	go func() {
		_, reloadErr := d.Reload(context.Background(), cfg.Revision, false)
		reloadDone <- reloadErr
	}()
	select {
	case <-plane.entered:
	case <-time.After(time.Second):
		t.Fatal("direct reload did not enter context-ignoring Apply")
	}
	cancel()
	go d.finish(nil)

	deadline := time.Now().Add(time.Second)
	for {
		_, statusErr := d.Status(context.Background())
		if publicerr.Code(statusErr, "operation.failed") == "service.stopping" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new operation was not rejected during shutdown: %v", statusErr)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-d.done:
		t.Fatal("daemon released owned resources while direct reload was still running")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-plane.closed:
		t.Fatal("runtime plane closed underneath direct reload")
	default:
	}
	if _, err := configstore.LoadStoreExisting(lease.Store()); err != nil {
		t.Fatalf("state store closed underneath direct reload: %v", err)
	}
	if _, err := acquireInstanceLease(configPath); err == nil || !strings.Contains(err.Error(), "daemon.already_running") {
		t.Fatalf("instance lease released underneath direct reload: %v", err)
	}

	close(plane.release)
	select {
	case <-reloadDone:
	case <-time.After(time.Second):
		t.Fatal("direct reload did not exit after release")
	}
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("daemon did not finish after direct reload exited")
	}
	if err := d.Wait(); !errors.Is(err, errOperationShutdownTimedOut) ||
		!errors.Is(err, xrayrt.ErrShutdownIncomplete) {
		t.Fatalf("finish error=%v, want explicit incomplete operation shutdown", err)
	}
	select {
	case <-plane.closed:
	default:
		t.Fatal("runtime plane was not closed after direct reload exited")
	}
	second, err := acquireInstanceLease(configPath)
	if err != nil {
		t.Fatalf("instance lease not released after shutdown: %v", err)
	}
	_ = second.Close()
}

func TestReconcilerRetriesAsynchronousXrayGenerationCleanup(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plane := &cleanupRetryPlane{
		status: dataplane.Status{
			State: "running", AppliedRevision: cfg.Revision, AttemptedRevision: cfg.Revision,
			AppliedDigest: digest, AttemptedDigest: digest, ObservedAt: time.Now().UTC(),
			Xray: xrayrt.Status{Draining: []xrayrt.GenerationStatus{{
				Generation: 1, Draining: true, CleanupError: "remove failed with credential super-secret",
			}}},
		},
		applied: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{
		ctx: ctx, cancel: cancel, runtimeConfigPath: configPath, plane: plane,
		reconcileDone: make(chan struct{}), state: controlapi.DaemonStateDegraded,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: cfg.Revision,
		},
	}
	go d.reconcileLoop(cfg.Revision)
	select {
	case <-plane.applied:
	case <-time.After(3 * time.Second):
		cancel()
		<-d.reconcileDone
		t.Fatal("reconciler did not retry asynchronous Xray cleanup")
	}
	cancel()
	<-d.reconcileDone
	status := plane.Status()
	if xrayCleanupPending(status.Xray) {
		t.Fatalf("cleanup remained pending after retry: %+v", status.Xray)
	}
}

func TestReconcilerRepairsFailedRendrAtSameRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plane := &rendrRecoveryPlane{
		status: dataplane.Status{
			State: "degraded", AppliedRevision: cfg.Revision, AttemptedRevision: cfg.Revision,
			AppliedDigest: digest, AttemptedDigest: digest, ObservedAt: time.Now().UTC(),
			Rendr: rendradapter.RuntimeStatus{State: "failed"},
		},
		applied: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{
		ctx: ctx, cancel: cancel, runtimeConfigPath: configPath, plane: plane,
		reconcileDone: make(chan struct{}), state: controlapi.DaemonStateDegraded,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: cfg.Revision,
		},
	}
	go d.reconcileLoop(cfg.Revision)
	select {
	case <-plane.applied:
	case <-time.After(3 * time.Second):
		cancel()
		<-d.reconcileDone
		t.Fatal("reconciler did not repair failed rendr runtime")
	}
	cancel()
	<-d.reconcileDone
	if status := plane.Status(); status.Rendr.State != "running" {
		t.Fatalf("rendr state after reconcile = %+v", status.Rendr)
	}
}

func TestXrayStatusRedactsCleanupDiagnostics(t *testing.T) {
	const secret = "vless-credential-super-secret"
	status := xrayStatusFrom(dataplane.Status{Xray: xrayrt.Status{
		Draining: []xrayrt.GenerationStatus{{
			Generation: 7, Draining: true, CleanupError: "remove " + secret + " at peer.internal:2443",
		}},
	}})
	if len(status.Draining) != 1 || status.Draining[0].CleanupError == "" {
		t.Fatalf("public cleanup state missing: %+v", status)
	}
	if strings.Contains(status.Draining[0].CleanupError, secret) || strings.Contains(status.Draining[0].CleanupError, "peer.internal") {
		t.Fatalf("public cleanup status leaked diagnostics: %+v", status.Draining[0])
	}
	if status.Draining[0].CleanupError != "operation failed (runtime.xray_cleanup_failed)" {
		t.Fatalf("public cleanup error=%q", status.Draining[0].CleanupError)
	}
}

func TestXrayStatusReflectsObservedRuntimeAndFailStop(t *testing.T) {
	generation := &xrayrt.GenerationStatus{Generation: 3}
	authorizationDigest := [32]byte{1, 2, 3}
	observed := xrayrt.Status{
		Current: generation, StrictStreamOutbound: true, StrictPacketOutbound: false,
		Draining: []xrayrt.GenerationStatus{},
	}
	running := xrayStatusFrom(dataplane.Status{
		State: "running", Xray: observed,
		EgressAuthorizationRevision: 9,
		EgressAuthorizationDigest:   authorizationDigest,
		EgressAuthorizationSources:  2,
		EgressAuthorizationDenials:  4,
	})
	if running.State != controlapi.RuntimeStateRunning || running.FailStopped ||
		!running.StrictStreamOutbound || running.StrictPacketOutbound ||
		running.EgressAuthorizationRevision != 9 || running.EgressAuthorizationSources != 2 ||
		running.EgressAuthorizationDenials != 4 ||
		running.EgressAuthorizationDigest != hex.EncodeToString(authorizationDigest[:]) {
		t.Fatalf("running Xray status = %+v", running)
	}
	failStopped := xrayStatusFrom(dataplane.Status{State: "running", FailStopped: true, Xray: observed})
	if failStopped.State != controlapi.RuntimeStateFailed || !failStopped.FailStopped || failStopped.Current == nil {
		t.Fatalf("fail-stopped Xray status = %+v", failStopped)
	}
	stopping := xrayStatusFrom(dataplane.Status{State: "stopping", Xray: observed})
	if stopping.State != controlapi.RuntimeStateStopping {
		t.Fatalf("stopping Xray status = %+v", stopping)
	}
	stopped := xrayStatusFrom(dataplane.Status{State: "stopped", Xray: xrayrt.Status{Closed: true, StrictStreamOutbound: true}})
	if stopped.State != controlapi.RuntimeStateStopped {
		t.Fatalf("stopped Xray status = %+v", stopped)
	}
}

func TestOperationalStateDegradesWhenRendrAcceptLoopFails(t *testing.T) {
	cfg := configstore.DefaultConfig()
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := dataplane.Status{
		AppliedRevision: cfg.Revision,
		AppliedDigest:   digest,
	}
	status.Rendr.State = "failed"
	checkpoint := controlapi.ConfigurationStatus{LastKnownGoodRevision: cfg.Revision}
	if got := operationalStateFrom(controlapi.DaemonStateRunning, cfg.Revision, digest, status, checkpoint); got != controlapi.DaemonStateDegraded {
		t.Fatalf("failed rendr state produced daemon state %q", got)
	}
}

func TestOperationalStateDegradesWhenConfiguredListenerIsUnavailable(t *testing.T) {
	cfg := configstore.DefaultConfig()
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := dataplane.Status{
		State:           "running",
		AppliedRevision: cfg.Revision,
		AppliedDigest:   digest,
		Listeners: []dataplane.ListenerStatus{
			{Tag: "xtier-user-socks", Listen: "127.0.0.1:1080", State: "unavailable"},
		},
	}
	status.Rendr.State = "running"
	checkpoint := controlapi.ConfigurationStatus{LastKnownGoodRevision: cfg.Revision}
	if got := operationalStateFrom(controlapi.DaemonStateRunning, cfg.Revision, digest, status, checkpoint); got != controlapi.DaemonStateDegraded {
		t.Fatalf("unavailable listener produced daemon state %q", got)
	}
}

func TestOperationalStateAllowsUnavailableNodeListenerWithoutAuthorizedPeer(t *testing.T) {
	cfg := configstore.DefaultConfig()
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := dataplane.Status{
		State:           "running",
		AppliedRevision: cfg.Revision,
		AppliedDigest:   digest,
		Listeners: []dataplane.ListenerStatus{
			{Tag: xrayconfig.NodeVLESSTag, Listen: "127.0.0.1:2443", State: "unavailable"},
		},
	}
	status.Rendr.State = "running"
	checkpoint := controlapi.ConfigurationStatus{LastKnownGoodRevision: cfg.Revision}
	if got := operationalStateFrom(controlapi.DaemonStateRunning, cfg.Revision, digest, status, checkpoint); got != controlapi.DaemonStateRunning {
		t.Fatalf("intentionally unavailable node listener produced daemon state %q", got)
	}
}

func TestDaemonStartsControlPlaneFromLastKnownGoodAfterRuntimeApplyFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	initial, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	blockedAddress := blocker.Addr().String()
	if _, err := configstore.UpdateCAS(configPath, 0, func(cfg *configstore.Config) error {
		cfg.XrayProfiles["carrier-in"] = configstore.XrayProfile{
			ID: "carrier-in", Kind: "vless", VLESS: &configstore.VLESSProfile{
				UUID: "d342d11e-d424-4583-b36e-524ab1f0afa4", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			},
		}
		cfg.NodeInbound = []configstore.InboundConfig{{
			Kind: "node-vless", Purpose: "node", Listen: blockedAddress, Enabled: true,
		}}
		cfg.Peers = []configstore.PeerConfig{{
			Name: "A", NodeID: "node-a", Direction: route.DirectionInbound,
			XrayProfileID: "carrier-in", Enabled: true,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	recovered, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start from last-known-good: %v", err)
	}
	status, err := daemonStatus(recovered)
	if err != nil {
		_ = recovered.Close()
		t.Fatal(err)
	}
	if status.State != controlapi.DaemonStateDegraded || status.Revision != 1 ||
		status.Reconcile.State != controlapi.ReconcileStateFailed ||
		status.Reconcile.AppliedRevision != 0 || status.Reconcile.AttemptedRevision != 1 ||
		status.Reconcile.LastError == "" || status.Configuration.StartupRollback == nil ||
		status.Configuration.StartupRollback.ConfiguredRevision != 1 ||
		status.Configuration.StartupRollback.AppliedRevision != 0 ||
		status.Configuration.StartupRollback.ErrorCode != "dataplane.startup_apply_failed" {
		_ = recovered.Close()
		t.Fatalf("recovered daemon status = %+v", status)
	}
	lastGood, err := loadStoreLastKnownGood(configPath)
	if err != nil {
		_ = recovered.Close()
		t.Fatal(err)
	}
	if lastGood.Revision != 0 {
		_ = recovered.Close()
		t.Fatalf("failed candidate replaced last-known-good revision: %d", lastGood.Revision)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := configstore.UpdateCAS(configPath, 1, func(cfg *configstore.Config) error {
		cfg.NodeInbound[0].Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fixed, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer fixed.Close()
	fixedStatus, err := fixed.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fixedStatus.Configuration.StartupRollback != nil {
		t.Fatalf("successful startup retained rollback status: %+v", fixedStatus.Configuration.StartupRollback)
	}
	lastGood, err = loadStoreLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if lastGood.Revision != 2 || lastGood.NodeInbound[0].Enabled {
		t.Fatalf("last-known-good was not advanced after repair: %+v", lastGood)
	}
}

func TestDaemonStartsLastKnownGoodWhenConfiguredContentIsInvalid(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	initial, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := loadStoreLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}

	invalid := configstore.DefaultConfig()
	invalid.Revision = checkpoint.Revision + 1
	invalid.NodeInbound = []configstore.InboundConfig{
		{Kind: "socks", Listen: "127.0.0.1:1080"},
		{Kind: "socks", Listen: "127.0.0.1:2080"},
	}
	payload, err := json.MarshalIndent(invalid, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := Start(context.Background(), Options{
		ConfigPath: configPath, ControlAddr: "127.0.0.1:0", ConfigReadFailureTolerance: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start from last-known-good after invalid configured content: %v", err)
	}
	defer recovered.Close()
	status, err := recovered.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Addr() == "" || status.State != controlapi.DaemonStateDegraded ||
		status.Reconcile.AppliedRevision != checkpoint.Revision ||
		status.Configuration.LastKnownGoodRevision != checkpoint.Revision ||
		status.Configuration.StartupRollback == nil ||
		status.Configuration.StartupRollback.ConfiguredRevision != -1 ||
		status.Configuration.StartupRollback.AppliedRevision != checkpoint.Revision ||
		status.Configuration.StartupRollback.ErrorCode != "config.startup_content_invalid" ||
		status.Rendr.State != controlapi.RuntimeStateRunning {
		t.Fatalf("invalid-content recovery status = %+v", status)
	}
	time.Sleep(250 * time.Millisecond)
	status, err = recovered.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Xray.FailStopped || status.Rendr.State != controlapi.RuntimeStateRunning ||
		status.Reconcile.LastError != publicerr.MessageCode(configContentInvalidError) ||
		status.Reconcile.LastErrorCode != configContentInvalidError {
		t.Fatalf("invalid content incorrectly fail-stopped the recovered runtime: %+v", status)
	}
	httpStatus, err := daemonStatus(recovered)
	if err != nil {
		t.Fatalf("authenticated status rejected content-invalid LKG state: %v", err)
	}
	if httpStatus.Revision != checkpoint.Revision || httpStatus.Configuration.StartupRollback == nil ||
		httpStatus.Configuration.StartupRollback.ConfiguredRevision != -1 {
		t.Fatalf("authenticated content-invalid status=%+v", httpStatus)
	}
	for _, dryRun := range []bool{true, false} {
		if _, reloadErr := recovered.Reload(context.Background(), checkpoint.Revision, dryRun); reloadErr == nil ||
			publicerr.Message(reloadErr, "fallback") != publicerr.MessageCode(configContentInvalidError) {
			t.Fatalf("content-invalid reload dry_run=%t error=%v", dryRun, reloadErr)
		}
		status, err = recovered.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.Xray.FailStopped || status.Rendr.State != controlapi.RuntimeStateRunning {
			t.Fatalf("content-invalid reload stopped the LKG runtime: dry_run=%t status=%+v", dryRun, status)
		}
	}
	domainRequestID, err := controlapi.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	domainBody, err := json.Marshal(controlapi.ConfigRestoreRequest{DomainMutationRequest: controlapi.DomainMutationRequest{
		APIVersion: controlapi.DomainAPIVersion,
		Revision:   checkpoint.Revision,
		DryRun:     true,
		RequestID:  domainRequestID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := daemonControlToken(recovered)
	if err != nil {
		t.Fatal(err)
	}
	domainStatus, domainResponse, err := controlapi.AuthenticatedRequestTokenContext(
		context.Background(), recovered.Addr(), token,
		http.MethodPost, controlapi.DomainConfigRestorePath, domainBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	if domainStatus != http.StatusOK || !bytes.Contains(domainResponse, []byte(`"source":"last-known-good"`)) ||
		!bytes.Contains(domainResponse, []byte(`"changed":true`)) {
		t.Fatalf("daemon restore domain dry-run status=%d body=%s", domainStatus, domainResponse)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, payload) {
		t.Fatalf("invalid configured source was rewritten: before=%s after=%s", payload, after)
	}
	stored, err := loadStoreLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != checkpoint.Revision {
		t.Fatalf("invalid configured source replaced checkpoint: %+v", stored)
	}
	requestID, err := controlapi.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	recovered.applyPersistMu.Lock()
	applyLocked := true
	defer func() {
		if applyLocked {
			recovered.applyPersistMu.Unlock()
		}
	}()
	response, err := daemonExecute(recovered, controlapi.Request{
		Args:      []string{"local", "config", "restore-last-good"},
		JSON:      true,
		Revision:  checkpoint.Revision,
		RequestID: requestID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 0 || !strings.Contains(response.Stdout, `"source":"last-known-good"`) {
		t.Fatalf("restore response = %+v", response)
	}
	postRestoreStatus, err := daemonStatus(recovered)
	if err != nil {
		t.Fatalf("authenticated status failed between restore and reconcile: %v", err)
	}
	if postRestoreStatus.Revision != checkpoint.Revision+1 || postRestoreStatus.Reconcile.AppliedRevision != checkpoint.Revision ||
		postRestoreStatus.Configuration.StartupRollback == nil {
		t.Fatalf("post-restore pre-reconcile status=%+v", postRestoreStatus)
	}
	applyLocked = false
	recovered.applyPersistMu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err = recovered.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if status.State == controlapi.DaemonStateRunning && status.Revision == checkpoint.Revision+1 &&
			status.Reconcile.State == controlapi.ReconcileStateApplied && status.Configuration.StartupRollback == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if status.State != controlapi.DaemonStateRunning || status.Revision != checkpoint.Revision+1 ||
		status.Reconcile.State != controlapi.ReconcileStateApplied || status.Configuration.StartupRollback != nil ||
		status.Xray.FailStopped {
		t.Fatalf("restored daemon did not reconcile: %+v", status)
	}
	repaired, err := configstore.LoadExisting(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Revision != checkpoint.Revision+1 {
		t.Fatalf("repaired active config = %+v", repaired)
	}
}

func TestContentAndRuntimeStartupRollbacksKeepRepairControlPlaneServing(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := configstore.DefaultConfig()
	checkpoint.Revision = 5
	checkpoint.Node.DisplayName = "checkpoint"
	if err := configstore.Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	saveStoreLastKnownGood(t, configPath, checkpoint)
	if err := os.WriteFile(configPath, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := configstore.ContentDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	plane := &permanentlyUnhealthyApplyPlane{status: dataplane.Status{
		State: "degraded", AppliedRevision: checkpoint.Revision, AttemptedRevision: checkpoint.Revision,
		AppliedDigest: digest, AttemptedDigest: digest,
		LastError: publicerr.MessageCode("runtime.xray_cleanup_failed"),
		Rendr:     healthyRendrStatusForDaemonTest(),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := start(ctx, Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"}, func(
		context.Context, string, *statestore.Store, configstore.Config,
	) (runtimePlane, int64, *controlapi.StartupRollbackStatus, error) {
		return plane, checkpoint.Revision, &controlapi.StartupRollbackStatus{
			ConfiguredRevision: checkpoint.Revision,
			AppliedRevision:    checkpoint.Revision,
			ErrorCode:          "dataplane.startup_apply_failed",
		}, nil
	})
	if err != nil {
		t.Fatalf("combined startup recovery removed the repair control plane: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	status, err := daemonStatus(d)
	if err != nil {
		t.Fatalf("combined startup rollback made authenticated status unavailable: %v", err)
	}
	if status.State != controlapi.DaemonStateDegraded || status.Configuration.StartupRollback == nil ||
		status.Configuration.StartupRollback.ErrorCode != "config.startup_content_invalid" ||
		status.Reconcile.LastErrorCode != configContentInvalidError {
		t.Fatalf("combined startup rollback status=%+v", status)
	}
}

func TestAuthenticatedStatusSurvivesPermanentUnhealthyApplyAfterRestore(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := configstore.DefaultConfig()
	checkpoint.Revision = 5
	checkpoint.Node.DisplayName = "checkpoint"
	if err := configstore.Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	saveStoreLastKnownGood(t, configPath, checkpoint)
	if err := os.WriteFile(configPath, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := configstore.ContentDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	plane := &permanentlyUnhealthyApplyPlane{
		status: dataplane.Status{
			State: "running", AppliedRevision: checkpoint.Revision, AttemptedRevision: checkpoint.Revision,
			AppliedDigest: digest, AttemptedDigest: digest, ObservationFresh: true,
			Rendr: healthyRendrStatusForDaemonTest(),
		},
		applied: make(chan int64, 4),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := start(ctx, Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"}, func(
		context.Context, string, *statestore.Store, configstore.Config,
	) (runtimePlane, int64, *controlapi.StartupRollbackStatus, error) {
		return plane, checkpoint.Revision, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	requestID, err := controlapi.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	response, err := daemonExecute(d, controlapi.Request{
		Args:      []string{"local", "config", "restore-last-good"},
		JSON:      true,
		Revision:  checkpoint.Revision,
		RequestID: requestID,
	})
	if err != nil || response.ExitCode != 0 {
		t.Fatalf("restore failed: response=%+v err=%v", response, err)
	}
	select {
	case revision := <-plane.applied:
		if revision != checkpoint.Revision+1 {
			t.Fatalf("unhealthy apply revision=%d", revision)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restored configuration was not applied")
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	reads := 0
	for time.Now().Before(deadline) {
		status, statusErr := daemonStatus(d)
		if statusErr != nil {
			t.Fatalf("authenticated status failed while unhealthy apply remained committed: %v", statusErr)
		}
		if status.Revision != checkpoint.Revision+1 || status.Reconcile.AppliedRevision != checkpoint.Revision+1 ||
			!status.Reconcile.ConfigurationPublished || status.Configuration.StartupRollback == nil ||
			status.Configuration.LastKnownGoodRevision != checkpoint.Revision {
			t.Fatalf("permanently unhealthy restore status=%+v", status)
		}
		reads++
		time.Sleep(25 * time.Millisecond)
	}
	if reads < 10 {
		t.Fatalf("status continuity read count=%d", reads)
	}
}

func TestDaemonRestartRejectsSameRevisionContentDriftWithoutReplacingLastKnownGood(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	cfg.Revision = 4
	cfg.Node.DisplayName = "checkpoint-name"
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	initial, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	checkpointBefore, err := loadStoreLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}

	drifted := cfg
	drifted.Node.DisplayName = "drifted-without-revision"
	if err := configstore.Save(configPath, drifted); err != nil {
		t.Fatal(err)
	}
	recovered, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start from last-known-good after same-revision drift: %v", err)
	}
	defer recovered.Close()
	status, err := recovered.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != controlapi.DaemonStateDegraded || status.Revision != drifted.Revision ||
		status.Reconcile.State != controlapi.ReconcileStateFailed ||
		status.Reconcile.AppliedRevision != checkpointBefore.Revision ||
		status.Reconcile.AttemptedRevision != drifted.Revision ||
		!strings.Contains(status.Reconcile.LastError, "dataplane.revision_content_mismatch") ||
		status.Configuration.StartupRollback == nil ||
		status.Configuration.StartupRollback.ErrorCode != "dataplane.revision_content_mismatch" {
		t.Fatalf("same-revision restart drift was not observable: %+v", status)
	}
	checkpointAfter, err := loadStoreLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest, err := configstore.ContentDigest(checkpointBefore)
	if err != nil {
		t.Fatal(err)
	}
	afterDigest, err := configstore.ContentDigest(checkpointAfter)
	if err != nil {
		t.Fatal(err)
	}
	if beforeDigest != afterDigest || checkpointAfter.Node.DisplayName != "checkpoint-name" {
		t.Fatalf("same-revision drift replaced checkpoint: before=%+v after=%+v", checkpointBefore, checkpointAfter)
	}
}

func TestDaemonRestartKeepsNewerLastKnownGoodWhenConfigIsOlder(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := configstore.DefaultConfig()
	checkpoint.Revision = 4
	checkpoint.Node.DisplayName = "newer checkpoint"
	if err := configstore.Save(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	initial, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	downgraded := checkpoint
	downgraded.Revision--
	downgraded.Node.DisplayName = "restored older backup"
	if err := configstore.Save(configPath, downgraded); err != nil {
		t.Fatal(err)
	}
	recovered, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("start with newer last-known-good: %v", err)
	}
	defer recovered.Close()
	status, err := recovered.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Addr() == "" || status.State != controlapi.DaemonStateDegraded ||
		status.Revision != downgraded.Revision || status.Reconcile.State != controlapi.ReconcileStateFailed ||
		status.Reconcile.AppliedRevision != checkpoint.Revision ||
		status.Reconcile.AttemptedRevision != downgraded.Revision ||
		!strings.Contains(status.Reconcile.LastError, "dataplane.revision_regression") ||
		status.Configuration.LastKnownGoodRevision != checkpoint.Revision ||
		status.Configuration.StartupRollback == nil ||
		status.Configuration.StartupRollback.ErrorCode != "dataplane.revision_regression" {
		t.Fatalf("newer checkpoint was not kept as a diagnosable degraded runtime: %+v", status)
	}
	stored, err := loadStoreLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != checkpoint.Revision || stored.Node.DisplayName != checkpoint.Node.DisplayName {
		t.Fatalf("downgraded config replaced newer checkpoint: %+v", stored)
	}
}

func TestSaveLastKnownGoodMonotonicRejectsRollbackAndContentDrift(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := configstore.DefaultConfig()
	checkpoint.Revision = 4
	checkpoint.Node.DisplayName = "checkpoint"
	if err := configstore.SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}

	older := checkpoint
	older.Revision--
	if err := saveLastKnownGoodMonotonic(configPath, older); err == nil || !strings.Contains(err.Error(), "lastgood.revision_regression") {
		t.Fatalf("rollback error=%v", err)
	}
	drifted := checkpoint
	drifted.Node.DisplayName = "drifted"
	if err := saveLastKnownGoodMonotonic(configPath, drifted); err == nil || !strings.Contains(err.Error(), "lastgood.revision_content_mismatch") {
		t.Fatalf("same-revision drift error=%v", err)
	}
	if err := saveLastKnownGoodMonotonic(configPath, checkpoint); err != nil {
		t.Fatalf("idempotent checkpoint save: %v", err)
	}

	newer := checkpoint
	newer.Revision++
	newer.Node.DisplayName = "newer"
	if err := saveLastKnownGoodMonotonic(configPath, newer); err != nil {
		t.Fatalf("advance checkpoint: %v", err)
	}
	stored, err := configstore.LoadLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != newer.Revision || stored.Node.DisplayName != newer.Node.DisplayName {
		t.Fatalf("checkpoint=%+v want=%+v", stored, newer)
	}
}

func TestDaemonReportsLastKnownGoodPersistFailureUntilRetrySucceeds(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	checkpointPath, err := d.stateStore.DiagnosticPath(statestore.LastKnownGood)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(checkpointPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkpointPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := configstore.UpdateStoreCAS(d.stateStore, 0, func(cfg *configstore.Config) error {
		cfg.Node.DisplayName = "checkpoint-write-must-fail"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var failed controlapi.DaemonStatus
	for time.Now().Before(deadline) {
		failed, err = daemonStatus(d)
		if err != nil {
			t.Fatal(err)
		}
		if failed.Reconcile.AppliedRevision == 1 && failed.Configuration.LastKnownGoodError == lastKnownGoodPersistError {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if failed.State != controlapi.DaemonStateDegraded || failed.Configuration.LastKnownGoodRevision != 0 ||
		failed.Configuration.LastKnownGoodError != lastKnownGoodPersistError {
		t.Fatalf("persist failure was not observable: %+v", failed)
	}

	if err := os.Remove(checkpointPath); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	var recovered controlapi.DaemonStatus
	for time.Now().Before(deadline) {
		recovered, err = daemonStatus(d)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.State == controlapi.DaemonStateRunning &&
			recovered.Configuration.LastKnownGoodRevision == 1 &&
			recovered.Configuration.LastKnownGoodError == "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if recovered.State != controlapi.DaemonStateRunning || recovered.Configuration.LastKnownGoodRevision != 1 ||
		recovered.Configuration.LastKnownGoodError != "" {
		t.Fatalf("persist retry did not recover: %+v", recovered)
	}
}

func TestDaemonMigratesKnownUnversionedConfigBeforeStarting(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "xtier.json")
	legacySeedPath := filepath.Join(dir, "keystore", "node-seed.v1.json")
	backing, err := identity.Create(legacySeedPath)
	if err != nil {
		t.Fatal(err)
	}
	public := backing.Public()
	legacy, err := json.Marshal(struct {
		Revision int64                  `json:"revision"`
		Node     configstore.NodeConfig `json:"node"`
		Settings any                    `json:"system"`
	}{
		Revision: 4,
		Node: configstore.NodeConfig{
			NodeID: public.NodeID.String(), PublicKey: public.PublicKey, RendrCapable: true,
		},
		Settings: configstore.DefaultConfig().System,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	status, err := daemonStatus(d)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != controlapi.DaemonStateRunning || status.Revision != 5 ||
		status.Reconcile.AppliedRevision != 5 || !status.Configuration.MigratedAtStartup ||
		status.Configuration.SchemaVersion != configstore.CurrentSchemaVersion ||
		status.Configuration.LastKnownGoodRevision != 5 {
		t.Fatalf("migrated daemon status = %+v", status)
	}
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	wantSchema := []byte(fmt.Sprintf(`"schema_version": %d`, configstore.CurrentSchemaVersion))
	if !bytes.Contains(payload, wantSchema) || bytes.Contains(payload, []byte(`"schema_version": 1`)) {
		t.Fatalf("daemon did not version the migrated config: %s", payload)
	}
	migratedBacking, err := identity.LoadStore(d.stateStore)
	if err != nil {
		t.Fatalf("load migrated identity backing: %v", err)
	}
	if migratedBacking.Public() != public {
		t.Fatalf("migrated seed identity=%+v want=%+v", migratedBacking.Public(), public)
	}
	if _, err := os.Stat(legacySeedPath); err != nil {
		t.Fatalf("legacy seed source was removed: %v", err)
	}
}

func TestDaemonRejectsUnknownUnversionedConfigWithoutRewrite(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	legacy := []byte(`{"revision":4,"node":{},"system":{},"future_extension":true}`)
	if err := configstore.Save(configPath, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Options{ConfigPath: configPath, ControlAddr: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("daemon start error = %v, want unknown field rejection", err)
	}
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, legacy) {
		t.Fatalf("rejected config changed: before=%s after=%s", legacy, payload)
	}
	backups, err := filepath.Glob(configPath + ".bak.*")
	if err != nil || len(backups) != 0 {
		t.Fatalf("rejected config backups=%v err=%v", backups, err)
	}
}

func TestPersistLastKnownGoodNeverRegressesRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	newer := configstore.DefaultConfig()
	newer.Revision = 2
	if err := configstore.SaveLastKnownGood(configPath, newer); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		runtimeConfigPath: configPath,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion:         configstore.CurrentSchemaVersion,
			LastKnownGoodRevision: newer.Revision,
			LastKnownGoodError:    lastKnownGoodPersistError,
		},
	}
	older := configstore.DefaultConfig()
	older.Revision = 1
	if err := d.persistLastKnownGood(older); !errors.Is(err, errLastKnownGoodRevisionAhead) {
		t.Fatalf("checkpoint-ahead error=%v", err)
	}
	got, err := configstore.LoadLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != newer.Revision || d.configurationStatus().LastKnownGoodRevision != newer.Revision ||
		d.configurationStatus().LastKnownGoodError != lastKnownGoodRevisionAheadError {
		t.Fatalf("checkpoint regressed: file=%d status=%+v", got.Revision, d.configurationStatus())
	}
}

func TestReloadReportsCheckpointAheadOfAppliedRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	applied := configstore.DefaultConfig()
	applied.Revision = 1
	if err := configstore.Save(configPath, applied); err != nil {
		t.Fatal(err)
	}
	checkpoint := applied
	checkpoint.Revision = 2
	if err := configstore.SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	digest, err := configstore.ContentDigest(applied)
	if err != nil {
		t.Fatal(err)
	}
	plane := &statusCountingPlane{status: dataplane.Status{
		State: "running", AppliedRevision: applied.Revision, AttemptedRevision: applied.Revision,
		AppliedDigest: digest, AttemptedDigest: digest, Rendr: rendradapter.RuntimeStatus{State: "running"},
	}}
	d := &Daemon{
		ctx: context.Background(), runtimeConfigPath: configPath, plane: plane,
		state: controlapi.DaemonStateDegraded,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: checkpoint.Revision,
		},
	}
	_, err = d.Reload(context.Background(), applied.Revision, false)
	if !errors.Is(err, errLastKnownGoodRevisionAhead) ||
		publicerr.Message(err, "fallback") != "operation failed (lastgood.revision_ahead_of_applied)" {
		t.Fatalf("reload checkpoint-ahead error=%v", err)
	}
	if d.configurationStatus().LastKnownGoodError != lastKnownGoodRevisionAheadError {
		t.Fatalf("checkpoint status=%+v", d.configurationStatus())
	}
}

func TestAppliedConfigurationRequiresRunningNonFailStoppedRuntime(t *testing.T) {
	digest := [32]byte{1}
	base := dataplane.Status{
		State: "running", AppliedRevision: 3, AppliedDigest: digest,
		Rendr: rendradapter.RuntimeStatus{State: "running"},
	}
	if !appliedConfigurationMatches(base, 3, digest) {
		t.Fatal("healthy applied status did not match")
	}
	if !appliedRuntimeHealthy(base, 3, digest) {
		t.Fatal("healthy applied runtime was not healthy")
	}
	applying := base
	applying.State = "applying"
	if appliedConfigurationMatches(applying, 3, digest) {
		t.Fatal("applying status was reported as applied")
	}
	failStopped := base
	failStopped.FailStopped = true
	if appliedConfigurationMatches(failStopped, 3, digest) {
		t.Fatal("fail-stopped status was reported as applied")
	}
	if !publishedConfigurationMatches(failStopped, 3, digest) {
		t.Fatal("published configuration was hidden by fail-stop health")
	}
	rendrFailed := base
	rendrFailed.Rendr.State = "failed"
	if appliedRuntimeHealthy(rendrFailed, 3, digest) {
		t.Fatal("failed Rendr runtime was reported as healthy and applied")
	}
	d := &Daemon{}
	if got := d.reconcileStatusFrom(3, digest, rendrFailed); got.State == controlapi.ReconcileStateApplied || !got.ConfigurationPublished {
		t.Fatalf("failed Rendr runtime produced applied reconcile state: %+v", got)
	}
}

func TestReloadDoesNotCheckpointSuccessfulButUnhealthyApply(t *testing.T) {
	for _, testCase := range []struct {
		name                   string
		errorCode              string
		configurationPublished bool
		status                 func(configstore.Config, [32]byte) dataplane.Status
	}{
		{
			name:                   "fail-stopped target",
			errorCode:              "service.reload_applied_unhealthy",
			configurationPublished: true,
			status: func(cfg configstore.Config, digest [32]byte) dataplane.Status {
				return dataplane.Status{
					State: "running", AppliedRevision: cfg.Revision, AttemptedRevision: cfg.Revision,
					AppliedDigest: digest, AttemptedDigest: digest, FailStopped: true,
					Rendr: rendradapter.RuntimeStatus{State: "running"},
				}
			},
		},
		{
			name:      "healthy stale revision",
			errorCode: "service.reload_not_applied",
			status: func(cfg configstore.Config, _ [32]byte) dataplane.Status {
				return dataplane.Status{
					State: "running", AppliedRevision: cfg.Revision - 1, AttemptedRevision: cfg.Revision - 1,
					Rendr: rendradapter.RuntimeStatus{State: "running"},
				}
			},
		},
		{
			name:                   "failed rendr runtime",
			errorCode:              "service.reload_applied_unhealthy",
			configurationPublished: true,
			status: func(cfg configstore.Config, digest [32]byte) dataplane.Status {
				return dataplane.Status{
					State: "running", AppliedRevision: cfg.Revision, AttemptedRevision: cfg.Revision,
					AppliedDigest: digest, AttemptedDigest: digest,
					Rendr: rendradapter.RuntimeStatus{State: "failed"},
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "xtier.json")
			checkpoint := configstore.DefaultConfig()
			candidate := checkpoint
			candidate.Revision++
			candidate.Node.DisplayName = testCase.name
			if err := configstore.Save(configPath, candidate); err != nil {
				t.Fatal(err)
			}
			if err := configstore.SaveLastKnownGood(configPath, checkpoint); err != nil {
				t.Fatal(err)
			}
			digest, err := configstore.ContentDigest(candidate)
			if err != nil {
				t.Fatal(err)
			}
			plane := &statusCountingPlane{status: testCase.status(candidate, digest)}
			d := &Daemon{
				ctx: context.Background(), runtimeConfigPath: configPath, plane: plane,
				state: controlapi.DaemonStateDegraded,
				configuration: controlapi.ConfigurationStatus{
					SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: checkpoint.Revision,
				},
			}

			status, err := d.Reload(context.Background(), candidate.Revision, false)
			if publicerr.Message(err, "fallback") != "operation failed ("+testCase.errorCode+")" {
				t.Fatalf("reload error=%v", err)
			}
			if status.ConfigurationPublished != testCase.configurationPublished {
				t.Fatalf("configuration_published=%v, want %v; status=%+v", status.ConfigurationPublished, testCase.configurationPublished, status)
			}
			stored, loadErr := configstore.LoadLastKnownGood(configPath)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if stored.Revision != checkpoint.Revision || d.configurationStatus().LastKnownGoodRevision != checkpoint.Revision {
				t.Fatalf("unhealthy apply advanced checkpoint: file=%d status=%+v", stored.Revision, d.configurationStatus())
			}
		})
	}
}

func TestCheckpointAheadDoesNotSchedulePersistenceRetry(t *testing.T) {
	checkpoint := controlapi.ConfigurationStatus{
		LastKnownGoodRevision: 9,
		LastKnownGoodError:    lastKnownGoodRevisionAheadError,
	}
	if shouldPersistLastKnownGood(checkpoint, 8) {
		t.Fatal("checkpoint ahead of the applied revision scheduled a hot retry")
	}
	checkpoint.LastKnownGoodRevision = 8
	if !shouldPersistLastKnownGood(checkpoint, 8) {
		t.Fatal("same-revision checkpoint error did not schedule repair")
	}
}

func TestPersistLastKnownGoodRejectsDifferentContentAtSameRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	checkpoint := configstore.DefaultConfig()
	checkpoint.Revision = 2
	checkpoint.Node.DisplayName = "checkpoint"
	if err := configstore.SaveLastKnownGood(configPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		runtimeConfigPath: configPath,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: checkpoint.Revision,
		},
	}
	candidate := checkpoint
	candidate.Node.DisplayName = "drifted"
	if err := d.persistLastKnownGood(candidate); err == nil || !strings.Contains(err.Error(), "lastgood.revision_content_mismatch") {
		t.Fatalf("same-revision checkpoint error=%v", err)
	}
	got, err := configstore.LoadLastKnownGood(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Node.DisplayName != checkpoint.Node.DisplayName || d.configurationStatus().LastKnownGoodError != lastKnownGoodPersistError {
		t.Fatalf("same-revision drift changed checkpoint or health: config=%+v status=%+v", got, d.configurationStatus())
	}
}

type permanentlyUnhealthyApplyPlane struct {
	mu      sync.Mutex
	status  dataplane.Status
	applied chan int64
}

func (*permanentlyUnhealthyApplyPlane) Close() error { return nil }

func (p *permanentlyUnhealthyApplyPlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *permanentlyUnhealthyApplyPlane) Apply(_ context.Context, cfg configstore.Config) error {
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.status.State = "running"
	p.status.AppliedRevision = cfg.Revision
	p.status.AttemptedRevision = cfg.Revision
	p.status.AppliedDigest = digest
	p.status.AttemptedDigest = digest
	p.status.LastError = publicerr.MessageCode("runtime.xray_cleanup_failed")
	p.status.FailStopped = true
	p.status.EgressAuthorizationRevision = -1
	p.status.EgressAuthorizationDigest = sha256.Sum256([]byte("xtier:egress-authorization:fail-stop:v1"))
	p.status.EgressAuthorizationSources = 0
	p.status.ObservationFresh = true
	p.status.ObservedAt = time.Now().UTC()
	p.status.Rendr = healthyRendrStatusForDaemonTest()
	applied := p.applied
	p.mu.Unlock()
	if applied != nil {
		select {
		case applied <- cfg.Revision:
		default:
		}
	}
	return errors.New("injected persistent post-commit runtime failure")
}

func healthyRendrStatusForDaemonTest() rendradapter.RuntimeStatus {
	return rendradapter.RuntimeStatus{
		State: "running", StreamFactory: "xray-stream", StreamCarrier: "unknown", MobilityMode: "redial_attach",
	}
}

type rejectingRollbackPlane struct {
	mu       sync.Mutex
	status   dataplane.Status
	attempts int
	first    chan struct{}
}

func (*rejectingRollbackPlane) Close() error { return nil }

func (p *rejectingRollbackPlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *rejectingRollbackPlane) Apply(_ context.Context, cfg configstore.Config) error {
	p.mu.Lock()
	p.attempts++
	p.status.State = "degraded"
	p.status.AttemptedRevision = cfg.Revision
	p.status.LastError = "dataplane.revision_regression"
	first := p.attempts == 1
	p.mu.Unlock()
	if first {
		close(p.first)
	}
	return errors.New("dataplane.revision_regression")
}

func (p *rejectingRollbackPlane) attemptCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts
}

func TestReconcileBacksOffRejectedRevisionRollback(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	cfg.Revision = 1
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	plane := &rejectingRollbackPlane{
		status: dataplane.Status{
			State:             "running",
			AppliedRevision:   2,
			AttemptedRevision: 2,
		},
		first: make(chan struct{}),
	}
	d := &Daemon{
		ctx:               ctx,
		cancel:            cancel,
		runtimeConfigPath: configPath,
		plane:             plane,
		reconcileDone:     make(chan struct{}),
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion:         configstore.CurrentSchemaVersion,
			LastKnownGoodRevision: 2,
		},
	}
	go d.reconcileLoop(2)
	select {
	case <-plane.first:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not reject the rollback")
	}
	time.Sleep(350 * time.Millisecond)
	if attempts := plane.attemptCount(); attempts != 1 {
		t.Fatalf("rollback attempts during backoff = %d, want 1", attempts)
	}
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := d.reconcileStatusFrom(1, digest, plane.Status())
	if status.ConsecutiveFailures != 1 || status.FirstFailureAt == nil || status.NextRetryAt == nil ||
		!status.NextRetryAt.After(*status.FirstFailureAt) {
		t.Fatalf("rollback failure was not observable: %+v", status)
	}
	cancel()
	<-d.reconcileDone
}

func TestReconcileBackoffGrowsAndCaps(t *testing.T) {
	previous := time.Duration(0)
	for failures := 1; failures <= 12; failures++ {
		delay := reconcileBackoff(failures, 17)
		if delay < previous {
			t.Fatalf("backoff decreased at failure %d: previous=%s current=%s", failures, previous, delay)
		}
		if delay > maxReconcileBackoff {
			t.Fatalf("backoff exceeded cap at failure %d: %s", failures, delay)
		}
		previous = delay
	}
	if first := reconcileBackoff(1, 17); first < time.Second || first > 1200*time.Millisecond {
		t.Fatalf("first backoff=%s, want 1s plus at most 20%% jitter", first)
	}
	if got := reconcileBackoff(12, 17); got != maxReconcileBackoff {
		t.Fatalf("capped backoff=%s, want %s", got, maxReconcileBackoff)
	}
}

type statusCountingPlane struct {
	mu     sync.Mutex
	status dataplane.Status
	calls  int
}

type failStopTrackingPlane struct {
	mu       sync.Mutex
	called   chan string
	calls    int
	failures int
	status   dataplane.Status
}

func (*failStopTrackingPlane) Close() error { return nil }

func (p *failStopTrackingPlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status.State != "" {
		return p.status
	}
	return dataplane.Status{State: "running", AppliedRevision: 0, AttemptedRevision: 0}
}

func (*failStopTrackingPlane) Apply(context.Context, configstore.Config) error { return nil }

func (p *failStopTrackingPlane) FailStop(_ context.Context, code string) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	failures := p.failures
	p.mu.Unlock()
	select {
	case p.called <- code:
	default:
	}
	if call <= failures {
		return errors.New("injected incomplete fail-stop")
	}
	return nil
}

func (p *failStopTrackingPlane) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (*statusCountingPlane) Close() error { return nil }

func (p *statusCountingPlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.status
}

func (*statusCountingPlane) Apply(context.Context, configstore.Config) error { return nil }

func (p *statusCountingPlane) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestDaemonStatusUsesExactlyOnePlaneSnapshot(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plane := &statusCountingPlane{status: dataplane.Status{
		State: "running", AppliedRevision: cfg.Revision, AttemptedRevision: cfg.Revision,
		AppliedDigest: digest, AttemptedDigest: digest, ObservedAt: time.Now().UTC(),
		Rendr: rendradapter.RuntimeStatus{State: "running"},
	}}
	d := &Daemon{
		configPath: configPath, runtimeConfigPath: configPath, plane: plane,
		state: controlapi.DaemonStateRunning,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: cfg.Revision,
		},
	}
	status, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plane.callCount() != 1 || status.Reconcile.State != controlapi.ReconcileStateApplied {
		t.Fatalf("status used %d plane snapshots: %+v", plane.callCount(), status.Reconcile)
	}
}

type blockingApplyPlane struct {
	mu      sync.Mutex
	status  dataplane.Status
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type contextIgnoringApplyPlane struct {
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (p *contextIgnoringApplyPlane) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (*contextIgnoringApplyPlane) Status() dataplane.Status {
	return dataplane.Status{
		State: "running", AppliedRevision: -1, AttemptedRevision: -1,
		Rendr: rendradapter.RuntimeStatus{State: "running"},
	}
}

func (p *contextIgnoringApplyPlane) Apply(context.Context, configstore.Config) error {
	select {
	case <-p.entered:
	default:
		close(p.entered)
	}
	<-p.release
	return nil
}

func (*blockingApplyPlane) Close() error { return nil }

func (p *blockingApplyPlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *blockingApplyPlane) Apply(ctx context.Context, cfg configstore.Config) error {
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.status.State = "applying"
	p.status.AttemptedRevision = cfg.Revision
	p.status.AttemptedDigest = digest
	p.status.LastError = ""
	p.status.ObservedAt = time.Now().UTC()
	p.mu.Unlock()
	p.once.Do(func() { close(p.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
	}
	p.mu.Lock()
	p.status.State = "running"
	p.status.AppliedRevision = cfg.Revision
	p.status.AttemptedRevision = cfg.Revision
	p.status.AppliedDigest = digest
	p.status.AttemptedDigest = digest
	p.status.Rendr.State = "running"
	p.status.ObservedAt = time.Now().UTC()
	p.mu.Unlock()
	return nil
}

func TestDaemonStatusReturnsWhileApplyIsBlocked(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	plane := &blockingApplyPlane{
		status:  dataplane.Status{State: "running", AppliedRevision: -1, AttemptedRevision: -1, ObservedAt: time.Now().UTC()},
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	d := &Daemon{
		configPath: configPath, runtimeConfigPath: configPath, plane: plane,
		state: controlapi.DaemonStateDegraded,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: -1,
		},
	}
	reloadDone := make(chan error, 1)
	go func() {
		_, reloadErr := d.Reload(context.Background(), cfg.Revision, false)
		reloadDone <- reloadErr
	}()
	select {
	case <-plane.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not enter the blocked apply")
	}

	statusCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	status, err := d.Status(statusCtx)
	if err != nil {
		t.Fatalf("status blocked behind apply: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 200*time.Millisecond {
		t.Fatalf("status took %s while apply was blocked", elapsed)
	}
	if status.Reconcile.State != controlapi.ReconcileStatePending || status.Reconcile.AttemptedRevision != cfg.Revision {
		t.Fatalf("blocked apply status was not observable: %+v", status.Reconcile)
	}
	if status.Reconcile.ObservationFresh {
		t.Fatalf("blocked apply reported a fresh runtime observation: %+v", status.Reconcile)
	}
	close(plane.release)
	if err := <-reloadDone; err != nil {
		t.Fatalf("reload after release: %v", err)
	}
}

func TestReloadApplyIsCanceledWithDaemonLifetime(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	daemonCtx, cancelDaemon := context.WithCancel(context.Background())
	plane := &blockingApplyPlane{
		status:  dataplane.Status{State: "running", AppliedRevision: -1, AttemptedRevision: -1},
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	d := &Daemon{
		ctx: daemonCtx, cancel: cancelDaemon, runtimeConfigPath: configPath, plane: plane,
		state: controlapi.DaemonStateDegraded,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion: configstore.CurrentSchemaVersion, LastKnownGoodRevision: -1,
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.Reload(context.Background(), cfg.Revision, false)
		done <- err
	}()
	select {
	case <-plane.entered:
	case <-time.After(time.Second):
		t.Fatal("reload did not enter Apply")
	}
	cancelDaemon()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reload error=%v, want daemon cancellation", err)
		}
	case <-time.After(time.Second):
		close(plane.release)
		t.Fatal("reload ignored daemon lifetime cancellation")
	}
}

type driftRuntimePlane struct {
	mu      sync.Mutex
	status  dataplane.Status
	applied chan int64
}

func (p *driftRuntimePlane) Close() error { return nil }

func (p *driftRuntimePlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *driftRuntimePlane) Apply(_ context.Context, cfg configstore.Config) error {
	p.mu.Lock()
	p.status.State = "running"
	p.status.AppliedRevision = cfg.Revision
	p.status.AttemptedRevision = cfg.Revision
	p.status.LastError = ""
	p.mu.Unlock()
	select {
	case p.applied <- cfg.Revision:
	default:
	}
	return nil
}

func TestReconcileRepairsRevisionDriftWithoutRuntimeError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cfg := configstore.DefaultConfig()
	cfg.Revision = 2
	if err := configstore.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	plane := &driftRuntimePlane{
		status: dataplane.Status{
			State:             "running",
			AppliedRevision:   1,
			AttemptedRevision: 1,
		},
		applied: make(chan int64, 1),
	}
	d := &Daemon{
		ctx:               ctx,
		cancel:            cancel,
		runtimeConfigPath: configPath,
		plane:             plane,
		reconcileDone:     make(chan struct{}),
		state:             controlapi.DaemonStateDegraded,
		configuration: controlapi.ConfigurationStatus{
			SchemaVersion:         configstore.CurrentSchemaVersion,
			LastKnownGoodRevision: 1,
		},
	}
	go d.reconcileLoop(cfg.Revision)
	select {
	case revision := <-plane.applied:
		if revision != cfg.Revision {
			t.Fatalf("applied revision=%d want=%d", revision, cfg.Revision)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reconciler did not repair revision drift")
	}
	cancel()
	<-d.reconcileDone
	status := plane.Status()
	if status.AppliedRevision != cfg.Revision {
		t.Fatalf("runtime remained at revision %d", status.AppliedRevision)
	}
}

type retryXrayRuntime struct {
	mu           sync.Mutex
	attempts     int
	failures     int
	firstAttempt chan struct{}
	releaseRetry chan struct{}
}

func (r *retryXrayRuntime) Close() error {
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()
	if attempt == 1 {
		close(r.firstAttempt)
	}
	<-r.releaseRetry
	if attempt <= r.failures {
		return xrayrt.ErrShutdownIncomplete
	}
	return nil
}

func (*retryXrayRuntime) Status() dataplane.Status {
	return dataplane.Status{Xray: xrayrt.Status{Closed: true, Draining: []xrayrt.GenerationStatus{}}}
}

func (*retryXrayRuntime) Apply(context.Context, configstore.Config) error { return nil }

type forceRequiredRuntime struct {
	mu       sync.Mutex
	attempts int
	forced   bool
}

type deadlineIgnoringRuntime struct {
	release      <-chan struct{}
	closeEntered chan struct{}
	forceEntered chan struct{}
	closeOnce    sync.Once
	forceOnce    sync.Once
}

func (r *deadlineIgnoringRuntime) Close() error {
	return r.CloseContext(context.Background())
}

func (r *deadlineIgnoringRuntime) CloseContext(context.Context) error {
	r.closeOnce.Do(func() { close(r.closeEntered) })
	<-r.release
	return nil
}

func (r *deadlineIgnoringRuntime) ForceClose() error {
	r.forceOnce.Do(func() { close(r.forceEntered) })
	<-r.release
	return nil
}

func (*deadlineIgnoringRuntime) Status() dataplane.Status { return dataplane.Status{} }

func (*deadlineIgnoringRuntime) Apply(context.Context, configstore.Config) error { return nil }

func (r *forceRequiredRuntime) Close() error {
	r.mu.Lock()
	r.attempts++
	r.mu.Unlock()
	return xrayrt.ErrShutdownIncomplete
}

func (r *forceRequiredRuntime) ForceClose() error {
	r.mu.Lock()
	r.forced = true
	r.mu.Unlock()
	return nil
}

func (*forceRequiredRuntime) Status() dataplane.Status { return dataplane.Status{} }

func (*forceRequiredRuntime) Apply(context.Context, configstore.Config) error { return nil }

type cleanupRetryPlane struct {
	mu      sync.Mutex
	status  dataplane.Status
	applied chan struct{}
}

type rendrRecoveryPlane struct {
	mu      sync.Mutex
	status  dataplane.Status
	applied chan struct{}
}

func (*rendrRecoveryPlane) Close() error { return nil }

func (p *rendrRecoveryPlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *rendrRecoveryPlane) Apply(_ context.Context, cfg configstore.Config) error {
	p.mu.Lock()
	p.status.State = "running"
	p.status.Rendr.State = "running"
	p.status.LastError = ""
	p.status.ObservedAt = time.Now().UTC()
	p.mu.Unlock()
	select {
	case p.applied <- struct{}{}:
	default:
	}
	return nil
}

func (*cleanupRetryPlane) Close() error { return nil }

func (p *cleanupRetryPlane) Status() dataplane.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

func (p *cleanupRetryPlane) Apply(_ context.Context, cfg configstore.Config) error {
	digest, err := configstore.ContentDigest(cfg)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cfg.Revision != p.status.AppliedRevision || digest != p.status.AppliedDigest {
		return errors.New("cleanup retry unexpectedly changed desired configuration")
	}
	p.status.Xray.Draining = []xrayrt.GenerationStatus{}
	p.status.LastError = ""
	p.status.ObservedAt = time.Now().UTC()
	select {
	case p.applied <- struct{}{}:
	default:
	}
	return nil
}
