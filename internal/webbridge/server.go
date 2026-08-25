package webbridge

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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
)

const (
	DefaultAddr   = "127.0.0.1:19091"
	BasicUsername = "xtier"

	CSRFHeader          = "X-XTier-CSRF-Token"
	SessionCookieName   = "xtier_web_session"
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
// server. Addr and ControlAddr must resolve to literal loopback endpoints;
// hostnames and wildcard addresses are deliberately unsupported.
type Config struct {
	Addr            string
	ControlAddr     string
	TokenPath       string
	CredentialPath  string
	StaticDir       string
	UpstreamTimeout time.Duration
}

// Server proxies the typed browser domain API without exposing the daemon
// control token. The CLI-only command endpoint is deliberately not routed.
// A successful read with a positive same-origin browser signal establishes the
// HttpOnly session and returns its browser-readable CSRF token in CSRFHeader.
type Server struct {
	httpServer *http.Server
	listener   net.Listener

	host                   string
	origin                 string
	staticRoot             *os.Root
	controlAddr            string
	tokenPath              string
	credentialPath         string
	readUpstreamBudget     time.Duration
	mutationUpstreamBudget time.Duration
	sessionKey             [sha256.Size]byte
	now                    func() time.Time

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

// Start binds a loopback-only HTTP bridge and begins serving immediately.
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
	if cfg.TokenPath == "" {
		return nil, fmt.Errorf("webbridge.token_path_required")
	}
	if cfg.CredentialPath == "" {
		return nil, fmt.Errorf("webbridge.credential_path_required")
	}
	controlTokenPath, err := filepath.Abs(cfg.TokenPath)
	if err != nil {
		return nil, fmt.Errorf("webbridge.token_path_invalid: %w", err)
	}
	credentialPath, err := filepath.Abs(cfg.CredentialPath)
	if err != nil {
		return nil, fmt.Errorf("webbridge.credential_path_invalid: %w", err)
	}
	if filepath.Clean(controlTokenPath) == filepath.Clean(credentialPath) {
		return nil, fmt.Errorf("webbridge.credential_reuses_control_token")
	}
	credential, err := controlapi.ReadToken(credentialPath)
	if err != nil {
		return nil, fmt.Errorf("webbridge.credential_unavailable: %w", err)
	}
	controlToken, err := controlapi.ReadToken(controlTokenPath)
	if err != nil {
		return nil, fmt.Errorf("webbridge.control_token_unavailable: %w", err)
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

	listenAddr, err := literalLoopbackAddr(cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("webbridge.addr_invalid: %w", err)
	}
	if _, err := literalControlAddr(cfg.ControlAddr); err != nil {
		return nil, fmt.Errorf("webbridge.control_addr_invalid: %w", err)
	}

	listener, err := net.ListenTCP("tcp", listenAddr)
	if err != nil {
		return nil, err
	}

	s := &Server{
		listener:               listener,
		host:                   listener.Addr().String(),
		controlAddr:            cfg.ControlAddr,
		tokenPath:              controlTokenPath,
		credentialPath:         credentialPath,
		readUpstreamBudget:     cfg.UpstreamTimeout,
		mutationUpstreamBudget: mutationUpstreamBudget,
		staticRoot:             staticRoot,
		now:                    time.Now,
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

// Addr returns the concrete loopback listener address.
func (s *Server) Addr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
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
	for i := range s.sessionKey {
		s.sessionKey[i] = 0
	}
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
	if !s.beginRequest() {
		writeError(w, http.StatusServiceUnavailable, "webbridge.closing")
		return
	}
	defer s.requests.Done()

	setResponseSecurityHeaders(w.Header())
	if !s.authenticate(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="X-Tier", charset="UTF-8"`)
		writeError(w, http.StatusUnauthorized, "webbridge.authentication_required")
		return
	}
	if r.URL.IsAbs() || r.URL.RawPath != "" || r.URL.Fragment != "" {
		writeError(w, http.StatusBadRequest, "webbridge.request_target_invalid")
		return
	}
	if r.Host != s.host {
		writeError(w, http.StatusForbidden, "webbridge.host_forbidden")
		return
	}
	domainRoute, domainRouteOK := controlapi.LookupDomainRoute(path, r.Method)
	if r.URL.RawQuery != "" && strings.HasPrefix(path, "/v1/") {
		writeError(w, http.StatusBadRequest, "webbridge.request_target_invalid")
		return
	}
	if controlapi.IsDomainPath(path) && !domainRouteOK {
		w.Header().Set("Allow", domainAllowedMethods(path))
		writeError(w, http.StatusMethodNotAllowed, "webbridge.method_not_allowed")
		return
	}
	if !domainRouteOK && path != controlapi.StatusPath && path != controlapi.HealthPath {
		if strings.HasPrefix(path, "/v1/") || s.staticRoot == nil {
			writeError(w, http.StatusNotFound, "webbridge.not_found")
			return
		}
		s.serveStatic(w, r)
		return
	}
	if origin, ok := optionalExactHeader(r.Header, "Origin"); !ok ||
		(origin != "" && origin != s.origin) || (domainRouteOK && domainRoute.Method != http.MethodGet && origin == "") {
		writeError(w, http.StatusForbidden, "webbridge.origin_forbidden")
		return
	}

	if domainRouteOK {
		if domainRoute.Method == http.MethodGet {
			s.handleRead(w, r, path)
		} else {
			s.handleDomainAction(w, r, domainRoute)
		}
		return
	}
	switch path {
	case controlapi.StatusPath, controlapi.HealthPath:
		s.handleRead(w, r, path)
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

func (s *Server) authenticate(r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	credential, err := controlapi.ReadToken(s.credentialPath)
	if err != nil {
		return false
	}
	controlToken, err := controlapi.ReadToken(s.tokenPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(credential), []byte(controlToken)) == 1 {
		return false
	}
	usernameOK := subtle.ConstantTimeCompare([]byte(username), []byte(BasicUsername))
	passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(credential))
	return usernameOK&passwordOK == 1
}

func CredentialPath(configPath string) string {
	return configPath + ".web-token"
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
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
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

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "webbridge.method_not_allowed")
		return
	}
	if data, err := readBoundedBody(r.Body, 0); err != nil || len(data) != 0 {
		writeError(w, http.StatusBadRequest, "webbridge.body_forbidden")
		return
	}

	session, csrf, expires, err := s.safeSession(r)
	if err != nil {
		writeError(w, http.StatusForbidden, "webbridge.session_invalid")
		return
	}
	status, body, err := s.upstream(r.Context(), s.readUpstreamBudget, http.MethodGet, path, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "webbridge.upstream_unavailable")
		return
	}
	if session != "" {
		if len(namedCookies(r, SessionCookieName)) != 0 {
			clearSessionCookies(w)
		}
		maxAge := int(expires.Sub(s.now().UTC()).Seconds())
		if maxAge < 1 {
			maxAge = 1
		}
		http.SetCookie(w, &http.Cookie{
			Name:     SessionCookieName,
			Value:    session,
			Path:     "/v1/",
			Expires:  expires,
			MaxAge:   maxAge,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
	}
	if csrf != "" {
		w.Header().Set(CSRFHeader, csrf)
	}
	writeUpstreamResponse(w, status, body)
}

func (s *Server) handleDomainAction(w http.ResponseWriter, r *http.Request, route controlapi.DomainRoute) {
	csrf, ok := s.authenticatedSession(r)
	if !ok {
		writeError(w, http.StatusForbidden, "webbridge.session_invalid")
		return
	}
	provided, ok := exactHeader(r.Header, CSRFHeader)
	if !ok || !constantTimeStringEqual(provided, csrf) {
		writeError(w, http.StatusForbidden, "webbridge.csrf_invalid")
		return
	}
	if !validJSONContentType(r.Header) {
		writeError(w, http.StatusUnsupportedMediaType, "webbridge.content_type_invalid")
		return
	}
	if r.ContentLength > MaxDomainBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "webbridge.request_too_large")
		return
	}
	body, err := readBoundedBody(r.Body, MaxDomainBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "webbridge.request_too_large")
		} else {
			writeError(w, http.StatusBadRequest, "webbridge.body_invalid")
		}
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
	w.Header().Set(CSRFHeader, csrf)
	writeUpstreamResponse(w, status, response)
}

func (s *Server) upstream(parent context.Context, budget time.Duration, method, path string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	return controlapi.AuthenticatedRequestContext(ctx, s.controlAddr, s.tokenPath, method, path, body)
}

func (s *Server) safeSession(r *http.Request) (string, string, time.Time, error) {
	csrf, expires, ok := s.uniqueValidSession(r)
	if ok {
		return "", csrf, expires, nil
	}
	if !s.mayIssueReadSession(r) {
		return "", "", time.Time{}, nil
	}
	// Read requests recover stale or ambiguous HttpOnly sessions. handleRead
	// clears historical cookie paths before installing this replacement.
	return s.newSession()
}

func (s *Server) mayIssueReadSession(r *http.Request) bool {
	origin, ok := optionalExactHeader(r.Header, "Origin")
	if !ok {
		return false
	}
	if origin != "" {
		return origin == s.origin
	}
	fetchSite, ok := optionalExactHeader(r.Header, "Sec-Fetch-Site")
	return ok && fetchSite == "same-origin"
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

func (s *Server) authenticatedSession(r *http.Request) (string, bool) {
	csrf, _, ok := s.uniqueValidSession(r)
	return csrf, ok
}

func (s *Server) uniqueValidSession(r *http.Request) (string, time.Time, bool) {
	cookies := namedCookies(r, SessionCookieName)
	seenValues := make(map[string]struct{}, len(cookies))
	var selectedCSRF string
	var selectedExpiry time.Time
	found := false
	for _, cookie := range cookies {
		if _, duplicate := seenValues[cookie.Value]; duplicate {
			continue
		}
		seenValues[cookie.Value] = struct{}{}
		csrf, expires, ok := s.verifySession(cookie.Value)
		if !ok {
			continue
		}
		if found {
			return "", time.Time{}, false
		}
		selectedCSRF = csrf
		selectedExpiry = expires
		found = true
	}
	return selectedCSRF, selectedExpiry, found
}

func (s *Server) newSession() (string, string, time.Time, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", "", time.Time{}, err
	}
	expires := s.now().UTC().Add(defaultSessionTTL).Truncate(time.Second)
	payload := sessionVersion + "." + strconv.FormatInt(expires.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce[:])
	signature := s.sessionMAC(sessionDomain, payload)
	value := payload + "." + base64.RawURLEncoding.EncodeToString(signature)
	return value, s.csrfToken(payload), expires, nil
}

func (s *Server) verifySession(value string) (string, time.Time, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 || parts[0] != sessionVersion || value != strings.TrimSpace(value) {
		return "", time.Time{}, false
	}
	expiresUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || strconv.FormatInt(expiresUnix, 10) != parts[1] {
		return "", time.Time{}, false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(nonce) != 32 || base64.RawURLEncoding.EncodeToString(nonce) != parts[2] {
		return "", time.Time{}, false
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(provided) != sha256.Size || base64.RawURLEncoding.EncodeToString(provided) != parts[3] {
		return "", time.Time{}, false
	}
	expires := time.Unix(expiresUnix, 0).UTC()
	now := s.now().UTC()
	if !expires.After(now) || expires.After(now.Add(defaultSessionTTL+time.Minute)) {
		return "", time.Time{}, false
	}
	payload := strings.Join(parts[:3], ".")
	if !hmac.Equal(provided, s.sessionMAC(sessionDomain, payload)) {
		return "", time.Time{}, false
	}
	return s.csrfToken(payload), expires, true
}

func (s *Server) csrfToken(sessionPayload string) string {
	return base64.RawURLEncoding.EncodeToString(s.sessionMAC(csrfDomain, sessionPayload))
}

func (s *Server) sessionMAC(domain, value string) []byte {
	mac := hmac.New(sha256.New, s.sessionKey[:])
	_, _ = io.WriteString(mac, domain)
	_, _ = mac.Write([]byte{0})
	_, _ = io.WriteString(mac, value)
	return mac.Sum(nil)
}

func literalLoopbackAddr(addr string) (*net.TCPAddr, error) {
	if strings.Contains(addr, "://") {
		return nil, fmt.Errorf("scheme forbidden")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("literal loopback required")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port")
	}
	return &net.TCPAddr{IP: ip, Port: int(port)}, nil
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
	_, _ = fmt.Fprintf(w, "{\"error\":%q}\n", code)
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
		response.Outcome = "indeterminate"
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
