package controlserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/FrankoonG/x-tier/internal/configops"
	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/localview"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

type cachedDomainResponse struct {
	fingerprint [sha256.Size]byte
	result      domainResult
	done        chan struct{}
	completed   bool
	protected   bool
}

type domainResult struct {
	status       int
	body         []byte
	applied      *bool
	outcome      controlapi.MutationOutcome
	preparations []controlapi.MutationPreparation
}

type domainMutationEffects struct {
	preparations []controlapi.MutationPreparation
}

type domainMutation func(*configstore.Config, bool, *domainMutationEffects) (any, error)
type domainIdentityCreator func(string) (*identity.Identity, error)
type domainStoreIdentityCreator func(*statestore.Store) (*identity.Identity, error)

func createDomainIdentity(path string) (*identity.Identity, error) {
	return identity.Create(path)
}

func createDomainStoreIdentity(store *statestore.Store) (*identity.Identity, error) {
	return identity.CreateStore(store)
}

type domainFailure struct {
	code string
	err  error
}

func (e domainFailure) Error() string           { return e.code + ": " + e.err.Error() }
func (e domainFailure) Unwrap() error           { return e.err }
func (e domainFailure) PublicErrorCode() string { return e.code }

type publicXrayProfile struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"`
	CredentialGroupID string `json:"credential_group_id,omitempty"`
}

const (
	identityStateUninitialized  = "uninitialized"
	identityStateBacked         = "backed"
	identityStateRecoverable    = "recoverable"
	identityStateLegacyUnbacked = "legacy_unbacked"
	identityStateBackingMissing = "backing_missing"
	identityStateMismatch       = "mismatch"
)

type domainIdentityView struct {
	State                 string `json:"state"`
	Version               int    `json:"version,omitempty"`
	Algorithm             string `json:"algorithm,omitempty"`
	NodeID                string `json:"node_id,omitempty"`
	PublicKey             string `json:"public_key,omitempty"`
	BackingNodeID         string `json:"backing_node_id,omitempty"`
	BackingPublicKey      string `json:"backing_public_key,omitempty"`
	OSACLReleaseQualified bool   `json:"os_acl_release_qualified"`
}

func (s *Server) registerDomainRoutes(mux *http.ServeMux) {
	registered := make(map[string]bool)
	for _, domainRoute := range controlapi.DomainRoutes() {
		if registered[domainRoute.Path] {
			continue
		}
		registered[domainRoute.Path] = true
		mux.HandleFunc(domainRoute.Path, s.authenticated(maxRequestBodyBytes, s.handleDomain))
	}
}

func (s *Server) handleDomain(w http.ResponseWriter, r *http.Request) {
	s.domainIngress.Add(1)
	if _, ok := controlapi.LookupDomainRoute(r.URL.Path, r.Method); !ok {
		if controlapi.IsDomainPath(r.URL.Path) {
			w.Header().Set("Allow", domainAllowedMethods(r.URL.Path))
			writeDomainResult(w, domainErrorResult(domainFailure{code: "domain.method_not_allowed", err: fmt.Errorf("method %s is not allowed", r.Method)}, http.StatusMethodNotAllowed))
			return
		}
		writeDomainResult(w, domainErrorResult(domainFailure{code: "domain.not_found", err: fmt.Errorf("resource is not available")}, http.StatusNotFound))
		return
	}

	switch {
	case r.Method == http.MethodGet:
		s.handleDomainRead(w, r)
	case r.URL.Path == controlapi.DomainProfileValidatePath:
		s.handleProfileValidate(w, r)
	case r.URL.Path == controlapi.DomainPathCompilePath:
		s.handlePathCompile(w, r)
	case r.URL.Path == controlapi.DomainRuntimeReloadPath:
		s.handleRuntimeReload(w, r)
	case r.URL.Path == controlapi.DomainConfigRestorePath:
		s.handleConfigRestore(w, r)
	default:
		s.handleDomainMutation(w, r)
	}
}

func domainAllowedMethods(path string) string {
	methods := make([]string, 0, 2)
	for _, domainRoute := range controlapi.DomainRoutes() {
		if domainRoute.Path == path {
			methods = append(methods, domainRoute.Method)
		}
	}
	return strings.Join(methods, ", ")
}

func (s *Server) handleDomainRead(w http.ResponseWriter, r *http.Request) {
	if err := requireEmptyDomainBody(r.Body); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	cfg, err := s.loadDomainConfig()
	if err != nil {
		writeDomainResult(w, domainErrorResult(err, 0))
		return
	}
	s.domainExecutions.Add(1)

	var payload map[string]any
	switch r.URL.Path {
	case controlapi.DomainLocalPath:
		identityStatus, err := s.inspectDomainIdentityView(cfg)
		if err != nil {
			writeDomainResult(w, domainErrorResult(err, 0))
			return
		}
		topology := localview.TopologyFromConfig(cfg)
		relations := route.PeerRelations(topology)
		local := relations[route.NodeID(cfg.Node.NodeID)]
		payload = map[string]any{
			"ok":            true,
			"revision":      cfg.Revision,
			"status_source": "config_only",
			"identity":      identityStatus,
			"node":          domainNodeView(cfg.Node),
			"display_name":  cfg.Node.DisplayName,
			"settings":      cfg.System,
			"runtime":       map[string]any{"available": false, "source": "config_only"},
			"peer_counts": map[string]int{
				"inbound":       len(local.Inbound),
				"outbound":      len(local.Outbound),
				"bidirectional": len(local.Bidirectional),
			},
			"inbounds": cfg.NodeInbound,
		}
	case controlapi.DomainIdentityPath:
		identityStatus, err := s.inspectDomainIdentityView(cfg)
		if err != nil {
			writeDomainResult(w, domainErrorResult(err, 0))
			return
		}
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "identity": identityStatus, "node": domainNodeView(cfg.Node)}
	case controlapi.DomainSettingsPath:
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "settings": cfg.System}
	case controlapi.DomainPeersPath:
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID, "peers": cfg.Peers}
	case controlapi.DomainNodeEgressGrantsPath:
		payload = map[string]any{
			"ok":                   true,
			"revision":             cfg.Revision,
			"target_local_node_id": cfg.Node.NodeID,
			"node_egress_grants":   domainNodeEgressGrantViews(cfg.NodeEgressGrants),
		}
	case controlapi.DomainInboundsPath:
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID, "inbounds": cfg.NodeInbound}
	case controlapi.DomainXrayProfilesPath:
		profiles := domainProfileViews(cfg.XrayProfiles)
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "xray_profiles": profiles}
	default:
		writeDomainResult(w, domainErrorResult(domainFailure{code: "domain.not_found", err: fmt.Errorf("resource is not available")}, http.StatusNotFound))
		return
	}
	writeDomainResult(w, domainJSONResult(http.StatusOK, payload))
}

func (s *Server) handleProfileValidate(w http.ResponseWriter, r *http.Request) {
	var request controlapi.XrayProfileValidateRequest
	if _, err := decodeDomainBody(r.Body, &request); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	if err := validateDomainVersion(request.APIVersion); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	cfg, err := s.loadDomainConfig()
	if err != nil {
		writeDomainResult(w, domainErrorResult(err, 0))
		return
	}
	if request.ID != "" {
		profile, ok := cfg.XrayProfiles[request.ID]
		if !ok {
			writeDomainResult(w, domainErrorResult(domainFailure{code: "config.profile_unknown", err: fmt.Errorf("%s", request.ID)}, http.StatusNotFound))
			return
		}
		if err := xrayconfig.CompileProfile(profile); err != nil {
			writeDomainResult(w, domainErrorResult(domainFailure{code: "config.profile_invalid", err: err}, http.StatusUnprocessableEntity))
			return
		}
	} else {
		for _, profile := range cfg.XrayProfiles {
			if err := xrayconfig.CompileProfile(profile); err != nil {
				writeDomainResult(w, domainErrorResult(domainFailure{code: "config.profile_invalid", err: fmt.Errorf("%s: %w", profile.ID, err)}, http.StatusUnprocessableEntity))
				return
			}
		}
	}
	s.domainExecutions.Add(1)
	writeDomainResult(w, domainJSONResult(http.StatusOK, map[string]any{"ok": true, "revision": cfg.Revision, "profile": request.ID}))
}

func (s *Server) handlePathCompile(w http.ResponseWriter, r *http.Request) {
	var request controlapi.PathCompileRequest
	if _, err := decodeDomainBody(r.Body, &request); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	if err := validateDomainVersion(request.APIVersion); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	cfg, err := s.loadDomainConfig()
	if err != nil {
		writeDomainResult(w, domainErrorResult(err, 0))
		return
	}
	compiled, err := route.Compile(localview.TopologyFromConfig(cfg), route.RouteIntent{
		Paths:        splitDomainCSV(request.Expression),
		Strategy:     route.Strategy(request.Strategy),
		EndpointKind: route.EndpointKind(request.EndpointKind),
	})
	if err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusUnprocessableEntity))
		return
	}
	s.domainExecutions.Add(1)
	writeDomainResult(w, domainJSONResult(http.StatusOK, map[string]any{"ok": true, "revision": cfg.Revision, "compiled": compiled}))
}

func (s *Server) handleRuntimeReload(w http.ResponseWriter, r *http.Request) {
	var request controlapi.RuntimeReloadRequest
	raw, err := decodeDomainBody(r.Body, &request)
	if err != nil {
		writeDomainResult(w, domainMutationErrorResult(err, http.StatusBadRequest, false, nil))
		return
	}
	if err := validateDomainMutationRequest(request.DomainMutationRequest); err != nil {
		writeDomainResult(w, domainMutationErrorResult(err, http.StatusBadRequest, request.DryRun, nil))
		return
	}
	s.executeCachedDomainMutation(w, r, request.DomainMutationRequest, raw, func() domainResult {
		s.domainExecutions.Add(1)
		ctx, cancel := s.mutationContext()
		defer cancel()
		if !request.DryRun {
			release, err := s.acquireReloadMutation(ctx)
			if err != nil {
				return mutationAdmissionDomainResult(err)
			}
			defer release()
		}
		reloader, ok := s.status.(RuntimeReloader)
		if !ok {
			return domainMutationErrorResult(
				domainFailure{code: "service.reload_unavailable", err: fmt.Errorf("runtime reloader is unavailable")},
				http.StatusServiceUnavailable,
				request.DryRun,
				nil,
			)
		}
		status, reloadErr := reloader.Reload(ctx, request.Revision, request.DryRun)
		if request.DryRun {
			if reloadErr != nil {
				return domainMutationErrorResult(reloadErr, 0, true, nil)
			}
		} else if classifiedErr, applied, outcome := classifyReloadMutation(status, reloadErr, request.Revision); classifiedErr != nil {
			failure := domainFailure{
				code: runtimeCommandErrorCode(classifiedErr),
				err:  errors.New(sanitizeRuntimeCommandError(classifiedErr)),
			}
			httpStatus := 0
			if failure.code == "service.reload_result_invalid" {
				httpStatus = http.StatusInternalServerError
			}
			return domainMutationErrorResultWithOutcome(failure, httpStatus, applied, outcome, nil)
		}
		return domainMutationJSONResult(request.DryRun, map[string]any{
			"ok":                   true,
			"changed":              true,
			"dry_run":              request.DryRun,
			"before_revision":      request.Revision,
			"after_revision":       request.Revision,
			"applied_revision":     status.AppliedRevision,
			"attempted_revision":   status.AttemptedRevision,
			"reconciliation_state": status.State,
		})
	})
}

func (s *Server) handleConfigRestore(w http.ResponseWriter, r *http.Request) {
	var request controlapi.ConfigRestoreRequest
	raw, err := decodeDomainBody(r.Body, &request)
	if err != nil {
		writeDomainResult(w, domainMutationErrorResult(err, http.StatusBadRequest, false, nil))
		return
	}
	if err := validateDomainMutationRequest(request.DomainMutationRequest); err != nil {
		writeDomainResult(w, domainMutationErrorResult(err, http.StatusBadRequest, request.DryRun, nil))
		return
	}
	s.executeCachedDomainMutation(w, r, request.DomainMutationRequest, raw, func() domainResult {
		s.domainExecutions.Add(1)
		if !request.DryRun {
			ctx, cancel := s.mutationContext()
			defer cancel()
			release, err := s.acquireRestoreMutation(ctx)
			if err != nil {
				return mutationAdmissionDomainResult(err)
			}
			defer release()
		}
		result, err := s.restoreLastKnownGood(request.Revision, request.DryRun)
		if err != nil {
			return domainMutationErrorResult(err, 0, request.DryRun, nil)
		}
		return domainMutationJSONResult(request.DryRun, map[string]any{
			"ok":              true,
			"changed":         true,
			"dry_run":         request.DryRun,
			"before_revision": result.BeforeRevision,
			"after_revision":  result.AfterRevision,
			"result": map[string]any{
				"source":   "last-known-good",
				"restored": !request.DryRun,
			},
		})
	})
}

func (s *Server) handleDomainMutation(w http.ResponseWriter, r *http.Request) {
	var (
		meta     controlapi.DomainMutationRequest
		raw      []byte
		mutation domainMutation
		err      error
	)

	switch {
	case r.URL.Path == controlapi.DomainIdentityInitPath:
		var request controlapi.IdentityInitRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = s.identityInitMutation(request)
	case r.URL.Path == controlapi.DomainIdentityPath:
		var request controlapi.IdentityRenameRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = identityRenameMutation(request)
	case r.URL.Path == controlapi.DomainSettingsPath:
		var request controlapi.SettingsUpdateRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = settingsMutation(request)
	case r.URL.Path == controlapi.DomainInboundsPath:
		var request controlapi.InboundPutRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = inboundPutMutation(request)
	case r.URL.Path == controlapi.DomainInboundStatePath:
		var request controlapi.InboundStateRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = inboundStateMutation(request)
	case r.URL.Path == controlapi.DomainPeersPath && r.Method == http.MethodPost:
		var request controlapi.PeerCreateRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = peerCreateMutation(request)
	case r.URL.Path == controlapi.DomainPeersPath && r.Method == http.MethodPatch:
		var request controlapi.PeerUpdateRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = peerUpdateMutation(request)
	case r.URL.Path == controlapi.DomainPeersPath && r.Method == http.MethodDelete:
		var request controlapi.PeerRemoveRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = peerRemoveMutation(request)
	case r.URL.Path == controlapi.DomainPeerStatePath:
		var request controlapi.PeerStateRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = peerStateMutation(request)
	case r.URL.Path == controlapi.DomainNodeEgressGrantsPath && r.Method == http.MethodPut:
		var request controlapi.NodeEgressGrantPutRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = nodeEgressGrantPutMutation(request)
	case r.URL.Path == controlapi.DomainNodeEgressGrantsPath && r.Method == http.MethodDelete:
		var request controlapi.NodeEgressGrantRevokeRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = nodeEgressGrantRevokeMutation(request)
	case r.URL.Path == controlapi.DomainXrayProfilesPath && r.Method == http.MethodPut:
		var request controlapi.XrayProfilePutRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = profilePutMutation(request)
	case r.URL.Path == controlapi.DomainXrayProfilesPath && r.Method == http.MethodDelete:
		var request controlapi.XrayProfileRemoveRequest
		raw, err = decodeDomainBody(r.Body, &request)
		meta = request.DomainMutationRequest
		mutation = profileRemoveMutation(request)
	default:
		err = domainFailure{code: "domain.not_found", err: fmt.Errorf("mutation is not available")}
	}
	if err != nil {
		writeDomainResult(w, domainMutationErrorResult(err, http.StatusBadRequest, false, nil))
		return
	}
	if err := validateDomainMutationRequest(meta); err != nil {
		writeDomainResult(w, domainMutationErrorResult(err, http.StatusBadRequest, meta.DryRun, nil))
		return
	}
	s.executeCachedDomainMutation(w, r, meta, raw, func() domainResult {
		return s.executeConfigDomainMutation(meta, mutation)
	})
}

func (s *Server) executeCachedDomainMutation(w http.ResponseWriter, r *http.Request, meta controlapi.DomainMutationRequest, raw []byte, execute func() domainResult) {
	if !s.beginCommand() {
		writeDomainResult(w, domainMutationErrorResult(
			domainFailure{code: "control.stopping", err: fmt.Errorf("control server is stopping")},
			http.StatusServiceUnavailable,
			meta.DryRun,
			nil,
		))
		return
	}
	defer s.commands.Done()

	if meta.DryRun {
		result := executeDomainSafely(execute, false)
		writeDomainResult(w, result)
		return
	}
	fingerprint := sha256.Sum256(append([]byte(r.Method+"\x00"+r.URL.Path+"\x00"), raw...))
	entry, leader, status := s.claimDomainRequest(meta.RequestID, fingerprint)
	if status != 0 {
		code := "domain.request_id_conflict"
		if status == http.StatusServiceUnavailable {
			code = "domain.idempotency_capacity_exhausted"
		}
		writeDomainResult(w, domainMutationErrorResult(
			domainFailure{code: code, err: fmt.Errorf("request_id cannot be claimed")},
			status,
			false,
			nil,
		))
		return
	}
	if !leader {
		writeDomainResult(w, waitDomainResult(r.Context(), entry))
		return
	}
	result := executeDomainSafely(execute, true)
	s.completeDomainRequest(meta.RequestID, entry, result)
	writeDomainResult(w, result)
}

func executeDomainSafely(execute func() domainResult, mutation bool) (result domainResult) {
	defer func() {
		if recover() != nil {
			failure := domainFailure{code: "domain.execution_failed", err: errors.New("domain operation did not return")}
			if mutation {
				failure.code = "domain.execution_indeterminate"
				result = domainMutationErrorResultWithOutcome(
					failure,
					http.StatusInternalServerError,
					false,
					controlapi.MutationOutcomeIndeterminate,
					nil,
				)
				return
			}
			result = domainErrorResult(failure, http.StatusInternalServerError)
		}
	}()
	return execute()
}

func waitDomainResult(ctx context.Context, entry *cachedDomainResponse) domainResult {
	select {
	case <-entry.done:
		return entry.result
	case <-ctx.Done():
		return domainMutationErrorResultWithOutcome(
			domainFailure{code: "domain.request_canceled", err: errors.New("request canceled while awaiting the original execution")},
			http.StatusRequestTimeout,
			false,
			controlapi.MutationOutcomeIndeterminate,
			nil,
		)
	}
}

func (s *Server) executeConfigDomainMutation(meta controlapi.DomainMutationRequest, mutation domainMutation) domainResult {
	s.domainExecutions.Add(1)
	effects := &domainMutationEffects{}
	if meta.DryRun {
		cfg, err := s.loadDomainConfig()
		if err != nil {
			return domainErrorResult(err, 0)
		}
		before := cfg.Revision
		if err := configstore.ValidateRevision(cfg, meta.Revision); err != nil {
			return domainErrorResult(err, 0)
		}
		if before == math.MaxInt64 {
			return domainMutationErrorResult(
				domainFailure{code: "config.revision_exhausted", err: errors.New("configuration revision is exhausted")},
				0,
				true,
				nil,
			)
		}
		payload, err := mutation(&cfg, true, effects)
		if err != nil {
			return domainMutationErrorResult(err, 0, true, nil)
		}
		cfg.Revision = before
		if err := configstore.Validate(cfg); err != nil {
			return domainErrorResult(err, http.StatusUnprocessableEntity)
		}
		return domainMutationResult(true, before, before+1, payload)
	}

	ctx, cancel := s.mutationContext()
	defer cancel()
	release, err := s.acquireConfigMutation(ctx)
	if err != nil {
		return mutationAdmissionDomainResult(err)
	}
	defer release()
	var payload any
	update := configstore.UpdateCAS
	if s.stateStore != nil {
		update = func(_ string, revision int64, mutate func(*configstore.Config) error) (configstore.UpdateResult, error) {
			return configstore.UpdateStoreCAS(s.stateStore, revision, mutate)
		}
	} else if s.ownershipKey != "" {
		update = func(path string, revision int64, mutate func(*configstore.Config) error) (configstore.UpdateResult, error) {
			return configstore.UpdatePinnedCAS(path, s.ownershipKey, revision, mutate)
		}
	}
	result, err := update(s.configPath, meta.Revision, func(cfg *configstore.Config) error {
		var mutateErr error
		payload, mutateErr = mutation(cfg, false, effects)
		return mutateErr
	})
	if err != nil {
		if configstore.CommitVisible(err) && result.AfterRevision > meta.Revision {
			if _, supported, reconcileErr := s.reconcileCommittedRevision(ctx, result.AfterRevision); supported && reconcileErr != nil {
				return committedRevisionDomainError(reconcileErr, effects.preparations)
			}
		}
		return domainMutationErrorResult(err, 0, false, effects.preparations)
	}
	if _, supported, reconcileErr := s.reconcileCommittedRevision(ctx, result.AfterRevision); supported && reconcileErr != nil {
		return committedRevisionDomainError(reconcileErr, effects.preparations)
	}
	return domainMutationResult(false, result.BeforeRevision, result.AfterRevision, payload)
}

func committedRevisionDomainError(err error, preparations []controlapi.MutationPreparation) domainResult {
	failure := domainFailure{
		code: runtimeCommandErrorCode(err),
		err:  errors.New(sanitizeRuntimeCommandError(err)),
	}
	status := 0
	if failure.code == "service.reload_result_invalid" {
		status = http.StatusInternalServerError
	}
	return domainMutationErrorResultWithOutcome(
		failure,
		status,
		true,
		controlapi.MutationOutcomeApplied,
		preparations,
	)
}

func domainMutationResult(dryRun bool, before, after int64, payload any) domainResult {
	return domainMutationJSONResult(dryRun, map[string]any{
		"ok":              true,
		"changed":         true,
		"dry_run":         dryRun,
		"before_revision": before,
		"after_revision":  after,
		"result":          payload,
	})
}

func mutationAdmissionDomainResult(err error) domainResult {
	code := "control.mutation_timeout"
	message := "mutation did not enter execution before its deadline"
	if errors.Is(err, context.Canceled) {
		code = "control.stopping"
		message = "control server stopped before mutation execution"
	}
	return domainMutationErrorResult(
		domainFailure{code: code, err: errors.New(message)},
		http.StatusServiceUnavailable,
		false,
		nil,
	)
}

func (s *Server) identityInitMutation(request controlapi.IdentityInitRequest) domainMutation {
	return func(cfg *configstore.Config, dryRun bool, effects *domainMutationEffects) (any, error) {
		observed, backing, err := s.inspectDomainIdentity(*cfg)
		if err != nil {
			return nil, err
		}
		id := backing
		created := false
		switch observed.State {
		case identityStateUninitialized:
			if dryRun {
				proposed := domainNodeView(cfg.Node)
				proposed.DisplayName = request.Name
				proposed.RendrCapable = true
				return map[string]any{"identity": observed, "node": proposed, "would_create_backing": true}, nil
			}
			if s.stateStore != nil {
				creator := s.createStoreIdentity
				if creator == nil {
					creator = createDomainStoreIdentity
				}
				id, err = creator(s.stateStore)
			} else {
				creator := s.createIdentity
				if creator == nil {
					creator = createDomainIdentity
				}
				id, err = creator(domainIdentitySeedPath(s.configPath))
			}
			created = err == nil
			if err != nil {
				if published, loadErr := s.loadDomainIdentity(); loadErr == nil && effects != nil {
					public := published.Public()
					effects.preparations = append(effects.preparations, controlapi.MutationPreparation{
						Kind:   "identity_backing",
						State:  identityStateRecoverable,
						NodeID: public.NodeID.String(),
					})
				}
			}
		case identityStateRecoverable:
		case identityStateBacked:
			return nil, domainFailure{code: "identity.exists", err: fmt.Errorf("node identity is already initialized and backed")}
		case identityStateLegacyUnbacked:
			return nil, domainFailure{code: "identity.legacy_unbacked", err: fmt.Errorf("configured identity has no cryptographic backing")}
		case identityStateBackingMissing:
			return nil, domainFailure{code: "identity.backing_missing", err: fmt.Errorf("configured v2 identity is missing its seed backing")}
		case identityStateMismatch:
			return nil, domainFailure{code: "identity.config_mismatch", err: fmt.Errorf("configured identity does not match cryptographic backing")}
		}
		if err != nil {
			return nil, err
		}
		public := id.Public()
		if created && effects != nil {
			effects.preparations = append(effects.preparations, controlapi.MutationPreparation{
				Kind:   "identity_backing",
				State:  identityStateRecoverable,
				NodeID: public.NodeID.String(),
			})
		}
		cfg.Node.NodeID = public.NodeID.String()
		cfg.Node.PublicKey = public.PublicKey
		if request.Name != "" {
			cfg.Node.DisplayName = request.Name
		} else if cfg.Node.DisplayName == "" {
			cfg.Node.DisplayName = public.NodeID.String()
		}
		cfg.Node.RendrCapable = true
		return map[string]any{
			"identity":  domainIdentityViewFromPublic(identityStateBacked, public),
			"node":      domainNodeView(cfg.Node),
			"created":   created,
			"recovered": !created,
		}, nil
	}
}

func identityRenameMutation(request controlapi.IdentityRenameRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		if request.Name == "" {
			return nil, domainFailure{code: "identity.name_required", err: fmt.Errorf("display name is required")}
		}
		cfg.Node.DisplayName = request.Name
		return map[string]any{"node": domainNodeView(cfg.Node)}, nil
	}
}

func settingsMutation(request controlapi.SettingsUpdateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		patch := request.Settings
		if patch.LogLevel == nil && patch.MaxNestedDepth == nil && patch.MaxResponseNodes == nil &&
			patch.MaxResponseBytes == nil && patch.MaxCacheEntries == nil && patch.MaxFetchFanOut == nil {
			return nil, domainFailure{code: "settings.patch_empty", err: fmt.Errorf("at least one setting is required")}
		}
		if patch.LogLevel != nil {
			cfg.System.LogLevel = *patch.LogLevel
		}
		if patch.MaxNestedDepth != nil {
			cfg.System.MaxNestedDepth = *patch.MaxNestedDepth
		}
		if patch.MaxResponseNodes != nil {
			cfg.System.MaxResponseNodes = *patch.MaxResponseNodes
		}
		if patch.MaxResponseBytes != nil {
			cfg.System.MaxResponseBytes = *patch.MaxResponseBytes
		}
		if patch.MaxCacheEntries != nil {
			cfg.System.MaxCacheEntries = *patch.MaxCacheEntries
		}
		if patch.MaxFetchFanOut != nil {
			cfg.System.MaxFetchFanOut = *patch.MaxFetchFanOut
		}
		return map[string]any{"settings": cfg.System}, nil
	}
}

func inboundPutMutation(request controlapi.InboundPutRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		if request.Kind == "" {
			return nil, domainFailure{code: "config.inbound_kind_required", err: fmt.Errorf("inbound kind is required")}
		}
		if request.Kind == "node-vless" && request.XrayProfileID != nil {
			return nil, domainFailure{code: "config.inbound_profile_forbidden", err: fmt.Errorf("node-vless credentials are configured on inbound peers")}
		}
		index := domainInboundIndex(cfg.NodeInbound, request.Kind)
		inbound := configstore.InboundConfig{Kind: request.Kind, Enabled: true}
		if index >= 0 {
			inbound = cfg.NodeInbound[index]
		}
		if request.Listen != nil {
			inbound.Listen = *request.Listen
		}
		if request.XrayProfileID != nil {
			inbound.XrayProfileID = *request.XrayProfileID
		}
		if request.Purpose != nil {
			inbound.Purpose = *request.Purpose
		} else if inbound.Purpose == "" {
			switch request.Kind {
			case "socks":
				inbound.Purpose = "user"
			case "node-vless":
				inbound.Purpose = "node"
			}
		}
		if request.Kind == "node-vless" {
			inbound.XrayProfileID = ""
		}
		if request.ExitPeer != nil {
			inbound.ExitPeer = *request.ExitPeer
		}
		if index >= 0 {
			cfg.NodeInbound[index] = inbound
		} else {
			cfg.NodeInbound = append(cfg.NodeInbound, inbound)
		}
		return map[string]any{"inbound": inbound}, nil
	}
}

func inboundStateMutation(request controlapi.InboundStateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		index := domainInboundIndex(cfg.NodeInbound, request.Kind)
		if index < 0 {
			return nil, domainFailure{code: "config.inbound_unknown", err: fmt.Errorf("%s", request.Kind)}
		}
		cfg.NodeInbound[index].Enabled = request.Enabled
		if request.Enabled {
			cfg.NodeInbound[index].DisabledCause = ""
		} else {
			reason := request.Reason
			if reason == "" {
				reason = "disabled"
			}
			cfg.NodeInbound[index].DisabledCause = reason
		}
		return map[string]any{"inbound": cfg.NodeInbound[index]}, nil
	}
}

func peerCreateMutation(request controlapi.PeerCreateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		peer := configstore.PeerConfig{
			Name:          request.Name,
			NodeID:        request.NodeID,
			DisplayName:   request.Name,
			Addr:          request.Addr,
			GatewayAddr:   request.Addr,
			Direction:     route.Direction(request.Direction),
			XrayProfileID: request.XrayProfileID,
			NestedEnabled: request.NestedEnabled,
			Enabled:       true,
			RendrCapable:  true,
		}
		result, err := configops.AddPeer(*cfg, peer)
		if err != nil {
			return nil, err
		}
		*cfg = result.Config
		return map[string]any{"peer": result.Peer}, nil
	}
}

func peerUpdateMutation(request controlapi.PeerUpdateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		var (
			peer    configstore.PeerConfig
			revoked bool
		)
		if request.Patch.Direction != nil {
			result, err := configops.UpdatePeerDirection(
				*cfg,
				request.Name,
				route.Direction(*request.Patch.Direction),
				request.Patch.RevokeNodeEgressGrant,
			)
			if err != nil {
				return nil, err
			}
			*cfg = result.Config
			peer = result.Peer
			revoked = result.NodeEgressGrantRevoked
		} else {
			if request.Patch.RevokeNodeEgressGrant {
				return nil, domainFailure{
					code: "domain.request_invalid",
					err:  fmt.Errorf("revoke_node_egress_grant requires direction"),
				}
			}
			var ok bool
			peer, _, ok = configstore.FindPeer(cfg.Peers, request.Name)
			if !ok {
				return nil, domainFailure{code: "config.peer_unknown", err: fmt.Errorf("%s", request.Name)}
			}
		}
		_, index, ok := configstore.FindPeer(cfg.Peers, peer.NodeID)
		if !ok {
			return nil, domainFailure{code: "config.peer_unknown", err: fmt.Errorf("%s", request.Name)}
		}
		if request.Patch.XrayProfileID != nil {
			result, err := configops.UpdatePeerProfile(*cfg, peer.NodeID, *request.Patch.XrayProfileID)
			if err != nil {
				return nil, err
			}
			*cfg = result.Config
			peer = result.Peer
			_, index, _ = configstore.FindPeer(cfg.Peers, peer.NodeID)
		}
		if request.Patch.Addr != nil {
			peer.Addr = *request.Patch.Addr
			peer.GatewayAddr = *request.Patch.Addr
		}
		if request.Patch.NestedEnabled != nil {
			peer.NestedEnabled = *request.Patch.NestedEnabled
		}
		cfg.Peers[index] = peer
		return map[string]any{
			"peer":                      peer,
			"node_egress_grant_revoked": revoked,
		}, nil
	}
}

func peerStateMutation(request controlapi.PeerStateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		result, err := configops.SetPeerEnabled(*cfg, request.Name, request.Enabled, request.Reason)
		if err != nil {
			return nil, err
		}
		*cfg = result.Config
		return map[string]any{"peer": result.Peer}, nil
	}
}

func peerRemoveMutation(request controlapi.PeerRemoveRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		result, err := configops.RemovePeer(*cfg, request.Name)
		if err != nil {
			return nil, err
		}
		*cfg = result.Config
		return map[string]any{
			"removed":                   result.Peer.Name,
			"node_id":                   result.Peer.NodeID,
			"node_egress_grant_revoked": result.NodeEgressGrantRevoked,
		}, nil
	}
}

func nodeEgressGrantPutMutation(request controlapi.NodeEgressGrantPutRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		if request.SourceNodeID == "" {
			return nil, domainFailure{
				code: "config.node_egress_grant_source_required",
				err:  fmt.Errorf("source_node_id is required"),
			}
		}
		peer, found := domainDirectPeerByNodeID(cfg.Peers, request.SourceNodeID)
		if !found {
			return nil, domainFailure{
				code: "config.node_egress_grant_peer_unknown",
				err:  fmt.Errorf("source peer was not found"),
			}
		}
		if !peer.Direction.CanAcceptInbound() {
			return nil, domainFailure{
				code: "config.node_egress_grant_peer_inbound_required",
				err:  fmt.Errorf("source peer cannot accept inbound traffic"),
			}
		}
		grant := configstore.NodeEgressGrant{
			SourceNodeID:      request.SourceNodeID,
			Network:           request.Network,
			AllowCIDRs:        append([]string{}, request.AllowCIDRs...),
			AllowPrivateCIDRs: append([]string{}, request.AllowPrivateCIDRs...),
			DenyCIDRs:         append([]string{}, request.DenyCIDRs...),
			AllowPorts:        domainConfigEgressPortRanges(request.AllowPorts),
		}
		if cfg.NodeEgressGrants == nil {
			cfg.NodeEgressGrants = make(map[string]configstore.NodeEgressGrant)
		}
		cfg.NodeEgressGrants[request.SourceNodeID] = grant
		return map[string]any{"node_egress_grant": domainNodeEgressGrantView(grant)}, nil
	}
}

func nodeEgressGrantRevokeMutation(request controlapi.NodeEgressGrantRevokeRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		if _, found := cfg.NodeEgressGrants[request.SourceNodeID]; !found {
			return nil, domainFailure{
				code: "config.node_egress_grant_unknown",
				err:  fmt.Errorf("node egress grant was not found"),
			}
		}
		delete(cfg.NodeEgressGrants, request.SourceNodeID)
		return map[string]any{"source_node_id": request.SourceNodeID}, nil
	}
}

func domainDirectPeerByNodeID(peers []configstore.PeerConfig, nodeID string) (configstore.PeerConfig, bool) {
	for _, peer := range peers {
		if peer.NodeID == nodeID {
			return peer, true
		}
	}
	return configstore.PeerConfig{}, false
}

func domainNodeEgressGrantViews(grants map[string]configstore.NodeEgressGrant) map[string]controlapi.NodeEgressGrant {
	views := make(map[string]controlapi.NodeEgressGrant, len(grants))
	for nodeID, grant := range grants {
		views[nodeID] = domainNodeEgressGrantView(grant)
	}
	return views
}

func domainNodeEgressGrantView(grant configstore.NodeEgressGrant) controlapi.NodeEgressGrant {
	ports := make([]controlapi.EgressPortRange, len(grant.AllowPorts))
	for index, portRange := range grant.AllowPorts {
		ports[index] = controlapi.EgressPortRange{From: portRange.From, To: portRange.To}
	}
	return controlapi.NodeEgressGrant{
		SourceNodeID:      grant.SourceNodeID,
		Network:           grant.Network,
		AllowCIDRs:        append([]string{}, grant.AllowCIDRs...),
		AllowPrivateCIDRs: append([]string{}, grant.AllowPrivateCIDRs...),
		DenyCIDRs:         append([]string{}, grant.DenyCIDRs...),
		AllowPorts:        ports,
	}
}

func domainConfigEgressPortRanges(ranges []controlapi.EgressPortRange) []configstore.EgressPortRange {
	ports := make([]configstore.EgressPortRange, len(ranges))
	for index, portRange := range ranges {
		ports[index] = configstore.EgressPortRange{From: portRange.From, To: portRange.To}
	}
	return ports
}

func profilePutMutation(request controlapi.XrayProfilePutRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		if request.ID == "" || request.Kind == "" {
			return nil, domainFailure{code: "config.profile_identity_required", err: fmt.Errorf("profile id and kind are required")}
		}
		profile := configstore.XrayProfile{ID: request.ID, Kind: request.Kind}
		switch request.Kind {
		case "vless":
			credential, err := validateDomainCredential(request.Credential)
			if err != nil {
				return nil, domainFailure{code: "config.credential_invalid", err: err}
			}
			profile.VLESS = &configstore.VLESSProfile{
				UUID:                   credential,
				Transport:              request.Transport,
				Security:               request.Security,
				AllowInsecurePlaintext: request.AllowInsecurePlaintext,
			}
		case "socks":
			var password string
			if request.Username != "" || request.Credential != "" {
				credential, err := validateDomainCredential(request.Credential)
				if err != nil {
					return nil, domainFailure{code: "config.credential_invalid", err: err}
				}
				password = credential
			}
			profile.SOCKS = &configstore.SOCKSProfile{Username: request.Username, Password: password}
		default:
			return nil, domainFailure{code: "config.profile_invalid", err: fmt.Errorf("profile kind %q is unsupported", request.Kind)}
		}
		if err := xrayconfig.CompileProfile(profile); err != nil {
			return nil, domainFailure{code: "config.profile_invalid", err: err}
		}
		cfg.XrayProfiles[request.ID] = profile
		return map[string]any{"xray_profile": domainProfileViews(cfg.XrayProfiles)[request.ID]}, nil
	}
}

func profileRemoveMutation(request controlapi.XrayProfileRemoveRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool, _ *domainMutationEffects) (any, error) {
		if domainProfileInUse(*cfg, request.ID) {
			return nil, domainFailure{code: "config.in_use", err: fmt.Errorf("%s", request.ID)}
		}
		delete(cfg.XrayProfiles, request.ID)
		return map[string]any{"removed": request.ID}, nil
	}
}

func (s *Server) claimDomainRequest(requestID string, fingerprint [sha256.Size]byte) (*cachedDomainResponse, bool, int) {
	s.domainRequestsMu.Lock()
	defer s.domainRequestsMu.Unlock()
	if cached, ok := s.domainRequests[requestID]; ok {
		if cached.fingerprint != fingerprint {
			return nil, false, http.StatusConflict
		}
		return cached, false, 0
	}
	if len(s.domainRequests) >= requestCacheCapacity {
		s.evictCompletedDomainRequestsLocked()
		if len(s.domainRequests) >= requestCacheCapacity {
			return nil, false, http.StatusServiceUnavailable
		}
	}
	entry := &cachedDomainResponse{fingerprint: fingerprint, done: make(chan struct{})}
	s.domainRequests[requestID] = entry
	return entry, true, 0
}

func (s *Server) completeDomainRequest(requestID string, entry *cachedDomainResponse, result domainResult) {
	s.domainRequestsMu.Lock()
	defer s.domainRequestsMu.Unlock()
	current, ok := s.domainRequests[requestID]
	if !ok || current != entry || entry.completed {
		panic("controlserver: invalid domain request completion")
	}
	entry.result = domainResult{
		status:       result.status,
		body:         append([]byte(nil), result.body...),
		applied:      cloneDomainBool(result.applied),
		outcome:      result.outcome,
		preparations: append([]controlapi.MutationPreparation(nil), result.preparations...),
	}
	entry.completed = true
	entry.protected = result.outcome == controlapi.MutationOutcomeIndeterminate
	if !entry.protected {
		s.domainCompletedRequests = append(s.domainCompletedRequests, requestID)
	}
	close(entry.done)
}

func (s *Server) evictCompletedDomainRequestsLocked() {
	for len(s.domainRequests) >= requestCacheCapacity && len(s.domainCompletedRequests) > 0 {
		requestID := s.domainCompletedRequests[0]
		s.domainCompletedRequests[0] = ""
		s.domainCompletedRequests = s.domainCompletedRequests[1:]
		entry, ok := s.domainRequests[requestID]
		if ok && entry.completed {
			delete(s.domainRequests, requestID)
		}
	}
	if len(s.domainCompletedRequests) == 0 {
		s.domainCompletedRequests = nil
	}
}

func classifyReloadMutation(status controlapi.ReconcileStatus, reloadErr error, revision int64) (error, bool, controlapi.MutationOutcome) {
	published := status.ConfigurationPublished &&
		status.AppliedRevision == revision && status.AttemptedRevision == revision
	confirmed := published && status.State == controlapi.ReconcileStateApplied
	if reloadErr == nil {
		if confirmed {
			return nil, true, controlapi.MutationOutcomeApplied
		}
		return publicerr.Errorf(
			"service.reload_result_invalid",
			"runtime did not confirm revision %d as applied",
			revision,
		), false, controlapi.MutationOutcomeIndeterminate
	}
	if published {
		return reloadErr, true, controlapi.MutationOutcomeApplied
	}
	code := runtimeCommandErrorCode(reloadErr)
	if status.State == controlapi.ReconcileStateFailed && status.AttemptedRevision == revision ||
		reloadFailureWasNotAttempted(code) {
		return reloadErr, false, controlapi.MutationOutcomeNotApplied
	}
	return reloadErr, false, controlapi.MutationOutcomeIndeterminate
}

func reloadFailureWasNotAttempted(code string) bool {
	switch code {
	case "config.content_invalid", "config.revision_conflict", "config.revision_required", "service.reload_config", "service.reload_config_digest", "service.reload_validate":
		return true
	default:
		return false
	}
}

func decodeDomainBody(body io.ReadCloser, target any) ([]byte, error) {
	raw, err := readBoundedRequestBody(body, maxRequestBodyBytes)
	if err != nil {
		return nil, domainFailure{code: "domain.request_too_large", err: err}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, domainFailure{code: "domain.request_invalid", err: err}
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, domainFailure{code: "domain.request_invalid", err: err}
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return nil, domainFailure{code: "domain.request_invalid", err: err}
	}
	return canonical, nil
}

func requireEmptyDomainBody(body io.ReadCloser) error {
	raw, err := readBoundedRequestBody(body, 0)
	if err != nil || len(raw) != 0 {
		return domainFailure{code: "domain.body_forbidden", err: fmt.Errorf("GET requests cannot have a body")}
	}
	return nil
}

func validateDomainVersion(version int) error {
	if version != controlapi.DomainAPIVersion {
		return domainFailure{code: "domain.api_version_unsupported", err: fmt.Errorf("api_version=%d", version)}
	}
	return nil
}

func validateDomainMutationRequest(request controlapi.DomainMutationRequest) error {
	if err := validateDomainVersion(request.APIVersion); err != nil {
		return err
	}
	if request.Revision < 0 {
		return domainFailure{code: "config.revision_required", err: fmt.Errorf("expected revision is required")}
	}
	if len(request.RequestID) != 32 {
		return domainFailure{code: "domain.request_id_invalid", err: fmt.Errorf("request_id must be 32 lowercase hexadecimal characters")}
	}
	for _, character := range request.RequestID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return domainFailure{code: "domain.request_id_invalid", err: fmt.Errorf("request_id must be 32 lowercase hexadecimal characters")}
		}
	}
	return nil
}

func domainJSONResult(status int, payload map[string]any) domainResult {
	payload["api_version"] = controlapi.DomainAPIVersion
	body, err := json.Marshal(payload)
	if err != nil {
		return domainErrorResult(domainFailure{code: "domain.response_invalid", err: err}, http.StatusInternalServerError)
	}
	return domainResult{status: status, body: append(body, '\n')}
}

func domainMutationJSONResult(dryRun bool, payload map[string]any) domainResult {
	if dryRun {
		return domainJSONResult(http.StatusOK, payload)
	}
	payload["applied"] = true
	payload["outcome"] = controlapi.MutationOutcomeApplied
	payload["api_version"] = controlapi.DomainAPIVersion
	body, err := json.Marshal(payload)
	if err != nil {
		return domainMutationErrorResultWithOutcome(
			domainFailure{code: "domain.response_invalid", err: err},
			http.StatusInternalServerError,
			true,
			controlapi.MutationOutcomeApplied,
			nil,
		)
	}
	applied := true
	return domainResult{
		status:  http.StatusOK,
		body:    append(body, '\n'),
		applied: &applied,
		outcome: controlapi.MutationOutcomeApplied,
	}
}

func domainErrorResult(err error, status int) domainResult {
	code, message := classifyDomainError(err)
	return marshalDomainError(code, message, status, nil, "", nil)
}

func domainMutationErrorResult(err error, status int, dryRun bool, preparations []controlapi.MutationPreparation) domainResult {
	if dryRun {
		return domainErrorResult(err, status)
	}
	applied, outcome := mutationErrorMetadata(err)
	return domainMutationErrorResultWithOutcome(err, status, applied, outcome, preparations)
}

func domainMutationErrorResultWithOutcome(
	err error,
	status int,
	applied bool,
	outcome controlapi.MutationOutcome,
	preparations []controlapi.MutationPreparation,
) domainResult {
	code, message := classifyDomainError(err)
	if outcome != controlapi.MutationOutcomeNotApplied {
		preparations = nil
	}
	return marshalDomainError(code, message, status, &applied, outcome, preparations)
}

func marshalDomainError(
	code, message string,
	status int,
	applied *bool,
	outcome controlapi.MutationOutcome,
	preparations []controlapi.MutationPreparation,
) domainResult {
	if status == 0 {
		status = domainErrorStatus(code)
	}
	if applied != nil && *applied {
		status = http.StatusOK
	}
	body, marshalErr := json.Marshal(controlapi.DomainError{
		APIVersion:   controlapi.DomainAPIVersion,
		OK:           false,
		ErrorCode:    code,
		Message:      sanitizeDomainError(code, message),
		Applied:      cloneDomainBool(applied),
		Outcome:      outcome,
		Preparations: append([]controlapi.MutationPreparation(nil), preparations...),
	})
	if marshalErr != nil {
		body = []byte(`{"api_version":1,"ok":false,"error_code":"domain.response_invalid","message":"response encoding failed"}`)
		status = http.StatusInternalServerError
		applied = nil
		outcome = ""
		preparations = nil
	}
	return domainResult{
		status:       status,
		body:         append(body, '\n'),
		applied:      cloneDomainBool(applied),
		outcome:      outcome,
		preparations: append([]controlapi.MutationPreparation(nil), preparations...),
	}
}

func cloneDomainBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func classifyDomainError(err error) (string, string) {
	var failure domainFailure
	if errors.As(err, &failure) {
		return publicerr.NormalizeCode(failure.code, "domain.failed"), failure.err.Error()
	}
	var compileError *route.CompileError
	if errors.As(err, &compileError) {
		return publicerr.NormalizeCode(compileError.Code, "route.compile_failed"), compileError.Error()
	}
	return publicerr.Code(err, "domain.failed"), err.Error()
}

func domainErrorStatus(code string) int {
	switch {
	case code == "config.revision_conflict", code == "config.peer_exists", code == "config.in_use",
		code == configops.CodeNodeEgressGrantRevokeRequired, code == "domain.request_id_conflict":
		return http.StatusConflict
	case strings.HasSuffix(code, "_unknown"), code == "domain.not_found":
		return http.StatusNotFound
	case code == "config.revision_required", strings.HasPrefix(code, "domain.request_"), code == "settings.patch_empty":
		return http.StatusBadRequest
	case strings.HasPrefix(code, "route."), strings.HasPrefix(code, "settings."), strings.HasPrefix(code, "identity."), strings.HasPrefix(code, "config."):
		return http.StatusUnprocessableEntity
	case strings.Contains(code, "required"), strings.Contains(code, "invalid"):
		return http.StatusBadRequest
	case strings.HasPrefix(code, "service."):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func sanitizeDomainError(code, _ string) string {
	return publicerr.MessageCode(code)
}

func writeDomainResult(w http.ResponseWriter, result domainResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(result.status)
	_, _ = w.Write(result.body)
}

func domainIdentitySeedPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "keystore", "node-seed.v1.json")
}

func (s *Server) loadDomainConfig() (configstore.Config, error) {
	if s.stateStore != nil {
		return configstore.LoadStoreExisting(s.stateStore)
	}
	return configstore.LoadExisting(s.configPath)
}

func (s *Server) loadDomainIdentity() (*identity.Identity, error) {
	if s.stateStore != nil {
		return identity.LoadStore(s.stateStore)
	}
	return identity.Load(domainIdentitySeedPath(s.configPath))
}

func (s *Server) inspectDomainIdentityView(cfg configstore.Config) (domainIdentityView, error) {
	view, _, err := s.inspectDomainIdentity(cfg)
	return view, err
}

func (s *Server) inspectDomainIdentity(cfg configstore.Config) (domainIdentityView, *identity.Identity, error) {
	return inspectDomainIdentityWithLoader(cfg, s.loadDomainIdentity)
}

func inspectDomainIdentityView(cfg configstore.Config, seedPath string) (domainIdentityView, error) {
	view, _, err := inspectDomainIdentity(cfg, seedPath)
	return view, err
}

func inspectDomainIdentity(cfg configstore.Config, seedPath string) (domainIdentityView, *identity.Identity, error) {
	return inspectDomainIdentityWithLoader(cfg, func() (*identity.Identity, error) {
		return identity.Load(seedPath)
	})
}

func inspectDomainIdentityWithLoader(cfg configstore.Config, load func() (*identity.Identity, error)) (domainIdentityView, *identity.Identity, error) {
	id, err := load()
	if errors.Is(err, os.ErrNotExist) {
		configured, classifyErr := identity.ClassifyConfiguredIdentity(cfg.Node.NodeID, cfg.Node.PublicKey)
		if classifyErr != nil {
			return domainIdentityView{}, nil, classifyErr
		}
		state := identityStateUninitialized
		switch configured {
		case identity.ConfiguredIdentityLegacy:
			state = identityStateLegacyUnbacked
		case identity.ConfiguredIdentityV2:
			state = identityStateBackingMissing
		}
		return domainIdentityView{State: state, NodeID: cfg.Node.NodeID, PublicKey: cfg.Node.PublicKey}, nil, nil
	}
	if err != nil {
		return domainIdentityView{}, nil, err
	}
	public := id.Public()
	state := identityStateBacked
	if cfg.Node.NodeID == "" && cfg.Node.PublicKey == "" {
		state = identityStateRecoverable
	} else if cfg.Node.NodeID != public.NodeID.String() || cfg.Node.PublicKey != public.PublicKey {
		return domainIdentityView{
			State:                 identityStateMismatch,
			NodeID:                cfg.Node.NodeID,
			PublicKey:             cfg.Node.PublicKey,
			BackingNodeID:         public.NodeID.String(),
			BackingPublicKey:      public.PublicKey,
			OSACLReleaseQualified: true,
		}, id, nil
	}
	return domainIdentityViewFromPublic(state, public), id, nil
}

func domainIdentityViewFromPublic(state string, public identity.PublicIdentity) domainIdentityView {
	return domainIdentityView{
		State:                 state,
		Version:               public.Version,
		Algorithm:             public.Algorithm,
		NodeID:                public.NodeID.String(),
		PublicKey:             public.PublicKey,
		OSACLReleaseQualified: true,
	}
}

func domainNodeView(node configstore.NodeConfig) configstore.NodeConfig {
	node.RendrInstanceID = ""
	return node
}

func domainProfileViews(profiles map[string]configstore.XrayProfile) map[string]publicXrayProfile {
	credentialGroups := make(map[string][]string)
	for id, profile := range profiles {
		if profile.Kind != "vless" || profile.VLESS == nil {
			continue
		}
		credentialKey, err := xraycredential.VLESSKey(profile.VLESS.UUID)
		if err != nil {
			continue
		}
		credentialGroups[credentialKey] = append(credentialGroups[credentialKey], id)
	}

	groupIDs := make(map[string]string, len(profiles))
	for _, profileIDs := range credentialGroups {
		sort.Strings(profileIDs)
		// The label hashes public profile IDs, not UUID bytes. It exposes only
		// equality within this configuration and cannot be used to recover a
		// credential redacted by the domain API.
		digest := sha256.Sum256([]byte("xtier:vless-profile-group:v1\x00" + strings.Join(profileIDs, "\x00")))
		groupID := fmt.Sprintf("vless-%x", digest[:16])
		for _, profileID := range profileIDs {
			groupIDs[profileID] = groupID
		}
	}

	views := make(map[string]publicXrayProfile, len(profiles))
	for id, profile := range profiles {
		views[id] = publicXrayProfile{
			ID:                profile.ID,
			Kind:              profile.Kind,
			CredentialGroupID: groupIDs[id],
		}
	}
	return views
}

func domainInboundIndex(inbounds []configstore.InboundConfig, kind string) int {
	for index, inbound := range inbounds {
		if inbound.Kind == kind {
			return index
		}
	}
	return -1
}

func domainProfileInUse(cfg configstore.Config, id string) bool {
	return configstore.LocalProfileInUse(cfg, id)
}

func validateDomainCredential(value string) (string, error) {
	if len(value) == 0 || len(value) > 4096 {
		return "", fmt.Errorf("credential must contain between 1 and 4096 bytes")
	}
	credential := strings.TrimSpace(value)
	if credential == "" || strings.ContainsAny(credential, "\r\n\x00") {
		return "", fmt.Errorf("credential must contain exactly one non-empty line")
	}
	return credential, nil
}

func splitDomainCSV(expression string) []string {
	parts := strings.Split(expression, ",")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths
}
