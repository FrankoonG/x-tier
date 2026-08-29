package webbridge

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/FrankoonG/x-tier/internal/controlapi"
)

const (
	maxSessionBodyBytes = 1024
	maxActiveSessions   = 128
	loginFailureLimit   = 5
	loginFailureWindow  = time.Minute
	loginLockout        = time.Minute
	maxLoginSources     = 1024

	credentialRotationDomain = "xtier/webbridge/credential-rotation/v1"
)

type webSessionRecord struct {
	expires   time.Time
	sequence  uint64
	proofHash [sha256.Size]byte
}

type loginAttemptState struct {
	failures     []time.Time
	blockedUntil time.Time
	lastSeen     time.Time
}

type loginDecision uint8

const (
	loginAccepted loginDecision = iota
	loginRejected
	loginRateLimited
)

type logoutDecision uint8

const (
	logoutEnded logoutDecision = iota
	logoutSessionInvalid
	logoutProofInvalid
)

func (s *Server) handleWebSession(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeSessionError(w, http.StatusBadRequest, "webbridge.request_target_invalid", "The request target is invalid.")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleSessionRead(w, r)
	case http.MethodPost:
		s.handleSessionCreate(w, r)
	case http.MethodDelete:
		s.handleSessionDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeSessionError(w, http.StatusMethodNotAllowed, "webbridge.method_not_allowed", "The method is not allowed.")
	}
}

func (s *Server) handleSessionRead(w http.ResponseWriter, r *http.Request) {
	if !s.validReadOrigin(r) {
		writeSessionError(w, http.StatusForbidden, "webbridge.origin_forbidden", "The request origin is not allowed.")
		return
	}
	if body, err := readBoundedBody(r.Body, 0); err != nil || len(body) != 0 {
		writeSessionError(w, http.StatusBadRequest, "webbridge.body_forbidden", "A request body is not allowed.")
		return
	}

	_, csrf, ok, err := s.uniqueValidSession(r)
	if err != nil {
		writeSessionError(w, http.StatusServiceUnavailable, "webbridge.credential_unavailable", "The panel credential is unavailable.")
		return
	}
	if !ok {
		writeSessionError(w, http.StatusUnauthorized, "webbridge.session_invalid", "Sign in to continue.")
		return
	}
	if !requestProofValid(r, csrf) {
		writeSessionError(w, http.StatusForbidden, "webbridge.csrf_invalid", "The session check failed.")
		return
	}

	w.Header().Set(CSRFHeader, csrf)
	writeSessionState(w, http.StatusOK, true)
}

func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeSessionError(w, http.StatusForbidden, "webbridge.origin_forbidden", "The request origin is not allowed.")
		return
	}
	if !validJSONContentType(r.Header) {
		writeSessionError(w, http.StatusUnsupportedMediaType, "webbridge.content_type_invalid", "The request must contain JSON.")
		return
	}
	if r.ContentLength > maxSessionBodyBytes {
		writeSessionError(w, http.StatusRequestEntityTooLarge, "webbridge.request_too_large", "The request is too large.")
		return
	}
	body, err := readBoundedBody(r.Body, maxSessionBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeSessionError(w, http.StatusRequestEntityTooLarge, "webbridge.request_too_large", "The request is too large.")
		} else {
			writeSessionError(w, http.StatusBadRequest, "webbridge.body_invalid", "The request body is invalid.")
		}
		return
	}
	defer clearBytes(body)

	credential, err := decodeSessionCredential(body)
	if err != nil {
		writeSessionError(w, http.StatusBadRequest, "webbridge.body_invalid", "The request body is invalid.")
		return
	}

	decision, session, csrf, expires, retryAfter, err := s.login(credential, loginSourceKey(r.RemoteAddr))
	credential = ""
	if err != nil {
		writeSessionError(w, http.StatusServiceUnavailable, "webbridge.credential_unavailable", "The panel credential is unavailable.")
		return
	}
	// A stale DELETE response can arrive after another tab has logged in. Never
	// send an unconditional cookie deletion that could erase that newer login;
	// server-side revocation plus removal of the tab-local proof ends authority.
	switch decision {
	case loginRateLimited:
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeSessionError(w, http.StatusTooManyRequests, "webbridge.rate_limited", "Too many sign-in attempts.")
		return
	case loginRejected:
		writeSessionError(w, http.StatusUnauthorized, "webbridge.credential_invalid", "The credential was not accepted.")
		return
	case loginAccepted:
		// Continue below.
	default:
		writeSessionError(w, http.StatusInternalServerError, "webbridge.session_unavailable", "A session could not be created.")
		return
	}

	committed, err := s.commitLogin(w, r, session, csrf, expires)
	if err != nil {
		writeSessionError(w, http.StatusServiceUnavailable, "webbridge.credential_unavailable", "The panel credential is unavailable.")
		return
	}
	if !committed {
		writeSessionError(w, http.StatusConflict, "webbridge.session_changed", "The panel credential changed during sign-in.")
	}
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeSessionError(w, http.StatusForbidden, "webbridge.origin_forbidden", "The request origin is not allowed.")
		return
	}
	if body, err := readBoundedBody(r.Body, 0); err != nil || len(body) != 0 {
		writeSessionError(w, http.StatusBadRequest, "webbridge.body_forbidden", "A request body is not allowed.")
		return
	}

	decision, err := s.logout(r)
	if err != nil {
		writeSessionError(w, http.StatusServiceUnavailable, "webbridge.credential_unavailable", "The panel credential is unavailable.")
		return
	}
	switch decision {
	case logoutSessionInvalid:
		writeSessionError(w, http.StatusUnauthorized, "webbridge.session_invalid", "The session is no longer valid.")
		return
	case logoutProofInvalid:
		writeSessionError(w, http.StatusForbidden, "webbridge.csrf_invalid", "The session check failed.")
		return
	case logoutEnded:
		writeSessionState(w, http.StatusOK, false)
		return
	default:
		writeSessionError(w, http.StatusInternalServerError, "webbridge.session_unavailable", "The session could not be ended.")
	}
}

func (s *Server) validReadOrigin(r *http.Request) bool {
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

func (s *Server) validMutationOrigin(r *http.Request) bool {
	origin, ok := exactHeader(r.Header, "Origin")
	return ok && origin == s.origin
}

func decodeSessionCredential(body []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errors.New("session request must be an object")
	}
	found := false
	credential := ""
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return "", err
		}
		name, ok := nameToken.(string)
		if !ok || name != "credential" || found {
			return "", errors.New("session request has an unknown or duplicate field")
		}
		if err := decoder.Decode(&credential); err != nil {
			return "", err
		}
		found = true
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || !found {
		return "", errors.New("session request is incomplete")
	}
	if token, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("session request has trailing data")
	}
	return credential, nil
}

func (s *Server) login(credential, source string) (loginDecision, string, string, time.Time, int, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	expected, err := s.currentCredentialLocked()
	if err != nil {
		return loginRejected, "", "", time.Time{}, 0, err
	}
	now := s.now().UTC()
	if retryAfter := s.loginRetryAfterLocked(source, now); retryAfter > 0 {
		return loginRateLimited, "", "", time.Time{}, retryAfter, nil
	}

	expectedDigest := sha256.Sum256([]byte(expected))
	providedDigest := sha256.Sum256([]byte(credential))
	if subtle.ConstantTimeCompare(expectedDigest[:], providedDigest[:]) != 1 {
		s.recordLoginFailureLocked(source, now)
		return loginRejected, "", "", time.Time{}, 0, nil
	}

	delete(s.loginAttempts, source)
	session, csrf, expires, err := s.newSessionLocked(now)
	if err != nil {
		return loginRejected, "", "", time.Time{}, 0, err
	}
	return loginAccepted, session, csrf, expires, 0, nil
}

func (s *Server) loginRetryAfterLocked(source string, now time.Time) int {
	s.pruneLoginAttemptsLocked(now)
	state, ok := s.loginAttempts[source]
	if !ok {
		return 0
	}
	state = pruneLoginAttemptState(state, now)
	if len(state.failures) >= loginFailureLimit && !state.blockedUntil.After(now) {
		state.blockedUntil = now.Add(loginLockout)
	}
	state.lastSeen = now
	s.loginAttempts[source] = state
	if !state.blockedUntil.After(now) {
		return 0
	}
	seconds := int((state.blockedUntil.Sub(now) + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func (s *Server) recordLoginFailureLocked(source string, now time.Time) {
	s.pruneLoginAttemptsLocked(now)
	if _, exists := s.loginAttempts[source]; !exists && len(s.loginAttempts) >= maxLoginSources {
		s.evictOldestLoginSourceLocked()
	}
	state := pruneLoginAttemptState(s.loginAttempts[source], now)
	state.failures = append(state.failures, now)
	state.lastSeen = now
	s.loginAttempts[source] = state
}

func pruneLoginAttemptState(state loginAttemptState, now time.Time) loginAttemptState {
	cutoff := now.Add(-loginFailureWindow)
	first := 0
	for first < len(state.failures) && !state.failures[first].After(cutoff) {
		first++
	}
	if first > 0 {
		copy(state.failures, state.failures[first:])
		state.failures = state.failures[:len(state.failures)-first]
	}
	if !state.blockedUntil.After(now) && len(state.failures) < loginFailureLimit {
		state.blockedUntil = time.Time{}
	}
	return state
}

func (s *Server) pruneLoginAttemptsLocked(now time.Time) {
	for source, state := range s.loginAttempts {
		state = pruneLoginAttemptState(state, now)
		if len(state.failures) == 0 && !state.blockedUntil.After(now) {
			delete(s.loginAttempts, source)
			continue
		}
		s.loginAttempts[source] = state
	}
}

func (s *Server) evictOldestLoginSourceLocked() {
	oldestSource := ""
	var oldest time.Time
	for source, state := range s.loginAttempts {
		if oldestSource == "" || state.lastSeen.Before(oldest) {
			oldestSource = source
			oldest = state.lastSeen
		}
	}
	if oldestSource != "" {
		delete(s.loginAttempts, oldestSource)
	}
}

func (s *Server) currentCredentialLocked() (string, error) {
	credential, err := s.readCredential()
	if err != nil {
		return "", err
	}
	controlToken, err := s.readControlToken()
	if err != nil {
		return "", err
	}
	if subtle.ConstantTimeCompare([]byte(credential), []byte(controlToken)) == 1 {
		invalid := sha256.Sum256(append([]byte("invalid-control-token-reuse\x00"), []byte(credential)...))
		s.syncCredentialLocked(invalid)
		return "", errors.New("webbridge credential reuses the control token")
	}
	s.syncCredentialLocked(sha256.Sum256([]byte(credential)))
	return credential, nil
}

func (s *Server) syncCredentialLocked(fingerprint [sha256.Size]byte) {
	if subtle.ConstantTimeCompare(s.credentialFingerprint[:], fingerprint[:]) == 1 {
		return
	}
	mac := hmac.New(sha256.New, s.sessionKey[:])
	_, _ = io.WriteString(mac, credentialRotationDomain)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(fingerprint[:])
	next := mac.Sum(nil)
	copy(s.sessionKey[:], next)
	s.credentialFingerprint = fingerprint
	for key := range s.sessions {
		delete(s.sessions, key)
	}
	clear(s.loginAttempts)
}

func (s *Server) uniqueValidSession(r *http.Request) (string, string, bool, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if _, err := s.currentCredentialLocked(); err != nil {
		return "", "", false, err
	}
	now := s.now().UTC()
	s.pruneSessionsLocked(now)
	cookies := namedCookies(r, SessionCookieName)
	seenValues := make(map[string]struct{}, len(cookies))
	selectedValue := ""
	selectedCSRF := ""
	found := false
	for _, cookie := range cookies {
		if _, duplicate := seenValues[cookie.Value]; duplicate {
			continue
		}
		seenValues[cookie.Value] = struct{}{}
		csrf, ok := s.verifySessionLocked(cookie.Value, now)
		if !ok {
			continue
		}
		if found {
			return "", "", false, nil
		}
		selectedValue = cookie.Value
		selectedCSRF = csrf
		found = true
	}
	return selectedValue, selectedCSRF, found, nil
}

func (s *Server) logout(r *http.Request) (logoutDecision, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()

	if _, err := s.currentCredentialLocked(); err != nil {
		return logoutSessionInvalid, err
	}
	now := s.now().UTC()
	s.pruneSessionsLocked(now)

	validCookieSessions := make(map[[sha256.Size]byte]struct{})
	seenValues := make(map[string]struct{})
	for _, cookie := range namedCookies(r, SessionCookieName) {
		if _, duplicate := seenValues[cookie.Value]; duplicate {
			continue
		}
		seenValues[cookie.Value] = struct{}{}
		if _, ok := s.verifySessionLocked(cookie.Value, now); ok {
			validCookieSessions[sha256.Sum256([]byte(cookie.Value))] = struct{}{}
		}
	}

	provided, proofOK := exactHeader(r.Header, CSRFHeader)
	if proofOK {
		providedHash := sha256.Sum256([]byte(provided))
		for sessionHash, record := range s.sessions {
			if !hmac.Equal(record.proofHash[:], providedHash[:]) {
				continue
			}
			delete(s.sessions, sessionHash)
			for cookieSession := range validCookieSessions {
				delete(s.sessions, cookieSession)
			}
			return logoutEnded, nil
		}
	}
	if len(validCookieSessions) != 0 {
		return logoutProofInvalid, nil
	}
	return logoutSessionInvalid, nil
}

func (s *Server) newSessionLocked(now time.Time) (string, string, time.Time, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", "", time.Time{}, err
	}
	expires := now.Add(defaultSessionTTL).Truncate(time.Second)
	payload := sessionVersion + "." + strconv.FormatInt(expires.Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce[:])
	signature := s.sessionMACLocked(sessionDomain, payload)
	value := payload + "." + base64.RawURLEncoding.EncodeToString(signature)
	s.pruneSessionsLocked(now)
	if len(s.sessions) >= maxActiveSessions {
		s.evictEarliestSessionLocked()
	}
	s.nextSessionSequence++
	csrf := s.csrfTokenLocked(payload)
	s.sessions[sha256.Sum256([]byte(value))] = webSessionRecord{
		expires:   expires,
		sequence:  s.nextSessionSequence,
		proofHash: sha256.Sum256([]byte(csrf)),
	}
	return value, csrf, expires, nil
}

func (s *Server) verifySessionLocked(value string, now time.Time) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 || parts[0] != sessionVersion || value != strings.TrimSpace(value) {
		return "", false
	}
	expiresUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || strconv.FormatInt(expiresUnix, 10) != parts[1] {
		return "", false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(nonce) != 32 || base64.RawURLEncoding.EncodeToString(nonce) != parts[2] {
		return "", false
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(provided) != sha256.Size || base64.RawURLEncoding.EncodeToString(provided) != parts[3] {
		return "", false
	}
	expires := time.Unix(expiresUnix, 0).UTC()
	if !expires.After(now) || expires.After(now.Add(defaultSessionTTL+time.Minute)) {
		return "", false
	}
	payload := strings.Join(parts[:3], ".")
	if !hmac.Equal(provided, s.sessionMACLocked(sessionDomain, payload)) {
		return "", false
	}
	record, ok := s.sessions[sha256.Sum256([]byte(value))]
	if !ok || !record.expires.Equal(expires) {
		return "", false
	}
	return s.csrfTokenLocked(payload), true
}

func (s *Server) pruneSessionsLocked(now time.Time) {
	for key, record := range s.sessions {
		if !record.expires.After(now) {
			delete(s.sessions, key)
		}
	}
}

func (s *Server) evictEarliestSessionLocked() {
	var selected [sha256.Size]byte
	var earliest uint64
	found := false
	for key, record := range s.sessions {
		if !found || record.sequence < earliest {
			selected = key
			earliest = record.sequence
			found = true
		}
	}
	if found {
		delete(s.sessions, selected)
	}
}

func (s *Server) commitLogin(w http.ResponseWriter, r *http.Request, session, csrf string, expires time.Time) (bool, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if _, err := s.currentCredentialLocked(); err != nil {
		return false, err
	}
	verifiedCSRF, ok := s.verifySessionLocked(session, s.now().UTC())
	if !ok || !constantTimeStringEqual(verifiedCSRF, csrf) {
		return false, nil
	}
	for _, cookie := range namedCookies(r, SessionCookieName) {
		delete(s.sessions, sha256.Sum256([]byte(cookie.Value)))
	}
	clearSessionCookies(w)
	setSessionCookie(w, session, expires, s.now().UTC())
	w.Header().Set(CSRFHeader, csrf)
	writeSessionState(w, http.StatusOK, true)
	return true, nil
}

func requestProofValid(r *http.Request, expected string) bool {
	provided, ok := exactHeader(r.Header, CSRFHeader)
	return ok && constantTimeStringEqual(provided, expected)
}

func loginSourceKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return "unknown"
	}
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	return ip.String()
}

func (s *Server) csrfTokenLocked(sessionPayload string) string {
	return base64.RawURLEncoding.EncodeToString(s.sessionMACLocked(csrfDomain, sessionPayload))
}

func (s *Server) sessionMACLocked(domain, value string) []byte {
	mac := hmac.New(sha256.New, s.sessionKey[:])
	_, _ = io.WriteString(mac, domain)
	_, _ = mac.Write([]byte{0})
	_, _ = io.WriteString(mac, value)
	return mac.Sum(nil)
}

func setSessionCookie(w http.ResponseWriter, value string, expires, now time.Time) {
	maxAge := int(expires.Sub(now).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/v1/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func writeSessionState(w http.ResponseWriter, status int, authenticated bool) {
	setResponseSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		APIVersion    int  `json:"api_version"`
		Authenticated bool `json:"authenticated"`
	}{APIVersion: controlapi.DomainAPIVersion, Authenticated: authenticated})
}

func writeSessionError(w http.ResponseWriter, status int, code, message string) {
	setResponseSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(controlapi.DomainError{
		APIVersion: controlapi.DomainAPIVersion,
		OK:         false,
		ErrorCode:  code,
		Message:    message,
	})
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
