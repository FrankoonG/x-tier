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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/identity"
	"github.com/FrankoonG/x-tier/internal/localview"
	"github.com/FrankoonG/x-tier/internal/publicerr"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/xrayconfig"
)

type cachedDomainResponse struct {
	fingerprint [sha256.Size]byte
	result      domainResult
	done        chan struct{}
	completed   bool
	protected   bool
	completedAt time.Time
}

type domainResult struct {
	status int
	body   []byte
}

type domainMutation func(*configstore.Config, bool) (any, error)

type domainFailure struct {
	code string
	err  error
}

func (e domainFailure) Error() string           { return e.code + ": " + e.err.Error() }
func (e domainFailure) Unwrap() error           { return e.err }
func (e domainFailure) PublicErrorCode() string { return e.code }

type publicXrayProfile struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
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
	cfg, err := configstore.LoadExisting(s.configPath)
	if err != nil {
		writeDomainResult(w, domainErrorResult(err, 0))
		return
	}
	s.domainExecutions.Add(1)

	var payload map[string]any
	switch r.URL.Path {
	case controlapi.DomainLocalPath:
		identityStatus, err := inspectDomainIdentityView(cfg, domainIdentitySeedPath(s.configPath))
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
		identityStatus, err := inspectDomainIdentityView(cfg, domainIdentitySeedPath(s.configPath))
		if err != nil {
			writeDomainResult(w, domainErrorResult(err, 0))
			return
		}
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "identity": identityStatus, "node": domainNodeView(cfg.Node)}
	case controlapi.DomainSettingsPath:
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "settings": cfg.System}
	case controlapi.DomainPeersPath:
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID, "peers": cfg.Peers}
	case controlapi.DomainInboundsPath:
		payload = map[string]any{"ok": true, "revision": cfg.Revision, "target_local_node_id": cfg.Node.NodeID, "inbounds": cfg.NodeInbound}
	case controlapi.DomainXrayProfilesPath:
		profiles := make(map[string]publicXrayProfile, len(cfg.XrayProfiles))
		for id, profile := range cfg.XrayProfiles {
			profiles[id] = domainProfileView(profile)
		}
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
	cfg, err := configstore.LoadExisting(s.configPath)
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
	cfg, err := configstore.LoadExisting(s.configPath)
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
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	if err := validateDomainMutationRequest(request.DomainMutationRequest); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	s.executeCachedDomainMutation(w, r, request.DomainMutationRequest, raw, true, func() domainResult {
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
			return domainErrorResult(domainFailure{code: "service.reload_unavailable", err: fmt.Errorf("runtime reloader is unavailable")}, http.StatusServiceUnavailable)
		}
		status, err := reloader.Reload(ctx, request.Revision, request.DryRun)
		if err != nil {
			failure := domainFailure{code: runtimeCommandErrorCode(err), err: errors.New(sanitizeRuntimeCommandError(err))}
			if !request.DryRun && status.State == controlapi.ReconcileStateApplied && status.AppliedRevision == request.Revision {
				return domainErrorResultWithOutcome(failure, 0, true, string(controlapi.MutationOutcomeApplied))
			}
			if request.DryRun || (status.State == controlapi.ReconcileStateFailed && status.AttemptedRevision == request.Revision) {
				return domainErrorResult(failure, 0)
			}
			if reloadFailureWasNotAttempted(failure.code) {
				return domainErrorResult(failure, 0)
			}
			return domainErrorResultWithOutcome(failure, 0, false, "indeterminate")
		}
		return domainJSONResult(http.StatusOK, map[string]any{
			"ok":                   true,
			"dry_run":              request.DryRun,
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
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	if err := validateDomainMutationRequest(request.DomainMutationRequest); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	s.executeCachedDomainMutation(w, r, request.DomainMutationRequest, raw, true, func() domainResult {
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
			return domainErrorResult(err, 0)
		}
		return domainJSONResult(http.StatusOK, map[string]any{
			"ok":              true,
			"changed":         !request.DryRun,
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
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	if err := validateDomainMutationRequest(meta); err != nil {
		writeDomainResult(w, domainErrorResult(err, http.StatusBadRequest))
		return
	}
	s.executeCachedDomainMutation(w, r, meta, raw, false, func() domainResult {
		return s.executeConfigDomainMutation(meta, mutation)
	})
}

func (s *Server) executeCachedDomainMutation(w http.ResponseWriter, r *http.Request, meta controlapi.DomainMutationRequest, raw []byte, protect bool, execute func() domainResult) {
	if !s.beginCommand() {
		writeDomainResult(w, domainErrorResult(domainFailure{code: "control.stopping", err: fmt.Errorf("control server is stopping")}, http.StatusServiceUnavailable))
		return
	}
	defer s.commands.Done()

	if meta.DryRun {
		result, _ := executeDomainSafely(execute)
		writeDomainResult(w, result)
		return
	}
	fingerprint := sha256.Sum256(append([]byte(r.Method+"\x00"+r.URL.Path+"\x00"), raw...))
	entry, leader, status := s.claimDomainRequest(meta.RequestID, fingerprint, protect)
	if status != 0 {
		code := "domain.request_id_conflict"
		if status == http.StatusServiceUnavailable {
			code = "domain.idempotency_capacity_exhausted"
		}
		writeDomainResult(w, domainErrorResult(domainFailure{code: code, err: fmt.Errorf("request_id cannot be claimed")}, status))
		return
	}
	if !leader {
		writeDomainResult(w, waitDomainResult(r.Context(), entry))
		return
	}
	result, indeterminate := executeDomainSafely(execute)
	s.completeDomainRequest(meta.RequestID, entry, result, protect || indeterminate)
	writeDomainResult(w, result)
}

func executeDomainSafely(execute func() domainResult) (result domainResult, indeterminate bool) {
	defer func() {
		if recover() != nil {
			result = domainErrorResultWithOutcome(
				domainFailure{code: "domain.execution_indeterminate", err: errors.New("domain mutation did not return")},
				http.StatusInternalServerError,
				false,
				"indeterminate",
			)
			indeterminate = true
		}
	}()
	return execute(), false
}

func waitDomainResult(ctx context.Context, entry *cachedDomainResponse) domainResult {
	select {
	case <-entry.done:
		return entry.result
	case <-ctx.Done():
		return domainErrorResultWithOutcome(
			domainFailure{code: "domain.request_canceled", err: errors.New("request canceled while awaiting the original execution")},
			http.StatusRequestTimeout,
			false,
			"indeterminate",
		)
	}
}

func (s *Server) executeConfigDomainMutation(meta controlapi.DomainMutationRequest, mutation domainMutation) domainResult {
	s.domainExecutions.Add(1)
	if meta.DryRun {
		cfg, err := configstore.LoadExisting(s.configPath)
		if err != nil {
			return domainErrorResult(err, 0)
		}
		before := cfg.Revision
		if err := configstore.ValidateRevision(cfg, meta.Revision); err != nil {
			return domainErrorResult(err, 0)
		}
		payload, err := mutation(&cfg, true)
		if err != nil {
			return domainErrorResult(err, 0)
		}
		cfg.Revision = before
		if err := configstore.Validate(cfg); err != nil {
			return domainErrorResult(err, http.StatusUnprocessableEntity)
		}
		return domainMutationResult(true, before, before, payload)
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
	if s.ownershipKey != "" {
		update = func(path string, revision int64, mutate func(*configstore.Config) error) (configstore.UpdateResult, error) {
			return configstore.UpdatePinnedCAS(path, s.ownershipKey, revision, mutate)
		}
	}
	result, err := update(s.configPath, meta.Revision, func(cfg *configstore.Config) error {
		var mutateErr error
		payload, mutateErr = mutation(cfg, false)
		return mutateErr
	})
	if err != nil {
		return domainErrorResult(err, 0)
	}
	return domainMutationResult(false, result.BeforeRevision, result.AfterRevision, payload)
}

func domainMutationResult(dryRun bool, before, after int64, payload any) domainResult {
	return domainJSONResult(http.StatusOK, map[string]any{
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
	return domainErrorResult(domainFailure{code: code, err: errors.New(message)}, http.StatusServiceUnavailable)
}

func (s *Server) identityInitMutation(request controlapi.IdentityInitRequest) domainMutation {
	return func(cfg *configstore.Config, dryRun bool) (any, error) {
		seedPath := domainIdentitySeedPath(s.configPath)
		observed, backing, err := inspectDomainIdentity(*cfg, seedPath)
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
			id, err = identity.Create(seedPath)
			created = err == nil
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
	return func(cfg *configstore.Config, _ bool) (any, error) {
		if request.Name == "" {
			return nil, domainFailure{code: "identity.name_required", err: fmt.Errorf("display name is required")}
		}
		cfg.Node.DisplayName = request.Name
		return map[string]any{"node": domainNodeView(cfg.Node)}, nil
	}
}

func settingsMutation(request controlapi.SettingsUpdateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool) (any, error) {
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
	return func(cfg *configstore.Config, _ bool) (any, error) {
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
	return func(cfg *configstore.Config, _ bool) (any, error) {
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
	return func(cfg *configstore.Config, _ bool) (any, error) {
		if request.Name == "" || request.NodeID == "" {
			return nil, domainFailure{code: "config.peer_identity_required", err: fmt.Errorf("peer name and node_id are required")}
		}
		if _, _, ok := configstore.FindPeer(cfg.Peers, request.Name); ok {
			return nil, domainFailure{code: "config.peer_exists", err: fmt.Errorf("%s", request.Name)}
		}
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
		cfg.Peers = append(cfg.Peers, peer)
		return map[string]any{"peer": peer}, nil
	}
}

func peerUpdateMutation(request controlapi.PeerUpdateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool) (any, error) {
		peer, index, ok := configstore.FindPeer(cfg.Peers, request.Name)
		if !ok {
			return nil, domainFailure{code: "config.peer_unknown", err: fmt.Errorf("%s", request.Name)}
		}
		if request.Patch.Addr != nil {
			peer.Addr = *request.Patch.Addr
			peer.GatewayAddr = *request.Patch.Addr
		}
		if request.Patch.Direction != nil {
			peer.Direction = route.Direction(*request.Patch.Direction)
		}
		if request.Patch.XrayProfileID != nil {
			peer.XrayProfileID = *request.Patch.XrayProfileID
		}
		if request.Patch.NestedEnabled != nil {
			peer.NestedEnabled = *request.Patch.NestedEnabled
		}
		cfg.Peers[index] = peer
		return map[string]any{"peer": peer}, nil
	}
}

func peerStateMutation(request controlapi.PeerStateRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool) (any, error) {
		peer, index, ok := configstore.FindPeer(cfg.Peers, request.Name)
		if !ok {
			return nil, domainFailure{code: "config.peer_unknown", err: fmt.Errorf("%s", request.Name)}
		}
		peer.Enabled = request.Enabled
		if request.Enabled {
			peer.DisabledCause = ""
		} else {
			peer.DisabledCause = request.Reason
			if peer.DisabledCause == "" {
				peer.DisabledCause = "disabled"
			}
		}
		cfg.Peers[index] = peer
		return map[string]any{"peer": peer}, nil
	}
}

func peerRemoveMutation(request controlapi.PeerRemoveRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool) (any, error) {
		_, index, ok := configstore.FindPeer(cfg.Peers, request.Name)
		if !ok {
			return nil, domainFailure{code: "config.peer_unknown", err: fmt.Errorf("%s", request.Name)}
		}
		cfg.Peers = append(cfg.Peers[:index], cfg.Peers[index+1:]...)
		return map[string]any{"removed": request.Name}, nil
	}
}

func profilePutMutation(request controlapi.XrayProfilePutRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool) (any, error) {
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
		return map[string]any{"xray_profile": domainProfileView(profile)}, nil
	}
}

func profileRemoveMutation(request controlapi.XrayProfileRemoveRequest) domainMutation {
	return func(cfg *configstore.Config, _ bool) (any, error) {
		if domainProfileInUse(*cfg, request.ID) {
			return nil, domainFailure{code: "config.in_use", err: fmt.Errorf("%s", request.ID)}
		}
		delete(cfg.XrayProfiles, request.ID)
		return map[string]any{"removed": request.ID}, nil
	}
}

func (s *Server) claimDomainRequest(requestID string, fingerprint [sha256.Size]byte, protect bool) (*cachedDomainResponse, bool, int) {
	s.domainRequestsMu.Lock()
	defer s.domainRequestsMu.Unlock()
	s.evictExpiredProtectedDomainRequestsLocked(s.cacheNow())
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
	entry := &cachedDomainResponse{fingerprint: fingerprint, done: make(chan struct{}), protected: protect}
	s.domainRequests[requestID] = entry
	return entry, true, 0
}

func (s *Server) completeDomainRequest(requestID string, entry *cachedDomainResponse, result domainResult, protect bool) {
	s.domainRequestsMu.Lock()
	defer s.domainRequestsMu.Unlock()
	current, ok := s.domainRequests[requestID]
	if !ok || current != entry || entry.completed {
		panic("controlserver: invalid domain request completion")
	}
	entry.result = domainResult{status: result.status, body: append([]byte(nil), result.body...)}
	entry.completed = true
	entry.protected = protect
	entry.completedAt = s.cacheNow()
	if protect {
		s.domainProtectedRequests = append(s.domainProtectedRequests, requestID)
		s.trimProtectedDomainRequestsLocked()
	} else {
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
	for len(s.domainRequests) >= requestCacheCapacity && len(s.domainProtectedRequests) > 0 {
		s.evictOldestProtectedDomainRequestLocked()
	}
}

func (s *Server) evictExpiredProtectedDomainRequestsLocked(now time.Time) {
	for len(s.domainProtectedRequests) > 0 {
		requestID := s.domainProtectedRequests[0]
		entry, ok := s.domainRequests[requestID]
		if ok && entry.completed && entry.protected && now.Before(entry.completedAt.Add(protectedDomainRequestTTL)) {
			break
		}
		s.domainProtectedRequests[0] = ""
		s.domainProtectedRequests = s.domainProtectedRequests[1:]
		if ok && entry.completed && entry.protected {
			delete(s.domainRequests, requestID)
		}
	}
	if len(s.domainProtectedRequests) == 0 {
		s.domainProtectedRequests = nil
	}
}

func (s *Server) trimProtectedDomainRequestsLocked() {
	for len(s.domainProtectedRequests) > protectedDomainRequestCapacity {
		s.evictOldestProtectedDomainRequestLocked()
	}
}

func (s *Server) evictOldestProtectedDomainRequestLocked() {
	requestID := s.domainProtectedRequests[0]
	s.domainProtectedRequests[0] = ""
	s.domainProtectedRequests = s.domainProtectedRequests[1:]
	entry, ok := s.domainRequests[requestID]
	if ok && entry.completed && entry.protected {
		delete(s.domainRequests, requestID)
	}
	if len(s.domainProtectedRequests) == 0 {
		s.domainProtectedRequests = nil
	}
}

func (s *Server) cacheNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
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

func domainErrorResult(err error, status int) domainResult {
	code, message := classifyDomainError(err)
	applied, outcome := mutationErrorMetadata(err)
	if outcome == controlapi.MutationOutcomeNotApplied {
		outcome = ""
	}
	return marshalDomainError(code, message, status, applied, string(outcome))
}

func domainErrorResultWithOutcome(err error, status int, applied bool, outcome string) domainResult {
	code, message := classifyDomainError(err)
	return marshalDomainError(code, message, status, applied, outcome)
}

func marshalDomainError(code, message string, status int, applied bool, outcome string) domainResult {
	if status == 0 {
		status = domainErrorStatus(code)
	}
	if applied {
		status = http.StatusOK
	}
	body, marshalErr := json.Marshal(controlapi.DomainError{
		APIVersion: controlapi.DomainAPIVersion,
		OK:         false,
		ErrorCode:  code,
		Message:    sanitizeDomainError(code, message),
		Applied:    applied,
		Outcome:    outcome,
	})
	if marshalErr != nil {
		body = []byte(`{"api_version":1,"ok":false,"error_code":"domain.response_invalid","message":"response encoding failed"}`)
		status = http.StatusInternalServerError
	}
	return domainResult{status: status, body: append(body, '\n')}
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
	case code == "config.revision_conflict", code == "config.peer_exists", code == "config.in_use", code == "domain.request_id_conflict":
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

func inspectDomainIdentityView(cfg configstore.Config, seedPath string) (domainIdentityView, error) {
	view, _, err := inspectDomainIdentity(cfg, seedPath)
	return view, err
}

func inspectDomainIdentity(cfg configstore.Config, seedPath string) (domainIdentityView, *identity.Identity, error) {
	id, err := identity.Load(seedPath)
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

func domainProfileView(profile configstore.XrayProfile) publicXrayProfile {
	return publicXrayProfile{ID: profile.ID, Kind: profile.Kind}
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
