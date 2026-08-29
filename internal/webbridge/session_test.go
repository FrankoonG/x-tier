package webbridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/controlapi"
)

func sessionRequest(t *testing.T, server *Server, method string, body []byte) *http.Request {
	t.Helper()
	request := newBridgeRequest(t, server, method, SessionPath, body)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func credentialBody(t *testing.T, credential string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]string{"credential": credential})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertSessionError(t *testing.T, result bridgeResult, status int, code string) {
	t.Helper()
	if result.status != status {
		t.Fatalf("status=%d want=%d body=%s", result.status, status, result.body)
	}
	var failure controlapi.DomainError
	if err := json.Unmarshal(result.body, &failure); err != nil {
		t.Fatalf("decode failure: %v body=%s", err, result.body)
	}
	if failure.APIVersion != controlapi.DomainAPIVersion || failure.OK || failure.ErrorCode != code || failure.Message == "" {
		t.Fatalf("failure=%+v", failure)
	}
	assertNoBrowserAuthority(t, result)
}

func TestWebSessionEndpointRejectsMalformedRequests(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	credential, err := server.readCredential()
	if err != nil {
		t.Fatal(err)
	}
	secret := "secret-that-must-not-be-reflected"

	tests := []struct {
		name   string
		build  func() *http.Request
		status int
		code   string
		allow  string
	}{
		{
			name: "query",
			build: func() *http.Request {
				return newBridgeRequest(t, server, http.MethodGet, SessionPath+"?probe=1", nil)
			},
			status: http.StatusBadRequest,
			code:   "webbridge.request_target_invalid",
		},
		{
			name: "unsupported method",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodPut, nil)
			},
			status: http.StatusMethodNotAllowed,
			code:   "webbridge.method_not_allowed",
			allow:  "GET, POST, DELETE",
		},
		{
			name: "read body",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodGet, []byte("{}"))
			},
			status: http.StatusBadRequest,
			code:   "webbridge.body_forbidden",
		},
		{
			name: "login missing origin",
			build: func() *http.Request {
				request := sessionRequest(t, server, http.MethodPost, credentialBody(t, credential))
				request.Header.Del("Origin")
				return request
			},
			status: http.StatusForbidden,
			code:   "webbridge.origin_forbidden",
		},
		{
			name: "login missing content type",
			build: func() *http.Request {
				request := sessionRequest(t, server, http.MethodPost, credentialBody(t, credential))
				request.Header.Del("Content-Type")
				return request
			},
			status: http.StatusUnsupportedMediaType,
			code:   "webbridge.content_type_invalid",
		},
		{
			name: "login wrong content type",
			build: func() *http.Request {
				request := sessionRequest(t, server, http.MethodPost, credentialBody(t, credential))
				request.Header.Set("Content-Type", "text/plain")
				return request
			},
			status: http.StatusUnsupportedMediaType,
			code:   "webbridge.content_type_invalid",
		},
		{
			name: "login malformed JSON",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodPost, []byte(`{"credential":`))
			},
			status: http.StatusBadRequest,
			code:   "webbridge.body_invalid",
		},
		{
			name: "login missing credential",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodPost, []byte(`{}`))
			},
			status: http.StatusBadRequest,
			code:   "webbridge.body_invalid",
		},
		{
			name: "login unknown field",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodPost, []byte(`{"credential":"x","extra":true}`))
			},
			status: http.StatusBadRequest,
			code:   "webbridge.body_invalid",
		},
		{
			name: "login duplicate credential",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodPost, []byte(`{"credential":"x","credential":"y"}`))
			},
			status: http.StatusBadRequest,
			code:   "webbridge.body_invalid",
		},
		{
			name: "login trailing JSON",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodPost, []byte(`{"credential":"x"}{}`))
			},
			status: http.StatusBadRequest,
			code:   "webbridge.body_invalid",
		},
		{
			name: "login oversized",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodPost, bytes.Repeat([]byte("x"), maxSessionBodyBytes+1))
			},
			status: http.StatusRequestEntityTooLarge,
			code:   "webbridge.request_too_large",
		},
		{
			name: "logout body",
			build: func() *http.Request {
				return sessionRequest(t, server, http.MethodDelete, []byte("{}"))
			},
			status: http.StatusBadRequest,
			code:   "webbridge.body_forbidden",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := doBridgeRequest(t, test.build())
			assertSessionError(t, result, test.status, test.code)
			if got := result.header.Get("Allow"); got != test.allow {
				t.Fatalf("Allow=%q want=%q", got, test.allow)
			}
			if bytes.Contains(result.body, []byte(secret)) {
				t.Fatalf("response reflected a secret: %s", result.body)
			}
		})
	}
	if got := upstream.requests.Load(); got != 0 {
		t.Fatalf("malformed session requests reached upstream: %d", got)
	}
}

func TestWebSessionLoginProbeLogoutLifecycle(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	credential, err := server.readCredential()
	if err != nil {
		t.Fatal(err)
	}

	anonymous := doBridgeRequest(t, sessionRequest(t, server, http.MethodGet, nil))
	assertSessionError(t, anonymous, http.StatusUnauthorized, "webbridge.session_invalid")
	if anonymous.header.Get("WWW-Authenticate") != "" {
		t.Fatal("session endpoint advertised browser-managed authentication")
	}

	wrong := doBridgeRequest(t, sessionRequest(t, server, http.MethodPost, credentialBody(t, credential+"-wrong")))
	assertSessionError(t, wrong, http.StatusUnauthorized, "webbridge.credential_invalid")
	if bytes.Contains(wrong.body, []byte(credential)) {
		t.Fatalf("credential leaked in rejection: %s", wrong.body)
	}

	session, login := bootstrapSession(t, server)
	if !session.cookie.HttpOnly || session.cookie.Secure || session.cookie.SameSite != http.SameSiteStrictMode ||
		session.cookie.Domain != "" || session.cookie.Path != "/v1/" || session.cookie.MaxAge < 1 || session.cookie.Expires.IsZero() {
		t.Fatalf("unsafe login cookie: %#v", session.cookie)
	}
	if login.header.Get("Cache-Control") != "no-store" || login.header.Get("Pragma") != "no-cache" {
		t.Fatalf("login response is cacheable: %v", login.header)
	}
	stolenCookieProbe := sessionRequest(t, server, http.MethodGet, nil)
	stolenCookieProbe.AddCookie(session.cookie)
	assertSessionError(t, doBridgeRequest(t, stolenCookieProbe), http.StatusForbidden, "webbridge.csrf_invalid")
	stolenCookieRead := newBridgeRequest(t, server, http.MethodGet, controlapi.StatusPath, nil)
	stolenCookieRead.AddCookie(session.cookie)
	assertSessionError(t, doBridgeRequest(t, stolenCookieRead), http.StatusForbidden, "webbridge.csrf_invalid")

	probeRequest := sessionRequest(t, server, http.MethodGet, nil)
	probeRequest.AddCookie(session.cookie)
	probeRequest.Header.Set(CSRFHeader, session.csrf)
	probe := doBridgeRequest(t, probeRequest)
	if probe.status != http.StatusOK || string(probe.body) != "{\"api_version\":1,\"authenticated\":true}\n" ||
		probe.header.Get(CSRFHeader) != session.csrf || len(probe.cookies) != 0 {
		t.Fatalf("authenticated probe status=%d headers=%v body=%s", probe.status, probe.header, probe.body)
	}

	missingCSRF := sessionRequest(t, server, http.MethodDelete, nil)
	missingCSRF.AddCookie(session.cookie)
	assertSessionError(t, doBridgeRequest(t, missingCSRF), http.StatusForbidden, "webbridge.csrf_invalid")

	stillValid := sessionRequest(t, server, http.MethodGet, nil)
	stillValid.AddCookie(session.cookie)
	stillValid.Header.Set(CSRFHeader, session.csrf)
	if result := doBridgeRequest(t, stillValid); result.status != http.StatusOK {
		t.Fatalf("failed logout revoked session: status=%d body=%s", result.status, result.body)
	}

	logout := sessionRequest(t, server, http.MethodDelete, nil)
	authenticateBrowserRequest(logout, session)
	ended := doBridgeRequest(t, logout)
	if ended.status != http.StatusOK || string(ended.body) != "{\"api_version\":1,\"authenticated\":false}\n" {
		t.Fatalf("logout status=%d body=%s", ended.status, ended.body)
	}
	assertNoBrowserAuthority(t, ended)
	assertNoSessionCookieChanges(t, ended)

	replay := sessionRequest(t, server, http.MethodGet, nil)
	replay.AddCookie(session.cookie)
	assertSessionError(t, doBridgeRequest(t, replay), http.StatusUnauthorized, "webbridge.session_invalid")
	if got := upstream.requests.Load(); got != 0 {
		t.Fatalf("session lifecycle unexpectedly reached upstream: %d", got)
	}
}

func TestWebSessionLogoutRevokesProofAuthorityAfterCookieReplacement(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	session, _ := bootstrapSession(t, server)

	logout := sessionRequest(t, server, http.MethodDelete, nil)
	logout.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "v1.0.invalid.invalid", Path: "/v1/"})
	logout.Header.Set(CSRFHeader, session.csrf)
	if result := doBridgeRequest(t, logout); result.status != http.StatusOK {
		t.Fatalf("proof-authorized logout status=%d body=%s", result.status, result.body)
	}

	replay := sessionRequest(t, server, http.MethodGet, nil)
	authenticateBrowserRequest(replay, session)
	assertSessionError(t, doBridgeRequest(t, replay), http.StatusUnauthorized, "webbridge.session_invalid")

	current, _ := bootstrapSession(t, server)
	invalid := sessionRequest(t, server, http.MethodDelete, nil)
	invalid.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "v1.0.invalid.invalid", Path: "/v1/"})
	invalid.Header.Set(CSRFHeader, strings.Repeat("z", 43))
	assertSessionError(t, doBridgeRequest(t, invalid), http.StatusUnauthorized, "webbridge.session_invalid")

	probe := sessionRequest(t, server, http.MethodGet, nil)
	authenticateBrowserRequest(probe, current)
	if result := doBridgeRequest(t, probe); result.status != http.StatusOK {
		t.Fatalf("invalid proof revoked another session: status=%d body=%s", result.status, result.body)
	}
}

func TestWebSessionCredentialRotationRevokesIssuedSessions(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	oldCredential, err := server.readCredential()
	if err != nil {
		t.Fatal(err)
	}
	oldSession, _ := bootstrapSession(t, server)
	newCredential := strings.Repeat("b", 64)
	if newCredential == oldCredential {
		newCredential = strings.Repeat("c", 64)
	}
	if err := os.WriteFile(server.credentialPath, []byte(newCredential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	probe := sessionRequest(t, server, http.MethodGet, nil)
	probe.AddCookie(oldSession.cookie)
	assertSessionError(t, doBridgeRequest(t, probe), http.StatusUnauthorized, "webbridge.session_invalid")
	assertSessionError(
		t,
		doBridgeRequest(t, sessionRequest(t, server, http.MethodPost, credentialBody(t, oldCredential))),
		http.StatusUnauthorized,
		"webbridge.credential_invalid",
	)
	newSession, _ := bootstrapSession(t, server)
	if newSession.cookie.Value == oldSession.cookie.Value || newSession.csrf == oldSession.csrf {
		t.Fatal("credential rotation reused old browser authority")
	}
}

func TestWebSessionLoginRateLimitAndRecovery(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	credential, err := server.readCredential()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	server.now = func() time.Time { return now }

	for i := 0; i < loginFailureLimit; i++ {
		result := doBridgeRequest(t, sessionRequest(t, server, http.MethodPost, credentialBody(t, "wrong-"+strconv.Itoa(i))))
		assertSessionError(t, result, http.StatusUnauthorized, "webbridge.credential_invalid")
	}
	limited := doBridgeRequest(t, sessionRequest(t, server, http.MethodPost, credentialBody(t, credential)))
	assertSessionError(t, limited, http.StatusTooManyRequests, "webbridge.rate_limited")
	if limited.header.Get("Retry-After") != strconv.Itoa(int(loginLockout/time.Second)) {
		t.Fatalf("Retry-After=%q", limited.header.Get("Retry-After"))
	}

	now = now.Add(loginLockout + time.Second)
	recovered := doBridgeRequest(t, sessionRequest(t, server, http.MethodPost, credentialBody(t, credential)))
	if recovered.status != http.StatusOK {
		t.Fatalf("valid login remained locked out: status=%d body=%s", recovered.status, recovered.body)
	}
	recoveredSession := browserSession{cookie: recoveredSessionCookie(t, recovered), csrf: recovered.header.Get(CSRFHeader)}
	probe := sessionRequest(t, server, http.MethodGet, nil)
	probe.AddCookie(recoveredSession.cookie)
	probe.Header.Set(CSRFHeader, recoveredSession.csrf)
	if result := doBridgeRequest(t, probe); result.status != http.StatusOK {
		t.Fatalf("recovered session probe status=%d body=%s", result.status, result.body)
	}
}

func TestWebSessionLoginRateLimitIsIsolatedBySource(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	credential, err := server.readCredential()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	server.now = func() time.Time { return now }

	for i := 0; i < loginFailureLimit; i++ {
		decision, _, _, _, _, err := server.login("wrong-"+strconv.Itoa(i), "10.130.40.80")
		if err != nil || decision != loginRejected {
			t.Fatalf("attacker attempt %d decision=%v err=%v", i, decision, err)
		}
	}
	decision, _, _, _, _, err := server.login(credential, "10.130.40.81")
	if err != nil || decision != loginAccepted {
		t.Fatalf("independent source decision=%v err=%v", decision, err)
	}
	decision, _, _, _, retryAfter, err := server.login(credential, "10.130.40.80")
	if err != nil || decision != loginRateLimited || retryAfter != int(loginLockout/time.Second) {
		t.Fatalf("limited source decision=%v retry=%d err=%v", decision, retryAfter, err)
	}
}

func TestLoginSourceTrackingIsCanonicalAndBounded(t *testing.T) {
	for _, test := range []struct {
		remote string
		want   string
	}{
		{remote: "10.130.40.80:49152", want: "10.130.40.80"},
		{remote: "[fd00::80]:49152", want: "fd00::80"},
		{remote: "[fe80::80%eth0]:49152", want: "fe80::80"},
		{remote: "malformed", want: "unknown"},
	} {
		if got := loginSourceKey(test.remote); got != test.want {
			t.Fatalf("loginSourceKey(%q)=%q want=%q", test.remote, got, test.want)
		}
	}

	server := &Server{loginAttempts: make(map[string]loginAttemptState)}
	now := time.Now().UTC()
	server.authMu.Lock()
	for i := 0; i < maxLoginSources+64; i++ {
		server.recordLoginFailureLocked("source-"+strconv.Itoa(i), now.Add(time.Duration(i)))
	}
	tracked := len(server.loginAttempts)
	_, newestPresent := server.loginAttempts["source-"+strconv.Itoa(maxLoginSources+63)]
	server.authMu.Unlock()
	if tracked != maxLoginSources || !newestPresent {
		t.Fatalf("tracked sources=%d newest=%t", tracked, newestPresent)
	}
}

func TestCredentialRotationCannotCommitStaleLogin(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	credential, err := server.readCredential()
	if err != nil {
		t.Fatal(err)
	}
	decision, value, csrf, expires, _, err := server.login(credential, "10.130.40.80")
	if err != nil || decision != loginAccepted {
		t.Fatalf("login decision=%v err=%v", decision, err)
	}
	rotated := strings.Repeat("d", 64)
	if err := os.WriteFile(server.credentialPath, []byte(rotated+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := sessionRequest(t, server, http.MethodPost, nil)
	committed, err := server.commitLogin(recorder, request, value, csrf, expires)
	if err != nil {
		t.Fatal(err)
	}
	if committed || recorder.Header().Get("Set-Cookie") != "" || recorder.Header().Get(CSRFHeader) != "" {
		t.Fatalf("stale login committed=%t headers=%v", committed, recorder.Header())
	}
}

func TestWebSessionRegistryIsBounded(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	credential, err := server.readCredential()
	if err != nil {
		t.Fatal(err)
	}

	sessions := make([]browserSession, 0, maxActiveSessions+1)
	for i := 0; i < maxActiveSessions+1; i++ {
		result := doBridgeRequest(t, sessionRequest(t, server, http.MethodPost, credentialBody(t, credential)))
		if result.status != http.StatusOK {
			t.Fatalf("login %d status=%d body=%s", i, result.status, result.body)
		}
		sessions = append(sessions, browserSession{cookie: recoveredSessionCookie(t, result), csrf: result.header.Get(CSRFHeader)})
	}
	server.authMu.Lock()
	active := len(server.sessions)
	server.authMu.Unlock()
	if active != maxActiveSessions {
		t.Fatalf("active sessions=%d want=%d", active, maxActiveSessions)
	}
	for index, session := range sessions {
		probe := sessionRequest(t, server, http.MethodGet, nil)
		probe.AddCookie(session.cookie)
		probe.Header.Set(CSRFHeader, session.csrf)
		result := doBridgeRequest(t, probe)
		want := http.StatusOK
		if index == 0 {
			want = http.StatusUnauthorized
		}
		if result.status != want {
			t.Fatalf("bounded session %d status=%d want=%d body=%s", index, result.status, want, result.body)
		}
	}
}

func TestEveryBrowserDomainRouteRequiresSessionAndNonGETRequiresCSRF(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	session, _ := bootstrapSession(t, server)
	baseline := upstream.requests.Load()

	paths := []string{controlapi.StatusPath, controlapi.HealthPath}
	for _, path := range paths {
		request := newBridgeRequest(t, server, http.MethodGet, path, nil)
		assertSessionError(t, doBridgeRequest(t, request), http.StatusUnauthorized, "webbridge.session_invalid")
		request = newBridgeRequest(t, server, http.MethodGet, path, nil)
		request.AddCookie(session.cookie)
		assertSessionError(t, doBridgeRequest(t, request), http.StatusForbidden, "webbridge.csrf_invalid")
	}
	for _, route := range controlapi.DomainRoutes() {
		route := route
		t.Run(route.Method+" "+route.Path, func(t *testing.T) {
			anonymous := newBridgeRequest(t, server, route.Method, route.Path, []byte("{}"))
			if route.Method != http.MethodGet {
				anonymous.Header.Set("Content-Type", "application/json")
			}
			assertSessionError(t, doBridgeRequest(t, anonymous), http.StatusUnauthorized, "webbridge.session_invalid")

			if route.Method == http.MethodGet {
				cookieOnly := newBridgeRequest(t, server, route.Method, route.Path, nil)
				cookieOnly.AddCookie(session.cookie)
				assertSessionError(t, doBridgeRequest(t, cookieOnly), http.StatusForbidden, "webbridge.csrf_invalid")
				return
			}
			withoutCSRF := newBridgeRequest(t, server, route.Method, route.Path, []byte("{}"))
			withoutCSRF.Header.Set("Content-Type", "application/json")
			withoutCSRF.AddCookie(session.cookie)
			result := doBridgeRequest(t, withoutCSRF)
			if result.status != http.StatusForbidden || !bytes.Contains(result.body, []byte(`"error_code":"webbridge.csrf_invalid"`)) {
				t.Fatalf("non-GET route bypassed CSRF: status=%d body=%s", result.status, result.body)
			}
		})
	}
	if got := upstream.requests.Load(); got != baseline {
		t.Fatalf("denied route matrix reached upstream: baseline=%d got=%d", baseline, got)
	}
}
