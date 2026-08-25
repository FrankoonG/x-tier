package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestTrackedAuthenticatedRequestRequiresWriteEvidence(t *testing.T) {
	transportFailure := errors.New("injected transport failure")
	for _, test := range []struct {
		name     string
		wrote    bool
		mayApply bool
	}{
		{name: "failed before write"},
		{name: "failed after write", wrote: true, mayApply: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if test.wrote {
					trace := httptrace.ContextClientTrace(request.Context())
					if trace == nil || trace.WroteRequest == nil {
						t.Fatal("authenticated request did not install a write trace")
					}
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				return nil, transportFailure
			})}
			request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/command", strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = doTrackedAuthenticatedRequest(client, request)
			if err == nil || !errors.Is(err, transportFailure) {
				t.Fatalf("request error=%v", err)
			}
			if got := CommandMayHaveApplied(err); got != test.mayApply {
				t.Fatalf("CommandMayHaveApplied=%v, want %v: %v", got, test.mayApply, err)
			}
		})
	}
}

func TestTrackedAuthenticatedRequestUsesRealTransportBoundary(t *testing.T) {
	t.Run("dial failure is definitely pre-delivery", func(t *testing.T) {
		dialFailure := errors.New("injected dial failure")
		client := &http.Client{Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return nil, dialFailure
			},
		}}
		request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/v1/command", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = doTrackedAuthenticatedRequest(client, request)
		if err == nil || !errors.Is(err, dialFailure) || CommandMayHaveApplied(err) {
			t.Fatalf("dial failure classification=%v may_apply=%t", err, CommandMayHaveApplied(err))
		}
	})

	t.Run("server consumed request before response loss", func(t *testing.T) {
		received := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
			}
			received <- string(body)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack response: %v", err)
				return
			}
			_ = conn.Close()
		}))
		defer server.Close()

		request, err := http.NewRequest(http.MethodPost, server.URL+CommandPath, strings.NewReader(`{"request_id":"real-transport"}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = doTrackedAuthenticatedRequest(server.Client(), request)
		if err == nil || !CommandMayHaveApplied(err) {
			t.Fatalf("post-write failure classification=%v may_apply=%t", err, CommandMayHaveApplied(err))
		}
		select {
		case body := <-received:
			if body != `{"request_id":"real-transport"}` {
				t.Fatalf("received body=%q", body)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("server did not consume request")
		}
	})
}

func TestAuthenticatedRequestNeverSendsPersistentToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "control.token")
	token, err := CreateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("12", 32)
	challenge, err := SignChallenge(token, nonce, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	type capturedRequest struct {
		method string
		path   string
		header http.Header
		body   []byte
	}
	var captureMu sync.Mutex
	var captured []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captureMu.Lock()
		captured = append(captured, capturedRequest{r.Method, r.URL.Path, r.Header.Clone(), body})
		captureMu.Unlock()
		if r.URL.Path == ChallengePath {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(challenge)
			return
		}
		if !VerifyRequestAuthentication(r, token, challenge, body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		responseBody := []byte(`{"exit_code":0}`)
		if err := SignResponse(w.Header(), token, r, body, http.StatusOK, responseBody); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	body := []byte(`{"request_id":"00000000000000000000000000000001"}`)
	status, _, err := AuthenticatedRequestContext(context.Background(), server.URL, tokenPath, http.MethodPost, CommandPath, body)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured requests=%d, want 2", len(captured))
	}
	for _, request := range captured {
		var headers strings.Builder
		if err := request.header.Write(&headers); err != nil {
			t.Fatal(err)
		}
		wireView := request.method + " " + request.path + "\n" + headers.String() + string(request.body)
		if strings.Contains(wireView, token) {
			t.Fatalf("persistent token appeared on wire for %s", request.path)
		}
		if strings.HasPrefix(request.header.Get("Authorization"), "Bearer ") {
			t.Fatalf("bearer authorization used for %s", request.path)
		}
	}
	if got := captured[0].header.Get("Authorization"); got != "" {
		t.Fatalf("challenge request carried authorization: %q", got)
	}
	if got := captured[1].header.Get("Authorization"); !strings.HasPrefix(got, AuthScheme+" ") {
		t.Fatalf("command authorization=%q", got)
	}
}

func TestClientRejectsFakeChallengeBeforeSendingCommandProof(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "control.token")
	if _, err := CreateToken(tokenPath); err != nil {
		t.Fatal(err)
	}
	fakeToken := strings.Repeat("ab", 32)
	fakeChallenge, err := SignChallenge(fakeToken, strings.Repeat("34", 32), time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var commandHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ChallengePath {
			_ = json.NewEncoder(w).Encode(fakeChallenge)
			return
		}
		commandHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, _, err = AuthenticatedRequestContext(context.Background(), server.URL, tokenPath, http.MethodPost, CommandPath, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "challenge_auth_invalid") {
		t.Fatalf("error=%v", err)
	}
	if hits := commandHits.Load(); hits != 0 {
		t.Fatalf("client sent proof to fake server %d times", hits)
	}
}

func TestClientRejectsTamperedResponse(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "control.token")
	token, err := CreateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("56", 32)
	challenge, err := SignChallenge(token, nonce, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ChallengePath {
			_ = json.NewEncoder(w).Encode(challenge)
			return
		}
		signedBody := []byte(`{"exit_code":0}`)
		if err := SignResponse(w.Header(), token, r, []byte(`{}`), http.StatusOK, signedBody); err != nil {
			t.Error(err)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"exit_code":1}`))
	}))
	defer server.Close()

	_, _, err = AuthenticatedRequestContext(context.Background(), server.URL, tokenPath, http.MethodPost, CommandPath, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "response_auth_invalid") {
		t.Fatalf("error=%v", err)
	}
}

func TestAuthenticatedRequestReadHonorsShortCallerDeadline(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "control.token")
	token, err := CreateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := SignChallenge(token, strings.Repeat("67", 32), time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ChallengePath {
			_ = json.NewEncoder(w).Encode(challenge)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !VerifyRequestAuthentication(r, token, challenge, body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		responseBody := []byte(`{"ok":true}`)
		if err := SignResponse(w.Header(), token, r, body, http.StatusOK, responseBody); err != nil {
			t.Error(err)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err = AuthenticatedRequestContext(ctx, server.URL, tokenPath, http.MethodGet, StatusPath, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read error=%v, want caller deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("short read budget returned after %s", elapsed)
	}
}

func TestRequestAuthenticationBindsMethodPathAndBody(t *testing.T) {
	token := strings.Repeat("78", 32)
	challenge, err := SignChallenge(token, strings.Repeat("9a", 32), time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"value":"original"}`)
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:19090"+CommandPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthenticateRequest(request, token, challenge, body); err != nil {
		t.Fatal(err)
	}
	if !VerifyRequestAuthentication(request, token, challenge, body) {
		t.Fatal("valid request authentication rejected")
	}
	if VerifyRequestAuthentication(request, token, challenge, []byte(`{"value":"tampered"}`)) {
		t.Fatal("body tamper was accepted")
	}
	request.Method = http.MethodGet
	if VerifyRequestAuthentication(request, token, challenge, body) {
		t.Fatal("method tamper was accepted")
	}
	request.Method = http.MethodPost
	request.URL.Path = StatusPath
	if VerifyRequestAuthentication(request, token, challenge, body) {
		t.Fatal("path tamper was accepted")
	}
	responseBody := []byte(`{"ok":true}`)
	header := make(http.Header)
	request.Method = http.MethodPost
	request.URL.Path = CommandPath
	if err := SignResponse(header, token, request, body, http.StatusOK, responseBody); err != nil {
		t.Fatal(err)
	}
	if VerifyResponseAuthentication(header, token, request, body, http.StatusCreated, responseBody) {
		t.Fatal("response status tamper was accepted")
	}
	otherRequest, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:19090"+StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthenticateRequest(otherRequest, token, challenge, nil); err != nil {
		t.Fatal(err)
	}
	if VerifyResponseAuthentication(header, token, otherRequest, nil, http.StatusOK, responseBody) {
		t.Fatal("cross-request response substitution was accepted")
	}
}

func TestClientRejectsSignedResponseSubstitutedAcrossRequests(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "control.token")
	token, err := CreateToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := SignChallenge(token, strings.Repeat("cd", 32), time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	statusBody := []byte(`{"api_version":"v1","state":"running"}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ChallengePath {
			_ = json.NewEncoder(w).Encode(challenge)
			return
		}
		statusRequest, err := http.NewRequest(http.MethodGet, "http://127.0.0.1"+StatusPath, nil)
		if err != nil {
			t.Error(err)
			return
		}
		if err := AuthenticateRequest(statusRequest, token, challenge, nil); err != nil {
			t.Error(err)
			return
		}
		if err := SignResponse(w.Header(), token, statusRequest, nil, http.StatusOK, statusBody); err != nil {
			t.Error(err)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(statusBody)
	}))
	defer server.Close()

	_, _, err = AuthenticatedRequestContext(context.Background(), server.URL, tokenPath, http.MethodPost, CommandPath, []byte(`{"request_id":"00000000000000000000000000000001"}`))
	if err == nil || !strings.Contains(err.Error(), "response_auth_invalid") {
		t.Fatalf("cross-request substituted response error = %v", err)
	}
}

func TestStrictCommandResponseRequiresExitCodeAndRejectsUnknownFields(t *testing.T) {
	for name, body := range map[string][]byte{
		"missing": []byte(`{"stdout":"ok"}`),
		"unknown": []byte(`{"exit_code":0,"extra":true}`),
	} {
		t.Run(name, func(t *testing.T) {
			var response Response
			if err := decodeStrictResponse(body, &response); err == nil {
				t.Fatal("malformed command response was accepted")
			}
		})
	}
}
