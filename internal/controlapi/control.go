package controlapi

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
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	DefaultAddr = "127.0.0.1:19090"

	CommandPath = "/v1/command"
	HealthPath  = "/v1/health"
	StatusPath  = "/v1/status"

	APIVersion        = 1
	maxRequestBytes   = 1 << 20
	maxResponseBytes  = 2 << 20
	maxChallengeBytes = 16 << 10
)

type DaemonState string

const (
	DaemonStateStarting DaemonState = "starting"
	DaemonStateRunning  DaemonState = "running"
	DaemonStateDegraded DaemonState = "degraded"
	DaemonStateStopping DaemonState = "stopping"
	DaemonStateStopped  DaemonState = "stopped"
)

type RuntimeState string

const (
	RuntimeStateUnavailable RuntimeState = "unavailable"
	RuntimeStateStarting    RuntimeState = "starting"
	RuntimeStateRunning     RuntimeState = "running"
	RuntimeStateStopping    RuntimeState = "stopping"
	RuntimeStateStopped     RuntimeState = "stopped"
	RuntimeStateFailed      RuntimeState = "failed"
)

// RuntimeStatus describes observed runtime state. InstanceID is omitted until
// a runtime provider supplies an ID from a live instance.
type RuntimeStatus struct {
	State            RuntimeState `json:"state"`
	InstanceID       string       `json:"instance_id,omitempty"`
	InstanceIDSource string       `json:"instance_id_source,omitempty"`
	ActiveClient     int64        `json:"active_client_sessions,omitempty"`
	ActiveAccepted   int64        `json:"active_accepted_sessions,omitempty"`
	AcceptedFlowIDs  int          `json:"accepted_flow_ids,omitempty"`
	TotalClient      uint64       `json:"total_client_sessions,omitempty"`
	TotalAccepted    uint64       `json:"total_accepted_sessions,omitempty"`
	LastError        string       `json:"last_error,omitempty"`
	ObservedAt       time.Time    `json:"observed_at,omitempty"`
	StreamFactory    string       `json:"stream_factory,omitempty"`
	StreamCarrier    string       `json:"stream_carrier,omitempty"`
	MobilityMode     string       `json:"mobility_mode,omitempty"`
	EndpointOwned    bool         `json:"endpoint_owned"`
	PacketSupported  bool         `json:"packet_supported"`
}

type ReconcileState string

const (
	ReconcileStatePending ReconcileState = "pending"
	ReconcileStateApplied ReconcileState = "applied"
	ReconcileStateFailed  ReconcileState = "failed"
)

type ReconcileStatus struct {
	State                  ReconcileState `json:"state"`
	AppliedRevision        int64          `json:"applied_revision"`
	AttemptedRevision      int64          `json:"attempted_revision"`
	ConfigurationPublished bool           `json:"configuration_published"`
	LastError              string         `json:"last_error,omitempty"`
	LastErrorCode          string         `json:"last_error_code,omitempty"`
	ObservedAt             time.Time      `json:"observed_at"`
	ObservationFresh       bool           `json:"observation_fresh"`
	ConsecutiveFailures    int            `json:"consecutive_failures,omitempty"`
	FirstFailureAt         *time.Time     `json:"first_failure_at,omitempty"`
	NextRetryAt            *time.Time     `json:"next_retry_at,omitempty"`
}

type XrayGenerationStatus struct {
	Generation   uint64 `json:"generation"`
	RefCount     int64  `json:"ref_count"`
	Draining     bool   `json:"draining"`
	CleanupError string `json:"cleanup_error,omitempty"`
}

type XrayInboundStatus struct {
	Tag    string `json:"tag"`
	Listen string `json:"listen,omitempty"`
	State  string `json:"state"`
}

type XrayStatus struct {
	State                RuntimeState           `json:"state"`
	FailStopped          bool                   `json:"fail_stopped"`
	Current              *XrayGenerationStatus  `json:"current,omitempty"`
	Draining             []XrayGenerationStatus `json:"draining"`
	StrictStreamOutbound bool                   `json:"strict_stream_outbound"`
	StrictPacketOutbound bool                   `json:"strict_packet_outbound"`
	Inbounds             []XrayInboundStatus    `json:"inbounds"`
}

type StartupRollbackStatus struct {
	ConfiguredRevision int64  `json:"configured_revision"`
	AppliedRevision    int64  `json:"applied_revision"`
	ErrorCode          string `json:"error_code"`
}

type IdempotencyScope string

const IdempotencyScopeProcessMemory IdempotencyScope = "process_memory"

type IdempotencyStatus struct {
	Scope             IdempotencyScope `json:"scope"`
	RestartPersistent bool             `json:"restart_persistent"`
	Provisional       bool             `json:"provisional"`
}

type ControlStatus struct {
	CommandIngress    uint64 `json:"command_ingress"`
	CommandExecutions uint64 `json:"command_executions"`
	DomainIngress     uint64 `json:"domain_ingress"`
	DomainExecutions  uint64 `json:"domain_executions"`
}

type ConfigurationStatus struct {
	SchemaVersion         int                    `json:"schema_version"`
	MigratedAtStartup     bool                   `json:"migrated_at_startup"`
	LastKnownGoodRevision int64                  `json:"last_known_good_revision"`
	LastKnownGoodError    string                 `json:"last_known_good_error,omitempty"`
	StartupRollback       *StartupRollbackStatus `json:"startup_rollback,omitempty"`
}

const (
	LastKnownGoodPersistFailed          = "lastgood.persist_failed"
	LastKnownGoodRevisionAheadOfApplied = "lastgood.revision_ahead_of_applied"
)

type DaemonStatus struct {
	APIVersion    int                 `json:"api_version"`
	BootID        string              `json:"boot_id"`
	State         DaemonState         `json:"state"`
	Revision      int64               `json:"revision"`
	Reconcile     ReconcileStatus     `json:"reconcile"`
	ConfigPath    string              `json:"config_path"`
	ControlAddr   string              `json:"control_addr"`
	WebAddr       string              `json:"web_addr,omitempty"`
	StartedAt     time.Time           `json:"started_at"`
	Idempotency   IdempotencyStatus   `json:"idempotency"`
	Control       ControlStatus       `json:"control"`
	Configuration ConfigurationStatus `json:"configuration"`
	Rendr         RuntimeStatus       `json:"rendr"`
	Xray          XrayStatus          `json:"xray"`
}

type StatusProvider interface {
	Status(context.Context) (DaemonStatus, error)
}

type StatusProviderFunc func(context.Context) (DaemonStatus, error)

func (f StatusProviderFunc) Status(ctx context.Context) (DaemonStatus, error) {
	return f(ctx)
}

type Request struct {
	Args      []string `json:"args"`
	JSON      bool     `json:"json"`
	DryRun    bool     `json:"dry_run"`
	Revision  int64    `json:"revision"`
	RequestID string   `json:"request_id,omitempty"`
}

type MutationOutcome string

const (
	MutationOutcomeApplied       MutationOutcome = "applied"
	MutationOutcomeNotApplied    MutationOutcome = "not_applied"
	MutationOutcomeIndeterminate MutationOutcome = "indeterminate"
	IndeterminateExitCode                        = 3
)

type Response struct {
	ExitCode int             `json:"exit_code"`
	Stdout   string          `json:"stdout,omitempty"`
	Stderr   string          `json:"stderr,omitempty"`
	Applied  bool            `json:"applied"`
	Outcome  MutationOutcome `json:"outcome,omitempty"`
}

type requestDeliveryError struct {
	err error
}

func (e requestDeliveryError) Error() string { return e.err.Error() }
func (e requestDeliveryError) Unwrap() error { return e.err }

// CommandMayHaveApplied reports whether a command exchange failed after the
// authenticated request may have reached the daemon. Absence of this marker
// means the request was rejected or failed before delivery.
func CommandMayHaveApplied(err error) bool {
	var delivered requestDeliveryError
	return errors.As(err, &delivered)
}

func URL(addr string) string {
	if addr == "" {
		addr = DefaultAddr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + addr
}

func Execute(addr, tokenPath string, req Request) (Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), MutationClientBudget)
	defer cancel()
	return ExecuteContext(ctx, addr, tokenPath, req)
}

func ExecuteContext(ctx context.Context, addr, tokenPath string, req Request) (Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	status, body, err := AuthenticatedRequestContext(ctx, addr, tokenPath, http.MethodPost, CommandPath, b)
	if err != nil {
		return Response{}, err
	}
	if status != http.StatusOK {
		err := fmt.Errorf("control.http_status: %d %s", status, strings.TrimSpace(string(body)))
		if commandHTTPStatusMayHaveExecuted(status) {
			return Response{}, requestDeliveryError{err: err}
		}
		return Response{}, err
	}
	var out Response
	if err := decodeStrictResponse(body, &out); err != nil {
		return Response{}, requestDeliveryError{err: err}
	}
	return out, nil
}

func commandHTTPStatusMayHaveExecuted(status int) bool {
	switch status {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusMethodNotAllowed,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusServiceUnavailable:
		return false
	default:
		return true
	}
}

func GetStatus(addr, tokenPath string) (DaemonStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ReadRequestBudget)
	defer cancel()
	return GetStatusContext(ctx, addr, tokenPath)
}

func GetStatusContext(ctx context.Context, addr, tokenPath string) (DaemonStatus, error) {
	statusCode, body, err := AuthenticatedRequestContext(ctx, addr, tokenPath, http.MethodGet, StatusPath, nil)
	if err != nil {
		return DaemonStatus{}, err
	}
	if statusCode != http.StatusOK {
		return DaemonStatus{}, fmt.Errorf("control.http_status: %d %s", statusCode, strings.TrimSpace(string(body)))
	}
	var status DaemonStatus
	if err := decodeStrictJSON(body, &status); err != nil {
		return DaemonStatus{}, fmt.Errorf("control.status_invalid: %w", err)
	}
	return status, nil
}

// AuthenticatedRequestContext performs the complete challenge/request/response
// exchange. It is also the supported helper for callers that need a raw status
// and body while retaining all transport authentication checks.
func AuthenticatedRequestContext(ctx context.Context, addr, tokenPath, method, path string, body []byte) (int, []byte, error) {
	if ctx == nil {
		return 0, nil, fmt.Errorf("control.context_nil")
	}
	endpoint, err := localControlURL(addr)
	if err != nil {
		return 0, nil, err
	}
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return 0, nil, fmt.Errorf("control.path_invalid")
	}
	if len(body) > maxRequestBytes {
		return 0, nil, fmt.Errorf("control.request_too_large")
	}
	requestBody := append([]byte(nil), body...)
	token, err := ReadToken(tokenPath)
	if err != nil {
		return 0, nil, fmt.Errorf("control.token_unavailable: %w", err)
	}
	client := localHTTPClient()
	defer client.CloseIdleConnections()
	challenge, err := fetchChallenge(ctx, client, endpoint, token)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(requestBody))
	if err != nil {
		return 0, nil, err
	}
	if len(requestBody) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if err := AuthenticateRequest(request, token, challenge, requestBody); err != nil {
		return 0, nil, err
	}
	response, err := doTrackedAuthenticatedRequest(client, request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBody, err := readBoundedResponse(response.Body)
	if err != nil {
		return 0, nil, requestDeliveryError{err: err}
	}
	if !VerifyResponseAuthentication(response.Header, token, request, requestBody, response.StatusCode, responseBody) {
		return 0, nil, requestDeliveryError{err: fmt.Errorf("control.response_auth_invalid")}
	}
	return response.StatusCode, responseBody, nil
}

func doTrackedAuthenticatedRequest(client *http.Client, request *http.Request) (*http.Response, error) {
	var requestWriteStarted atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			// net/http may report this before its buffered writer flushes. That is
			// still a "may have arrived" boundary: a write error can be partial,
			// so treating it as definitely not applied would be unsafe.
			requestWriteStarted.Store(true)
		},
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := client.Do(request)
	if err == nil {
		return response, nil
	}
	unavailable := fmt.Errorf("control.unavailable: %w", err)
	if requestWriteStarted.Load() {
		return nil, requestDeliveryError{err: unavailable}
	}
	return nil, unavailable
}

func decodeStrictResponse(body []byte, response *Response) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	if _, ok := fields["exit_code"]; !ok {
		return fmt.Errorf("control.response_exit_code_missing")
	}
	if err := decodeStrictJSON(body, response); err != nil {
		return err
	}
	switch response.Outcome {
	case "":
		if response.Applied {
			return fmt.Errorf("control.response_outcome_missing")
		}
	case MutationOutcomeApplied:
		if !response.Applied {
			return fmt.Errorf("control.response_applied_invalid")
		}
		if response.ExitCode != 0 {
			return fmt.Errorf("control.response_exit_code_invalid")
		}
	case MutationOutcomeNotApplied:
		if response.Applied {
			return fmt.Errorf("control.response_applied_invalid")
		}
		if response.ExitCode == 0 {
			return fmt.Errorf("control.response_exit_code_invalid")
		}
	case MutationOutcomeIndeterminate:
		if response.Applied {
			return fmt.Errorf("control.response_applied_invalid")
		}
		if response.ExitCode != IndeterminateExitCode {
			return fmt.Errorf("control.response_exit_code_invalid")
		}
	default:
		return fmt.Errorf("control.response_outcome_invalid")
	}
	return nil
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func fetchChallenge(ctx context.Context, client *http.Client, endpoint, token string) (Challenge, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+ChallengePath, nil)
	if err != nil {
		return Challenge{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Challenge{}, fmt.Errorf("control.unavailable: %w", err)
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxChallengeBytes, "control.challenge_response_too_large")
	if err != nil {
		return Challenge{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Challenge{}, fmt.Errorf("control.challenge_http_status: %d", response.StatusCode)
	}
	var challenge Challenge
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&challenge); err != nil {
		return Challenge{}, fmt.Errorf("control.challenge_invalid: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Challenge{}, fmt.Errorf("control.challenge_invalid: %w", err)
	}
	if !VerifyChallenge(token, challenge, time.Now()) {
		return Challenge{}, fmt.Errorf("control.challenge_auth_invalid")
	}
	return challenge, nil
}

func localControlURL(addr string) (string, error) {
	endpoint := URL(addr)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("control.addr_invalid: %s", addr)
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("control.non_loopback_forbidden: %s", addr)
	}
	if parsed.Port() == "" {
		return "", fmt.Errorf("control.addr_port_required: %s", addr)
	}
	return strings.TrimRight(endpoint, "/"), nil
}

func localHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                  nil,
		MaxResponseHeaderBytes: 16 << 10,
		IdleConnTimeout:        30 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("control.addr_invalid: %w", err)
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("control.non_loopback_forbidden: %s", address)
			}
			return (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		},
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("control.redirect_forbidden")
		},
	}
}

func readBoundedResponse(reader io.Reader) ([]byte, error) {
	return readBounded(reader, maxResponseBytes, "control.response_too_large")
}

func readBounded(reader io.Reader, limit int64, tooLarge string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s", tooLarge)
	}
	return body, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON")
		}
		return err
	}
	return nil
}

func TokenPath(configPath string) string {
	return configPath + ".control-token"
}

func NewRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func CreateToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty token path")
	}
	if token, err := ReadToken(path); err == nil {
		return token, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	f, err := createSecretFile(path)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(f, token+"\n"); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return token, nil
}

func ReadToken(path string) (string, error) {
	b, err := readSecretFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("invalid control token")
	}
	return token, nil
}
