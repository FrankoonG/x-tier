package controlserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/cli"
	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/publicerr"
)

type domainTestProvider struct {
	reloads       atomic.Int32
	reloadStatus  controlapi.ReconcileStatus
	returnStatus  bool
	reloadErr     error
	reloadEntered chan<- struct{}
	reloadRelease <-chan struct{}
}

func (p *domainTestProvider) Status(context.Context) (controlapi.DaemonStatus, error) {
	return controlapi.DaemonStatus{}, nil
}

func (p *domainTestProvider) Reload(ctx context.Context, revision int64, dryRun bool) (controlapi.ReconcileStatus, error) {
	p.reloads.Add(1)
	if p.reloadEntered != nil {
		select {
		case p.reloadEntered <- struct{}{}:
		default:
		}
	}
	if p.reloadRelease != nil {
		select {
		case <-p.reloadRelease:
		case <-ctx.Done():
			return p.reloadStatus, ctx.Err()
		}
	}
	if p.reloadErr != nil {
		return p.reloadStatus, p.reloadErr
	}
	if p.returnStatus {
		return p.reloadStatus, nil
	}
	return controlapi.ReconcileStatus{
		State:                  controlapi.ReconcileStateApplied,
		AppliedRevision:        revision,
		AttemptedRevision:      revision,
		ConfigurationPublished: true,
		ObservedAt:             time.Now().UTC(),
	}, nil
}

func TestDomainDryRunPanicHasNoMutationOutcomeOrCacheEntry(t *testing.T) {
	server := &Server{domainRequests: make(map[string]*cachedDomainResponse)}
	meta := domainMutationRequest(0, "90000000000000000000000000000001")
	meta.DryRun = true
	response := httptest.NewRecorder()
	server.executeCachedDomainMutation(
		response,
		httptest.NewRequest(http.MethodPatch, controlapi.DomainIdentityPath, nil),
		meta,
		[]byte(`{"api_version":1,"revision":0,"dry_run":true}`),
		func() domainResult { panic("private dry-run panic") },
	)
	if response.Code != http.StatusInternalServerError ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"domain.execution_failed"`)) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"applied"`)) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"outcome"`)) ||
		bytes.Contains(response.Body.Bytes(), []byte("private dry-run panic")) {
		t.Fatalf("dry-run panic status=%d body=%s", response.Code, response.Body.Bytes())
	}
	if len(server.domainRequests) != 0 {
		t.Fatalf("dry-run panic populated idempotency cache: %+v", server.domainRequests)
	}
}

func TestDomainLeaderPanicCompletesProtectedIndeterminateResult(t *testing.T) {
	server := &Server{domainRequests: make(map[string]*cachedDomainResponse)}
	meta := domainMutationRequest(0, "91000000000000000000000000000001")
	raw := []byte(`{"api_version":1,"revision":0,"dry_run":false,"request_id":"91000000000000000000000000000001","name":"A"}`)
	request := httptest.NewRequest(http.MethodPatch, controlapi.DomainIdentityPath, nil)
	response := httptest.NewRecorder()
	executions := 0
	server.executeCachedDomainMutation(response, request, meta, raw, func() domainResult {
		executions++
		panic("sensitive panic detail")
	})
	if response.Code != http.StatusInternalServerError ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"domain.execution_indeterminate"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"outcome":"indeterminate"`)) ||
		bytes.Contains(response.Body.Bytes(), []byte("sensitive panic detail")) {
		t.Fatalf("panic response status=%d body=%s", response.Code, response.Body.Bytes())
	}

	server.domainRequestsMu.Lock()
	entry := server.domainRequests[meta.RequestID]
	completedOrder := len(server.domainCompletedRequests)
	server.domainRequestsMu.Unlock()
	if entry == nil || !entry.completed || !entry.protected || completedOrder != 0 {
		t.Fatalf("panic entry was not retained as a protected tombstone: entry=%+v order=%d", entry, completedOrder)
	}
	select {
	case <-entry.done:
	default:
		t.Fatal("panic entry did not release followers")
	}

	replay := httptest.NewRecorder()
	server.executeCachedDomainMutation(replay, httptest.NewRequest(http.MethodPatch, controlapi.DomainIdentityPath, nil), meta, raw, func() domainResult {
		executions++
		return domainJSONResult(http.StatusOK, map[string]any{"ok": true})
	})
	if executions != 1 || replay.Code != response.Code || !bytes.Equal(replay.Body.Bytes(), response.Body.Bytes()) {
		t.Fatalf("panic replay executed again or changed result: executions=%d status=%d body=%s", executions, replay.Code, replay.Body.Bytes())
	}
}

func TestCanceledDomainFollowerGetsExplicitIndeterminateResult(t *testing.T) {
	server := &Server{domainRequests: make(map[string]*cachedDomainResponse)}
	fingerprint := sha256.Sum256([]byte("request"))
	entry, leader, status := server.claimDomainRequest("92000000000000000000000000000001", fingerprint)
	if entry == nil || !leader || status != 0 {
		t.Fatalf("claim = (%+v, %v, %d)", entry, leader, status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := waitDomainResult(ctx, entry)
	if result.status != http.StatusRequestTimeout ||
		!bytes.Contains(result.body, []byte(`"error_code":"domain.request_canceled"`)) ||
		!bytes.Contains(result.body, []byte(`"outcome":"indeterminate"`)) {
		t.Fatalf("canceled follower result=%d %s", result.status, result.body)
	}
	server.completeDomainRequest("92000000000000000000000000000001", entry, domainJSONResult(http.StatusOK, map[string]any{"ok": true}))
}

func TestOrdinaryIndeterminateDomainMutationIsProtected(t *testing.T) {
	server := &Server{domainRequests: make(map[string]*cachedDomainResponse)}
	meta := domainMutationRequest(0, "92000000000000000000000000000002")
	raw := []byte(`{"api_version":1,"revision":0,"dry_run":false,"request_id":"92000000000000000000000000000002"}`)
	response := httptest.NewRecorder()
	unknown := errors.Join(
		configstore.ErrCommitOutcomeUnknown,
		publicerr.Errorf("config.commit_outcome_unknown", "private diagnostic"),
	)
	server.executeCachedDomainMutation(
		response,
		httptest.NewRequest(http.MethodPatch, controlapi.DomainSettingsPath, nil),
		meta,
		raw,
		func() domainResult { return domainMutationErrorResult(unknown, 0, false, nil) },
	)
	if response.Code < 400 || !bytes.Contains(response.Body.Bytes(), []byte(`"outcome":"indeterminate"`)) {
		t.Fatalf("indeterminate mutation status=%d body=%s", response.Code, response.Body.Bytes())
	}
	server.domainRequestsMu.Lock()
	entry := server.domainRequests[meta.RequestID]
	server.domainRequestsMu.Unlock()
	if entry == nil || !entry.completed || !entry.protected {
		t.Fatalf("indeterminate mutation was evictable: entry=%+v", entry)
	}
}

func TestConfigMutationResponseEncodingFailureReportsApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := &Server{configPath: path}
	result := server.executeConfigDomainMutation(
		domainMutationRequest(0, "92000000000000000000000000000003"),
		func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
			cfg.System.LogLevel = "debug"
			return make(chan int), nil
		},
	)
	var failure controlapi.DomainError
	if err := json.Unmarshal(result.body, &failure); err != nil {
		t.Fatal(err)
	}
	if result.status != http.StatusOK || failure.ErrorCode != "domain.response_invalid" ||
		failure.Applied == nil || !*failure.Applied || failure.Outcome != controlapi.MutationOutcomeApplied {
		t.Fatalf("post-commit encoding result=%d %+v", result.status, failure)
	}
	loaded, err := configstore.LoadExisting(path)
	if err != nil || loaded.Revision != 1 || loaded.System.LogLevel != "debug" {
		t.Fatalf("committed config=%+v err=%v", loaded, err)
	}
}

func TestIdentityInitReportsRecoverableBackingAfterConfigCommitFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	var blockers []string
	for i := 0; i < 5; i++ {
		blocker := fmt.Sprintf("%s.bak.%02d", path, i)
		if err := os.Mkdir(blocker, 0o700); err != nil {
			t.Fatal(err)
		}
		blockers = append(blockers, blocker)
	}
	server := startTestServer(t, path)
	request := controlapi.IdentityInitRequest{
		DomainMutationRequest: domainMutationRequest(0, "92000000000000000000000000000004"),
		Name:                  "A",
	}
	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, request)
	var failure controlapi.DomainError
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatal(err)
	}
	if status < 400 || failure.Applied == nil || *failure.Applied ||
		failure.Outcome != controlapi.MutationOutcomeNotApplied || len(failure.Preparations) != 1 {
		t.Fatalf("identity preparation status=%d failure=%+v body=%s", status, failure, body)
	}
	preparation := failure.Preparations[0]
	if preparation.Kind != "identity_backing" || preparation.State != identityStateRecoverable || preparation.NodeID == "" {
		t.Fatalf("identity preparation=%+v", preparation)
	}
	if _, err := os.Stat(domainIdentitySeedPath(path)); err != nil {
		t.Fatalf("recoverable seed was removed: %v", err)
	}
	unchanged, err := configstore.LoadExisting(path)
	if err != nil || unchanged.Revision != 0 || unchanged.Node.NodeID != "" {
		t.Fatalf("failed config commit changed config=%+v err=%v", unchanged, err)
	}
	for _, blocker := range blockers {
		if err := os.Remove(blocker); err != nil {
			t.Fatal(err)
		}
	}
	request.RequestID = "92000000000000000000000000000005"
	status, body = requestDomain(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, request)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"outcome":"applied"`)) {
		t.Fatalf("recover identity status=%d body=%s", status, body)
	}
	recovered, err := configstore.LoadExisting(path)
	if err != nil || recovered.Revision != 1 || recovered.Node.NodeID != preparation.NodeID {
		t.Fatalf("recovered identity=%+v err=%v want_node_id=%s", recovered, err, preparation.NodeID)
	}
}

func TestIdentityInitReportsBackingPublishedBeforeCreatorError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	server.createIdentity = func(seedPath string) (*identity.Identity, error) {
		created, err := identity.Create(seedPath)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("simulated directory sync failure after publication: " + created.Public().NodeID.String())
	}
	request := controlapi.IdentityInitRequest{
		DomainMutationRequest: domainMutationRequest(0, "92000000000000000000000000000006"),
		Name:                  "A",
	}
	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, request)
	var failure controlapi.DomainError
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatal(err)
	}
	if status < 400 || failure.Applied == nil || *failure.Applied ||
		failure.Outcome != controlapi.MutationOutcomeNotApplied || len(failure.Preparations) != 1 ||
		failure.Preparations[0].State != identityStateRecoverable || failure.Preparations[0].NodeID == "" {
		t.Fatalf("published seed outcome status=%d failure=%+v body=%s", status, failure, body)
	}
	if _, err := identity.Load(domainIdentitySeedPath(path)); err != nil {
		t.Fatalf("published seed is not recoverable: %v", err)
	}
	unchanged, err := configstore.LoadExisting(path)
	if err != nil || unchanged.Revision != 0 || unchanged.Node.NodeID != "" {
		t.Fatalf("creator error changed config=%+v err=%v", unchanged, err)
	}
}

func TestDomainAPIRejectsMissingLiveConfigWithoutRecreatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	status, _ := requestDomain(t, server, http.MethodGet, controlapi.DomainSettingsPath, nil)
	if status == http.StatusOK {
		t.Fatal("domain read returned revision-zero defaults for a missing live config")
	}
	for _, dryRun := range []bool{true, false} {
		logLevel := "debug"
		meta := domainMutationRequest(
			0, fmt.Sprintf("%032x", 9300+map[bool]int{false: 1, true: 2}[dryRun]),
		)
		meta.DryRun = dryRun
		status, _ = requestDomain(t, server, http.MethodPatch, controlapi.DomainSettingsPath, controlapi.SettingsUpdateRequest{
			DomainMutationRequest: meta,
			Settings:              controlapi.SettingsPatch{LogLevel: &logLevel},
		})
		if status == http.StatusOK {
			t.Fatalf("domain mutation succeeded with missing config: dry_run=%t", dryRun)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("domain mutation recreated missing config: dry_run=%t err=%v", dryRun, err)
		}
	}
}

func TestConfigRestoreDomainRepairsInvalidActiveConfigWithoutEnteringCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	checkpoint := configstore.DefaultConfig()
	checkpoint.Revision = 5
	if err := configstore.Save(path, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := configstore.SaveLastKnownGood(path, checkpoint); err != nil {
		t.Fatal(err)
	}
	invalid := []byte("{\n")
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := StartOwned(ctx, "127.0.0.1:0", path, path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	dryRequest := controlapi.ConfigRestoreRequest{DomainMutationRequest: domainMutationRequest(
		checkpoint.Revision, "92000000000000000000000000000011",
	)}
	dryRequest.DryRun = true
	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainConfigRestorePath, dryRequest)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"changed":true`)) ||
		!bytes.Contains(body, []byte(`"before_revision":5`)) || !bytes.Contains(body, []byte(`"after_revision":6`)) ||
		bytes.Contains(body, []byte(`"applied"`)) || bytes.Contains(body, []byte(`"outcome"`)) {
		t.Fatalf("restore dry-run status=%d body=%s", status, body)
	}
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, invalid) {
		t.Fatalf("restore dry-run mutated active config: err=%v body=%q", err, current)
	}

	const requestID = "92000000000000000000000000000012"
	request := controlapi.ConfigRestoreRequest{DomainMutationRequest: domainMutationRequest(checkpoint.Revision, requestID)}
	status, body = requestDomain(t, server, http.MethodPost, controlapi.DomainConfigRestorePath, request)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"changed":true`)) ||
		!bytes.Contains(body, []byte(`"source":"last-known-good"`)) ||
		!bytes.Contains(body, []byte(`"applied":true`)) ||
		!bytes.Contains(body, []byte(`"outcome":"applied"`)) {
		t.Fatalf("restore apply status=%d body=%s", status, body)
	}
	repaired, err := configstore.LoadExisting(path)
	if err != nil || repaired.Revision != checkpoint.Revision+1 {
		t.Fatalf("restored active config=%+v err=%v", repaired, err)
	}
	status, replay := requestDomain(t, server, http.MethodPost, controlapi.DomainConfigRestorePath, request)
	if status != http.StatusOK || !bytes.Equal(replay, body) {
		t.Fatalf("restore replay changed result: status=%d body=%s replay=%s", status, body, replay)
	}
	repaired, err = configstore.LoadExisting(path)
	if err != nil || repaired.Revision != checkpoint.Revision+1 {
		t.Fatalf("restore replay executed twice: config=%+v err=%v", repaired, err)
	}
	if server.commandIngress.Load() != 0 || server.commandExecutions.Load() != 0 || server.domainExecutions.Load() != 2 {
		t.Fatalf("restore domain crossed control boundaries: command=(%d,%d) domain=%d", server.commandIngress.Load(), server.commandExecutions.Load(), server.domainExecutions.Load())
	}
}

func TestConfigMutationAndRestoreReportUnknownCommitOutcomeConsistently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	unknown := errors.Join(
		configstore.ErrCommitOutcomeUnknown,
		publicerr.Errorf("config.commit_outcome_unknown", "injected durability failure"),
	)

	local := &Server{configPath: path}
	result := local.executeConfigDomainMutation(
		domainMutationRequest(0, "92000000000000000000000000000021"),
		func(*configstore.Config, bool, *domainMutationEffects) (any, error) { return nil, unknown },
	)
	var updateFailure controlapi.DomainError
	if err := json.Unmarshal(result.body, &updateFailure); err != nil {
		t.Fatal(err)
	}
	if result.status < 400 || updateFailure.Applied == nil || *updateFailure.Applied ||
		updateFailure.Outcome != controlapi.MutationOutcomeIndeterminate {
		t.Fatalf("config mutation result=%d %+v", result.status, updateFailure)
	}

	server := startTestServer(t, path)
	var restores atomic.Int32
	server.restoreConfig = func(int64, bool) (configstore.UpdateResult, error) {
		restores.Add(1)
		return configstore.UpdateResult{}, unknown
	}

	cliResponse, err := controlapi.Execute(server.Addr(), controlapi.TokenPath(path), controlapi.Request{
		Args:      []string{"local", "config", "restore-last-good"},
		JSON:      true,
		Revision:  0,
		RequestID: "92000000000000000000000000000022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cliResponse.ExitCode != controlapi.IndeterminateExitCode || cliResponse.Applied ||
		cliResponse.Outcome != controlapi.MutationOutcomeIndeterminate {
		t.Fatalf("CLI restore response=%+v", cliResponse)
	}

	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainConfigRestorePath, controlapi.ConfigRestoreRequest{
		DomainMutationRequest: domainMutationRequest(0, "92000000000000000000000000000023"),
	})
	var webFailure controlapi.DomainError
	if err := json.Unmarshal(body, &webFailure); err != nil {
		t.Fatal(err)
	}
	if status < 400 || webFailure.Applied == nil || *webFailure.Applied != cliResponse.Applied ||
		webFailure.Outcome != cliResponse.Outcome || restores.Load() != 2 {
		t.Fatalf("Web restore status=%d failure=%+v CLI=%+v restores=%d", status, webFailure, cliResponse, restores.Load())
	}
}

func TestConfigRestoreDoesNotWaitForBlockedRuntimeReload(t *testing.T) {
	for _, ingress := range []string{"domain", "CLI"} {
		t.Run(ingress, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			checkpoint := configstore.DefaultConfig()
			checkpoint.Revision = 5
			if err := configstore.Save(path, checkpoint); err != nil {
				t.Fatal(err)
			}
			if err := configstore.SaveLastKnownGood(path, checkpoint); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			releaseReload := sync.OnceFunc(func() { close(release) })
			provider := &domainTestProvider{reloadEntered: entered, reloadRelease: release}
			ctx, cancel := context.WithCancel(context.Background())
			server, err := StartOwned(ctx, "127.0.0.1:0", path, path, provider)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			defer cancel()
			defer releaseReload()

			reloadDone := make(chan error, 1)
			if ingress == "domain" {
				go func() {
					status, body, err := requestDomainRaw(server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
						DomainMutationRequest: domainMutationRequest(checkpoint.Revision, "92000000000000000000000000000021"),
					})
					if err == nil && status != http.StatusOK {
						err = fmt.Errorf("reload status=%d body=%s", status, body)
					}
					reloadDone <- err
				}()
			} else {
				go func() {
					response := server.executeCommand(controlapi.Request{
						Args: []string{"local", "reload"}, JSON: true, Revision: checkpoint.Revision,
					})
					if response.ExitCode != 0 {
						reloadDone <- fmt.Errorf("reload response=%+v", response)
						return
					}
					reloadDone <- nil
				}()
			}
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("reload did not enter provider")
			}

			restoreDone := make(chan struct {
				status int
				body   []byte
				err    error
			}, 1)
			go func() {
				status, body, err := requestDomainRaw(server, http.MethodPost, controlapi.DomainConfigRestorePath, controlapi.ConfigRestoreRequest{
					DomainMutationRequest: domainMutationRequest(checkpoint.Revision, "92000000000000000000000000000022"),
				})
				restoreDone <- struct {
					status int
					body   []byte
					err    error
				}{status: status, body: body, err: err}
			}()
			select {
			case result := <-restoreDone:
				if result.err != nil || result.status != http.StatusOK {
					t.Fatalf("restore while reload blocked: status=%d body=%s err=%v", result.status, result.body, result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("restore waited for blocked runtime reload")
			}

			repaired, err := configstore.LoadExisting(path)
			if err != nil || repaired.Revision != checkpoint.Revision+1 {
				t.Fatalf("restored active config=%+v err=%v", repaired, err)
			}
			releaseReload()
			select {
			case err := <-reloadDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reload did not finish after release")
			}
		})
	}
}

func TestConfigRestoreRemainsSerializedWithActiveConfigWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	checkpoint := configstore.DefaultConfig()
	checkpoint.Revision = 7
	if err := configstore.Save(path, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := configstore.SaveLastKnownGood(path, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := StartOwned(ctx, "127.0.0.1:0", path, path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writeEntered := make(chan struct{})
	writeRelease := make(chan struct{})
	releaseWrite := sync.OnceFunc(func() { close(writeRelease) })
	defer releaseWrite()
	server.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		close(writeEntered)
		<-writeRelease
		return cli.ExecutionResult{ExitCode: 1, Outcome: controlapi.MutationOutcomeNotApplied}
	}
	writeDone := make(chan controlapi.Response, 1)
	go func() {
		writeDone <- server.executeCommand(controlapi.Request{
			Args: []string{"local", "identity", "rename", "blocked"}, JSON: true, Revision: checkpoint.Revision,
		})
	}()
	select {
	case <-writeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("config write did not enter executor")
	}

	restoreDone := make(chan error, 1)
	go func() {
		status, body, err := requestDomainRaw(server, http.MethodPost, controlapi.DomainConfigRestorePath, controlapi.ConfigRestoreRequest{
			DomainMutationRequest: domainMutationRequest(checkpoint.Revision, "92000000000000000000000000000031"),
		})
		if err == nil && status != http.StatusOK {
			err = fmt.Errorf("restore status=%d body=%s", status, body)
		}
		restoreDone <- err
	}()
	select {
	case err := <-restoreDone:
		t.Fatalf("restore bypassed active config writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	releaseWrite()
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("config writer did not finish after release")
	}
	select {
	case err := <-restoreDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restore did not finish after config writer released lock")
	}
}

func TestActiveConfigWritesRemainSerializedWithRuntimeReload(t *testing.T) {
	for _, ingress := range []string{"domain", "CLI"} {
		t.Run(ingress, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			releaseReload := sync.OnceFunc(func() { close(release) })
			provider := &domainTestProvider{reloadEntered: entered, reloadRelease: release}
			ctx, cancel := context.WithCancel(context.Background())
			server, err := StartOwned(ctx, "127.0.0.1:0", path, path, provider)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			defer server.Close()
			defer cancel()
			defer releaseReload()

			reloadDone := make(chan error, 1)
			go func() {
				status, body, err := requestDomainRaw(server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
					DomainMutationRequest: domainMutationRequest(0, "92000000000000000000000000000041"),
				})
				if err == nil && status != http.StatusOK {
					err = fmt.Errorf("reload status=%d body=%s", status, body)
				}
				reloadDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("runtime reload did not enter provider")
			}

			mutationDone := make(chan error, 1)
			if ingress == "domain" {
				go func() {
					level := "debug"
					status, body, err := requestDomainRaw(server, http.MethodPatch, controlapi.DomainSettingsPath, controlapi.SettingsUpdateRequest{
						DomainMutationRequest: domainMutationRequest(0, "92000000000000000000000000000042"),
						Settings:              controlapi.SettingsPatch{LogLevel: &level},
					})
					if err == nil && status != http.StatusOK {
						err = fmt.Errorf("config mutation status=%d body=%s", status, body)
					}
					mutationDone <- err
				}()
			} else {
				go func() {
					response, err := controlapi.Execute(server.Addr(), controlapi.TokenPath(path), controlapi.Request{
						Args:      []string{"local", "settings", "set", "--log-level", "debug"},
						JSON:      true,
						Revision:  0,
						RequestID: "92000000000000000000000000000043",
					})
					if err == nil && response.ExitCode != 0 {
						err = fmt.Errorf("config mutation response=%+v", response)
					}
					mutationDone <- err
				}()
			}
			select {
			case err := <-mutationDone:
				t.Fatalf("config mutation bypassed blocked reload: %v", err)
			case <-time.After(100 * time.Millisecond):
			}

			releaseReload()
			select {
			case err := <-reloadDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reload did not finish after release")
			}
			select {
			case err := <-mutationDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("config mutation did not finish after reload")
			}
			cfg, err := configstore.LoadExisting(path)
			if err != nil || cfg.Revision != 1 || cfg.System.LogLevel != "debug" {
				t.Fatalf("serialized config mutation=%+v err=%v", cfg, err)
			}
		})
	}
}

func TestRuntimeReloadPostApplyErrorReportsAppliedAndCachesExactResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &domainTestProvider{
		reloadStatus: controlapi.ReconcileStatus{
			State: controlapi.ReconcileStateFailed, AppliedRevision: 4, AttemptedRevision: 4,
			ConfigurationPublished: true,
		},
		reloadErr: publicerr.Errorf("service.reload_apply", "old generation cleanup remains pending"),
	}
	server, err := Start(ctx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	const requestID = "93000000000000000000000000000001"
	request := controlapi.RuntimeReloadRequest{DomainMutationRequest: domainMutationRequest(4, requestID)}
	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, request)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"error_code":"service.reload_apply"`)) ||
		!bytes.Contains(body, []byte(`"applied":true`)) ||
		!bytes.Contains(body, []byte(`"outcome":"applied"`)) {
		t.Fatalf("post-apply reload error status=%d body=%s", status, body)
	}
	status, replay := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, request)
	if status != http.StatusOK || !bytes.Equal(replay, body) || provider.reloads.Load() != 1 {
		t.Fatalf("reload replay status=%d reloads=%d body=%s", status, provider.reloads.Load(), replay)
	}
	server.domainRequestsMu.Lock()
	entry := server.domainRequests[requestID]
	server.domainRequestsMu.Unlock()
	if entry == nil || !entry.completed || entry.protected {
		t.Fatalf("confirmed applied reload result has wrong cache state: %+v", entry)
	}
}

func TestCanceledRuntimeReloadLeaderCompletesForSameRequestIDReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseReload := sync.OnceFunc(func() { close(release) })
	provider := &domainTestProvider{reloadEntered: entered, reloadRelease: release}
	serverCtx, cancelServer := context.WithCancel(context.Background())
	server, err := Start(serverCtx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer cancelServer()
	defer releaseReload()

	request := controlapi.RuntimeReloadRequest{DomainMutationRequest: domainMutationRequest(
		0, "93000000000000000000000000000011",
	)}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelRequest()
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := controlapi.AuthenticatedRequestContext(
			requestCtx,
			server.Addr(),
			controlapi.TokenPath(path),
			http.MethodPost,
			controlapi.DomainRuntimeReloadPath,
			body,
		)
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime reload did not enter provider")
	}
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("first request error=%v, want caller deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not honor caller deadline")
	}

	releaseReload()
	status, replay := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, request)
	if status != http.StatusOK || !bytes.Contains(replay, []byte(`"reconciliation_state":"applied"`)) {
		t.Fatalf("same request_id replay status=%d body=%s", status, replay)
	}
	if calls := provider.reloads.Load(); calls != 1 {
		t.Fatalf("reload executions=%d, want 1", calls)
	}
}

func TestSuccessfulRuntimeReloadsRemainEvictable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &domainTestProvider{}
	server, err := Start(ctx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for i := 0; i < requestCacheCapacity+16; i++ {
		requestID := fmt.Sprintf("%032x", i+1)
		status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
			DomainMutationRequest: domainMutationRequest(0, requestID),
		})
		if status != http.StatusOK {
			t.Fatalf("reload %d status=%d body=%s", i, status, body)
		}
	}
	server.domainRequestsMu.Lock()
	cacheCount := len(server.domainRequests)
	server.domainRequestsMu.Unlock()
	if cacheCount != requestCacheCapacity {
		t.Fatalf("successful reload cache count=%d, want %d", cacheCount, requestCacheCapacity)
	}

	level := "debug"
	status, body := requestDomain(t, server, http.MethodPatch, controlapi.DomainSettingsPath, controlapi.SettingsUpdateRequest{
		DomainMutationRequest: domainMutationRequest(0, fmt.Sprintf("%032x", requestCacheCapacity+17)),
		Settings:              controlapi.SettingsPatch{LogLevel: &level},
	})
	if status != http.StatusOK {
		t.Fatalf("unrelated mutation after protected reloads status=%d body=%s", status, body)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.System.LogLevel != level {
		t.Fatalf("unrelated mutation was not committed: %+v", loaded)
	}
}

func TestIndeterminateDomainCacheFailsClosedAtCapacity(t *testing.T) {
	server := &Server{domainRequests: make(map[string]*cachedDomainResponse)}
	unknown := errors.Join(
		configstore.ErrCommitOutcomeUnknown,
		publicerr.Errorf("config.commit_outcome_unknown", "private diagnostic"),
	)
	executions := 0
	firstID := fmt.Sprintf("%032x", 1)
	firstRaw := []byte(fmt.Sprintf(
		`{"api_version":1,"revision":0,"dry_run":false,"request_id":%q}`,
		firstID,
	))

	for i := 0; i < requestCacheCapacity; i++ {
		requestID := fmt.Sprintf("%032x", i+1)
		raw := []byte(fmt.Sprintf(
			`{"api_version":1,"revision":0,"dry_run":false,"request_id":%q}`,
			requestID,
		))
		response := httptest.NewRecorder()
		server.executeCachedDomainMutation(
			response,
			httptest.NewRequest(http.MethodPatch, controlapi.DomainSettingsPath, nil),
			domainMutationRequest(0, requestID),
			raw,
			func() domainResult {
				executions++
				return domainMutationErrorResult(unknown, 0, false, nil)
			},
		)
		if response.Code < 400 || !bytes.Contains(response.Body.Bytes(), []byte(`"outcome":"indeterminate"`)) {
			t.Fatalf("indeterminate mutation %d status=%d body=%s", i, response.Code, response.Body.Bytes())
		}
	}

	replay := httptest.NewRecorder()
	server.executeCachedDomainMutation(
		replay,
		httptest.NewRequest(http.MethodPatch, controlapi.DomainSettingsPath, nil),
		domainMutationRequest(0, firstID),
		firstRaw,
		func() domainResult {
			executions++
			return domainJSONResult(http.StatusOK, map[string]any{"ok": true})
		},
	)
	if replay.Code < 400 || !bytes.Contains(replay.Body.Bytes(), []byte(`"outcome":"indeterminate"`)) || executions != requestCacheCapacity {
		t.Fatalf("protected replay status=%d executions=%d body=%s", replay.Code, executions, replay.Body.Bytes())
	}

	newID := fmt.Sprintf("%032x", requestCacheCapacity+1)
	refused := httptest.NewRecorder()
	server.executeCachedDomainMutation(
		refused,
		httptest.NewRequest(http.MethodPatch, controlapi.DomainSettingsPath, nil),
		domainMutationRequest(0, newID),
		[]byte(fmt.Sprintf(`{"api_version":1,"revision":0,"dry_run":false,"request_id":%q}`, newID)),
		func() domainResult {
			executions++
			return domainJSONResult(http.StatusOK, map[string]any{"ok": true})
		},
	)
	if refused.Code != http.StatusServiceUnavailable ||
		!bytes.Contains(refused.Body.Bytes(), []byte(`"error_code":"domain.idempotency_capacity_exhausted"`)) ||
		!bytes.Contains(refused.Body.Bytes(), []byte(`"outcome":"not_applied"`)) ||
		executions != requestCacheCapacity {
		t.Fatalf("capacity refusal status=%d executions=%d body=%s", refused.Code, executions, refused.Body.Bytes())
	}
}

func TestRuntimeReloadRevisionConflictIsDefiniteNotApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conflictConfig := configstore.DefaultConfig()
	conflictConfig.Revision = 8
	conflictErr := configstore.ValidateRevision(conflictConfig, 7)
	provider := &domainTestProvider{
		reloadStatus: controlapi.ReconcileStatus{
			State: controlapi.ReconcileStateApplied, AppliedRevision: 8, AttemptedRevision: 8,
		},
		reloadErr: conflictErr,
	}
	server, err := Start(ctx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
		DomainMutationRequest: domainMutationRequest(7, "94000000000000000000000000000001"),
	})
	if status != http.StatusConflict ||
		!bytes.Contains(body, []byte(`"error_code":"config.revision_conflict"`)) ||
		!bytes.Contains(body, []byte(`"applied":false`)) ||
		!bytes.Contains(body, []byte(`"outcome":"not_applied"`)) {
		t.Fatalf("revision conflict status=%d body=%s", status, body)
	}
}

func TestRuntimeReloadContentInvalidIsDefiniteNotAttempted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &domainTestProvider{reloadErr: publicerr.Errorf("config.content_invalid", "active document is invalid")}
	server, err := Start(ctx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
		DomainMutationRequest: domainMutationRequest(0, "94000000000000000000000000000002"),
	})
	if status != http.StatusUnprocessableEntity ||
		!bytes.Contains(body, []byte(`"error_code":"config.content_invalid"`)) ||
		!bytes.Contains(body, []byte(`"applied":false`)) ||
		!bytes.Contains(body, []byte(`"outcome":"not_applied"`)) {
		t.Fatalf("content-invalid reload status=%d body=%s", status, body)
	}
}

func TestRuntimeReloadSuccessWithoutAppliedRevisionIsIndeterminate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &domainTestProvider{
		returnStatus: true,
		reloadStatus: controlapi.ReconcileStatus{
			State:             controlapi.ReconcileStatePending,
			AppliedRevision:   0,
			AttemptedRevision: 0,
		},
	}
	server, err := Start(ctx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	const requestID = "94000000000000000000000000000003"
	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
		DomainMutationRequest: domainMutationRequest(0, requestID),
	})
	if status != http.StatusInternalServerError ||
		!bytes.Contains(body, []byte(`"error_code":"service.reload_result_invalid"`)) ||
		!bytes.Contains(body, []byte(`"applied":false`)) ||
		!bytes.Contains(body, []byte(`"outcome":"indeterminate"`)) {
		t.Fatalf("invalid reload result status=%d body=%s", status, body)
	}
	server.domainRequestsMu.Lock()
	entry := server.domainRequests[requestID]
	server.domainRequestsMu.Unlock()
	if entry == nil || !entry.protected {
		t.Fatalf("invalid reload result was not protected: %+v", entry)
	}
}

func TestDomainReloadErrorDoesNotExposeRawRuntimeDetails(t *testing.T) {
	const secret = "literal-password-without-a-marker"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &domainTestProvider{reloadErr: publicerr.Errorf("service.reload_apply", "upstream rejected %s", secret)}
	server, err := Start(ctx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
		DomainMutationRequest: domainMutationRequest(0, "90000000000000000000000000000001"),
	})
	if status != http.StatusServiceUnavailable || bytes.Contains(body, []byte(secret)) ||
		!bytes.Contains(body, []byte(`"error_code":"service.reload_apply"`)) {
		t.Fatalf("unsafe domain reload status=%d body=%s", status, body)
	}
}

func TestDiagnosticTextCannotForgeAppliedMutationOutcome(t *testing.T) {
	result := domainErrorResult(errors.New(`open C:\config.commit_visible_and_resynced: access denied`), 0)
	if result.status != http.StatusInternalServerError ||
		bytes.Contains(result.body, []byte(`"applied":true`)) ||
		!bytes.Contains(result.body, []byte(`"error_code":"domain.failed"`)) {
		t.Fatalf("forged outcome status=%d body=%s", result.status, result.body)
	}
}

func TestDomainMutationErrorResultPreservesTypedUnknownCommitOutcome(t *testing.T) {
	result := domainMutationErrorResult(errors.Join(
		configstore.ErrCommitOutcomeUnknown,
		publicerr.Errorf("config.commit_outcome_unknown", "private diagnostic"),
	), 0, false, nil)
	var failure controlapi.DomainError
	if err := json.Unmarshal(result.body, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Applied == nil || *failure.Applied || failure.Outcome != controlapi.MutationOutcomeIndeterminate {
		t.Fatalf("unknown commit outcome was collapsed: status=%d failure=%+v", result.status, failure)
	}
}

func TestConfigDomainDryRunForecastsNextRevisionWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	level := "debug"
	request := controlapi.SettingsUpdateRequest{
		DomainMutationRequest: domainMutationRequest(0, "99000000000000000000000000000001"),
		Settings:              controlapi.SettingsPatch{LogLevel: &level},
	}
	request.DryRun = true
	status, body := requestDomain(t, server, http.MethodPatch, controlapi.DomainSettingsPath, request)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"changed":true`)) ||
		!bytes.Contains(body, []byte(`"before_revision":0`)) || !bytes.Contains(body, []byte(`"after_revision":1`)) ||
		bytes.Contains(body, []byte(`"applied"`)) || bytes.Contains(body, []byte(`"outcome"`)) {
		t.Fatalf("config dry-run status=%d body=%s", status, body)
	}
	unchanged, err := configstore.LoadExisting(path)
	if err != nil || unchanged.Revision != 0 || unchanged.System.LogLevel == "debug" {
		t.Fatalf("dry-run changed config=%+v err=%v", unchanged, err)
	}
}

func TestDomainAPIUsesTypedJSONWithoutCLIExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	server.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		panic("typed domain API invoked the CLI executor")
	}

	status, body := requestDomain(t, server, http.MethodGet, controlapi.DomainSettingsPath, nil)
	if status != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", status, body)
	}
	assertNoCLIEnvelope(t, body)

	level := "debug"
	request := controlapi.SettingsUpdateRequest{
		DomainMutationRequest: domainMutationRequest(0, "10000000000000000000000000000000"),
		Settings:              controlapi.SettingsPatch{LogLevel: &level},
	}
	status, body = requestDomain(t, server, http.MethodPatch, controlapi.DomainSettingsPath, request)
	if status != http.StatusOK {
		t.Fatalf("settings mutation status=%d body=%s", status, body)
	}
	assertNoCLIEnvelope(t, body)
	var response struct {
		APIVersion     int   `json:"api_version"`
		OK             bool  `json:"ok"`
		BeforeRevision int64 `json:"before_revision"`
		AfterRevision  int64 `json:"after_revision"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.APIVersion != controlapi.DomainAPIVersion || !response.OK || response.BeforeRevision != 0 || response.AfterRevision != 1 {
		t.Fatalf("unexpected typed response: %+v", response)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.System.LogLevel != "debug" {
		t.Fatalf("typed mutation was not committed: revision=%d settings=%+v", loaded.Revision, loaded.System)
	}
	if server.commandIngress.Load() != 0 || server.commandExecutions.Load() != 0 {
		t.Fatalf("domain request entered CLI counters: ingress=%d executions=%d", server.commandIngress.Load(), server.commandExecutions.Load())
	}
	if server.domainIngress.Load() != 2 || server.domainExecutions.Load() != 2 {
		t.Fatalf("domain counters ingress=%d executions=%d", server.domainIngress.Load(), server.domainExecutions.Load())
	}
}

func TestNodeVLESSInboundCredentialsBelongToPeersAtDomainBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	listen := "127.0.0.1:2443"
	status, body := requestDomain(t, server, http.MethodPut, controlapi.DomainInboundsPath, controlapi.InboundPutRequest{
		DomainMutationRequest: domainMutationRequest(0, "11000000000000000000000000000000"),
		Kind:                  "node-vless",
		Listen:                &listen,
	})
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"after_revision":1`)) {
		t.Fatalf("node-vless mutation status=%d body=%s", status, body)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.NodeInbound) != 1 || loaded.NodeInbound[0].Kind != "node-vless" ||
		loaded.NodeInbound[0].Purpose != "node" || loaded.NodeInbound[0].Listen != listen ||
		loaded.NodeInbound[0].XrayProfileID != "" {
		t.Fatalf("node-vless inbound = %+v", loaded.NodeInbound)
	}

	for index, profileID := range []string{"vless", ""} {
		profileID := profileID
		requestID := fmt.Sprintf("1100000000000000000000000000000%d", index+1)
		status, body = requestDomain(t, server, http.MethodPut, controlapi.DomainInboundsPath, controlapi.InboundPutRequest{
			DomainMutationRequest: domainMutationRequest(1, requestID),
			Kind:                  "node-vless",
			XrayProfileID:         &profileID,
		})
		if status != http.StatusUnprocessableEntity ||
			!bytes.Contains(body, []byte(`"error_code":"config.inbound_profile_forbidden"`)) {
			t.Fatalf("node-vless profile %q status=%d body=%s", profileID, status, body)
		}
	}
	loaded, err = configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || len(loaded.NodeInbound) != 1 || loaded.NodeInbound[0].XrayProfileID != "" {
		t.Fatalf("rejected profile changed configuration: %+v", loaded)
	}
}

func TestDomainRejectsUserSOCKSOutsideIsolatedPlaintextScope(t *testing.T) {
	tests := map[string]string{
		"wildcard": "0.0.0.0:1080",
		"public":   "203.0.113.10:1080",
		"domain":   "proxy.example:1080",
	}
	for name, rejectedListen := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := configstore.DefaultConfig()
			cfg.XrayProfiles["vless"] = configstore.XrayProfile{ID: "vless", Kind: "vless", VLESS: &configstore.VLESSProfile{
				UUID: "66ad4540-b58c-4ad2-9926-ea63445a9b57", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
			}}
			cfg.XrayProfiles["socks"] = configstore.XrayProfile{ID: "socks", Kind: "socks", SOCKS: &configstore.SOCKSProfile{
				Username: "terminal", Password: "entry-secret",
			}}
			cfg.Peers = []configstore.PeerConfig{{
				Name: "B", NodeID: "node-b", Direction: "outbound", GatewayAddr: "127.0.0.1:2443",
				XrayProfileID: "vless", Enabled: true,
			}}
			const originalListen = "127.0.0.1:1080"
			cfg.NodeInbound = []configstore.InboundConfig{{
				Kind: "socks", Purpose: "user", Listen: originalListen, Enabled: true, XrayProfileID: "socks", ExitPeer: "B",
			}}
			if err := configstore.Validate(cfg); err != nil {
				t.Fatalf("valid fixture: %v", err)
			}
			if err := configstore.Save(path, cfg); err != nil {
				t.Fatal(err)
			}

			server := startTestServer(t, path)
			status, body := requestDomain(t, server, http.MethodPut, controlapi.DomainInboundsPath, controlapi.InboundPutRequest{
				DomainMutationRequest: domainMutationRequest(0, "12000000000000000000000000000000"),
				Kind:                  "socks",
				Listen:                &rejectedListen,
			})
			if status != http.StatusUnprocessableEntity ||
				!bytes.Contains(body, []byte(`"error_code":"config.inbound_plaintext_scope_invalid"`)) {
				t.Fatalf("rejected listen %q status=%d body=%s", rejectedListen, status, body)
			}

			loaded, err := configstore.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Revision != 0 || len(loaded.NodeInbound) != 1 || loaded.NodeInbound[0].Listen != originalListen {
				t.Fatalf("rejected mutation changed configuration: %+v", loaded)
			}
			if server.commandIngress.Load() != 0 || server.commandExecutions.Load() != 0 {
				t.Fatalf("domain request entered CLI: ingress=%d executions=%d", server.commandIngress.Load(), server.commandExecutions.Load())
			}
		})
	}
}

func TestDomainErrorStatusSeparatesRequestShapeFromDomainSemantics(t *testing.T) {
	tests := map[string]struct {
		code string
		want int
	}{
		"request JSON":       {code: "domain.request_invalid", want: http.StatusBadRequest},
		"request revision":   {code: "config.revision_required", want: http.StatusBadRequest},
		"empty patch":        {code: "settings.patch_empty", want: http.StatusBadRequest},
		"config semantics":   {code: "config.inbound_plaintext_scope_invalid", want: http.StatusUnprocessableEntity},
		"route semantics":    {code: "route.path_invalid", want: http.StatusUnprocessableEntity},
		"identity semantics": {code: "identity.seed_invalid", want: http.StatusUnprocessableEntity},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := domainErrorStatus(tc.code); got != tc.want {
				t.Fatalf("domainErrorStatus(%q)=%d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

func TestDomainMutationIdempotencyAndCASConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	value := 7
	request := controlapi.SettingsUpdateRequest{
		DomainMutationRequest: domainMutationRequest(0, "20000000000000000000000000000000"),
		Settings:              controlapi.SettingsPatch{MaxNestedDepth: &value},
	}

	const callers = 16
	start := make(chan struct{})
	results := make(chan []byte, callers)
	errorsFound := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			status, body, err := requestDomainRaw(server, http.MethodPatch, controlapi.DomainSettingsPath, request)
			if err != nil {
				errorsFound <- err
				return
			}
			if status != http.StatusOK {
				errorsFound <- fmt.Errorf("status=%d body=%s", status, body)
				return
			}
			results <- body
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	var first []byte
	for body := range results {
		if first == nil {
			first = body
		} else if !bytes.Equal(first, body) {
			t.Fatalf("idempotent response changed: first=%s got=%s", first, body)
		}
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.System.MaxNestedDepth != value {
		t.Fatalf("idempotent mutation executed incorrectly: revision=%d settings=%+v", loaded.Revision, loaded.System)
	}
	if server.domainExecutions.Load() != 1 {
		t.Fatalf("domain mutation executions=%d, want 1", server.domainExecutions.Load())
	}

	semanticallyIdentical := []byte(`{
		"settings": {"max_nested_depth": 7},
		"request_id": "20000000000000000000000000000000",
		"revision": 0,
		"api_version": 1
	}`)
	status, body, err := requestDomainBytes(server, http.MethodPatch, controlapi.DomainSettingsPath, semanticallyIdentical)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || !bytes.Equal(first, body) {
		t.Fatalf("semantic retry status=%d first=%s body=%s", status, first, body)
	}
	if server.domainExecutions.Load() != 1 {
		t.Fatalf("semantic retry executed mutation: executions=%d", server.domainExecutions.Load())
	}

	other := 8
	conflicting := request
	conflicting.Settings.MaxNestedDepth = &other
	status, body = requestDomain(t, server, http.MethodPatch, controlapi.DomainSettingsPath, conflicting)
	if status != http.StatusConflict || !bytes.Contains(body, []byte(`"error_code":"domain.request_id_conflict"`)) {
		t.Fatalf("request-id conflict status=%d body=%s", status, body)
	}
}

func TestDomainReadsRedactProfileCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := configstore.DefaultConfig()
	cfg.XrayProfiles["vless"] = configstore.XrayProfile{
		ID: "vless", Kind: "vless",
		VLESS: &configstore.VLESSProfile{UUID: "11111111-1111-4111-8111-111111111111", Transport: "tcp", Security: "none", AllowInsecurePlaintext: true},
	}
	cfg.XrayProfiles["socks"] = configstore.XrayProfile{
		ID: "socks", Kind: "socks", SOCKS: &configstore.SOCKSProfile{Username: "operator", Password: "do-not-return-this"},
	}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	status, body := requestDomain(t, server, http.MethodGet, controlapi.DomainXrayProfilesPath, nil)
	if status != http.StatusOK {
		t.Fatalf("profiles status=%d body=%s", status, body)
	}
	for _, secret := range []string{"11111111-1111-4111-8111-111111111111", "operator", "do-not-return-this", "uuid", "password"} {
		if bytes.Contains(bytes.ToLower(body), bytes.ToLower([]byte(secret))) {
			t.Fatalf("profile response exposed %q: %s", secret, body)
		}
	}
	if !bytes.Contains(body, []byte(`"id":"vless"`)) || !bytes.Contains(body, []byte(`"kind":"socks"`)) {
		t.Fatalf("profile identity was not preserved: %s", body)
	}
}

func TestDomainProfileInUseIgnoresRemoteObservedChildProfiles(t *testing.T) {
	cfg := configstore.DefaultConfig()
	cfg.XrayProfiles["local"] = configstore.XrayProfile{ID: "local", Kind: "historical-reference"}
	cfg.Peers = []configstore.PeerConfig{{
		Name: "managed", NodeID: "managed-id", XrayProfileID: "managed-profile",
		Children: []configstore.PeerConfig{{
			Name: "observed", NodeID: "observed-id", XrayProfileID: "local",
		}},
	}}
	if configstore.LocalProfileInUse(cfg, "local") {
		t.Fatal("remote-observed child pinned an unrelated local profile")
	}
	cfg.Peers[0].XrayProfileID = "local"
	if !configstore.LocalProfileInUse(cfg, "local") {
		t.Fatal("directly managed peer did not pin its local profile")
	}
}

func TestDomainRejectsUnknownFieldsAndUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)

	unknown := []byte(`{"api_version":1,"revision":0,"dry_run":false,"request_id":"30000000000000000000000000000000","name":"A","args":["local","identity","rename","A"]}`)
	status, body, err := requestDomainBytes(server, http.MethodPatch, controlapi.DomainIdentityPath, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte(`"error_code":"domain.request_invalid"`)) {
		t.Fatalf("unknown-field status=%d body=%s", status, body)
	}
	if !bytes.Contains(body, []byte(`"applied":false`)) || !bytes.Contains(body, []byte(`"outcome":"not_applied"`)) {
		t.Fatalf("undecodable pre-execution mutation was not definite: %s", body)
	}

	request := controlapi.IdentityRenameRequest{
		DomainMutationRequest: controlapi.DomainMutationRequest{APIVersion: 2, Revision: 0, RequestID: "30000000000000000000000000000001"},
		Name:                  "A",
	}
	status, body = requestDomain(t, server, http.MethodPatch, controlapi.DomainIdentityPath, request)
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte(`"error_code":"domain.api_version_unsupported"`)) ||
		!bytes.Contains(body, []byte(`"applied":false`)) ||
		!bytes.Contains(body, []byte(`"outcome":"not_applied"`)) {
		t.Fatalf("version status=%d body=%s", status, body)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 0 || loaded.Node.DisplayName != "" {
		t.Fatalf("rejected domain request mutated config: %+v", loaded)
	}
}

func TestDomainProfilePutAcceptsValueWithoutServerPathAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	credential := "browser-supplied-secret"
	request := controlapi.XrayProfilePutRequest{
		DomainMutationRequest: domainMutationRequest(0, "40000000000000000000000000000000"),
		ID:                    "socks",
		Kind:                  "socks",
		Username:              "operator",
		Credential:            credential,
	}
	status, body := requestDomain(t, server, http.MethodPut, controlapi.DomainXrayProfilesPath, request)
	if status != http.StatusOK {
		t.Fatalf("profile put status=%d body=%s", status, body)
	}
	if bytes.Contains(body, []byte(credential)) || bytes.Contains(body, []byte(`"password"`)) || bytes.Contains(body, []byte(`"credential"`)) {
		t.Fatalf("profile mutation exposed credential material: %s", body)
	}
	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.XrayProfiles["socks"].SOCKS == nil || loaded.XrayProfiles["socks"].SOCKS.Username != "operator" || loaded.XrayProfiles["socks"].SOCKS.Password != credential {
		t.Fatalf("profile was not stored from the typed credential value: %+v", loaded.XrayProfiles["socks"])
	}

	status, body = requestDomain(t, server, http.MethodPut, controlapi.DomainXrayProfilesPath, map[string]any{
		"api_version": 1, "revision": 1, "dry_run": false,
		"request_id": "40000000000000000000000000000001", "id": "bad", "kind": "socks",
		"username": "operator", "credential_file": filepath.Join(t.TempDir(), "must-not-read"),
	})
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte(`"error_code":"domain.request_invalid"`)) {
		t.Fatalf("server-side credential path status=%d body=%s", status, body)
	}
}

func TestDomainProfileValidationNeverEchoesCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	server := startTestServer(t, path)
	credential := "credential-value-that-must-never-echo"
	request := controlapi.XrayProfilePutRequest{
		DomainMutationRequest:  domainMutationRequest(0, "41000000000000000000000000000000"),
		ID:                     "vless",
		Kind:                   "vless",
		Credential:             credential,
		Transport:              "tcp",
		Security:               "none",
		AllowInsecurePlaintext: true,
	}
	status, body := requestDomain(t, server, http.MethodPut, controlapi.DomainXrayProfilesPath, request)
	if status != http.StatusUnprocessableEntity || bytes.Contains(body, []byte(credential)) ||
		!bytes.Contains(body, []byte(`"message":"profile validation failed; credential details were redacted"`)) {
		t.Fatalf("credential validation status=%d body=%s", status, body)
	}
}

func TestDomainCurrentWebOperationsEndToEndWithoutCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	provider := &domainTestProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, err := Start(ctx, "127.0.0.1:0", path, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.execute = func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult {
		panic("domain operation invoked CLI")
	}

	revision := int64(0)
	mutate := func(method, route string, request any) []byte {
		t.Helper()
		status, body := requestDomain(t, server, method, route, request)
		if status != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", method, route, status, body)
		}
		if !bytes.Contains(body, []byte(`"applied":true`)) ||
			!bytes.Contains(body, []byte(`"outcome":"applied"`)) {
			t.Fatalf("%s %s omitted applied mutation outcome: %s", method, route, body)
		}
		assertNoCLIEnvelope(t, body)
		return body
	}

	mutate(http.MethodPost, controlapi.DomainIdentityInitPath, controlapi.IdentityInitRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000000"),
		Name:                  "A",
	})
	revision++

	mutate(http.MethodPut, controlapi.DomainXrayProfilesPath, controlapi.XrayProfilePutRequest{
		DomainMutationRequest:  domainMutationRequest(revision, "50000000000000000000000000000001"),
		ID:                     "vless",
		Kind:                   "vless",
		Credential:             "11111111-1111-4111-8111-111111111111",
		Transport:              "tcp",
		Security:               "none",
		AllowInsecurePlaintext: true,
	})
	revision++

	mutate(http.MethodPut, controlapi.DomainXrayProfilesPath, controlapi.XrayProfilePutRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000002"),
		ID:                    "socks",
		Kind:                  "socks",
		Credential:            "terminal-password",
		Username:              "terminal",
	})
	revision++

	mutate(http.MethodPost, controlapi.DomainPeersPath, controlapi.PeerCreateRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000003"),
		Name:                  "B",
		NodeID:                "peer-b",
		Addr:                  "127.0.0.1:24443",
		Direction:             "outbound",
		XrayProfileID:         "vless",
	})
	revision++

	listen := "127.0.0.1:21080"
	profileID := "socks"
	exitPeer := "B"
	mutate(http.MethodPut, controlapi.DomainInboundsPath, controlapi.InboundPutRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000004"),
		Kind:                  "socks",
		Listen:                &listen,
		XrayProfileID:         &profileID,
		ExitPeer:              &exitPeer,
	})
	revision++

	for _, read := range []string{
		controlapi.DomainLocalPath,
		controlapi.DomainIdentityPath,
		controlapi.DomainSettingsPath,
		controlapi.DomainPeersPath,
		controlapi.DomainInboundsPath,
		controlapi.DomainXrayProfilesPath,
	} {
		status, body := requestDomain(t, server, http.MethodGet, read, nil)
		if status != http.StatusOK || !bytes.Contains(body, []byte(fmt.Sprintf(`"revision":%d`, revision))) {
			t.Fatalf("GET %s status=%d body=%s", read, status, body)
		}
		assertNoCLIEnvelope(t, body)
	}

	status, body := requestDomain(t, server, http.MethodPost, controlapi.DomainProfileValidatePath, controlapi.XrayProfileValidateRequest{
		DomainRequest: controlapi.DomainRequest{APIVersion: controlapi.DomainAPIVersion},
		ID:            "vless",
	})
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"profile":"vless"`)) {
		t.Fatalf("profile validate status=%d body=%s", status, body)
	}

	status, body = requestDomain(t, server, http.MethodPost, controlapi.DomainPathCompilePath, controlapi.PathCompileRequest{
		DomainRequest: controlapi.DomainRequest{APIVersion: controlapi.DomainAPIVersion},
		Expression:    "peer-b",
		Strategy:      "selector",
		EndpointKind:  "egress",
	})
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"final_peer":"peer-b"`)) {
		t.Fatalf("path compile status=%d body=%s", status, body)
	}
	assertNoCLIEnvelope(t, body)

	nested := true
	mutate(http.MethodPatch, controlapi.DomainPeersPath, controlapi.PeerUpdateRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000005"),
		Name:                  "B",
		Patch:                 controlapi.PeerPatch{NestedEnabled: &nested},
	})
	revision++
	mutate(http.MethodPatch, controlapi.DomainInboundStatePath, controlapi.InboundStateRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000006"),
		Kind:                  "socks",
		Enabled:               false,
		Reason:                "maintenance",
	})
	revision++
	mutate(http.MethodPatch, controlapi.DomainPeerStatePath, controlapi.PeerStateRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000007"),
		Name:                  "B",
		Enabled:               false,
		Reason:                "maintenance",
	})
	revision++
	status, body = requestDomain(t, server, http.MethodPost, controlapi.DomainPathCompilePath, controlapi.PathCompileRequest{
		DomainRequest: controlapi.DomainRequest{APIVersion: controlapi.DomainAPIVersion},
		Expression:    "peer-b",
		Strategy:      "selector",
		EndpointKind:  "egress",
	})
	if status != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte(`"error_code":"path.edge_disabled"`)) ||
		bytes.Contains(body, []byte(`"error_code":"route.compile_failed"`)) {
		t.Fatalf("disabled path compile status=%d body=%s", status, body)
	}

	level := "debug"
	mutate(http.MethodPatch, controlapi.DomainSettingsPath, controlapi.SettingsUpdateRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000008"),
		Settings:              controlapi.SettingsPatch{LogLevel: &level},
	})
	revision++
	mutate(http.MethodPatch, controlapi.DomainIdentityPath, controlapi.IdentityRenameRequest{
		DomainMutationRequest: domainMutationRequest(revision, "50000000000000000000000000000009"),
		Name:                  "A-renamed",
	})
	revision++

	status, body = requestDomain(t, server, http.MethodPost, controlapi.DomainRuntimeReloadPath, controlapi.RuntimeReloadRequest{
		DomainMutationRequest: domainMutationRequest(revision, "5000000000000000000000000000000a"),
	})
	if status != http.StatusOK || !bytes.Contains(body, []byte(fmt.Sprintf(`"applied_revision":%d`, revision))) ||
		!bytes.Contains(body, []byte(`"applied":true`)) ||
		!bytes.Contains(body, []byte(`"outcome":"applied"`)) {
		t.Fatalf("runtime reload status=%d body=%s", status, body)
	}
	if provider.reloads.Load() != 1 {
		t.Fatalf("reload calls=%d, want 1", provider.reloads.Load())
	}

	status, body = requestDomain(t, server, http.MethodDelete, controlapi.DomainXrayProfilesPath, controlapi.XrayProfileRemoveRequest{
		DomainMutationRequest: domainMutationRequest(revision, "5000000000000000000000000000000b"),
		ID:                    "vless",
	})
	if status != http.StatusConflict || !bytes.Contains(body, []byte(`"error_code":"config.in_use"`)) {
		t.Fatalf("in-use profile removal status=%d body=%s", status, body)
	}

	loaded, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != revision || loaded.Node.DisplayName != "A-renamed" || loaded.System.LogLevel != "debug" ||
		len(loaded.Peers) != 1 || loaded.Peers[0].Enabled || !loaded.Peers[0].NestedEnabled ||
		len(loaded.NodeInbound) != 1 || loaded.NodeInbound[0].Enabled {
		t.Fatalf("unexpected final domain state: %+v", loaded)
	}
	if server.commandIngress.Load() != 0 || server.commandExecutions.Load() != 0 {
		t.Fatalf("end-to-end domain sequence entered CLI: ingress=%d executions=%d", server.commandIngress.Load(), server.commandExecutions.Load())
	}
}

func TestInboundPutPreservesDisabledStateAndOmittedFields(t *testing.T) {
	cfg := configstore.DefaultConfig()
	cfg.NodeInbound = []configstore.InboundConfig{{
		Kind: "socks", Purpose: "user", Listen: "127.0.0.1:1080",
		XrayProfileID: "socks", ExitPeer: "B", Enabled: false,
		DisabledCause: "maintenance",
	}}
	listen := "127.0.0.1:2080"
	result, err := inboundPutMutation(controlapi.InboundPutRequest{
		Kind:   "socks",
		Listen: &listen,
	})(&cfg, false, &domainMutationEffects{})
	if err != nil {
		t.Fatal(err)
	}
	inbound := cfg.NodeInbound[0]
	if inbound.Enabled || inbound.DisabledCause != "maintenance" ||
		inbound.Listen != listen || inbound.XrayProfileID != "socks" || inbound.ExitPeer != "B" {
		t.Fatalf("partial inbound update changed omitted fields or administrative state: %+v", inbound)
	}
	resultMap, ok := result.(map[string]any)
	if !ok || resultMap["inbound"] != inbound {
		t.Fatalf("mutation result does not report preserved inbound: %#v", result)
	}
}

func domainMutationRequest(revision int64, requestID string) controlapi.DomainMutationRequest {
	return controlapi.DomainMutationRequest{
		APIVersion: controlapi.DomainAPIVersion,
		Revision:   revision,
		RequestID:  requestID,
	}
}

func requestDomain(t *testing.T, server *Server, method, path string, payload any) (int, []byte) {
	t.Helper()
	status, body, err := requestDomainRaw(server, method, path, payload)
	if err != nil {
		t.Fatal(err)
	}
	return status, body
}

func requestDomainRaw(server *Server, method, path string, payload any) (int, []byte, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
	}
	return requestDomainBytes(server, method, path, body)
}

func requestDomainBytes(server *Server, method, path string, body []byte) (int, []byte, error) {
	return controlapi.AuthenticatedRequestContext(
		context.Background(), server.Addr(), controlapi.TokenPath(server.configPath), method, path, body,
	)
}

func assertNoCLIEnvelope(t *testing.T, body []byte) {
	t.Helper()
	for _, field := range []string{`"args"`, `"argv"`, `"stdout"`, `"stderr"`, `"exit_code"`} {
		if bytes.Contains(body, []byte(field)) {
			t.Fatalf("typed domain response contained CLI field %s: %s", field, body)
		}
	}
}
