package controlserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/cli"
	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

func TestStartOwnedStoreUsesObjectBoundTokenAndConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	canonical, err := configstore.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := statestore.Open(canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server, err := StartOwnedStore(context.Background(), "127.0.0.1:0", store, canonical)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	token, err := controlapi.ReadStoreToken(store, statestore.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	response, err := controlapi.ExecuteToken(server.Addr(), token, controlapi.Request{
		Args: []string{"config", "validate"}, JSON: true,
		RequestID: "01010101010101010101010101010101",
	})
	if err != nil || response.ExitCode != 0 || !strings.Contains(response.Stdout, `"revision": 0`) {
		t.Fatalf("store-backed command response=%+v err=%v", response, err)
	}
	if _, err := os.Stat(controlapi.TokenPath(canonical)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store-backed server created legacy token: %v", err)
	}
}

type failingRuntimeReloader struct{ err error }

func (r failingRuntimeReloader) Reload(context.Context, int64, bool) (controlapi.ReconcileStatus, error) {
	return controlapi.ReconcileStatus{}, r.err
}

type fixedRuntimeReloader struct {
	status controlapi.ReconcileStatus
	err    error
}

func (r fixedRuntimeReloader) Reload(context.Context, int64, bool) (controlapi.ReconcileStatus, error) {
	return r.status, r.err
}

type fixedStatusProvider struct{ status controlapi.DaemonStatus }

func (p fixedStatusProvider) Status(context.Context) (controlapi.DaemonStatus, error) {
	return p.status, nil
}

func TestValidateStatusAcceptsCheckpointAheadSentinel(t *testing.T) {
	status := controlapi.DaemonStatus{
		APIVersion: controlapi.APIVersion,
		State:      controlapi.DaemonStateDegraded,
		Idempotency: controlapi.IdempotencyStatus{
			Scope:       controlapi.IdempotencyScopeProcessMemory,
			Provisional: true,
		},
		Configuration: controlapi.ConfigurationStatus{
			SchemaVersion:         1,
			LastKnownGoodRevision: 2,
			LastKnownGoodError:    controlapi.LastKnownGoodRevisionAheadOfApplied,
		},
		Rendr: controlapi.RuntimeStatus{State: controlapi.RuntimeStateUnavailable},
		Xray:  controlapi.XrayStatus{State: controlapi.RuntimeStateUnavailable},
	}
	if err := validateStatus(status); err != nil {
		t.Fatalf("checkpoint-ahead degraded status was rejected: %v", err)
	}
	status.Configuration.LastKnownGoodError = "lastgood.unrecognized"
	if err := validateStatus(status); err == nil {
		t.Fatal("unrecognized checkpoint error was accepted")
	}
}

func TestValidateStatusAcceptsExplicitStartupRollbackAndFailStoppedXray(t *testing.T) {
	status := controlapi.DaemonStatus{
		APIVersion: controlapi.APIVersion,
		State:      controlapi.DaemonStateDegraded,
		Revision:   2,
		Reconcile: controlapi.ReconcileStatus{
			State: controlapi.ReconcileStateFailed, AppliedRevision: 1, AttemptedRevision: 2,
		},
		Idempotency: controlapi.IdempotencyStatus{
			Scope: controlapi.IdempotencyScopeProcessMemory, Provisional: true,
		},
		Configuration: controlapi.ConfigurationStatus{
			SchemaVersion: 1, LastKnownGoodRevision: 1,
			StartupRollback: &controlapi.StartupRollbackStatus{
				ConfiguredRevision: 2, AppliedRevision: 1, ErrorCode: "dataplane.startup_apply_failed",
			},
		},
		Rendr: controlapi.RuntimeStatus{State: controlapi.RuntimeStateUnavailable},
		Xray: controlapi.XrayStatus{
			State: controlapi.RuntimeStateFailed, FailStopped: true,
			Current:                     &controlapi.XrayGenerationStatus{Generation: 1},
			StrictStreamOutbound:        true,
			EgressAuthorizationRevision: -1,
			EgressAuthorizationDigest:   strings.Repeat("a", 64),
		},
	}
	if err := validateStatus(status); err != nil {
		t.Fatalf("explicit startup rollback was rejected: %v", err)
	}
	status.Reconcile.AppliedRevision = 2
	if err := validateStatus(status); err != nil {
		t.Fatalf("historical startup rollback was tied to the live applied revision: %v", err)
	}
	status.Configuration.StartupRollback.AppliedRevision = 3
	if err := validateStatus(status); err == nil {
		t.Fatal("startup rollback newer than the live applied revision was accepted")
	}
	status.Configuration.StartupRollback.AppliedRevision = 1
	status.Configuration.StartupRollback.ErrorCode = "invalid"
	if err := validateStatus(status); err == nil {
		t.Fatal("invalid startup rollback code was accepted")
	}
	status.Configuration.StartupRollback.ErrorCode = "dataplane.startup_apply_failed"
	status.Xray.State = controlapi.RuntimeStateRunning
	if err := validateStatus(status); err == nil {
		t.Fatal("fail-stopped Xray was accepted as running")
	}
	status.Xray.FailStopped = false
	if err := validateStatus(status); err == nil {
		t.Fatal("running Xray with fail-stop authorization revision was accepted")
	}
	status.Xray.EgressAuthorizationRevision = status.Revision
	if err := validateStatus(status); err != nil {
		t.Fatalf("running Xray with current authorization was rejected: %v", err)
	}
}

func TestValidateStatusAcceptsUnknownConfiguredRevisionForInvalidStartupContent(t *testing.T) {
	status := controlapi.DaemonStatus{
		APIVersion: controlapi.APIVersion,
		State:      controlapi.DaemonStateDegraded,
		Revision:   5,
		Reconcile: controlapi.ReconcileStatus{
			State: controlapi.ReconcileStateFailed, AppliedRevision: 5, AttemptedRevision: 5,
		},
		Idempotency: controlapi.IdempotencyStatus{
			Scope: controlapi.IdempotencyScopeProcessMemory, Provisional: true,
		},
		Configuration: controlapi.ConfigurationStatus{
			SchemaVersion: 1, LastKnownGoodRevision: 5,
			StartupRollback: &controlapi.StartupRollbackStatus{
				ConfiguredRevision: -1, AppliedRevision: 5, ErrorCode: "config.startup_content_invalid",
			},
		},
		Rendr: controlapi.RuntimeStatus{State: controlapi.RuntimeStateUnavailable},
		Xray:  controlapi.XrayStatus{State: controlapi.RuntimeStateUnavailable},
	}
	if err := validateStatus(status); err != nil {
		t.Fatalf("content-invalid startup rollback was rejected: %v", err)
	}
	status.Configuration.StartupRollback.ConfiguredRevision = -2
	if err := validateStatus(status); err == nil {
		t.Fatal("arbitrary negative configured revision was accepted")
	}
	status.Configuration.StartupRollback.ConfiguredRevision = -1
	status.Revision++
	if err := validateStatus(status); err != nil {
		t.Fatalf("stale content-invalid rollback made a newer readable revision unreportable: %v", err)
	}
}

func TestAuthenticatedStatusKeepsStaleRollbackObservableWhenRepairCannotApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	status := controlapi.DaemonStatus{
		APIVersion: controlapi.APIVersion,
		State:      controlapi.DaemonStateDegraded,
		Revision:   6,
		Reconcile: controlapi.ReconcileStatus{
			State: controlapi.ReconcileStateFailed, AppliedRevision: 6, AttemptedRevision: 6,
			LastError: publicerr.MessageCode("runtime.listener_unavailable"), LastErrorCode: "runtime.listener_unavailable",
		},
		Configuration: controlapi.ConfigurationStatus{
			SchemaVersion: 1, LastKnownGoodRevision: 5,
			StartupRollback: &controlapi.StartupRollbackStatus{
				ConfiguredRevision: -1, AppliedRevision: 5, ErrorCode: "config.startup_content_invalid",
			},
		},
		Rendr: controlapi.RuntimeStatus{State: controlapi.RuntimeStateUnavailable},
		Xray:  controlapi.XrayStatus{State: controlapi.RuntimeStateUnavailable},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := Start(ctx, "127.0.0.1:0", path, fixedStatusProvider{status: status})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	observed, err := controlapi.GetStatus(server.Addr(), controlapi.TokenPath(path))
	if err != nil {
		t.Fatalf("stale rollback made authenticated status unavailable: %v", err)
	}
	if observed.Revision != 6 || observed.Reconcile.AppliedRevision != 6 ||
		observed.Configuration.StartupRollback == nil {
		t.Fatalf("authenticated stale rollback status=%+v", observed)
	}
}

func TestAuthenticatedStatusKeepsStartupRollbackObservableDuringShutdown(t *testing.T) {
	for _, state := range []controlapi.DaemonState{
		controlapi.DaemonStateStopping,
		controlapi.DaemonStateStopped,
	} {
		t.Run(string(state), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
				t.Fatal(err)
			}
			runtimeState := controlapi.RuntimeStateStopping
			if state == controlapi.DaemonStateStopped {
				runtimeState = controlapi.RuntimeStateStopped
			}
			status := controlapi.DaemonStatus{
				APIVersion: controlapi.APIVersion,
				State:      state,
				Revision:   2,
				Reconcile: controlapi.ReconcileStatus{
					State: controlapi.ReconcileStateFailed, AppliedRevision: 1, AttemptedRevision: 2,
				},
				Configuration: controlapi.ConfigurationStatus{
					SchemaVersion: 1, LastKnownGoodRevision: 1,
					StartupRollback: &controlapi.StartupRollbackStatus{
						ConfiguredRevision: 2, AppliedRevision: 1, ErrorCode: "dataplane.startup_apply_failed",
					},
				},
				Rendr: controlapi.RuntimeStatus{State: controlapi.RuntimeStateUnavailable},
				Xray:  controlapi.XrayStatus{State: runtimeState},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server, err := Start(ctx, "127.0.0.1:0", path, fixedStatusProvider{status: status})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()

			observed, err := controlapi.GetStatus(server.Addr(), controlapi.TokenPath(path))
			if err != nil {
				t.Fatalf("authenticated status during %s: %v", state, err)
			}
			if observed.State != state || observed.Configuration.StartupRollback == nil {
				t.Fatalf("shutdown rollback status=%+v", observed)
			}
		})
	}
}

func TestReloadResponseDoesNotExposeRawRuntimeDetails(t *testing.T) {
	const secret = "c0dec0de-f00d-4bee-8bad-0123456789ab"
	server := &Server{ctx: context.Background()}
	response := server.executeReload(
		failingRuntimeReloader{err: publicerr.Errorf("service.reload_apply", "upstream rejected %s", secret)},
		controlapi.Request{JSON: true, Revision: 7},
	)
	if response.ExitCode == 0 || strings.Contains(response.Stdout+response.Stderr, secret) ||
		!strings.Contains(response.Stdout, `"error_code":"service.reload_apply"`) {
		t.Fatalf("unsafe reload response = %+v", response)
	}
}

func TestCLIExecuteReloadRequiresExactAppliedConfirmation(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status controlapi.ReconcileStatus
		err    error
	}{
		{
			name:   "pending nil result",
			status: controlapi.ReconcileStatus{State: controlapi.ReconcileStatePending, AppliedRevision: 7, AttemptedRevision: 7},
		},
		{
			name:   "stale attempted revision",
			status: controlapi.ReconcileStatus{State: controlapi.ReconcileStateApplied, AppliedRevision: 7, AttemptedRevision: 6},
		},
		{
			name: "stale applied state accompanying error",
			status: controlapi.ReconcileStatus{
				State: controlapi.ReconcileStateApplied, AppliedRevision: 7, AttemptedRevision: 6,
			},
			err: publicerr.Errorf("service.reload_apply", "apply failed"),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := &Server{ctx: context.Background()}
			response := server.executeReload(
				fixedRuntimeReloader{status: testCase.status, err: testCase.err},
				controlapi.Request{JSON: true, Revision: 7},
			)
			if response.ExitCode != controlapi.IndeterminateExitCode || response.Applied ||
				response.Outcome != controlapi.MutationOutcomeIndeterminate {
				t.Fatalf("invalid reload confirmation was accepted: %+v", response)
			}
		})
	}
}

func TestCLIExecuteReloadReportsPublishedConfigurationDespiteCleanupFailure(t *testing.T) {
	server := &Server{ctx: context.Background()}
	response := server.executeReload(
		fixedRuntimeReloader{
			status: controlapi.ReconcileStatus{
				State:                  controlapi.ReconcileStateFailed,
				AppliedRevision:        7,
				AttemptedRevision:      7,
				ConfigurationPublished: true,
			},
			err: publicerr.Errorf("service.reload_apply", "old generation cleanup remains pending"),
		},
		controlapi.Request{JSON: true, Revision: 7},
	)
	if response.ExitCode != 0 || !response.Applied || response.Outcome != controlapi.MutationOutcomeApplied ||
		!strings.Contains(response.Stdout, `"error_code":"service.reload_apply"`) {
		t.Fatalf("published cleanup failure outcome=%+v", response)
	}
}

func TestServerExecutesCLIInsideDaemon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{NodeID: "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DisplayName: "A", Role: "thin", RendrCapable: true}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := Start(ctx, "127.0.0.1:0", path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	resp, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), controlapi.Request{
		Args:      []string{"local", "identity", "rename", "daemon-a"},
		JSON:      true,
		Revision:  0,
		RequestID: "00000000000000000000000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", resp.ExitCode, resp.Stdout, resp.Stderr)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(resp.Stdout), &body); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, resp.Stdout)
	}
	if body["ok"] != true {
		t.Fatalf("unexpected stdout: %s", resp.Stdout)
	}
	after, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Node.DisplayName != "daemon-a" || after.Revision != 1 {
		t.Fatalf("daemon did not own write: name=%s rev=%d", after.Node.DisplayName, after.Revision)
	}
}

func TestStartRejectsNonLoopbackControlAddress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Start(ctx, "0.0.0.0:0", filepath.Join(t.TempDir(), "config.json"))
	if err == nil {
		t.Fatal("non-loopback control address was accepted")
	}
}

func TestCommandRequiresTokenAndRejectsRequestIDReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{NodeID: "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DisplayName: "A", RendrCapable: true}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := Start(ctx, "127.0.0.1:0", path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	req := controlapi.Request{Args: []string{"local", "identity", "rename", "first"}, JSON: true, Revision: 0, RequestID: "00000000000000000000000000000002"}
	status := postUnauthenticatedCommand(t, srv.Addr(), req)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}

	tokenPath := controlapi.TokenPath(path)
	status = postCommand(t, srv.Addr(), tokenPath, req)
	if status != http.StatusOK {
		t.Fatalf("first command status=%d", status)
	}
	status = postCommand(t, srv.Addr(), tokenPath, req)
	if status != http.StatusOK {
		t.Fatalf("idempotent replay status=%d", status)
	}
	req.Args[len(req.Args)-1] = "different"
	status = postCommand(t, srv.Addr(), tokenPath, req)
	if status != http.StatusConflict {
		t.Fatalf("request-id conflict status=%d", status)
	}

	after, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Node.DisplayName != "first" || after.Revision != 1 {
		t.Fatalf("request executed more than once: name=%s revision=%d", after.Node.DisplayName, after.Revision)
	}
	if got := srv.commandIngress.Load(); got != 3 {
		t.Fatalf("authenticated command ingress=%d, want 3", got)
	}
	if got := srv.commandExecutions.Load(); got != 1 {
		t.Fatalf("command executions=%d, want 1", got)
	}
}

func TestCommandRejectsTrailingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{NodeID: "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DisplayName: "A", RendrCapable: true}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	body := `{"args":["local","identity","rename","changed"],"json":true,"revision":0,"request_id":"00000000000000000000000000000003"} {}`
	status, _ := postRawCommand(t, srv.Addr(), controlapi.TokenPath(path), body)
	if status != http.StatusBadRequest {
		t.Fatalf("trailing JSON status=%d", status)
	}
	after, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 0 || after.Node.DisplayName != "A" {
		t.Fatalf("trailing JSON executed command: revision=%d name=%q", after.Revision, after.Node.DisplayName)
	}
}

func TestRequestCacheCapacityEvictsOldestCompletedAndNeverReappliesCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{NodeID: "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DisplayName: "A", RendrCapable: true}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	known := controlapi.Request{Args: []string{"local", "identity", "rename", "cached-only"}, JSON: true, Revision: 0, RequestID: "00000000000000000000000000000004"}
	want := controlapi.Response{ExitCode: 17, Stdout: "cached"}
	srv.requestsMu.Lock()
	knownDone := make(chan struct{})
	close(knownDone)
	srv.requests[known.RequestID] = &cachedResponse{request: known, response: want, done: knownDone, completed: true}
	srv.completedRequests = append(srv.completedRequests, known.RequestID)
	for i := 1; i < requestCacheCapacity; i++ {
		id := fmt.Sprintf("%032x", i+4)
		req := controlapi.Request{Args: []string{"local", "status"}, Revision: -1, RequestID: id}
		done := make(chan struct{})
		close(done)
		srv.requests[id] = &cachedResponse{request: req, done: done, completed: true}
		srv.completedRequests = append(srv.completedRequests, id)
	}
	srv.requestsMu.Unlock()

	status, body := postCommandResponse(t, srv.Addr(), controlapi.TokenPath(path), known)
	if status != http.StatusOK {
		t.Fatalf("known replay status=%d body=%s", status, body)
	}
	var replay controlapi.Response
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatal(err)
	}
	if replay != want {
		t.Fatalf("known replay=%+v, want %+v", replay, want)
	}

	newRequest := controlapi.Request{
		Args:      []string{"local", "identity", "rename", "did-run"},
		JSON:      true,
		Revision:  0,
		RequestID: "ffffffffffffffffffffffffffffffff",
	}
	status, body = postCommandResponse(t, srv.Addr(), controlapi.TokenPath(path), newRequest)
	if status != http.StatusOK {
		t.Fatalf("new request status=%d body=%s", status, body)
	}
	after, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 1 || after.Node.DisplayName != "did-run" {
		t.Fatalf("new command did not execute once: revision=%d name=%q", after.Revision, after.Node.DisplayName)
	}
	srv.requestsMu.Lock()
	_, newCached := srv.requests[newRequest.RequestID]
	_, oldCached := srv.requests[known.RequestID]
	gotLen := len(srv.requests)
	srv.requestsMu.Unlock()
	if !newCached || oldCached || gotLen != requestCacheCapacity {
		t.Fatalf("cache eviction: new_cached=%v old_cached=%v len=%d", newCached, oldCached, gotLen)
	}

	status, body = postCommandResponse(t, srv.Addr(), controlapi.TokenPath(path), known)
	if status != http.StatusOK {
		t.Fatalf("evicted replay status=%d body=%s", status, body)
	}
	var evictedReplay controlapi.Response
	if err := json.Unmarshal(body, &evictedReplay); err != nil {
		t.Fatal(err)
	}
	if evictedReplay.ExitCode == 0 || !strings.Contains(evictedReplay.Stdout+evictedReplay.Stderr, "config.revision_conflict") {
		t.Fatalf("evicted replay bypassed CAS: %+v", evictedReplay)
	}
	after, err = configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 1 || after.Node.DisplayName != "did-run" {
		t.Fatalf("evicted replay mutated config: revision=%d name=%q", after.Revision, after.Node.DisplayName)
	}
}

func TestRequestCacheCapacityDoesNotEvictInflightRequests(t *testing.T) {
	srv := &Server{requests: make(map[string]*cachedResponse)}
	for i := 0; i < requestCacheCapacity; i++ {
		id := fmt.Sprintf("%032x", i)
		srv.requests[id] = &cachedResponse{request: controlapi.Request{RequestID: id}, done: make(chan struct{})}
	}
	request := controlapi.Request{RequestID: "ffffffffffffffffffffffffffffffff"}
	if entry, leader, status := srv.claimRequest(request); entry != nil || leader || status != http.StatusServiceUnavailable {
		t.Fatalf("claim at inflight capacity = (%v, %v, %d)", entry, leader, status)
	}
	if len(srv.requests) != requestCacheCapacity {
		t.Fatalf("inflight cache size = %d", len(srv.requests))
	}
}

func TestIndeterminateCommandCacheNeverEvicts(t *testing.T) {
	srv := &Server{requests: make(map[string]*cachedResponse)}
	firstRequest := controlapi.Request{
		Args:      []string{"local", "identity", "rename", "unknown"},
		Revision:  0,
		RequestID: "01000000000000000000000000000000",
	}
	firstEntry, leader, status := srv.claimRequest(firstRequest)
	if firstEntry == nil || !leader || status != 0 {
		t.Fatalf("initial claim=(%v,%v,%d)", firstEntry, leader, status)
	}
	srv.completeRequest(firstRequest.RequestID, firstEntry, controlapi.Response{
		ExitCode: controlapi.IndeterminateExitCode,
		Outcome:  controlapi.MutationOutcomeIndeterminate,
	})
	for i := 1; i < requestCacheCapacity; i++ {
		request := controlapi.Request{
			Args:      []string{"local", "identity", "rename", fmt.Sprintf("unknown-%d", i)},
			Revision:  0,
			RequestID: fmt.Sprintf("%032x", i+1),
		}
		entry, entryLeader, entryStatus := srv.claimRequest(request)
		if entry == nil || !entryLeader || entryStatus != 0 {
			t.Fatalf("claim %d=(%v,%v,%d)", i, entry, entryLeader, entryStatus)
		}
		srv.completeRequest(request.RequestID, entry, controlapi.Response{
			ExitCode: controlapi.IndeterminateExitCode,
			Outcome:  controlapi.MutationOutcomeIndeterminate,
		})
	}
	newRequest := controlapi.Request{RequestID: "ffffffffffffffffffffffffffffffff"}
	if entry, newLeader, newStatus := srv.claimRequest(newRequest); entry != nil || newLeader || newStatus != http.StatusServiceUnavailable {
		t.Fatalf("claim at protected capacity=(%v,%v,%d)", entry, newLeader, newStatus)
	}
	if cached, replayLeader, replayStatus := srv.claimRequest(firstRequest); cached != firstEntry || replayLeader || replayStatus != 0 {
		t.Fatalf("protected replay=(%v,%v,%d)", cached, replayLeader, replayStatus)
	}
	if len(srv.requests) != requestCacheCapacity {
		t.Fatalf("protected cache size=%d, want %d", len(srv.requests), requestCacheCapacity)
	}
}

func TestCommandCannotOverrideDaemonGlobals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	other := filepath.Join(dir, "other.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{NodeID: "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DisplayName: "A"}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if err := configstore.Save(other, cfg); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	requests := []controlapi.Request{
		{Args: []string{"--config", other, "local", "identity", "rename", "pwned"}, Revision: 0, RequestID: "10000000000000000000000000000000"},
		{Args: []string{"--dry-run=false", "local", "identity", "rename", "pwned"}, DryRun: true, Revision: 0, RequestID: "20000000000000000000000000000000"},
		{Args: []string{"--revision=0", "local", "identity", "rename", "pwned"}, Revision: 0, RequestID: "30000000000000000000000000000000"},
		{Args: []string{"-config", other, "local", "identity", "rename", "pwned"}, Revision: 0, RequestID: "40000000000000000000000000000001"},
		{Args: []string{"-dry-run=false", "local", "identity", "rename", "pwned"}, DryRun: true, Revision: 0, RequestID: "40000000000000000000000000000002"},
	}
	for _, request := range requests {
		if status := postCommand(t, srv.Addr(), controlapi.TokenPath(path), request); status != http.StatusBadRequest {
			t.Fatalf("override request status = %d", status)
		}
	}
	for _, candidate := range []string{path, other} {
		loaded, err := configstore.Load(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 0 || loaded.Node.DisplayName != "A" {
			t.Fatalf("global override changed %s: %+v", candidate, loaded)
		}
	}
}

func TestConcurrentIdenticalMutationExecutesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{NodeID: "node-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DisplayName: "A"}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	if _, err := controlapi.ReadToken(controlapi.TokenPath(path)); err != nil {
		t.Fatal(err)
	}
	request := controlapi.Request{
		Args: []string{"local", "identity", "rename", "once"}, JSON: true, Revision: 0,
		RequestID: "50000000000000000000000000000000",
	}
	const callers = 16
	responses := make(chan controlapi.Response, callers)
	errorsCh := make(chan error, callers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			response, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), request)
			responses <- response
			errorsCh <- err
		}()
	}
	close(start)
	group.Wait()
	close(responses)
	close(errorsCh)
	var first *controlapi.Response
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	for response := range responses {
		if first == nil {
			copy := response
			first = &copy
		} else if response != *first {
			t.Fatalf("replay response changed: first=%+v got=%+v", *first, response)
		}
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.Node.DisplayName != "once" {
		t.Fatalf("mutation executed more than once: %+v", loaded)
	}
}

func TestPanickingMutationCompletesIdempotencyEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	var executions atomic.Int32
	srv.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		executions.Add(1)
		panic("injected command panic")
	}
	request := controlapi.Request{
		Args:      []string{"local", "identity", "rename", "panic"},
		JSON:      true,
		Revision:  0,
		RequestID: "51000000000000000000000000000000",
	}

	first, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExitCode != controlapi.IndeterminateExitCode || first.Applied || first.Outcome != controlapi.MutationOutcomeIndeterminate {
		t.Fatalf("panic response = %+v", first)
	}
	replay, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), request)
	if err != nil {
		t.Fatal(err)
	}
	if replay != first || executions.Load() != 1 {
		t.Fatalf("panic replay=%+v first=%+v executions=%d", replay, first, executions.Load())
	}

	srv.requestsMu.Lock()
	entry := srv.requests[request.RequestID]
	srv.requestsMu.Unlock()
	if entry == nil || !entry.completed || !entry.protected {
		t.Fatalf("panic idempotency entry remained in flight: entry=%+v", entry)
	}
}

func TestIndeterminateCLIExecutionResponseRoundTripsStrictClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	srv.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		return cli.ExecutionResult{
			ExitCode: 1,
			Applied:  false,
			Outcome:  controlapi.MutationOutcomeIndeterminate,
		}
	}

	response, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), controlapi.Request{
		Args:      []string{"local", "identity", "rename", "unknown"},
		JSON:      true,
		Revision:  0,
		RequestID: "51000000000000000000000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != controlapi.IndeterminateExitCode || response.Applied ||
		response.Outcome != controlapi.MutationOutcomeIndeterminate {
		t.Fatalf("strict round-trip response=%+v", response)
	}
}

func TestAppliedCLIExecutionResponseForcesSuccessfulExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	srv.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		return cli.ExecutionResult{
			ExitCode: 1,
			Applied:  true,
			Outcome:  controlapi.MutationOutcomeApplied,
		}
	}

	response, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), controlapi.Request{
		Args:      []string{"local", "identity", "rename", "applied-with-warning"},
		JSON:      true,
		Revision:  0,
		RequestID: "51000000000000000000000000000002",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 0 || !response.Applied || response.Outcome != controlapi.MutationOutcomeApplied {
		t.Fatalf("applied strict response=%+v", response)
	}
}

func TestMutationBudgetReturnsIndeterminateAndRetainsGateUntilExecutionEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	srv.mutationWait = 75 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var executions atomic.Int32
	srv.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		if executions.Add(1) == 1 {
			close(entered)
			<-release
			close(finished)
		}
		return cli.ExecutionResult{Applied: true, Outcome: controlapi.MutationOutcomeApplied}
	}
	firstRequest := controlapi.Request{
		Args:      []string{"local", "identity", "rename", "slow"},
		JSON:      true,
		Revision:  0,
		RequestID: "52000000000000000000000000000000",
	}
	firstDone := make(chan struct {
		response controlapi.Response
		err      error
	}, 1)
	go func() {
		response, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), firstRequest)
		firstDone <- struct {
			response controlapi.Response
			err      error
		}{response: response, err: err}
	}()
	<-entered
	first := <-firstDone
	if first.err != nil || first.response.ExitCode != controlapi.IndeterminateExitCode ||
		first.response.Applied || first.response.Outcome != controlapi.MutationOutcomeIndeterminate {
		t.Fatalf("slow mutation response=%+v err=%v", first.response, first.err)
	}

	replay, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), firstRequest)
	if err != nil || replay != first.response || executions.Load() != 1 {
		t.Fatalf("slow mutation replay=%+v err=%v executions=%d", replay, err, executions.Load())
	}
	blocked, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), controlapi.Request{
		Args:      []string{"local", "identity", "rename", "blocked"},
		JSON:      true,
		Revision:  0,
		RequestID: "52000000000000000000000000000001",
	})
	if err != nil || blocked.ExitCode == 0 || blocked.Applied || blocked.Outcome != controlapi.MutationOutcomeNotApplied {
		t.Fatalf("blocked mutation response=%+v err=%v", blocked, err)
	}
	if executions.Load() != 1 {
		t.Fatalf("blocked mutation entered executor: %d", executions.Load())
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("timed-out mutation did not finish after release")
	}
	after, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), controlapi.Request{
		Args:      []string{"local", "identity", "rename", "after"},
		JSON:      true,
		Revision:  0,
		RequestID: "52000000000000000000000000000002",
	})
	if err != nil || after.ExitCode != 0 || !after.Applied || after.Outcome != controlapi.MutationOutcomeApplied {
		t.Fatalf("post-timeout mutation response=%+v err=%v", after, err)
	}
}

func TestCloseContextBoundsEnteredCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	srv.shutdownWait = 30 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	srv.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		close(entered)
		<-release
		return cli.ExecutionResult{Applied: true, Outcome: controlapi.MutationOutcomeApplied}
	}
	request := controlapi.Request{
		Args: []string{"local", "identity", "rename", "wait"}, Revision: 0,
		RequestID: "60000000000000000000000000000000",
	}
	requestDone := make(chan error, 1)
	go func() {
		_, err := controlapi.Execute(srv.Addr(), controlapi.TokenPath(path), request)
		requestDone <- err
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- srv.Close() }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, ErrShutdownIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close error = %v, want incomplete deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close remained blocked by an active command")
	}
	select {
	case <-srv.Done():
	case <-time.After(time.Second):
		t.Fatal("HTTP serving loop did not stop after forced close")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- srv.Wait() }()
	select {
	case err := <-waitDone:
		t.Fatalf("Wait returned before active command completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait after command completion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not finish after command exit")
	}
	<-requestDone
}

func TestUnexpectedServeExitAutomaticallyCompletesFullShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	if err := srv.listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-srv.Done():
	case <-time.After(time.Second):
		t.Fatal("serve loop did not report listener closure")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- srv.Wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait after listener closure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not include automatic shutdown after listener closure")
	}
}

func TestBearerAuthenticationHasNoCompatibilityPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	token, err := controlapi.ReadToken(controlapi.TokenPath(path))
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, controlapi.URL(srv.Addr())+controlapi.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bearer status=%d", response.StatusCode)
	}
}

func TestChallengeReplayAndBodyTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	token := readTestToken(t, path)
	challenge := fetchTestChallenge(t, srv.Addr())
	original := []byte(`{"args":["local","status"],"revision":-1,"request_id":"70000000000000000000000000000000"}`)
	tampered := []byte(`{"args":["local","status","tampered"],"revision":-1,"request_id":"70000000000000000000000000000000"}`)

	status, _, _, _ := sendAuthenticatedTestRequest(t, srv.Addr(), token, challenge, original, tampered)
	if status != http.StatusUnauthorized {
		t.Fatalf("tampered request status=%d", status)
	}
	status, responseBody, header, request := sendAuthenticatedTestRequest(t, srv.Addr(), token, challenge, original, original)
	if status != http.StatusOK {
		t.Fatalf("valid request status=%d body=%s", status, responseBody)
	}
	if !controlapi.VerifyResponseAuthentication(header, token, request, original, status, responseBody) {
		t.Fatal("valid response authentication rejected")
	}
	status, _, _, _ = sendAuthenticatedTestRequest(t, srv.Addr(), token, challenge, original, original)
	if status != http.StatusUnauthorized {
		t.Fatalf("challenge replay status=%d", status)
	}
}

func TestSignedStatelessChallengeAuthenticatesCurrentEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	token := readTestToken(t, path)
	challenge, err := controlapi.SignChallenge(token, "abababababababababababababababababababababababababababababababab", time.Now().Add(30*time.Second), srv.authEpoch)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"args":["local","status"],"revision":-1,"request_id":"70500000000000000000000000000000"}`)
	status, _, _, _ := sendAuthenticatedTestRequest(t, srv.Addr(), token, challenge, body, body)
	if status != http.StatusOK {
		t.Fatalf("valid stateless challenge status=%d", status)
	}
}

func TestControlTokenRotationRevokesOldValueWithoutRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	tokenPath := controlapi.TokenPath(path)
	oldToken := readTestToken(t, path)
	oldChallenge := fetchTestChallenge(t, srv.Addr())
	newToken := strings.Repeat("ef", 32)
	if newToken == oldToken {
		t.Fatal("test token unexpectedly matches existing value")
	}
	if err := os.WriteFile(tokenPath, []byte(newToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"args":["local","status"],"revision":-1,"request_id":"70550000000000000000000000000000"}`)
	status, _, _, _ := sendAuthenticatedTestRequest(t, srv.Addr(), oldToken, oldChallenge, body, body)
	if status != http.StatusUnauthorized {
		t.Fatalf("old token status=%d", status)
	}
	newChallenge := fetchTestChallenge(t, srv.Addr())
	status, responseBody, header, request := sendAuthenticatedTestRequest(t, srv.Addr(), newToken, newChallenge, body, body)
	if status != http.StatusOK {
		t.Fatalf("rotated token status=%d body=%s", status, responseBody)
	}
	if !controlapi.VerifyResponseAuthentication(header, newToken, request, body, status, responseBody) {
		t.Fatal("response was not signed with the rotated token")
	}
}

func TestChallengeFromPreviousServerEpochIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	token := readTestToken(t, path)
	challenge, err := controlapi.SignChallenge(token, strings.Repeat("ac", 32), time.Now().Add(30*time.Second), strings.Repeat("ff", 32))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"args":["local","status"],"revision":-1,"request_id":"70600000000000000000000000000000"}`)
	status, _, _, _ := sendAuthenticatedTestRequest(t, srv.Addr(), token, challenge, body, body)
	if status != http.StatusUnauthorized {
		t.Fatalf("previous epoch challenge status=%d", status)
	}
}

func TestChallengeExpiryAndExpiredCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	base := time.Now()
	srv.challengesMu.Lock()
	srv.now = func() time.Time { return base }
	srv.challengeTTL = 30 * time.Second
	srv.challengesMu.Unlock()
	token := readTestToken(t, path)
	challenge := fetchTestChallenge(t, srv.Addr())
	body := []byte(`{"args":["local","status"],"revision":-1,"request_id":"71000000000000000000000000000000"}`)

	srv.challengesMu.Lock()
	srv.now = func() time.Time { return base.Add(31 * time.Second) }
	srv.challengesMu.Unlock()
	status, _, _, _ := sendAuthenticatedTestRequest(t, srv.Addr(), token, challenge, body, body)
	if status != http.StatusUnauthorized {
		t.Fatalf("expired challenge status=%d", status)
	}
	response, err := http.Get(controlapi.URL(srv.Addr()) + controlapi.ChallengePath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("challenge after expired cleanup status=%d", response.StatusCode)
	}
}

func TestUnauthenticatedChallengeFloodDoesNotConsumeReplayCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	for range 4096 {
		_ = fetchTestChallenge(t, srv.Addr())
	}
	srv.challengesMu.Lock()
	used := len(srv.usedChallenges)
	srv.challengesMu.Unlock()
	if used != 0 {
		t.Fatalf("unauthenticated challenge flood populated replay table: %d", used)
	}
	challenge := fetchTestChallenge(t, srv.Addr())
	token := readTestToken(t, path)
	body := []byte(`{"args":["local","status"],"revision":-1,"request_id":"72000000000000000000000000000000"}`)
	status, _, _, _ := sendAuthenticatedTestRequest(t, srv.Addr(), token, challenge, body, body)
	if status != http.StatusOK {
		t.Fatalf("valid request after flood status=%d", status)
	}
}

func TestConcurrentChallengeUseExecutesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	srv := startTestServer(t, path)
	var executions atomic.Int32
	srv.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		executions.Add(1)
		return cli.ExecutionResult{}
	}
	token := readTestToken(t, path)
	challenge := fetchTestChallenge(t, srv.Addr())
	body := []byte(`{"args":["local","status"],"revision":-1,"request_id":"73000000000000000000000000000000"}`)

	const callers = 24
	start := make(chan struct{})
	statuses := make(chan int, callers)
	errorsCh := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			status, _, _, _, err := sendAuthenticatedTestRequestRaw(srv.Addr(), token, challenge, body, body)
			statuses <- status
			errorsCh <- err
		}()
	}
	close(start)
	group.Wait()
	close(statuses)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	ok, unauthorized := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusUnauthorized:
			unauthorized++
		default:
			t.Fatalf("unexpected status=%d", status)
		}
	}
	if ok != 1 || unauthorized != callers-1 || executions.Load() != 1 {
		t.Fatalf("ok=%d unauthorized=%d executions=%d", ok, unauthorized, executions.Load())
	}
}

func startTestServer(t *testing.T, path string) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, "127.0.0.1:0", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func postCommand(t *testing.T, addr, tokenPath string, payload controlapi.Request) int {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := postRawCommand(t, addr, tokenPath, string(body))
	return status
}

func postCommandResponse(t *testing.T, addr, tokenPath string, payload controlapi.Request) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return postRawCommand(t, addr, tokenPath, string(body))
}

func postRawCommand(t *testing.T, addr, tokenPath, body string) (int, []byte) {
	t.Helper()
	status, responseBody, err := controlapi.AuthenticatedRequestContext(
		context.Background(), addr, tokenPath, http.MethodPost, controlapi.CommandPath, []byte(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	return status, responseBody
}

func postUnauthenticatedCommand(t *testing.T, addr string, payload controlapi.Request) int {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, controlapi.URL(addr)+controlapi.CommandPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func readTestToken(t *testing.T, configPath string) string {
	t.Helper()
	token, err := controlapi.ReadToken(controlapi.TokenPath(configPath))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func fetchTestChallenge(t *testing.T, addr string) controlapi.Challenge {
	t.Helper()
	response, err := http.Get(controlapi.URL(addr) + controlapi.ChallengePath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("challenge status=%d body=%s", response.StatusCode, body)
	}
	var challenge controlapi.Challenge
	if err := json.NewDecoder(response.Body).Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	return challenge
}

func sendAuthenticatedTestRequest(t *testing.T, addr, token string, challenge controlapi.Challenge, signedBody, wireBody []byte) (int, []byte, http.Header, *http.Request) {
	t.Helper()
	status, body, header, request, err := sendAuthenticatedTestRequestRaw(addr, token, challenge, signedBody, wireBody)
	if err != nil {
		t.Fatal(err)
	}
	return status, body, header, request
}

func sendAuthenticatedTestRequestRaw(addr, token string, challenge controlapi.Challenge, signedBody, wireBody []byte) (int, []byte, http.Header, *http.Request, error) {
	request, err := http.NewRequest(http.MethodPost, controlapi.URL(addr)+controlapi.CommandPath, bytes.NewReader(wireBody))
	if err != nil {
		return 0, nil, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if err := controlapi.AuthenticateRequest(request, token, challenge, signedBody); err != nil {
		return 0, nil, nil, nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, nil, nil, err
	}
	return response.StatusCode, body, response.Header.Clone(), request, nil
}
