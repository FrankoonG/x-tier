package controlserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FrankoonG/x-tier/internal/cli"
	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/publicerr"
)

const (
	requestCacheCapacity    = 512
	challengeReplayCapacity = 4096
	challengeTTL            = 30 * time.Second
	maxRequestBodyBytes     = 1 << 20
	maxResponseBodyBytes    = 2 << 20
	maxCommandOutputBytes   = 128 << 10
	defaultShutdownTimeout  = 5 * time.Second
)

var ErrShutdownIncomplete = errors.New("control: server shutdown incomplete")

type Server struct {
	ctx          context.Context
	httpServer   *http.Server
	listener     net.Listener
	configPath   string
	ownershipKey string
	tokenPath    string
	authEpoch    string
	status       StatusProvider

	requestsMu              sync.Mutex
	requests                map[string]*cachedResponse
	completedRequests       []string
	domainRequestsMu        sync.Mutex
	domainRequests          map[string]*cachedDomainResponse
	domainCompletedRequests []string

	challengesMu       sync.Mutex
	usedChallenges     map[string]time.Time
	usedChallengeOrder []string
	challengeMax       int
	challengeTTL       time.Duration
	now                func() time.Time

	commandsMu sync.Mutex
	// Ordinary config writes take both locks. Reload and restore intentionally
	// take one each so repair can proceed while runtime apply is blocked.
	reloadMutationGate mutationGate
	configMutationGate mutationGate
	commandIngress     atomic.Uint64
	commandExecutions  atomic.Uint64
	domainIngress      atomic.Uint64
	domainExecutions   atomic.Uint64
	closing            bool
	commands           sync.WaitGroup
	execute            func(context.Context, []string, io.Writer, io.Writer) cli.ExecutionResult

	shutdownOnce   sync.Once
	serveDone      chan struct{}
	closedDone     chan struct{}
	serveErrMu     sync.Mutex
	serveErr       error
	shutdownErr    error
	shutdownWait   time.Duration
	mutationWait   time.Duration
	restoreConfig  func(int64, bool) (configstore.UpdateResult, error)
	createIdentity domainIdentityCreator
}

type cachedResponse struct {
	request       controlapi.Request
	response      controlapi.Response
	done          chan struct{}
	executionDone <-chan struct{}
	completed     bool
	protected     bool
}

// StatusProvider returns a snapshot of daemon-owned state. Runtime instance
// IDs must only be populated from an observed live runtime, never from config.
type StatusProvider = controlapi.StatusProvider
type StatusProviderFunc = controlapi.StatusProviderFunc

type RuntimeReloader interface {
	// Reload reconciles one desired revision; it is not a force-restart API.
	// Repeating an already healthy revision must not publish a new generation.
	Reload(context.Context, int64, bool) (controlapi.ReconcileStatus, error)
}

func Start(ctx context.Context, addr, configPath string, providers ...StatusProvider) (*Server, error) {
	return start(ctx, addr, configPath, "", providers...)
}

// StartOwned serves a daemon whose configPath is pinned to a directory handle
// and whose canonical ownershipKey is held for the daemon lifetime.
func StartOwned(ctx context.Context, addr, configPath, ownershipKey string, providers ...StatusProvider) (*Server, error) {
	if ownershipKey == "" {
		return nil, fmt.Errorf("control.ownership_key_required")
	}
	return start(ctx, addr, configPath, ownershipKey, providers...)
}

func start(ctx context.Context, addr, configPath, ownershipKey string, providers ...StatusProvider) (*Server, error) {
	if ctx == nil {
		return nil, fmt.Errorf("control.context_nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("control.context_done: %w", err)
	}
	if len(providers) > 1 {
		return nil, fmt.Errorf("control.status_provider_multiple")
	}
	var statusProvider StatusProvider
	if len(providers) == 1 {
		if providers[0] == nil {
			return nil, fmt.Errorf("control.status_provider_nil")
		}
		statusProvider = providers[0]
	}
	if addr == "" {
		addr = controlapi.DefaultAddr
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("control.addr_invalid: %w", err)
	}
	if tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
		return nil, fmt.Errorf("control.non_loopback_forbidden: %s", addr)
	}
	tokenPath := controlapi.TokenPath(configPath)
	if _, err := controlapi.CreateToken(tokenPath); err != nil {
		return nil, fmt.Errorf("control.token_create: %w", err)
	}
	ln, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return nil, err
	}
	var epoch [32]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("control.auth_epoch_random_failed")
	}
	s := &Server{
		ctx:            ctx,
		listener:       ln,
		configPath:     configPath,
		ownershipKey:   ownershipKey,
		tokenPath:      tokenPath,
		authEpoch:      hex.EncodeToString(epoch[:]),
		status:         statusProvider,
		requests:       make(map[string]*cachedResponse),
		domainRequests: make(map[string]*cachedDomainResponse),
		usedChallenges: make(map[string]time.Time),
		challengeMax:   challengeReplayCapacity,
		challengeTTL:   challengeTTL,
		now:            time.Now,
		shutdownWait:   defaultShutdownTimeout,
		createIdentity: createDomainIdentity,
		execute: func(ctx context.Context, args []string, stdout, stderr io.Writer) cli.ExecutionResult {
			if ownershipKey != "" {
				return cli.RunOwnedDaemonContext(ctx, args, ownershipKey, stdout, stderr)
			}
			return cli.RunDaemonContext(ctx, args, stdout, stderr)
		},
		serveDone:  make(chan struct{}),
		closedDone: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(controlapi.ChallengePath, s.handleChallenge)
	mux.HandleFunc(controlapi.CommandPath, s.authenticated(maxRequestBodyBytes, s.handleCommand))
	mux.HandleFunc(controlapi.HealthPath, s.authenticated(0, s.handleHealth))
	mux.HandleFunc(controlapi.StatusPath, s.authenticated(0, s.handleStatus))
	s.registerDomainRoutes(mux)
	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      controlapi.ControlServerWriteBudget,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.Done():
		}
	}()
	go func() {
		err := s.httpServer.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		s.stopAcceptingCommands()
		s.serveErrMu.Lock()
		s.serveErr = err
		s.serveErrMu.Unlock()
		close(s.serveDone)
		s.shutdownOnce.Do(func() { go s.shutdown() })
	}()
	return s, nil
}

func (s *Server) Addr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Close() error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	timeout := s.shutdownWait
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.CloseContext(ctx)
}

func (s *Server) CloseContext(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	if ctx == nil {
		return errors.Join(ErrShutdownIncomplete, errors.New("control: nil shutdown context"))
	}
	s.shutdownOnce.Do(func() {
		s.stopAcceptingCommands()
		go s.shutdown()
	})
	select {
	case <-s.closedDone:
		return errors.Join(s.shutdownErr, s.Wait())
	case <-ctx.Done():
		return errors.Join(ErrShutdownIncomplete, ctx.Err())
	}
}

func (s *Server) shutdown() {
	timeout := s.shutdownWait
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		if !errors.Is(err, net.ErrClosed) {
			s.shutdownErr = errors.Join(s.shutdownErr, err)
		}
		if closeErr := s.httpServer.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			s.shutdownErr = errors.Join(s.shutdownErr, closeErr)
		}
	}
	<-s.serveDone
	s.commands.Wait()
	close(s.closedDone)
}

func (s *Server) Done() <-chan struct{} {
	if s == nil || s.serveDone == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return s.serveDone
}

func (s *Server) Wait() error {
	if s == nil || s.closedDone == nil {
		return nil
	}
	<-s.closedDone
	s.serveErrMu.Lock()
	defer s.serveErrMu.Unlock()
	return s.serveErr
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ready := false
	state := "unavailable"
	if s.status != nil {
		if status, err := s.status.Status(r.Context()); err == nil {
			state = string(status.State)
			ready = status.State == controlapi.DaemonStateRunning
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": ready, "ready": ready, "state": state, "transport": "loopback_http_challenge_hmac", "provisional": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.status == nil {
		http.Error(w, "daemon status is unavailable", http.StatusServiceUnavailable)
		return
	}
	status, err := s.status.Status(r.Context())
	if err != nil {
		http.Error(w, "daemon status is unavailable", http.StatusServiceUnavailable)
		return
	}
	status.Idempotency = controlapi.IdempotencyStatus{
		Scope:             controlapi.IdempotencyScopeProcessMemory,
		RestartPersistent: false,
		Provisional:       true,
	}
	status.Control = controlapi.ControlStatus{
		CommandIngress:    s.commandIngress.Load(),
		CommandExecutions: s.commandExecutions.Load(),
		DomainIngress:     s.domainIngress.Load(),
		DomainExecutions:  s.domainExecutions.Load(),
	}
	if err := validateStatus(status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	s.commandIngress.Add(1)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req controlapi.Request
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		status := http.StatusBadRequest
		http.Error(w, err.Error(), status)
		return
	}
	if err := requireJSONEOF(dec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.RequestID == "" {
		http.Error(w, "request_id is required", http.StatusBadRequest)
		return
	}
	if err := validateCommandRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.beginCommand() {
		http.Error(w, "control.stopping", http.StatusServiceUnavailable)
		return
	}
	defer s.commands.Done()

	cache := cli.CommandMutates(req.Args) && !req.DryRun
	if cache {
		entry, leader, status := s.claimRequest(req)
		if status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
		if !leader {
			select {
			case <-entry.done:
				s.writeResponse(w, entry.response)
			case <-r.Context().Done():
			}
			return
		}
		response := s.executeCommandSafely(req)
		s.completeRequest(req.RequestID, entry, response)
		s.writeResponse(w, response)
		return
	}
	s.writeResponse(w, s.executeCommandSafely(req))
}

func (s *Server) executeCommandSafely(req controlapi.Request) (response controlapi.Response) {
	defer func() {
		if recover() != nil {
			response = commandExecutionFailureResponse(req, cli.CommandMutates(req.Args) && !req.DryRun)
		}
	}()
	return s.executeCommand(req)
}

func (s *Server) executeCommand(req controlapi.Request) controlapi.Response {
	s.commandExecutions.Add(1)
	mutates := cli.CommandMutates(req.Args) && !req.DryRun
	if !mutates {
		if isRuntimeReloadCommand(req.Args) {
			if reloader, ok := s.status.(RuntimeReloader); ok {
				return s.executeReload(reloader, req)
			}
		}
		if isConfigRestoreCommand(req.Args) {
			return s.executeRestoreLastKnownGood(req)
		}
		return s.executeCLICommand(s.ctx, req)
	}
	if req.Revision < 0 {
		return withMutationOutcome(
			runtimeCommandErrorResponse(
				req.JSON,
				publicerr.Errorf("config.revision_required", "mutating daemon commands require an expected revision"),
			),
			false,
			controlapi.MutationOutcomeNotApplied,
		)
	}

	mutationCtx, cancel := s.mutationContext()
	defer cancel()
	release, err := s.acquireCommandMutation(mutationCtx, req.Args)
	if err != nil {
		return withMutationOutcome(
			runtimeCommandErrorResponse(req.JSON, publicerr.Wrap("control.mutation_timeout", err)),
			false,
			controlapi.MutationOutcomeNotApplied,
		)
	}

	done := make(chan controlapi.Response, 1)
	executionDone := make(chan struct{})
	s.commands.Add(1)
	go func() {
		defer s.commands.Done()
		defer func() {
			close(executionDone)
			s.backgroundExecutionFinished(req.RequestID, executionDone)
		}()
		done <- executeMutationCommandSafely(req, func() controlapi.Response {
			defer release()
			if isRuntimeReloadCommand(req.Args) {
				if reloader, ok := s.status.(RuntimeReloader); ok {
					return s.executeReloadContext(mutationCtx, reloader, req)
				}
			}
			if isConfigRestoreCommand(req.Args) {
				return s.executeRestoreLastKnownGood(req)
			}
			return s.executeCLICommand(mutationCtx, req)
		})
	}()

	select {
	case response := <-done:
		return response
	case <-mutationCtx.Done():
		s.pinRequestExecution(req.RequestID, executionDone)
		return indeterminateCommandResponse(req, "control.mutation_timeout", "mutation execution exceeded its server budget")
	}
}

func (s *Server) executeCLICommand(ctx context.Context, req controlapi.Request) controlapi.Response {
	args := []string{"--offline", "--config", s.configPath}
	if req.JSON {
		args = append(args, "--json")
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.Revision >= 0 {
		args = append(args, "--revision", strconv.FormatInt(req.Revision, 10))
	}
	args = append(args, req.Args...)
	stdout := newBoundedBuffer(maxCommandOutputBytes)
	stderr := newBoundedBuffer(maxCommandOutputBytes)
	result := s.execute(ctx, args, &stdout, &stderr)
	if stdout.overflow || stderr.overflow {
		response := runtimeCommandErrorResponse(
			req.JSON,
			publicerr.Errorf("control.command_output_too_large", "command output exceeded its limit"),
		)
		return withMutationOutcome(response, result.Applied, result.Outcome)
	}
	return withMutationOutcome(controlapi.Response{
		ExitCode: result.ExitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, result.Applied, result.Outcome)
}

func executeMutationCommandSafely(req controlapi.Request, execute func() controlapi.Response) (response controlapi.Response) {
	defer func() {
		if recover() != nil {
			response = commandExecutionFailureResponse(req, true)
		}
	}()
	return execute()
}

func commandExecutionFailureResponse(req controlapi.Request, mutates bool) controlapi.Response {
	if mutates {
		return indeterminateCommandResponse(req, "control.command_execution_indeterminate", "mutation execution did not return")
	}
	return runtimeCommandErrorResponse(
		req.JSON,
		publicerr.Errorf("control.command_execution_failed", "command execution did not return"),
	)
}

func indeterminateCommandResponse(req controlapi.Request, code, message string) controlapi.Response {
	response := runtimeCommandErrorResponse(req.JSON, publicerr.Errorf(code, "%s", message))
	response.ExitCode = controlapi.IndeterminateExitCode
	return withMutationOutcome(response, false, controlapi.MutationOutcomeIndeterminate)
}

func withMutationOutcome(response controlapi.Response, applied bool, outcome controlapi.MutationOutcome) controlapi.Response {
	response.Applied = applied
	response.Outcome = outcome
	switch outcome {
	case controlapi.MutationOutcomeApplied:
		response.ExitCode = 0
	case controlapi.MutationOutcomeIndeterminate:
		response.ExitCode = controlapi.IndeterminateExitCode
	}
	return response
}

func isRuntimeReloadCommand(args []string) bool {
	return len(args) == 2 && args[0] == "local" && args[1] == "reload"
}

func isConfigRestoreCommand(args []string) bool {
	return len(args) == 3 && args[0] == "local" && args[1] == "config" && args[2] == "restore-last-good"
}

func (s *Server) executeRestoreLastKnownGood(req controlapi.Request) controlapi.Response {
	result, err := s.restoreLastKnownGood(req.Revision, req.DryRun)
	if err != nil {
		response := runtimeCommandErrorResponse(req.JSON, err)
		if req.DryRun {
			return response
		}
		applied, outcome := mutationErrorMetadata(err)
		return withMutationOutcome(response, applied, outcome)
	}
	payload := map[string]any{
		"ok":              true,
		"dry_run":         req.DryRun,
		"before_revision": result.BeforeRevision,
		"after_revision":  result.AfterRevision,
		"source":          "last-known-good",
	}
	if req.JSON {
		body, err := json.Marshal(payload)
		if err != nil {
			response := runtimeCommandErrorResponse(
				true,
				publicerr.Errorf("control.restore_response_invalid", "restore response could not be encoded"),
			)
			if !req.DryRun {
				return withMutationOutcome(response, true, controlapi.MutationOutcomeApplied)
			}
			return response
		}
		response := controlapi.Response{ExitCode: 0, Stdout: string(body) + "\n"}
		if !req.DryRun {
			return withMutationOutcome(response, true, controlapi.MutationOutcomeApplied)
		}
		return response
	}
	response := controlapi.Response{ExitCode: 0, Stdout: fmt.Sprintf(
		"restored=%t before_revision=%d after_revision=%d source=last-known-good\n",
		!req.DryRun,
		result.BeforeRevision,
		result.AfterRevision,
	)}
	if !req.DryRun {
		return withMutationOutcome(response, true, controlapi.MutationOutcomeApplied)
	}
	return response
}

func (s *Server) restoreLastKnownGood(revision int64, dryRun bool) (configstore.UpdateResult, error) {
	if s.restoreConfig != nil {
		return s.restoreConfig(revision, dryRun)
	}
	if s.ownershipKey == "" {
		return configstore.UpdateResult{}, publicerr.Errorf(
			"config.restore_unavailable",
			"last-known-good restore requires daemon-owned config identity",
		)
	}
	return configstore.RestorePinnedLastKnownGood(
		s.configPath,
		s.ownershipKey,
		revision,
		dryRun,
	)
}

func runtimeCommandErrorResponse(jsonOutput bool, err error) controlapi.Response {
	message := sanitizeRuntimeCommandError(err)
	if jsonOutput {
		body, _ := json.Marshal(map[string]any{
			"ok":         false,
			"error_code": runtimeCommandErrorCode(err),
			"message":    message,
		})
		return controlapi.Response{ExitCode: 1, Stdout: string(body) + "\n"}
	}
	return controlapi.Response{ExitCode: 1, Stderr: message + "\n"}
}

func (s *Server) executeReload(reloader RuntimeReloader, req controlapi.Request) controlapi.Response {
	ctx, cancel := s.mutationContext()
	defer cancel()
	return s.executeReloadContext(ctx, reloader, req)
}

func (s *Server) executeReloadContext(ctx context.Context, reloader RuntimeReloader, req controlapi.Request) controlapi.Response {
	status, reloadErr := reloader.Reload(ctx, req.Revision, req.DryRun)
	if req.DryRun && reloadErr != nil {
		return runtimeCommandErrorResponse(req.JSON, reloadErr)
	}
	if !req.DryRun {
		classifiedErr, applied, outcome := classifyReloadMutation(status, reloadErr, req.Revision)
		if classifiedErr != nil {
			response := runtimeCommandErrorResponse(req.JSON, classifiedErr)
			if outcome == controlapi.MutationOutcomeIndeterminate {
				response.ExitCode = controlapi.IndeterminateExitCode
			}
			return withMutationOutcome(response, applied, outcome)
		}
	}
	body, marshalErr := json.Marshal(map[string]any{
		"ok":                   true,
		"dry_run":              req.DryRun,
		"applied_revision":     status.AppliedRevision,
		"attempted_revision":   status.AttemptedRevision,
		"reconciliation_state": status.State,
	})
	if marshalErr != nil {
		response := runtimeCommandErrorResponse(
			req.JSON,
			publicerr.Errorf("control.reload_response_invalid", "reload response could not be encoded"),
		)
		if !req.DryRun {
			return withMutationOutcome(response, true, controlapi.MutationOutcomeApplied)
		}
		return response
	}
	response := controlapi.Response{ExitCode: 0, Stdout: string(body) + "\n"}
	if !req.DryRun {
		return withMutationOutcome(response, true, controlapi.MutationOutcomeApplied)
	}
	return response
}

func mutationErrorMetadata(err error) (bool, controlapi.MutationOutcome) {
	if configstore.CommitVisible(err) {
		return true, controlapi.MutationOutcomeApplied
	}
	if errors.Is(err, configstore.ErrCommitOutcomeUnknown) {
		return false, controlapi.MutationOutcomeIndeterminate
	}
	return false, controlapi.MutationOutcomeNotApplied
}

func runtimeCommandErrorCode(err error) string {
	return publicerr.Code(err, "service.reload_failed")
}

func sanitizeRuntimeCommandError(err error) string {
	return publicerr.Message(err, "service.reload_failed")
}

func (s *Server) writeResponse(w http.ResponseWriter, response controlapi.Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) claimRequest(request controlapi.Request) (*cachedResponse, bool, int) {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	if cached, ok := s.requests[request.RequestID]; ok {
		if !sameRequest(cached.request, request) {
			return nil, false, http.StatusConflict
		}
		return cached, false, 0
	}
	if len(s.requests) >= requestCacheCapacity {
		s.evictCompletedRequestsLocked()
		if len(s.requests) >= requestCacheCapacity {
			return nil, false, http.StatusServiceUnavailable
		}
	}
	entry := &cachedResponse{request: request, done: make(chan struct{})}
	s.requests[request.RequestID] = entry
	return entry, true, 0
}

func (s *Server) completeRequest(requestID string, entry *cachedResponse, response controlapi.Response) {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	current, ok := s.requests[requestID]
	if !ok || current != entry || entry.completed {
		panic("controlserver: invalid request completion")
	}
	entry.response = response
	entry.completed = true
	entry.protected = response.Outcome == controlapi.MutationOutcomeIndeterminate
	if !entry.protected {
		s.completedRequests = append(s.completedRequests, requestID)
	}
	close(entry.done)
}

func (s *Server) pinRequestExecution(requestID string, done <-chan struct{}) {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	if entry, ok := s.requests[requestID]; ok && !entry.completed {
		entry.executionDone = done
	}
}

func (s *Server) backgroundExecutionFinished(requestID string, done <-chan struct{}) {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	entry, ok := s.requests[requestID]
	if !ok || entry.executionDone != done {
		return
	}
	entry.executionDone = nil
}

func (s *Server) evictCompletedRequestsLocked() {
	for len(s.requests) >= requestCacheCapacity && len(s.completedRequests) > 0 {
		requestID := s.completedRequests[0]
		s.completedRequests[0] = ""
		s.completedRequests = s.completedRequests[1:]
		entry, ok := s.requests[requestID]
		if ok && entry.completed {
			delete(s.requests, requestID)
		}
	}
	if len(s.completedRequests) == 0 {
		s.completedRequests = nil
	}
}

func (s *Server) beginCommand() bool {
	s.commandsMu.Lock()
	defer s.commandsMu.Unlock()
	if s.closing {
		return false
	}
	s.commands.Add(1)
	return true
}

func (s *Server) stopAcceptingCommands() {
	s.commandsMu.Lock()
	s.closing = true
	s.commandsMu.Unlock()
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func newBoundedBuffer(limit int) boundedBuffer { return boundedBuffer{limit: limit} }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.overflow = true
		return len(data), nil
	}
	return b.Buffer.Write(data)
}

func validateCommandRequest(request controlapi.Request) error {
	if len(request.RequestID) != 32 {
		return fmt.Errorf("control.request_id_invalid")
	}
	for _, character := range request.RequestID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("control.request_id_invalid")
		}
	}
	for _, argument := range request.Args {
		if forbiddenGlobalArgument(argument) {
			return fmt.Errorf("control.global_argument_forbidden: %s", argument)
		}
	}
	return nil
}

func forbiddenGlobalArgument(argument string) bool {
	if argument == "--" {
		return true
	}
	if !strings.HasPrefix(argument, "-") {
		return false
	}
	trimmed := strings.TrimLeft(argument, "-")
	name := trimmed
	if index := strings.IndexByte(name, '='); index >= 0 {
		name = name[:index]
	}
	switch name {
	case "config", "control", "offline", "json", "dry-run", "revision":
		return true
	default:
		return false
	}
}

func requireJSONEOF(dec *json.Decoder) error {
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body contains trailing JSON")
		}
		return fmt.Errorf("request body contains trailing data: %w", err)
	}
	return nil
}

func sameRequest(a, b controlapi.Request) bool {
	if a.JSON != b.JSON || a.DryRun != b.DryRun || a.Revision != b.Revision || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	return true
}

func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if _, err := readBoundedRequestBody(r.Body, 0); err != nil {
		http.Error(w, "control.challenge_body_forbidden", http.StatusBadRequest)
		return
	}
	challenge, err := s.issueChallenge()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(challenge)
}

func (s *Server) issueChallenge() (controlapi.Challenge, error) {
	token, err := controlapi.ReadToken(s.tokenPath)
	if err != nil {
		return controlapi.Challenge{}, fmt.Errorf("control.auth_unavailable: %w", err)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return controlapi.Challenge{}, fmt.Errorf("control.challenge_random_failed")
	}
	nonce := hex.EncodeToString(raw[:])
	now := s.now()
	expiresAt := now.Add(s.challengeTTL)
	return controlapi.SignChallenge(token, nonce, expiresAt, s.authEpoch)
}

func (s *Server) authenticated(maxBody int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		challenge, ok := controlapi.RequestChallenge(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := readBoundedRequestBody(r.Body, maxBody)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		token, err := controlapi.ReadToken(s.tokenPath)
		if err != nil {
			http.Error(w, "control.auth_unavailable", http.StatusServiceUnavailable)
			return
		}
		if challenge.ServerEpoch != s.authEpoch || !controlapi.VerifyChallenge(token, challenge, s.now()) ||
			!controlapi.VerifyRequestAuthentication(r, token, challenge, body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !s.claimChallenge(challenge) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		recorder := newBufferedResponse(maxResponseBodyBytes)
		next(recorder, r)
		status, responseBody := recorder.result()
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if err := controlapi.SignResponse(w.Header(), token, r, body, status, responseBody); err != nil {
			w.Header().Del(controlapi.ResponseAuthHeader)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(responseBody)
	}
}

func (s *Server) claimChallenge(challenge controlapi.Challenge) bool {
	s.challengesMu.Lock()
	defer s.challengesMu.Unlock()
	now := s.now()
	s.removeExpiredChallengesLocked(now)
	if _, exists := s.usedChallenges[challenge.Nonce]; exists {
		return false
	}
	if len(s.usedChallenges) >= s.challengeMax {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, challenge.ExpiresAt)
	if err != nil || !expiresAt.After(now) {
		return false
	}
	s.usedChallenges[challenge.Nonce] = expiresAt
	s.usedChallengeOrder = append(s.usedChallengeOrder, challenge.Nonce)
	return true
}

func (s *Server) removeExpiredChallengesLocked(now time.Time) {
	kept := s.usedChallengeOrder[:0]
	for _, nonce := range s.usedChallengeOrder {
		expiresAt, exists := s.usedChallenges[nonce]
		if !exists || !expiresAt.After(now) {
			delete(s.usedChallenges, nonce)
			continue
		}
		kept = append(kept, nonce)
	}
	s.usedChallengeOrder = kept
	if len(kept) == 0 {
		s.usedChallengeOrder = nil
	}
}

func readBoundedRequestBody(body io.ReadCloser, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("control.request_too_large")
	}
	return data, nil
}

type bufferedResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	limit    int
	overflow bool
}

func newBufferedResponse(limit int) *bufferedResponse {
	return &bufferedResponse{header: make(http.Header), limit: limit}
}

func (w *bufferedResponse) Header() http.Header { return w.header }

func (w *bufferedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	remaining := w.limit - w.body.Len()
	if len(data) > remaining {
		if remaining > 0 {
			_, _ = w.body.Write(data[:remaining])
		}
		w.overflow = true
		return len(data), nil
	}
	return w.body.Write(data)
}

func (w *bufferedResponse) result() (int, []byte) {
	if w.overflow {
		return http.StatusInternalServerError, []byte("control.response_too_large\n")
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.status, w.body.Bytes()
}

func validateStatus(status controlapi.DaemonStatus) error {
	if status.APIVersion != controlapi.APIVersion {
		return fmt.Errorf("control.status_api_version_invalid: %d", status.APIVersion)
	}
	if status.Idempotency.Scope != controlapi.IdempotencyScopeProcessMemory ||
		status.Idempotency.RestartPersistent || !status.Idempotency.Provisional {
		return fmt.Errorf("control.status_idempotency_invalid")
	}
	switch status.State {
	case controlapi.DaemonStateStarting, controlapi.DaemonStateRunning, controlapi.DaemonStateDegraded,
		controlapi.DaemonStateStopping, controlapi.DaemonStateStopped:
	default:
		return fmt.Errorf("control.status_state_invalid: %q", status.State)
	}
	if status.Configuration.SchemaVersion < 1 || status.Configuration.LastKnownGoodRevision < 0 {
		return fmt.Errorf("control.status_configuration_invalid")
	}
	switch status.Configuration.LastKnownGoodError {
	case "", controlapi.LastKnownGoodPersistFailed, controlapi.LastKnownGoodRevisionAheadOfApplied:
	default:
		return fmt.Errorf("control.status_last_good_error_invalid")
	}
	if rollback := status.Configuration.StartupRollback; rollback != nil {
		configuredRevisionValid := rollback.ConfiguredRevision >= 0
		if rollback.ErrorCode == "config.startup_content_invalid" {
			configuredRevisionValid = rollback.ConfiguredRevision == -1
		}
		rollbackStateValid := status.State == controlapi.DaemonStateDegraded ||
			status.State == controlapi.DaemonStateStopping ||
			status.State == controlapi.DaemonStateStopped
		if !rollbackStateValid ||
			!configuredRevisionValid ||
			rollback.AppliedRevision < status.Configuration.LastKnownGoodRevision ||
			rollback.AppliedRevision > status.Reconcile.AppliedRevision ||
			rollback.ErrorCode == "" ||
			publicerr.NormalizeCode(rollback.ErrorCode, "operation.failed") != rollback.ErrorCode {
			return fmt.Errorf("control.status_startup_rollback_invalid")
		}
	}
	if status.Reconcile.LastErrorCode != "" &&
		(status.Reconcile.LastError == "" ||
			publicerr.NormalizeCode(status.Reconcile.LastErrorCode, "operation.failed") != status.Reconcile.LastErrorCode) {
		return fmt.Errorf("control.status_reconcile_error_code_invalid")
	}
	if status.State == controlapi.DaemonStateRunning &&
		(status.Configuration.LastKnownGoodError != "" ||
			status.Configuration.StartupRollback != nil ||
			status.Configuration.LastKnownGoodRevision != status.Reconcile.AppliedRevision ||
			status.Reconcile.State != controlapi.ReconcileStateApplied ||
			status.Revision != status.Reconcile.AppliedRevision) {
		return fmt.Errorf("control.status_running_with_stale_last_good")
	}
	if err := validateRuntimeStatus("rendr", status.Rendr); err != nil {
		return err
	}
	switch status.Xray.State {
	case controlapi.RuntimeStateUnavailable, controlapi.RuntimeStateStarting,
		controlapi.RuntimeStateRunning, controlapi.RuntimeStateStopping,
		controlapi.RuntimeStateStopped, controlapi.RuntimeStateFailed:
	default:
		return fmt.Errorf("control.status_xray_state_invalid: %q", status.Xray.State)
	}
	if status.Xray.StrictPacketOutbound {
		return fmt.Errorf("control.status_xray_packet_outbound_unverified")
	}
	if status.Xray.FailStopped && status.Xray.State != controlapi.RuntimeStateFailed {
		return fmt.Errorf("control.status_xray_fail_stop_state_invalid")
	}
	if status.Xray.Current != nil && status.Xray.State != controlapi.RuntimeStateRunning &&
		status.Xray.State != controlapi.RuntimeStateStopping &&
		!(status.Xray.FailStopped && status.Xray.State == controlapi.RuntimeStateFailed) {
		return fmt.Errorf("control.status_xray_generation_without_running_runtime")
	}
	for _, inbound := range status.Xray.Inbounds {
		if inbound.Tag == "" {
			return fmt.Errorf("control.status_xray_inbound_tag_missing")
		}
		switch inbound.State {
		case "bound", "missing", "unexpected", "unavailable":
		default:
			return fmt.Errorf("control.status_xray_inbound_state_invalid: %q", inbound.State)
		}
	}
	return nil
}

func validateRuntimeStatus(name string, status controlapi.RuntimeStatus) error {
	switch status.State {
	case controlapi.RuntimeStateUnavailable, controlapi.RuntimeStateStarting,
		controlapi.RuntimeStateRunning, controlapi.RuntimeStateStopping,
		controlapi.RuntimeStateStopped, controlapi.RuntimeStateFailed:
	default:
		return fmt.Errorf("control.status_%s_state_invalid: %q", name, status.State)
	}
	if status.InstanceID != "" && status.State != controlapi.RuntimeStateRunning {
		return fmt.Errorf("control.status_%s_instance_without_running_runtime", name)
	}
	if name == "rendr" && status.State != controlapi.RuntimeStateUnavailable {
		if status.StreamFactory != "xray-stream" || status.StreamCarrier != "unknown" ||
			status.MobilityMode != "redial_attach" || status.EndpointOwned || status.PacketSupported {
			return fmt.Errorf("control.status_rendr_capability_invalid")
		}
	}
	return nil
}
