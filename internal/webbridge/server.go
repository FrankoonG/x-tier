package webbridge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

const (
	DefaultAddr = "127.0.0.1:19091"

	CSRFHeader          = "X-XTier-CSRF-Token"
	SessionCookieName   = "xtier_web_session"
	SessionPath         = "/v1/web/session"
	MaxDomainBodyBytes  = 1 << 20
	defaultSessionTTL   = 8 * time.Hour
	shutdownTimeout     = 5 * time.Second
	maxHeaderBytes      = 16 << 10
	responseWriteMargin = controlapi.WebBridgeWriteBudget - controlapi.WebBridgeMutationBudget
)

var ErrShutdownIncomplete = errors.New("webbridge: server shutdown incomplete")

const (
	sessionVersion = "v1"
	sessionDomain  = "xtier/webbridge/session/v1"
	csrfDomain     = "xtier/webbridge/csrf/v1"
)

// Config describes a browser-facing bridge to an authenticated local control
// server. Addr must be a literal loopback endpoint unless the caller explicitly
// opts into insecure HTTP on a private network. ControlAddr remains loopback.
type Config struct {
	Addr                        string
	ControlAddr                 string
	TokenPath                   string
	CredentialPath              string
	StateStore                  *statestore.Store
	StaticDir                   string
	UpstreamTimeout             time.Duration
	AllowInsecurePrivateNetwork bool
}

// Server proxies the typed browser domain API without exposing the daemon
// control token. The CLI-only command endpoint is deliberately not routed.
// Browser authority is established only by an explicit login at SessionPath.
type Server struct {
	httpServer *http.Server
	listener   net.Listener

	host                   string
	origin                 string
	trustedWebOrigin       bool
	staticRoot             *os.Root
	controlAddr            string
	tokenPath              string
	credentialPath         string
	stateStore             *statestore.Store
	readUpstreamBudget     time.Duration
	mutationUpstreamBudget time.Duration
	sessionKey             [sha256.Size]byte
	credentialFingerprint  [sha256.Size]byte
	now                    func() time.Time
	authMu                 sync.Mutex
	sessions               map[[sha256.Size]byte]webSessionRecord
	nextSessionSequence    uint64
	loginAttempts          map[string]loginAttemptState

	requestsMu sync.Mutex
	closing    bool
	requests   sync.WaitGroup

	closeOnce  sync.Once
	serveDone  chan struct{}
	closedDone chan struct{}

	errMu        sync.Mutex
	serveErr     error
	shutdownErr  error
	shutdownWait time.Duration
}

// Start binds the configured HTTP bridge and begins serving immediately.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	if ctx == nil {
		return nil, fmt.Errorf("webbridge.context_nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("webbridge.context_done: %w", err)
	}
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.ControlAddr == "" {
		cfg.ControlAddr = controlapi.DefaultAddr
	}
	listenAddr, err := literalWebAddr(cfg.Addr, cfg.AllowInsecurePrivateNetwork)
	if err != nil {
		return nil, fmt.Errorf("webbridge.addr_invalid: %w", err)
	}
	if _, err := literalControlAddr(cfg.ControlAddr); err != nil {
		return nil, fmt.Errorf("webbridge.control_addr_invalid: %w", err)
	}
	var controlTokenPath, credentialPath string
	var credential, controlToken string
	if cfg.StateStore != nil {
		if cfg.TokenPath != "" || cfg.CredentialPath != "" {
			return nil, fmt.Errorf("webbridge.state_source_mixed")
		}
		credential, err = controlapi.ReadStoreToken(cfg.StateStore, statestore.WebToken)
		if err != nil {
			return nil, fmt.Errorf("webbridge.credential_unavailable: %w", err)
		}
		controlToken, err = controlapi.ReadStoreToken(cfg.StateStore, statestore.ControlToken)
		if err != nil {
			return nil, fmt.Errorf("webbridge.control_token_unavailable: %w", err)
		}
	} else {
		if cfg.TokenPath == "" {
			return nil, fmt.Errorf("webbridge.token_path_required")
		}
		if cfg.CredentialPath == "" {
			return nil, fmt.Errorf("webbridge.credential_path_required")
		}
		controlTokenPath, err = filepath.Abs(cfg.TokenPath)
		if err != nil {
			return nil, fmt.Errorf("webbridge.token_path_invalid: %w", err)
		}
		credentialPath, err = filepath.Abs(cfg.CredentialPath)
		if err != nil {
			return nil, fmt.Errorf("webbridge.credential_path_invalid: %w", err)
		}
		if filepath.Clean(controlTokenPath) == filepath.Clean(credentialPath) {
			return nil, fmt.Errorf("webbridge.credential_reuses_control_token")
		}
		credential, err = controlapi.ReadToken(credentialPath)
		if err != nil {
			return nil, fmt.Errorf("webbridge.credential_unavailable: %w", err)
		}
		controlToken, err = controlapi.ReadToken(controlTokenPath)
		if err != nil {
			return nil, fmt.Errorf("webbridge.control_token_unavailable: %w", err)
		}
	}
	if subtle.ConstantTimeCompare([]byte(credential), []byte(controlToken)) == 1 {
		return nil, fmt.Errorf("webbridge.credential_reuses_control_token")
	}
	if cfg.UpstreamTimeout < 0 {
		return nil, fmt.Errorf("webbridge.upstream_timeout_invalid")
	}
	if cfg.UpstreamTimeout == 0 {
		cfg.UpstreamTimeout = controlapi.ReadRequestBudget
	}
	if cfg.UpstreamTimeout > time.Duration(1<<63-1)-responseWriteMargin {
		return nil, fmt.Errorf("webbridge.upstream_timeout_invalid")
	}
	mutationUpstreamBudget := controlapi.WebBridgeMutationBudget
	if cfg.UpstreamTimeout > mutationUpstreamBudget {
		mutationUpstreamBudget = cfg.UpstreamTimeout
	}
	writeBudget := mutationUpstreamBudget + responseWriteMargin
	staticRoot, err := validateStaticRoot(cfg.StaticDir)
	if err != nil {
		return nil, fmt.Errorf("webbridge.static_dir_invalid: %w", err)
	}
	staticRootTransferred := false
	defer func() {
		if !staticRootTransferred && staticRoot != nil {
			_ = staticRoot.Close()
		}
	}()

	listener, err := net.ListenTCP("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	webAddr := listener.Addr().(*net.TCPAddr)
	s := &Server{
		listener:               listener,
		host:                   httpAuthority(webAddr),
		trustedWebOrigin:       webAddr.IP.IsLoopback(),
		controlAddr:            cfg.ControlAddr,
		tokenPath:              controlTokenPath,
		credentialPath:         credentialPath,
		stateStore:             cfg.StateStore,
		readUpstreamBudget:     cfg.UpstreamTimeout,
		mutationUpstreamBudget: mutationUpstreamBudget,
		staticRoot:             staticRoot,
		now:                    time.Now,
		credentialFingerprint:  sha256.Sum256([]byte(credential)),
		sessions:               make(map[[sha256.Size]byte]webSessionRecord),
		loginAttempts:          make(map[string]loginAttemptState),
		shutdownWait:           shutdownTimeout,
		serveDone:              make(chan struct{}),
		closedDone:             make(chan struct{}),
	}
	s.origin = "http://" + s.host
	if _, err := rand.Read(s.sessionKey[:]); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("webbridge.session_key_random_failed: %w", err)
	}
	s.httpServer = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      writeBudget,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}

	go func() {
		err := s.httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		s.errMu.Lock()
		s.serveErr = err
		s.errMu.Unlock()
		s.closeOnce.Do(func() {
			s.beginShutdown()
			go s.shutdown()
		})
		close(s.serveDone)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.Done():
		}
	}()
	staticRootTransferred = true

	return s, nil
}

// Addr returns the concrete browser authority used for Host and Origin checks.
func (s *Server) Addr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.host
}

// Close gracefully stops the bridge. It is safe to call concurrently.
func (s *Server) Close() error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	timeout := s.shutdownWait
	if timeout <= 0 {
		timeout = shutdownTimeout
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
		return errors.Join(ErrShutdownIncomplete, errors.New("webbridge: nil shutdown context"))
	}
	s.closeOnce.Do(func() {
		s.beginShutdown()
		go s.shutdown()
	})
	select {
	case <-s.closedDone:
		return errors.Join(s.shutdownErr, s.Wait())
	case <-ctx.Done():
		return errors.Join(ErrShutdownIncomplete, ctx.Err())
	}
}

func (s *Server) beginShutdown() {
	s.requestsMu.Lock()
	s.closing = true
	s.requestsMu.Unlock()
	s.httpServer.SetKeepAlivesEnabled(false)
}

func (s *Server) shutdown() {
	timeout := s.shutdownWait
	if timeout <= 0 {
		timeout = shutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		if !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			s.shutdownErr = errors.Join(s.shutdownErr, err)
		}
		if closeErr := s.httpServer.Close(); closeErr != nil &&
			!errors.Is(closeErr, http.ErrServerClosed) && !errors.Is(closeErr, net.ErrClosed) {
			s.shutdownErr = errors.Join(s.shutdownErr, closeErr)
		}
	}
	<-s.serveDone
	s.requests.Wait()
	if s.staticRoot != nil {
		if err := s.staticRoot.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			s.shutdownErr = errors.Join(s.shutdownErr, err)
		}
		s.staticRoot = nil
	}
	s.authMu.Lock()
	for i := range s.sessionKey {
		s.sessionKey[i] = 0
	}
	for key := range s.sessions {
		delete(s.sessions, key)
	}
	clear(s.loginAttempts)
	s.authMu.Unlock()
	close(s.closedDone)
}

// Done closes when the HTTP serving loop has stopped accepting connections.
func (s *Server) Done() <-chan struct{} {
	if s == nil || s.serveDone == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return s.serveDone
}

// Wait waits for the serving loop and all accepted requests to finish.
func (s *Server) Wait() error {
	if s == nil || s.closedDone == nil {
		return nil
	}
	<-s.closedDone
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.serveErr
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	domainRoute, domainRouteOK := controlapi.LookupDomainRoute(path, r.Method)
	mutationRequest := domainRouteOK && domainRoute.Mutating
	if !domainRouteOK && path != SessionPath && isAPINamespace(path) && mutationMethod(r.Method) {
		mutationRequest = true
	}
	if !s.beginRequest() {
		writeRouteSessionError(w, http.StatusServiceUnavailable, "webbridge.closing", "The web bridge is shutting down.", mutationRequest)
		return
	}
	defer s.requests.Done()

	setResponseSecurityHeaders(w.Header())
	if r.URL.IsAbs() || r.URL.RawPath != "" || r.URL.Fragment != "" {
		writeRouteSessionError(w, http.StatusBadRequest, "webbridge.request_target_invalid", "The request target is invalid.", mutationRequest)
		return
	}
	if r.Host != s.host {
		writeRouteSessionError(w, http.StatusForbidden, "webbridge.host_forbidden", "The request host is not allowed.", mutationRequest)
		return
	}
	if path == SessionPath {
		s.handleWebSession(w, r)
		return
	}
	if r.URL.RawQuery != "" && isAPINamespace(path) {
		writeRouteSessionError(w, http.StatusBadRequest, "webbridge.request_target_invalid", "The request target is invalid.", mutationRequest)
		return
	}
	if controlapi.IsDomainPath(path) && !domainRouteOK {
		w.Header().Set("Allow", domainAllowedMethods(path))
		writeRouteSessionError(w, http.StatusMethodNotAllowed, "webbridge.method_not_allowed", "The method is not allowed.", mutationRequest)
		return
	}
	if !domainRouteOK && path != controlapi.StatusPath && path != controlapi.HealthPath {
		if isAPINamespace(path) || s.staticRoot == nil {
			writeRouteSessionError(w, http.StatusNotFound, "webbridge.not_found", "The route was not found.", mutationRequest)
			return
		}
		s.serveStatic(w, r)
		return
	}
	validOrigin := s.validReadOrigin(r)
	if domainRouteOK && domainRoute.Method != http.MethodGet {
		validOrigin = s.validMutationOrigin(r)
	}
	if !validOrigin {
		writeRouteSessionError(w, http.StatusForbidden, "webbridge.origin_forbidden", "The request origin is not allowed.", mutationRequest)
		return
	}
	session, csrf, ok, err := s.uniqueValidSession(r)
	if err != nil {
		writeRouteSessionError(w, http.StatusServiceUnavailable, "webbridge.credential_unavailable", "The panel credential is unavailable.", mutationRequest)
		return
	}
	if !ok {
		writeRouteSessionError(w, http.StatusUnauthorized, "webbridge.session_invalid", "Sign in to continue.", mutationRequest)
		return
	}
	if !requestProofValid(r, csrf) {
		writeRouteSessionError(w, http.StatusForbidden, "webbridge.csrf_invalid", "The session check failed.", mutationRequest)
		return
	}

	if domainRouteOK {
		if domainRoute.Method == http.MethodGet {
			s.handleRead(w, r, path, csrf)
		} else {
			s.handleDomainAction(w, r, domainRoute, session, csrf)
		}
		return
	}
	switch path {
	case controlapi.StatusPath, controlapi.HealthPath:
		s.handleRead(w, r, path, csrf)
	}
}

func domainAllowedMethods(path string) string {
	methods := make([]string, 0, 2)
	for _, route := range controlapi.DomainRoutes() {
		if route.Path == path {
			methods = append(methods, route.Method)
		}
	}
	return strings.Join(methods, ", ")
}

func writeRouteSessionError(w http.ResponseWriter, status int, code, message string, mutating bool) {
	if mutating {
		writeDomainBridgeError(w, status, code, nil, true)
		return
	}
	writeSessionError(w, status, code, message)
}

func mutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func CredentialPath(configPath string) string {
	return configPath + ".web-token"
}

func (s *Server) readCredential() (string, error) {
	if s.stateStore != nil {
		return controlapi.ReadStoreToken(s.stateStore, statestore.WebToken)
	}
	return controlapi.ReadToken(s.credentialPath)
}

func (s *Server) readControlToken() (string, error) {
	if s.stateStore != nil {
		return controlapi.ReadStoreToken(s.stateStore, statestore.ControlToken)
	}
	return controlapi.ReadToken(s.tokenPath)
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "webbridge.method_not_allowed")
		return
	}
	if r.URL.Path == "" || strings.ContainsAny(r.URL.Path, "\\\x00") {
		writeError(w, http.StatusBadRequest, "webbridge.request_target_invalid")
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, "/")
	if relative == "" {
		relative = "index.html"
	}
	if !validStaticPath(relative) {
		writeError(w, http.StatusBadRequest, "webbridge.request_target_invalid")
		return
	}
	name := relative
	file, info, err := s.openStatic(relative)
	if err != nil && filepath.Ext(relative) == "" &&
		(errors.Is(err, os.ErrNotExist) || errors.Is(err, errStaticNotRegular)) {
		name = "index.html"
		file, info, err = s.openStatic(name)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "webbridge.not_found")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; connect-src 'self'; img-src 'self' data:; font-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
	if s.trustedWebOrigin {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	}
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Type", staticContentType(name))
	http.ServeContent(w, r, filepath.Base(name), info.ModTime(), file)
}

var errStaticNotRegular = errors.New("static asset is not a regular file")

func validStaticPath(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return validPlatformStaticPath(name)
}

func (s *Server) openStatic(relative string) (*os.File, os.FileInfo, error) {
	file, err := s.staticRoot.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errStaticNotRegular
	}
	return file, info, nil
}

func validateStaticRoot(path string) (*os.Root, error) {
	if path == "" {
		return nil, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, err
	}
	index, err := root.Open("index.html")
	if err != nil {
		_ = root.Close()
		return nil, errors.New("static root has no index.html")
	}
	info, statErr := index.Stat()
	closeErr := index.Close()
	if statErr != nil || !info.Mode().IsRegular() {
		_ = root.Close()
		return nil, errors.New("static index is not a regular file")
	}
	if closeErr != nil {
		_ = root.Close()
		return nil, closeErr
	}
	return root, nil
}

func staticContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".wasm":
		return "application/wasm"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func (s *Server) beginRequest() bool {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	if s.closing {
		return false
	}
	s.requests.Add(1)
	return true
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request, path, csrf string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "webbridge.method_not_allowed")
		return
	}
	if data, err := readBoundedBody(r.Body, 0); err != nil || len(data) != 0 {
		writeError(w, http.StatusBadRequest, "webbridge.body_forbidden")
		return
	}

	status, body, err := s.upstream(r.Context(), s.readUpstreamBudget, http.MethodGet, path, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "webbridge.upstream_unavailable")
		return
	}
	w.Header().Set(CSRFHeader, csrf)
	writeUpstreamResponse(w, status, body)
}

func (s *Server) handleDomainAction(w http.ResponseWriter, r *http.Request, route controlapi.DomainRoute, session, csrf string) {
	if !validJSONContentType(r.Header) {
		writeDomainBridgeError(w, http.StatusUnsupportedMediaType, "webbridge.content_type_invalid", nil, route.Mutating)
		return
	}
	if r.ContentLength > MaxDomainBodyBytes {
		writeDomainBridgeError(w, http.StatusRequestEntityTooLarge, "webbridge.request_too_large", nil, route.Mutating)
		return
	}
	body, err := readBoundedBody(r.Body, MaxDomainBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeDomainBridgeError(w, http.StatusRequestEntityTooLarge, "webbridge.request_too_large", nil, route.Mutating)
		} else {
			writeDomainBridgeError(w, http.StatusBadRequest, "webbridge.body_invalid", nil, route.Mutating)
		}
		return
	}
	currentSession, currentCSRF, ok, err := s.uniqueValidSession(r)
	if err != nil {
		writeDomainBridgeError(w, http.StatusServiceUnavailable, "webbridge.credential_unavailable", body, route.Mutating)
		return
	}
	if !ok || !constantTimeStringEqual(currentSession, session) || !constantTimeStringEqual(currentCSRF, csrf) {
		writeDomainBridgeError(w, http.StatusUnauthorized, "webbridge.session_invalid", body, route.Mutating)
		return
	}
	if !requestProofValid(r, currentCSRF) {
		writeDomainBridgeError(w, http.StatusForbidden, "webbridge.csrf_invalid", body, route.Mutating)
		return
	}
	budget := s.readUpstreamBudget
	if route.Mutating {
		budget = s.mutationUpstreamBudget
	}
	status, response, err := s.upstream(r.Context(), budget, route.Method, route.Path, body)
	if err != nil {
		if route.Mutating {
			writeMutationUpstreamError(w, body, err)
			return
		}
		writeError(w, http.StatusBadGateway, "webbridge.upstream_unavailable")
		return
	}
	w.Header().Set(CSRFHeader, currentCSRF)
	writeUpstreamResponse(w, status, response)
}

func (s *Server) upstream(parent context.Context, budget time.Duration, method, path string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	token, err := s.readControlToken()
	if err != nil {
		return 0, nil, fmt.Errorf("webbridge.control_token_unavailable: %w", err)
	}
	return controlapi.AuthenticatedRequestTokenContext(ctx, s.controlAddr, token, method, path, body)
}

func clearSessionCookies(w http.ResponseWriter) {
	for _, path := range []string{"/", "/v1", "/v1/"} {
		http.SetCookie(w, &http.Cookie{
			Name:     SessionCookieName,
			Path:     path,
			Expires:  time.Unix(1, 0).UTC(),
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

func literalIPAddr(addr string) (*net.TCPAddr, error) {
	if strings.Contains(addr, "://") {
		return nil, fmt.Errorf("scheme forbidden")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("literal IP required")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port")
	}
	return &net.TCPAddr{IP: ip, Port: int(port)}, nil
}

func literalWebAddr(addr string, allowInsecurePrivateNetwork bool) (*net.TCPAddr, error) {
	parsed, err := literalIPAddr(addr)
	if err != nil {
		return nil, err
	}
	if parsed.IP.IsLoopback() {
		return parsed, nil
	}
	if !parsed.IP.IsPrivate() {
		return nil, fmt.Errorf("literal loopback or private IP required")
	}
	if !allowInsecurePrivateNetwork {
		return nil, fmt.Errorf("private-network HTTP requires explicit insecure opt-in")
	}
	return parsed, nil
}

func httpAuthority(addr *net.TCPAddr) string {
	host := addr.IP.String()
	if addr.Port != 80 {
		return net.JoinHostPort(host, strconv.Itoa(addr.Port))
	}
	if addr.IP.To4() == nil {
		return "[" + host + "]"
	}
	return host
}

func isAPINamespace(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}

func literalLoopbackAddr(addr string) (*net.TCPAddr, error) {
	parsed, err := literalIPAddr(addr)
	if err != nil {
		return nil, err
	}
	if !parsed.IP.IsLoopback() {
		return nil, fmt.Errorf("literal loopback required")
	}
	return parsed, nil
}

func literalControlAddr(addr string) (*net.TCPAddr, error) {
	if strings.HasPrefix(addr, "http://") {
		addr = strings.TrimPrefix(addr, "http://")
	}
	return literalLoopbackAddr(addr)
}

func namedCookies(r *http.Request, name string) []*http.Cookie {
	var matches []*http.Cookie
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			matches = append(matches, cookie)
		}
	}
	return matches
}

func validJSONContentType(header http.Header) bool {
	value, ok := exactHeader(header, "Content-Type")
	if !ok {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return false
	}
	for name, value := range params {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func exactHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.Contains(values[0], ",") || strings.TrimSpace(values[0]) != values[0] {
		return "", false
	}
	return values[0], true
}

func optionalExactHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", true
	}
	return exactHeader(header, name)
}

func constantTimeStringEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

var errBodyTooLarge = errors.New("webbridge body too large")

func readBoundedBody(body io.ReadCloser, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}

func setResponseSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
}

func writeUpstreamResponse(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	setResponseSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(controlapi.DomainError{
		APIVersion: controlapi.DomainAPIVersion,
		OK:         false,
		ErrorCode:  code,
		Message:    "the local web bridge rejected the request",
	})
}

func writeDomainBridgeError(w http.ResponseWriter, status int, code string, body []byte, mutating bool) {
	response := controlapi.DomainError{
		APIVersion: controlapi.DomainAPIVersion,
		OK:         false,
		ErrorCode:  code,
		Message:    "the local web bridge rejected the request",
	}
	if mutating && mutationMayApply(body) {
		applied := false
		response.Applied = &applied
		response.Outcome = controlapi.MutationOutcomeNotApplied
	}
	setResponseSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func writeMutationUpstreamError(w http.ResponseWriter, body []byte, err error) {
	status := http.StatusBadGateway
	code := "webbridge.upstream_unavailable"
	message := "the local control service did not return an authenticated response"
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		code = "webbridge.upstream_timeout"
		message = "the local control service did not return before the mutation deadline"
	}
	response := controlapi.DomainError{
		APIVersion: controlapi.DomainAPIVersion,
		OK:         false,
		ErrorCode:  code,
		Message:    message,
	}
	if mutationMayApply(body) {
		applied := false
		response.Applied = &applied
		response.Outcome = controlapi.MutationOutcomeNotApplied
		if controlapi.CommandMayHaveApplied(err) {
			response.Outcome = controlapi.MutationOutcomeIndeterminate
		}
	}
	setResponseSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func mutationMayApply(body []byte) bool {
	var metadata struct {
		DryRun bool `json:"dry_run"`
	}
	return json.Unmarshal(body, &metadata) != nil || !metadata.DryRun
}
