package webbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

func TestStoreBackedBridgeAuthenticatesAndProxiesWithoutLegacyPaths(t *testing.T) {
	upstream := newTestUpstream(t)
	store, err := statestore.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Replace(statestore.ControlToken, []byte(upstream.token+"\n")); err != nil {
		t.Fatal(err)
	}
	if err := store.Replace(statestore.WebToken, []byte(upstream.webToken+"\n")); err != nil {
		t.Fatal(err)
	}
	server, err := Start(context.Background(), Config{
		Addr: "127.0.0.1:0", ControlAddr: upstream.listener.Addr().String(), StateStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	request, err := http.NewRequest(http.MethodGet, "http://"+server.Addr()+controlapi.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = server.Addr()
	request.Header.Set("Origin", "http://"+server.Addr())
	request.SetBasicAuth(BasicUsername, upstream.webToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("store-backed bridge status=%d", response.StatusCode)
	}
}

type upstreamRecord struct {
	method      string
	path        string
	contentType string
	body        []byte
}

type testUpstream struct {
	listener  net.Listener
	server    *http.Server
	token     string
	tokenPath string
	webToken  string
	webPath   string

	records         chan upstreamRecord
	errors          chan error
	requests        atomic.Int64
	signResponses   bool
	corruptResponse bool
	blockCommand    <-chan struct{}
	commandEntered  chan<- struct{}
}

func newTestUpstream(t *testing.T) *testUpstream {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "control-token")
	token, err := controlapi.CreateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	webPath := filepath.Join(t.TempDir(), "web-token")
	webToken, err := controlapi.CreateToken(webPath)
	if err != nil {
		t.Fatal(err)
	}
	if webToken == token {
		t.Fatal("test Web credential reused the control token")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &testUpstream{
		listener:      listener,
		token:         token,
		tokenPath:     tokenPath,
		webToken:      webToken,
		webPath:       webPath,
		records:       make(chan upstreamRecord, 64),
		errors:        make(chan error, 16),
		signResponses: true,
	}
	u.server = &http.Server{Handler: http.HandlerFunc(u.serveHTTP)}
	go func() {
		if err := u.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			u.report(err)
		}
	}()
	t.Cleanup(func() {
		_ = u.server.Close()
		u.assertNoErrors(t)
	})
	return u
}

func (u *testUpstream) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == controlapi.ChallengePath {
		u.serveChallenge(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		u.report(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	challenge, ok := controlapi.RequestChallenge(r)
	if !ok || !controlapi.VerifyChallenge(u.token, challenge, time.Now()) ||
		!controlapi.VerifyRequestAuthentication(r, u.token, challenge, body) {
		u.report(fmt.Errorf("invalid authenticated request for %s", r.URL.Path))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	u.requests.Add(1)
	u.records <- upstreamRecord{
		method:      r.Method,
		path:        r.URL.Path,
		contentType: r.Header.Get("Content-Type"),
		body:        append([]byte(nil), body...),
	}

	if r.URL.Path == controlapi.DomainIdentityInitPath && u.blockCommand != nil {
		if u.commandEntered != nil {
			select {
			case u.commandEntered <- struct{}{}:
			default:
			}
		}
		select {
		case <-u.blockCommand:
		case <-r.Context().Done():
			return
		}
	}

	status, response := upstreamResponse(r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if u.signResponses {
		if err := controlapi.SignResponse(w.Header(), u.token, r, body, status, response); err != nil {
			u.report(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	if u.corruptResponse {
		response = append(append([]byte(nil), response...), ' ')
	}
	w.WriteHeader(status)
	_, _ = w.Write(response)
}

func (u *testUpstream) serveChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		u.report(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	challenge, err := controlapi.SignChallenge(
		u.token,
		hex.EncodeToString(raw[:]),
		time.Now().Add(30*time.Second),
		strings.Repeat("a", 64),
	)
	if err != nil {
		u.report(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(challenge); err != nil {
		u.report(err)
	}
}

func (u *testUpstream) report(err error) {
	select {
	case u.errors <- err:
	default:
	}
}

func (u *testUpstream) assertNoErrors(t *testing.T) {
	t.Helper()
	select {
	case err := <-u.errors:
		t.Errorf("test upstream: %v", err)
	default:
	}
}

func upstreamResponse(path string) (int, []byte) {
	switch path {
	case controlapi.HealthPath:
		return http.StatusOK, []byte("{\"ok\":true}\n")
	case controlapi.StatusPath:
		return http.StatusOK, []byte("{\"api_version\":1,\"state\":\"running\",\"revision\":7}\n")
	case controlapi.DomainIdentityInitPath:
		return http.StatusOK, []byte("{\"api_version\":1,\"ok\":true,\"changed\":true,\"before_revision\":7,\"after_revision\":8}\n")
	default:
		return http.StatusNotFound, []byte("{\"error\":\"not_found\"}\n")
	}
}

func startTestBridge(t *testing.T, upstream *testUpstream, mutate ...func(*Config)) *Server {
	t.Helper()
	cfg := Config{
		Addr:           "127.0.0.1:0",
		ControlAddr:    upstream.listener.Addr().String(),
		TokenPath:      upstream.tokenPath,
		CredentialPath: upstream.webPath,
	}
	for _, fn := range mutate {
		fn(&cfg)
	}
	server, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return server
}

type bridgeResult struct {
	status  int
	header  http.Header
	body    []byte
	cookies []*http.Cookie
}

func newBridgeRequest(t *testing.T, server *Server, method, path string, body []byte) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+server.Addr()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = server.Addr()
	request.Header.Set("Origin", "http://"+server.Addr())
	credential, err := controlapi.ReadToken(server.credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(BasicUsername, credential)
	return request
}

func doBridgeRequest(t *testing.T, request *http.Request) bridgeResult {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	return doBridgeRequestWithClient(t, client, request)
}

func doBridgeRequestWithClient(t *testing.T, client *http.Client, request *http.Request) bridgeResult {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return bridgeResult{
		status:  response.StatusCode,
		header:  response.Header.Clone(),
		body:    body,
		cookies: response.Cookies(),
	}
}

type browserSession struct {
	cookie *http.Cookie
	csrf   string
}

func bootstrapSession(t *testing.T, server *Server) (browserSession, bridgeResult) {
	t.Helper()
	result := doBridgeRequest(t, newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil))
	if result.status != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", result.status, result.body)
	}
	if len(result.cookies) != 1 {
		t.Fatalf("bootstrap cookies=%d", len(result.cookies))
	}
	csrf := result.header.Get(CSRFHeader)
	if csrf == "" {
		t.Fatal("bootstrap response did not provide CSRF token")
	}
	return browserSession{cookie: result.cookies[0], csrf: csrf}, result
}

func recoveredSessionCookie(t *testing.T, result bridgeResult) *http.Cookie {
	t.Helper()
	deletedPaths := make(map[string]bool)
	namedCount := 0
	var fresh *http.Cookie
	for _, cookie := range result.cookies {
		if cookie.Name != SessionCookieName {
			continue
		}
		namedCount++
		if cookie.MaxAge < 0 || cookie.Value == "" {
			deletedPaths[cookie.Path] = true
			continue
		}
		if fresh != nil {
			t.Fatalf("session recovery issued multiple live sessions: %v", result.header.Values("Set-Cookie"))
		}
		fresh = cookie
	}
	if namedCount != 4 {
		t.Fatalf("session recovery Set-Cookie count=%d headers=%v", namedCount, result.header.Values("Set-Cookie"))
	}
	for _, path := range []string{"/", "/v1", "/v1/"} {
		if !deletedPaths[path] {
			t.Fatalf("session recovery did not clear path %q: %v", path, result.header.Values("Set-Cookie"))
		}
	}
	if fresh == nil || fresh.Path != "/v1/" || fresh.MaxAge < 1 {
		t.Fatalf("session recovery did not issue one fresh scoped session: %+v", fresh)
	}
	return fresh
}

func TestBridgeRequiresIndependentBasicCredential(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)

	request, err := http.NewRequest(http.MethodGet, "http://"+server.Addr()+controlapi.HealthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = server.Addr()
	request.Header.Set("Origin", "http://"+server.Addr())
	unauthenticated := doBridgeRequest(t, request)
	if unauthenticated.status != http.StatusUnauthorized || len(unauthenticated.cookies) != 0 || unauthenticated.header.Get(CSRFHeader) != "" {
		t.Fatalf("anonymous bootstrap=%d headers=%v", unauthenticated.status, unauthenticated.header)
	}
	if unauthenticated.header.Get("WWW-Authenticate") == "" {
		t.Fatal("anonymous response omitted Basic challenge")
	}

	request, err = http.NewRequest(http.MethodGet, "http://"+server.Addr()+controlapi.HealthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = server.Addr()
	request.Header.Set("Origin", "http://"+server.Addr())
	request.SetBasicAuth(BasicUsername, upstream.token)
	if result := doBridgeRequest(t, request); result.status != http.StatusUnauthorized {
		t.Fatalf("control token authenticated to Web bridge: %d", result.status)
	}

	if _, result := bootstrapSession(t, server); result.status != http.StatusOK {
		t.Fatalf("Web credential bootstrap=%d", result.status)
	}
}

func TestBridgeCredentialRotationRevokesOldValueWithoutRestart(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	oldCredential, err := controlapi.ReadToken(server.credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	newCredential := strings.Repeat("cd", 32)
	if newCredential == oldCredential {
		t.Fatal("test credential unexpectedly matches existing value")
	}
	if err := os.WriteFile(server.credentialPath, []byte(newCredential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldRequest, err := http.NewRequest(http.MethodGet, "http://"+server.Addr()+controlapi.HealthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldRequest.Host = server.Addr()
	oldRequest.Header.Set("Origin", "http://"+server.Addr())
	oldRequest.SetBasicAuth(BasicUsername, oldCredential)
	if result := doBridgeRequest(t, oldRequest); result.status != http.StatusUnauthorized {
		t.Fatalf("old credential status=%d", result.status)
	}
	if result := doBridgeRequest(t, newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil)); result.status != http.StatusOK {
		t.Fatalf("rotated credential status=%d body=%s", result.status, result.body)
	}

	if err := os.WriteFile(server.credentialPath, []byte(upstream.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := doBridgeRequest(t, newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil)); result.status != http.StatusUnauthorized {
		t.Fatalf("control-token reuse status=%d", result.status)
	}
}

func TestBridgeRejectsEqualControlAndWebCredentialAtStartup(t *testing.T) {
	upstream := newTestUpstream(t)
	if err := os.WriteFile(upstream.webPath, []byte(upstream.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Config{
		Addr:           "127.0.0.1:0",
		ControlAddr:    upstream.listener.Addr().String(),
		TokenPath:      upstream.tokenPath,
		CredentialPath: upstream.webPath,
	})
	if err == nil || !strings.Contains(err.Error(), "webbridge.credential_reuses_control_token") {
		t.Fatalf("equal credential error=%v", err)
	}
}

func authenticateBrowserRequest(request *http.Request, session browserSession) {
	request.AddCookie(session.cookie)
	request.Header.Set(CSRFHeader, session.csrf)
}

func TestBridgeProxiesAuthenticatedAPIWithoutDisclosingToken(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)

	session, health := bootstrapSession(t, server)
	if string(health.body) != "{\"ok\":true}\n" {
		t.Fatalf("health body changed: %q", health.body)
	}
	if !session.cookie.HttpOnly || session.cookie.SameSite != http.SameSiteStrictMode || session.cookie.Path != "/v1/" || session.cookie.MaxAge <= 0 {
		t.Fatalf("unsafe session cookie: %#v", session.cookie)
	}
	assertTokenAbsent(t, upstream.token, health)

	statusRequest := newBridgeRequest(t, server, http.MethodGet, controlapi.StatusPath, nil)
	statusRequest.AddCookie(session.cookie)
	status := doBridgeRequest(t, statusRequest)
	_, wantStatusBody := upstreamResponse(controlapi.StatusPath)
	if status.status != http.StatusOK || !bytes.Equal(status.body, wantStatusBody) {
		t.Fatalf("status response=%d %q", status.status, status.body)
	}
	if len(status.cookies) != 0 || status.header.Get(CSRFHeader) != session.csrf {
		t.Fatalf("existing session was unexpectedly replaced: headers=%v", status.header)
	}
	assertTokenAbsent(t, upstream.token, status)

	commandBody := []byte(" {\n  \"api_version\": 1, \"revision\": 7, \"dry_run\": false, \"request_id\": \"0123456789abcdef0123456789abcdef\", \"name\": \"node-a\"\n}\n")
	commandRequest := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, commandBody)
	commandRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	authenticateBrowserRequest(commandRequest, session)
	command := doBridgeRequest(t, commandRequest)
	_, wantCommandBody := upstreamResponse(controlapi.DomainIdentityInitPath)
	if command.status != http.StatusOK || !bytes.Equal(command.body, wantCommandBody) {
		t.Fatalf("command response=%d %q", command.status, command.body)
	}
	assertTokenAbsent(t, upstream.token, command)

	records := []upstreamRecord{<-upstream.records, <-upstream.records, <-upstream.records}
	if records[0].method != http.MethodGet || records[0].path != controlapi.HealthPath || len(records[0].body) != 0 {
		t.Fatalf("health upstream request=%+v", records[0])
	}
	if records[1].method != http.MethodGet || records[1].path != controlapi.StatusPath || len(records[1].body) != 0 {
		t.Fatalf("status upstream request=%+v", records[1])
	}
	if records[2].method != http.MethodPost || records[2].path != controlapi.DomainIdentityInitPath ||
		records[2].contentType != "application/json" || !bytes.Equal(records[2].body, commandBody) {
		t.Fatalf("domain request changed upstream: %+v", records[2])
	}
	if bytes.Contains(records[2].body, []byte(`"args"`)) || bytes.Contains(records[2].body, []byte(`"argv"`)) {
		t.Fatalf("browser forwarded a CLI envelope: %s", records[2].body)
	}
	upstream.assertNoErrors(t)
}

func TestBridgeReplacesInvalidReadSessions(t *testing.T) {
	upstream := newTestUpstream(t)
	oldServer := startTestBridge(t, upstream)
	oldSession, _ := bootstrapSession(t, oldServer)
	server := startTestBridge(t, upstream)
	currentSession, _ := bootstrapSession(t, server)
	baseline := upstream.requests.Load()

	expired := *currentSession.cookie
	parts := strings.Split(expired.Value, ".")
	if len(parts) != 4 {
		t.Fatalf("unexpected session format: %q", expired.Value)
	}
	parts[1] = "1"
	payload := strings.Join(parts[:3], ".")
	parts[3] = base64.RawURLEncoding.EncodeToString(server.sessionMAC(sessionDomain, payload))
	expired.Value = strings.Join(parts, ".")

	forged := *currentSession.cookie
	forged.Value += "x"
	tests := []struct {
		name   string
		cookie *http.Cookie
	}{
		{name: "old session key", cookie: oldSession.cookie},
		{name: "forged session", cookie: &forged},
		{name: "expired session", cookie: &expired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := newBridgeRequest(t, server, http.MethodGet, controlapi.StatusPath, nil)
			request.AddCookie(test.cookie)
			result := doBridgeRequest(t, request)
			_, wantBody := upstreamResponse(controlapi.StatusPath)
			if result.status != http.StatusOK || !bytes.Equal(result.body, wantBody) {
				t.Fatalf("status=%d body=%s", result.status, result.body)
			}
			fresh := recoveredSessionCookie(t, result)
			if result.header.Get(CSRFHeader) == "" || fresh.Value == test.cookie.Value {
				t.Fatalf("invalid read session was not replaced: headers=%v", result.header)
			}
		})
	}
	if got, want := upstream.requests.Load(), baseline+int64(len(tests)); got != want {
		t.Fatalf("recovered read sessions did not reach upstream: got=%d want=%d", got, want)
	}
	upstream.assertNoErrors(t)
}

func TestConcurrentRestartRecoveryCannotDesynchronizeCookieAndCSRF(t *testing.T) {
	upstream := newTestUpstream(t)
	oldServer := startTestBridge(t, upstream)
	oldSession, _ := bootstrapSession(t, oldServer)
	server := startTestBridge(t, upstream)

	requests := make([]*http.Request, 2)
	for i := range requests {
		requests[i] = newBridgeRequest(t, server, http.MethodGet, controlapi.StatusPath, nil)
		requests[i].AddCookie(oldSession.cookie)
	}
	type outcome struct {
		result bridgeResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, len(requests))
	for _, request := range requests {
		go func(request *http.Request) {
			<-start
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			results <- outcome{result: bridgeResult{
				status: response.StatusCode, header: response.Header.Clone(), body: body, cookies: response.Cookies(),
			}, err: err}
		}(request)
	}
	close(start)

	sessions := make([]browserSession, 0, len(requests))
	for range requests {
		outcome := <-results
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.result.status != http.StatusOK {
			t.Fatalf("concurrent recovery result=%+v", outcome.result)
		}
		sessions = append(sessions, browserSession{
			cookie: recoveredSessionCookie(t, outcome.result), csrf: outcome.result.header.Get(CSRFHeader),
		})
	}
	if sessions[0].cookie.Value == sessions[1].cookie.Value {
		t.Fatal("concurrent restart recovery reused a random session cookie")
	}
	if sessions[0].csrf == "" || sessions[1].csrf == "" || sessions[0].csrf == sessions[1].csrf ||
		sessions[0].csrf == oldSession.csrf || sessions[1].csrf == oldSession.csrf {
		t.Fatal("restart recovery did not bind a fresh CSRF token to each session")
	}

	body := []byte(`{"api_version":1,"revision":0,"dry_run":true,"request_id":"0123456789abcdef0123456789abcdef","name":"node-a"}`)
	oldCSRFRequest := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, body)
	oldCSRFRequest.Header.Set("Content-Type", "application/json")
	authenticateBrowserRequest(oldCSRFRequest, browserSession{cookie: sessions[0].cookie, csrf: oldSession.csrf})
	if result := doBridgeRequest(t, oldCSRFRequest); result.status != http.StatusForbidden {
		t.Fatalf("pre-restart CSRF authenticated after restart: %d", result.status)
	}

	crossPaired := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, body)
	crossPaired.Header.Set("Content-Type", "application/json")
	authenticateBrowserRequest(crossPaired, browserSession{cookie: sessions[0].cookie, csrf: sessions[1].csrf})
	if result := doBridgeRequest(t, crossPaired); result.status != http.StatusForbidden {
		t.Fatalf("CSRF token from another concurrent session authenticated: %d body=%s", result.status, result.body)
	}

	for i, session := range sessions {
		selfPaired := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, body)
		selfPaired.Header.Set("Content-Type", "application/json")
		authenticateBrowserRequest(selfPaired, session)
		if result := doBridgeRequest(t, selfPaired); result.status != http.StatusOK {
			t.Fatalf("concurrent session %d rejected its own CSRF token: %d body=%s", i, result.status, result.body)
		}
	}
	upstream.assertNoErrors(t)
}

func TestBridgeRejectsHostOriginSessionAndCSRFAttacks(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	session, _ := bootstrapSession(t, server)
	secondSession, _ := bootstrapSession(t, server)
	baseline := upstream.requests.Load()

	tests := []struct {
		name   string
		build  func() *http.Request
		status int
	}{
		{
			name: "wrong host",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil)
				_, port, _ := net.SplitHostPort(server.Addr())
				r.Host = net.JoinHostPort("localhost", port)
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "missing origin",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Del("Origin")
				r.Header.Set("Content-Type", "application/json")
				r.AddCookie(session.cookie)
				r.Header.Set(CSRFHeader, session.csrf)
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "cross origin",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil)
				r.Header.Set("Origin", "https://attacker.invalid")
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "duplicate origin",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil)
				r.Header.Add("Origin", "http://"+server.Addr())
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "missing session",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set(CSRFHeader, session.csrf)
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "missing csrf",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Set("Content-Type", "application/json")
				r.AddCookie(session.cookie)
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "wrong csrf",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Set("Content-Type", "application/json")
				r.AddCookie(session.cookie)
				r.Header.Set(CSRFHeader, strings.Repeat("x", len(session.csrf)))
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "duplicate csrf",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Set("Content-Type", "application/json")
				r.AddCookie(session.cookie)
				r.Header.Add(CSRFHeader, session.csrf)
				r.Header.Add(CSRFHeader, session.csrf)
				return r
			},
			status: http.StatusForbidden,
		},
		{
			name: "forged session",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Set("Content-Type", "application/json")
				forged := *session.cookie
				forged.Value += "x"
				r.AddCookie(&forged)
				r.Header.Set(CSRFHeader, session.csrf)
				return r
			},
			status: http.StatusForbidden,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := doBridgeRequest(t, test.build())
			if result.status != test.status {
				t.Fatalf("status=%d body=%s", result.status, result.body)
			}
			if len(result.cookies) != 0 || result.header.Get(CSRFHeader) != "" {
				t.Fatalf("rejected request received a browser session: headers=%v", result.header)
			}
			assertTokenAbsent(t, upstream.token, result)
		})
	}
	if got := upstream.requests.Load(); got != baseline {
		t.Fatalf("rejected browser requests reached upstream: baseline=%d got=%d", baseline, got)
	}

	request := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(session.cookie)
	request.AddCookie(secondSession.cookie)
	request.Header.Set(CSRFHeader, session.csrf)
	result := doBridgeRequest(t, request)
	if result.status != http.StatusForbidden {
		t.Fatalf("request with distinct valid sessions status=%d body=%s", result.status, result.body)
	}
	if len(result.cookies) != 0 || result.header.Get(CSRFHeader) != "" {
		t.Fatalf("rejected ambiguous session received browser authority: headers=%v", result.header)
	}
	if got := upstream.requests.Load(); got != baseline {
		t.Fatalf("ambiguous-session domain request reached upstream: baseline=%d got=%d", baseline, got)
	}

	scenario := doBridgeRequest(t, newBridgeRequest(t, server, http.MethodGet, "/v1/__scenario", nil))
	if scenario.status != http.StatusNotFound {
		t.Fatalf("scenario endpoint status=%d", scenario.status)
	}
	for _, method := range []string{
		http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodOptions,
	} {
		legacy := newBridgeRequest(t, server, method, controlapi.CommandPath, []byte(`{"args":["local","status"]}`))
		legacy.Header.Set("Content-Type", "application/json")
		authenticateBrowserRequest(legacy, session)
		if result := doBridgeRequest(t, legacy); result.status != http.StatusNotFound {
			t.Fatalf("CLI command endpoint remained browser-accessible for %s: status=%d body=%s", method, result.status, result.body)
		}
	}
	if got := upstream.requests.Load(); got != baseline {
		t.Fatalf("non-domain request reached upstream: baseline=%d got=%d", baseline, got)
	}
}

func TestReadAcceptsDuplicateCopiesOfOneValidSession(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	oldSession, _ := bootstrapSession(t, server)

	read := newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil)
	read.AddCookie(oldSession.cookie)
	read.AddCookie(oldSession.cookie)
	accepted := doBridgeRequest(t, read)
	if accepted.status != http.StatusOK {
		t.Fatalf("duplicate-copy read status=%d body=%s", accepted.status, accepted.body)
	}
	csrf := accepted.header.Get(CSRFHeader)
	if csrf != oldSession.csrf || len(accepted.cookies) != 0 {
		t.Fatalf("duplicate-copy read changed session: csrf=%q cookies=%v", csrf, accepted.cookies)
	}

	mutation := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
	mutation.Header.Set("Content-Type", "application/json")
	mutation.AddCookie(oldSession.cookie)
	mutation.AddCookie(oldSession.cookie)
	mutation.Header.Set(CSRFHeader, csrf)
	result := doBridgeRequest(t, mutation)
	wantStatus, wantBody := upstreamResponse(controlapi.DomainIdentityInitPath)
	if result.status != wantStatus || !bytes.Equal(result.body, wantBody) {
		t.Fatalf("mutation with duplicate session copies status=%d body=%s", result.status, result.body)
	}
	if result.header.Get(CSRFHeader) != csrf {
		t.Fatalf("mutation with duplicate session copies lost CSRF continuity: %v", result.header)
	}
	upstream.assertNoErrors(t)
}

func TestPathScopedInvalidCookieCannotLockBrowserMutations(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}

	bootstrap := doBridgeRequestWithClient(t, client, newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil))
	if bootstrap.status != http.StatusOK || bootstrap.header.Get(CSRFHeader) == "" {
		t.Fatalf("bootstrap status=%d headers=%v body=%s", bootstrap.status, bootstrap.header, bootstrap.body)
	}
	csrf := bootstrap.header.Get(CSRFHeader)
	mutationURL := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, nil).URL
	jar.SetCookies(mutationURL, []*http.Cookie{{
		Name: SessionCookieName, Value: "v1.0.invalid.invalid", Path: "/v1/domain",
	}})

	read := doBridgeRequestWithClient(t, client, newBridgeRequest(t, server, http.MethodGet, controlapi.DomainLocalPath, nil))
	wantReadStatus, wantReadBody := upstreamResponse(controlapi.DomainLocalPath)
	if read.status != wantReadStatus || !bytes.Equal(read.body, wantReadBody) || read.header.Get(CSRFHeader) != csrf || len(read.cookies) != 0 {
		t.Fatalf("poisoned read status=%d csrf=%q cookies=%v body=%s", read.status, read.header.Get(CSRFHeader), read.cookies, read.body)
	}
	named := 0
	for _, cookie := range jar.Cookies(mutationURL) {
		if cookie.Name == SessionCookieName {
			named++
		}
	}
	if named != 2 {
		t.Fatalf("browser jar did not retain both scoped cookies: %v", jar.Cookies(mutationURL))
	}

	mutation := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
	mutation.Header.Set("Content-Type", "application/json")
	mutation.Header.Set(CSRFHeader, csrf)
	result := doBridgeRequestWithClient(t, client, mutation)
	wantStatus, wantBody := upstreamResponse(controlapi.DomainIdentityInitPath)
	if result.status != wantStatus || !bytes.Equal(result.body, wantBody) || result.header.Get(CSRFHeader) != csrf {
		t.Fatalf("mutation with retained poison status=%d headers=%v body=%s", result.status, result.header, result.body)
	}
	upstream.assertNoErrors(t)
}

func TestBridgeValidatesMethodContentTypeAndBodyBounds(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	session, _ := bootstrapSession(t, server)
	baseline := upstream.requests.Load()

	tests := []struct {
		name                string
		build               func() *http.Request
		status              int
		definitelyUnapplied bool
	}{
		{
			name: "command get",
			build: func() *http.Request {
				return newBridgeRequest(t, server, http.MethodGet, controlapi.DomainIdentityInitPath, nil)
			},
			status: http.StatusMethodNotAllowed,
		},
		{
			name: "health post",
			build: func() *http.Request {
				return newBridgeRequest(t, server, http.MethodPost, controlapi.HealthPath, nil)
			},
			status: http.StatusMethodNotAllowed,
		},
		{
			name: "missing content type",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				authenticateBrowserRequest(r, session)
				return r
			},
			status:              http.StatusUnsupportedMediaType,
			definitelyUnapplied: true,
		},
		{
			name: "wrong content type",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Set("Content-Type", "text/plain")
				authenticateBrowserRequest(r, session)
				return r
			},
			status:              http.StatusUnsupportedMediaType,
			definitelyUnapplied: true,
		},
		{
			name: "wrong charset",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
				r.Header.Set("Content-Type", "application/json; charset=iso-8859-1")
				authenticateBrowserRequest(r, session)
				return r
			},
			status:              http.StatusUnsupportedMediaType,
			definitelyUnapplied: true,
		},
		{
			name: "oversized command",
			build: func() *http.Request {
				r := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, bytes.Repeat([]byte("x"), MaxDomainBodyBytes+1))
				r.Header.Set("Content-Type", "application/json")
				authenticateBrowserRequest(r, session)
				return r
			},
			status:              http.StatusRequestEntityTooLarge,
			definitelyUnapplied: true,
		},
		{
			name: "health body",
			build: func() *http.Request {
				return newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, []byte("{}"))
			},
			status: http.StatusBadRequest,
		},
		{
			name: "query forbidden",
			build: func() *http.Request {
				return newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath+"?probe=1", nil)
			},
			status: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := doBridgeRequest(t, test.build())
			if result.status != test.status {
				t.Fatalf("status=%d body=%s", result.status, result.body)
			}
			if test.definitelyUnapplied {
				var failure controlapi.DomainError
				if err := json.Unmarshal(result.body, &failure); err != nil {
					t.Fatal(err)
				}
				if failure.Applied == nil || *failure.Applied || failure.Outcome != controlapi.MutationOutcomeNotApplied {
					t.Fatalf("pre-delivery refusal did not report not_applied: %+v", failure)
				}
			}
		})
	}
	if got := upstream.requests.Load(); got != baseline {
		t.Fatalf("invalid requests reached upstream: baseline=%d got=%d", baseline, got)
	}

	maxBody := bytes.Repeat([]byte("x"), MaxDomainBodyBytes)
	request := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, maxBody)
	request.Header.Set("Content-Type", "application/json")
	authenticateBrowserRequest(request, session)
	result := doBridgeRequest(t, request)
	if result.status != http.StatusOK {
		t.Fatalf("max-sized request status=%d body=%s", result.status, result.body)
	}
	record := <-upstream.records
	for record.path != controlapi.DomainIdentityInitPath {
		record = <-upstream.records
	}
	if !bytes.Equal(record.body, maxBody) {
		t.Fatalf("max-sized body changed: got=%d want=%d", len(record.body), len(maxBody))
	}
}

func TestBridgeServesStaticAppWithoutMintingAuthorityForUnsignaledGET(t *testing.T) {
	upstream := newTestUpstream(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>X-Tier</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("console.log('xtier')"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.css"), []byte("body{display:block}"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	outsideBody := []byte("outside-static-root")
	if err := os.WriteFile(outsidePath, outsideBody, 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, "escape.js")
	symlinkCreated := os.Symlink(outsidePath, symlinkPath) == nil
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, err := Start(ctx, Config{
		Addr:           "127.0.0.1:0",
		ControlAddr:    upstream.listener.Addr().String(),
		TokenPath:      upstream.tokenPath,
		CredentialPath: upstream.webPath,
		StaticDir:      root,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	requestWith := func(method, path string, authenticate bool, headers map[string]string) bridgeResult {
		r, err := http.NewRequest(method, "http://"+server.Addr()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if authenticate {
			r.SetBasicAuth(BasicUsername, upstream.webToken)
		}
		for name, value := range headers {
			r.Header.Set(name, value)
		}
		return doBridgeRequest(t, r)
	}
	request := func(method, path string) bridgeResult {
		return requestWith(method, path, true, nil)
	}
	if result := request(http.MethodGet, "/"); result.status != http.StatusOK || !bytes.Contains(result.body, []byte("X-Tier")) {
		t.Fatalf("index response=%d %q", result.status, result.body)
	}
	if result := request(http.MethodGet, "/index.html"); result.status != http.StatusOK ||
		!bytes.Equal(result.body, []byte("<!doctype html><title>X-Tier</title>")) ||
		result.header.Get("Location") != "" || result.header.Get("Content-Type") != "text/html; charset=utf-8" ||
		result.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("explicit index response=%d headers=%v body=%q", result.status, result.header, result.body)
	}
	if result := request(http.MethodHead, "/index.html"); result.status != http.StatusOK ||
		len(result.body) != 0 || result.header.Get("Location") != "" {
		t.Fatalf("explicit index HEAD response=%d location=%q body=%q", result.status, result.header.Get("Location"), result.body)
	}
	if result := request(http.MethodGet, "/peers/B"); result.status != http.StatusOK || !bytes.Contains(result.body, []byte("X-Tier")) {
		t.Fatalf("SPA response=%d %q", result.status, result.body)
	}
	if result := request(http.MethodGet, "/peers/B?filter=down"); result.status != http.StatusOK || !bytes.Contains(result.body, []byte("X-Tier")) {
		t.Fatalf("SPA query response=%d %q", result.status, result.body)
	}
	if result := request(http.MethodGet, "/app.js?v=1"); result.status != http.StatusOK ||
		!bytes.Contains(result.body, []byte("xtier")) || result.header.Get("Content-Type") != "text/javascript; charset=utf-8" ||
		result.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("JavaScript asset response=%d headers=%v body=%q", result.status, result.header, result.body)
	}
	if result := request(http.MethodGet, "/app.css"); result.status != http.StatusOK ||
		result.header.Get("Content-Type") != "text/css; charset=utf-8" || result.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("stylesheet asset response=%d headers=%v body=%q", result.status, result.header, result.body)
	}
	if result := requestWith(http.MethodGet, "/app.js", true, map[string]string{"Range": "bytes=0-3"}); result.status != http.StatusPartialContent || !bytes.Equal(result.body, []byte("cons")) {
		t.Fatalf("range response=%d headers=%v body=%q", result.status, result.header, result.body)
	}
	if result := requestWith(http.MethodGet, "/app.js", true, map[string]string{
		"If-Modified-Since": time.Now().UTC().Add(time.Hour).Format(http.TimeFormat),
	}); result.status != http.StatusNotModified || len(result.body) != 0 {
		t.Fatalf("conditional response=%d headers=%v body=%q", result.status, result.header, result.body)
	}
	if result := request(http.MethodPost, "/app.js"); result.status != http.StatusMethodNotAllowed || result.header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("static POST response=%d allow=%q body=%q", result.status, result.header.Get("Allow"), result.body)
	}
	if result := requestWith(http.MethodGet, "/app.js", false, nil); result.status != http.StatusUnauthorized ||
		result.header.Get("WWW-Authenticate") != `Basic realm="X-Tier", charset="UTF-8"` {
		t.Fatalf("anonymous asset response=%d headers=%v body=%q", result.status, result.header, result.body)
	}
	if result := request(http.MethodGet, "/missing.js"); result.status != http.StatusNotFound {
		t.Fatalf("missing asset status=%d", result.status)
	}
	if result := request(http.MethodGet, "/../outside.txt"); result.status != http.StatusBadRequest || bytes.Contains(result.body, outsideBody) {
		t.Fatalf("traversal response=%d body=%q", result.status, result.body)
	}
	if result := request(http.MethodGet, "/..%2foutside.txt"); result.status != http.StatusBadRequest || bytes.Contains(result.body, outsideBody) {
		t.Fatalf("encoded traversal response=%d body=%q", result.status, result.body)
	}
	if symlinkCreated {
		if result := request(http.MethodGet, "/escape.js"); result.status != http.StatusNotFound || bytes.Contains(result.body, outsideBody) {
			t.Fatalf("escaping symlink response=%d body=%q", result.status, result.body)
		}
	}
	if result := request(http.MethodGet, "/v1/__scenario"); result.status != http.StatusNotFound {
		t.Fatalf("scenario status=%d", result.status)
	}
	health := request(http.MethodGet, controlapi.HealthPath)
	if health.status != http.StatusOK || health.header.Get(CSRFHeader) != "" || len(health.cookies) != 0 {
		t.Fatalf("originless health response=%d headers=%v cookies=%v", health.status, health.header, health.cookies)
	}

	sameOriginFetch, err := http.NewRequest(http.MethodGet, "http://"+server.Addr()+controlapi.HealthPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	sameOriginFetch.SetBasicAuth(BasicUsername, upstream.webToken)
	sameOriginFetch.Header.Set("Sec-Fetch-Site", "same-origin")
	trusted := doBridgeRequest(t, sameOriginFetch)
	if trusted.status != http.StatusOK || trusted.header.Get(CSRFHeader) == "" || len(trusted.cookies) != 1 {
		t.Fatalf("same-origin fetch response=%d headers=%v cookies=%v", trusted.status, trusted.header, trusted.cookies)
	}
	if got := upstream.requests.Load(); got != 2 {
		t.Fatalf("unexpected upstream request count=%d", got)
	}
}

func TestStartRequiresLiteralLoopbackEndpoints(t *testing.T) {
	for _, addr := range []string{
		"localhost:0",
		"0.0.0.0:0",
		"[::]:0",
		"192.0.2.1:0",
		"http://127.0.0.1:0",
		"127.0.0.1",
		":0",
	} {
		t.Run(addr, func(t *testing.T) {
			credentialPath := newTestCredentialPath(t)
			server, err := Start(context.Background(), Config{
				Addr:           addr,
				ControlAddr:    "127.0.0.1:1",
				TokenPath:      filepath.Join(t.TempDir(), "token"),
				CredentialPath: credentialPath,
			})
			if err == nil {
				_ = server.Close()
				t.Fatalf("Start accepted non-literal-loopback bind %q", addr)
			}
		})
	}

	for _, controlAddr := range []string{"localhost:19090", "0.0.0.0:19090", "https://127.0.0.1:19090"} {
		t.Run("control "+controlAddr, func(t *testing.T) {
			credentialPath := newTestCredentialPath(t)
			server, err := Start(context.Background(), Config{
				Addr:           "127.0.0.1:0",
				ControlAddr:    controlAddr,
				TokenPath:      filepath.Join(t.TempDir(), "token"),
				CredentialPath: credentialPath,
			})
			if err == nil {
				_ = server.Close()
				t.Fatalf("Start accepted non-literal-loopback control address %q", controlAddr)
			}
		})
	}
}

func TestBridgeLocalMutationRejectionUsesDomainOutcomeContract(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	session, _ := bootstrapSession(t, server)
	baseline := upstream.requests.Load()

	for _, test := range []struct {
		name        string
		dryRun      bool
		wantOutcome controlapi.MutationOutcome
	}{
		{name: "write", wantOutcome: controlapi.MutationOutcomeNotApplied},
		{name: "dry run", dryRun: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(fmt.Sprintf(
				`{"api_version":1,"revision":0,"dry_run":%t,"request_id":"b0000000000000000000000000000001"}`,
				test.dryRun,
			))
			request := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, payload)
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(session.cookie)
			// Missing CSRF is a local, pre-delivery refusal.
			result := doBridgeRequest(t, request)
			if result.status != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", result.status, result.body)
			}
			var failure controlapi.DomainError
			if err := json.Unmarshal(result.body, &failure); err != nil {
				t.Fatal(err)
			}
			if failure.ErrorCode != "webbridge.csrf_invalid" || failure.Outcome != test.wantOutcome {
				t.Fatalf("failure=%+v", failure)
			}
			if test.dryRun && failure.Applied != nil {
				t.Fatalf("dry-run rejection carried mutation facts: %+v", failure)
			}
			if !test.dryRun && (failure.Applied == nil || *failure.Applied) {
				t.Fatalf("write rejection did not report applied=false: %+v", failure)
			}
		})
	}
	if got := upstream.requests.Load(); got != baseline {
		t.Fatalf("local rejections reached upstream: baseline=%d got=%d", baseline, got)
	}
}

func TestBridgeRejectsUnauthenticatedUpstreamResponse(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*testUpstream)
	}{
		{
			name: "missing authentication",
			configure: func(upstream *testUpstream) {
				upstream.signResponses = false
			},
		},
		{
			name: "authenticated body tampered",
			configure: func(upstream *testUpstream) {
				upstream.corruptResponse = true
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := newTestUpstream(t)
			test.configure(upstream)
			server := startTestBridge(t, upstream)

			result := doBridgeRequest(t, newBridgeRequest(t, server, http.MethodGet, controlapi.HealthPath, nil))
			if result.status != http.StatusBadGateway || !bytes.Contains(result.body, []byte("webbridge.upstream_unavailable")) {
				t.Fatalf("status=%d body=%s", result.status, result.body)
			}
			if len(result.cookies) != 0 || result.header.Get(CSRFHeader) != "" {
				t.Fatalf("failed upstream established browser session: headers=%v", result.header)
			}
			assertTokenAbsent(t, upstream.token, result)
		})
	}
}

func TestBridgeReportsUnavailableUpstreamWithoutLeakingToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "control-token")
	token, err := controlapi.CreateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	controlAddr := listener.Addr().String()
	_ = listener.Close()

	server, err := Start(context.Background(), Config{
		Addr:            "127.0.0.1:0",
		ControlAddr:     controlAddr,
		TokenPath:       tokenPath,
		CredentialPath:  newTestCredentialPath(t),
		UpstreamTimeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	result := doBridgeRequest(t, newBridgeRequest(t, server, http.MethodGet, controlapi.StatusPath, nil))
	if result.status != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", result.status, result.body)
	}
	assertTokenAbsent(t, token, result)
}

func TestMutationUpstreamErrorWithoutDeliveryIsDefinitelyNotApplied(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    []byte
		outcome controlapi.MutationOutcome
	}{
		{name: "write rejected before delivery", body: []byte(`{"dry_run":false}`), outcome: controlapi.MutationOutcomeNotApplied},
		{name: "dry run", body: []byte(`{"dry_run":true}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeMutationUpstreamError(response, test.body, context.DeadlineExceeded)
			if response.Code != http.StatusGatewayTimeout ||
				!bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"webbridge.upstream_timeout"`)) {
				t.Fatalf("timeout response status=%d body=%s", response.Code, response.Body.Bytes())
			}
			var failure controlapi.DomainError
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Outcome != test.outcome {
				t.Fatalf("outcome=%q, want %q: %s", failure.Outcome, test.outcome, response.Body.Bytes())
			}
			if test.outcome == "" && failure.Applied != nil {
				t.Fatalf("dry-run error carried mutation fact: %+v", failure)
			}
			if test.outcome != "" && (failure.Applied == nil || *failure.Applied) {
				t.Fatalf("pre-delivery error did not say applied=false: %+v", failure)
			}
		})
	}
}

func TestMutationUpstreamFailureAfterDeliveryIsIndeterminate(t *testing.T) {
	upstream := newTestUpstream(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	upstream.blockCommand = release
	upstream.commandEntered = entered
	bridge := startTestBridge(t, upstream)
	bridge.mutationUpstreamBudget = 50 * time.Millisecond
	session, _ := bootstrapSession(t, bridge)
	t.Cleanup(func() { close(release) })

	payload := []byte(`{"api_version":1,"revision":0,"dry_run":false,"request_id":"a0000000000000000000000000000001"}`)
	request := newBridgeRequest(t, bridge, http.MethodPost, controlapi.DomainIdentityInitPath, payload)
	request.Header.Set("Content-Type", "application/json")
	authenticateBrowserRequest(request, session)
	result := doBridgeRequest(t, request)
	if result.status != http.StatusGatewayTimeout {
		t.Fatalf("delivered timeout status=%d body=%s", result.status, result.body)
	}
	select {
	case <-entered:
	default:
		t.Fatal("timed-out mutation never reached the upstream")
	}
	var failure controlapi.DomainError
	if err := json.Unmarshal(result.body, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Applied == nil || *failure.Applied || failure.Outcome != controlapi.MutationOutcomeIndeterminate {
		t.Fatalf("delivered timeout failure=%+v", failure)
	}
}

func TestMutationResponseHeadersMayArriveAfterTenSeconds(t *testing.T) {
	upstream := newTestUpstream(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	upstream.blockCommand = release
	upstream.commandEntered = entered
	bridge := startTestBridge(t, upstream, func(cfg *Config) {
		cfg.UpstreamTimeout = 250 * time.Millisecond
	})
	session, _ := bootstrapSession(t, bridge)
	payload := []byte(`{"api_version":1,"revision":0,"dry_run":false,"request_id":"a1000000000000000000000000000001"}`)
	request := newBridgeRequest(t, bridge, http.MethodPost, controlapi.DomainIdentityInitPath, payload)
	request.Header.Set("Content-Type", "application/json")
	authenticateBrowserRequest(request, session)

	delay := 10*time.Second + 200*time.Millisecond
	abortDelay := make(chan struct{})
	releaseCommand := sync.OnceFunc(func() { close(release) })
	defer releaseCommand()
	defer close(abortDelay)
	go func() {
		select {
		case <-entered:
		case <-abortDelay:
			return
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			releaseCommand()
		case <-abortDelay:
		}
	}()
	started := time.Now()
	result := doBridgeRequestWithClient(t, &http.Client{Timeout: 15 * time.Second}, request)
	if result.status != http.StatusOK || !bytes.Contains(result.body, []byte(`"after_revision":8`)) {
		t.Fatalf("slow mutation status=%d body=%s", result.status, result.body)
	}
	if elapsed := time.Since(started); elapsed < delay {
		t.Fatalf("slow mutation returned in %s before delayed response headers at %s", elapsed, delay)
	}
}

func TestCloseIsConcurrentAndWaitsForAcceptedRequest(t *testing.T) {
	upstream := newTestUpstream(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	upstream.blockCommand = release
	upstream.commandEntered = entered
	server := startTestBridge(t, upstream)
	session, _ := bootstrapSession(t, server)

	request := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
	request.Header.Set("Content-Type", "application/json")
	authenticateBrowserRequest(request, session)
	type requestOutcome struct {
		result bridgeResult
		err    error
	}
	requestDone := make(chan requestOutcome, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			requestDone <- requestOutcome{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		requestDone <- requestOutcome{
			result: bridgeResult{status: response.StatusCode, header: response.Header.Clone(), body: body},
			err:    err,
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("command did not reach upstream")
	}

	const closers = 8
	closeResults := make(chan error, closers)
	for range closers {
		go func() { closeResults <- server.Close() }()
	}
	select {
	case err := <-closeResults:
		t.Fatalf("Close returned before accepted request completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	outcome := <-requestDone
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	if outcome.result.status != http.StatusOK {
		t.Fatalf("in-flight command status=%d body=%s", outcome.result.status, outcome.result.body)
	}
	for range closers {
		if err := <-closeResults; err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("Done did not close")
	}
	if err := server.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCloseContextBoundsBlockedAcceptedRequest(t *testing.T) {
	upstream := newTestUpstream(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	upstream.blockCommand = release
	upstream.commandEntered = entered
	server, err := Start(context.Background(), Config{
		Addr: "127.0.0.1:0", ControlAddr: upstream.listener.Addr().String(),
		TokenPath: upstream.tokenPath, CredentialPath: upstream.webPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.shutdownWait = 30 * time.Millisecond
	session, _ := bootstrapSession(t, server)

	request := newBridgeRequest(t, server, http.MethodPost, controlapi.DomainIdentityInitPath, []byte("{}"))
	request.Header.Set("Content-Type", "application/json")
	authenticateBrowserRequest(request, session)
	requestDone := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err == nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach upstream")
	}

	started := time.Now()
	err = server.Close()
	if !errors.Is(err, ErrShutdownIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want incomplete deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close remained blocked for %s", elapsed)
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("HTTP serving loop did not stop")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- server.Wait() }()
	waitCompleted := false
	select {
	case err := <-waitDone:
		waitCompleted = true
		if err != nil {
			t.Fatalf("Wait after forced request cancellation: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-server.closedDone:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown worker did not finish after request release")
	}
	if !waitCompleted {
		if err := <-waitDone; err != nil {
			t.Fatalf("Wait after request release: %v", err)
		}
	}
	<-requestDone
}

func TestUnexpectedListenerCloseAutomaticallyCompletesShutdown(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	if err := server.listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("serving loop did not stop after listener failure")
	}
	if server.beginRequest() {
		server.requests.Done()
		t.Fatal("automatic shutdown continued accepting requests")
	}
	if err := server.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if server.staticRoot != nil {
		t.Fatal("static root remained open after automatic shutdown")
	}
	for _, value := range server.sessionKey {
		if value != 0 {
			t.Fatal("session key was not cleared after automatic shutdown")
		}
	}
}

func TestContextCancellationStopsBridge(t *testing.T) {
	upstream := newTestUpstream(t)
	ctx, cancel := context.WithCancel(context.Background())
	server, err := Start(ctx, Config{
		Addr:           "127.0.0.1:0",
		ControlAddr:    upstream.listener.Addr().String(),
		TokenPath:      upstream.tokenPath,
		CredentialPath: upstream.webPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-server.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not stop bridge")
	}
	if err := server.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertTokenAbsent(t *testing.T, token string, result bridgeResult) {
	t.Helper()
	if bytes.Contains(result.body, []byte(token)) || strings.Contains(fmt.Sprint(result.header), token) {
		t.Fatal("daemon control token was exposed to browser")
	}
	for _, name := range []string{
		"Authorization",
		controlapi.ChallengeHeader,
		controlapi.ChallengeEpochHeader,
		controlapi.ChallengeExpiryHeader,
		controlapi.ChallengeProofHeader,
		controlapi.ResponseAuthHeader,
	} {
		if result.header.Get(name) != "" {
			t.Fatalf("control authentication header %s was exposed", name)
		}
	}
}

func TestConcurrentSessionValidation(t *testing.T) {
	upstream := newTestUpstream(t)
	server := startTestBridge(t, upstream)
	session, _ := bootstrapSession(t, server)
	transport := &http.Transport{}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer transport.CloseIdleConnections()

	const callers = 24
	var wait sync.WaitGroup
	wait.Add(callers)
	errorsFound := make(chan error, callers)
	for range callers {
		go func() {
			defer wait.Done()
			request, err := http.NewRequest(http.MethodGet, "http://"+server.Addr()+controlapi.StatusPath, nil)
			if err != nil {
				errorsFound <- err
				return
			}
			request.Host = server.Addr()
			request.Header.Set("Origin", "http://"+server.Addr())
			request.SetBasicAuth(BasicUsername, upstream.webToken)
			request.AddCookie(session.cookie)
			response, err := client.Do(request)
			if err != nil {
				errorsFound <- err
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK || response.Header.Get(CSRFHeader) != session.csrf {
				errorsFound <- fmt.Errorf("status=%d csrf=%q", response.StatusCode, response.Header.Get(CSRFHeader))
			}
		}()
	}
	wait.Wait()
	transport.CloseIdleConnections()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func newTestCredentialPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web-token")
	if _, err := controlapi.CreateToken(path); err != nil {
		t.Fatal(err)
	}
	return path
}
