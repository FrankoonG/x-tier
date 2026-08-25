package controlapi

import "net/http"

const (
	DomainAPIVersion = 1

	DomainLocalPath           = "/v1/domain/local"
	DomainIdentityPath        = "/v1/domain/identity"
	DomainIdentityInitPath    = "/v1/domain/identity/init"
	DomainSettingsPath        = "/v1/domain/settings"
	DomainPeersPath           = "/v1/domain/peers"
	DomainPeerStatePath       = "/v1/domain/peers/state"
	DomainInboundsPath        = "/v1/domain/inbounds"
	DomainInboundStatePath    = "/v1/domain/inbounds/state"
	DomainXrayProfilesPath    = "/v1/domain/xray-profiles"
	DomainProfileValidatePath = "/v1/domain/xray-profiles/validate"
	DomainPathCompilePath     = "/v1/domain/paths/compile"
	DomainRuntimeReloadPath   = "/v1/domain/runtime/reload"
	DomainConfigRestorePath   = "/v1/domain/config/restore-last-good"
)

// DomainRoute is the exact, closed route set shared by the authenticated
// control server and the browser bridge. Mutating describes backend state, not
// whether the HTTP method requires browser CSRF protection.
type DomainRoute struct {
	Path     string
	Method   string
	Mutating bool
}

var domainRoutes = []DomainRoute{
	{Path: DomainLocalPath, Method: http.MethodGet},
	{Path: DomainIdentityPath, Method: http.MethodGet},
	{Path: DomainIdentityInitPath, Method: http.MethodPost, Mutating: true},
	{Path: DomainIdentityPath, Method: http.MethodPatch, Mutating: true},
	{Path: DomainSettingsPath, Method: http.MethodGet},
	{Path: DomainSettingsPath, Method: http.MethodPatch, Mutating: true},
	{Path: DomainPeersPath, Method: http.MethodGet},
	{Path: DomainPeersPath, Method: http.MethodPost, Mutating: true},
	{Path: DomainPeersPath, Method: http.MethodPatch, Mutating: true},
	{Path: DomainPeersPath, Method: http.MethodDelete, Mutating: true},
	{Path: DomainPeerStatePath, Method: http.MethodPatch, Mutating: true},
	{Path: DomainInboundsPath, Method: http.MethodGet},
	{Path: DomainInboundsPath, Method: http.MethodPut, Mutating: true},
	{Path: DomainInboundStatePath, Method: http.MethodPatch, Mutating: true},
	{Path: DomainXrayProfilesPath, Method: http.MethodGet},
	{Path: DomainXrayProfilesPath, Method: http.MethodPut, Mutating: true},
	{Path: DomainXrayProfilesPath, Method: http.MethodDelete, Mutating: true},
	{Path: DomainProfileValidatePath, Method: http.MethodPost},
	{Path: DomainPathCompilePath, Method: http.MethodPost},
	{Path: DomainRuntimeReloadPath, Method: http.MethodPost, Mutating: true},
	{Path: DomainConfigRestorePath, Method: http.MethodPost, Mutating: true},
}

func DomainRoutes() []DomainRoute {
	return append([]DomainRoute(nil), domainRoutes...)
}

func LookupDomainRoute(path, method string) (DomainRoute, bool) {
	for _, route := range domainRoutes {
		if route.Path == path && route.Method == method {
			return route, true
		}
	}
	return DomainRoute{}, false
}

func IsDomainPath(path string) bool {
	for _, route := range domainRoutes {
		if route.Path == path {
			return true
		}
	}
	return false
}

type DomainRequest struct {
	APIVersion int `json:"api_version"`
}

type DomainMutationRequest struct {
	APIVersion int    `json:"api_version"`
	Revision   int64  `json:"revision"`
	DryRun     bool   `json:"dry_run"`
	RequestID  string `json:"request_id"`
}

type DomainError struct {
	APIVersion   int                   `json:"api_version"`
	OK           bool                  `json:"ok"`
	ErrorCode    string                `json:"error_code"`
	Message      string                `json:"message"`
	Applied      *bool                 `json:"applied,omitempty"`
	Outcome      MutationOutcome       `json:"outcome,omitempty"`
	Preparations []MutationPreparation `json:"preparations,omitempty"`
}

type MutationPreparation struct {
	Kind   string `json:"kind"`
	State  string `json:"state"`
	NodeID string `json:"node_id,omitempty"`
}

type IdentityInitRequest struct {
	DomainMutationRequest
	Name string `json:"name,omitempty"`
}

type IdentityRenameRequest struct {
	DomainMutationRequest
	Name string `json:"name"`
}

type SettingsPatch struct {
	LogLevel         *string `json:"log_level,omitempty"`
	MaxNestedDepth   *int    `json:"max_nested_depth,omitempty"`
	MaxResponseNodes *int    `json:"max_response_nodes,omitempty"`
	MaxResponseBytes *int    `json:"max_response_bytes,omitempty"`
	MaxCacheEntries  *int    `json:"max_cache_entries,omitempty"`
	MaxFetchFanOut   *int    `json:"max_fetch_fan_out,omitempty"`
}

type SettingsUpdateRequest struct {
	DomainMutationRequest
	Settings SettingsPatch `json:"settings"`
}

type InboundPutRequest struct {
	DomainMutationRequest
	Kind          string  `json:"kind"`
	Listen        *string `json:"listen,omitempty"`
	Purpose       *string `json:"purpose,omitempty"`
	XrayProfileID *string `json:"xray_profile_id,omitempty"`
	ExitPeer      *string `json:"exit_peer,omitempty"`
}

type InboundStateRequest struct {
	DomainMutationRequest
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

type PeerCreateRequest struct {
	DomainMutationRequest
	Name          string `json:"name"`
	NodeID        string `json:"node_id"`
	Addr          string `json:"addr,omitempty"`
	Direction     string `json:"direction"`
	XrayProfileID string `json:"xray_profile_id,omitempty"`
	NestedEnabled bool   `json:"nested_enabled"`
}

type PeerPatch struct {
	Addr          *string `json:"addr,omitempty"`
	Direction     *string `json:"direction,omitempty"`
	XrayProfileID *string `json:"xray_profile_id,omitempty"`
	NestedEnabled *bool   `json:"nested_enabled,omitempty"`
}

type PeerUpdateRequest struct {
	DomainMutationRequest
	Name  string    `json:"name"`
	Patch PeerPatch `json:"patch"`
}

type PeerStateRequest struct {
	DomainMutationRequest
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

type PeerRemoveRequest struct {
	DomainMutationRequest
	Name string `json:"name"`
}

type XrayProfilePutRequest struct {
	DomainMutationRequest
	ID                     string `json:"id"`
	Kind                   string `json:"kind"`
	Credential             string `json:"credential"`
	Username               string `json:"username,omitempty"`
	Transport              string `json:"transport,omitempty"`
	Security               string `json:"security,omitempty"`
	AllowInsecurePlaintext bool   `json:"allow_insecure_plaintext,omitempty"`
}

type XrayProfileRemoveRequest struct {
	DomainMutationRequest
	ID string `json:"id"`
}

type XrayProfileValidateRequest struct {
	DomainRequest
	ID string `json:"id,omitempty"`
}

type PathCompileRequest struct {
	DomainRequest
	Expression   string `json:"expression"`
	Strategy     string `json:"strategy"`
	EndpointKind string `json:"endpoint_kind"`
}

type RuntimeReloadRequest struct {
	DomainMutationRequest
}

type ConfigRestoreRequest struct {
	DomainMutationRequest
}
