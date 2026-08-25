package controlapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

func TestCreateTokenPersistsAndDoesNotRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	first, err := CreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("CreateToken rotated an existing daemon token")
	}
	loaded, err := ReadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != first {
		t.Fatal("ReadToken returned different token")
	}
}

func TestReadTokenRejectsMalformedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(path); err == nil {
		t.Fatal("malformed token was accepted")
	}
}

func TestStoreTokensPersistWithoutSharingObjects(t *testing.T) {
	store, err := statestore.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	control, err := CreateStoreToken(store, statestore.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	again, err := CreateStoreToken(store, statestore.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	web, err := CreateStoreToken(store, statestore.WebToken)
	if err != nil {
		t.Fatal(err)
	}
	if control != again {
		t.Fatal("store token rotated")
	}
	if control == web {
		t.Fatal("control and Web token objects share credentials")
	}
	if loaded, err := ReadStoreToken(store, statestore.ControlToken); err != nil || loaded != control {
		t.Fatalf("ReadStoreToken() = %q, %v", loaded, err)
	}
}

func TestExplicitTokenRejectsWhitespace(t *testing.T) {
	token := strings.Repeat("a", 64)
	if err := validateToken(token); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{" " + token, token + "\n", ""} {
		if err := validateToken(malformed); err == nil {
			t.Fatalf("explicit token %q was accepted", malformed)
		}
	}
}

func TestRequestIDsAreUniqueAndCanonical(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for range 128 {
		id, err := NewRequestID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 32 {
			t.Fatalf("request id length=%d", len(id))
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate request id %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestControlClientRejectsNonLoopbackBeforeReadingToken(t *testing.T) {
	missingToken := filepath.Join(t.TempDir(), "missing-token")
	for _, addr := range []string{
		"192.0.2.1:19090",
		"http://192.0.2.1:19090",
		"https://127.0.0.1:19090",
		"localhost:19090",
		"127.0.0.1",
		"http://127.0.0.1:19090/extra",
	} {
		if _, err := Execute(addr, missingToken, Request{}); err == nil || strings.Contains(err.Error(), "token_unavailable") {
			t.Fatalf("Execute(%q) error = %v; address must be rejected before token access", addr, err)
		}
		if _, err := GetStatusContext(context.Background(), addr, missingToken); err == nil || strings.Contains(err.Error(), "token_unavailable") {
			t.Fatalf("GetStatusContext(%q) error = %v; address must be rejected before token access", addr, err)
		}
	}
}

func TestLocalControlURLAcceptsIPv4AndIPv6Loopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:19090", "http://127.0.0.1:19090", "[::1]:19090"} {
		if _, err := localControlURL(addr); err != nil {
			t.Fatalf("localControlURL(%q): %v", addr, err)
		}
	}
}

func TestMutationBudgetsAreStrictlyLayered(t *testing.T) {
	budgets := []struct {
		name  string
		value time.Duration
	}{
		{"read request", ReadRequestBudget},
		{"mutation execution", MutationExecutionBudget},
		{"control server write", ControlServerWriteBudget},
		{"WebBridge mutation", WebBridgeMutationBudget},
		{"WebBridge write", WebBridgeWriteBudget},
		{"mutation client", MutationClientBudget},
	}
	for i := 1; i < len(budgets); i++ {
		if budgets[i-1].value >= budgets[i].value {
			t.Fatalf("%s budget %s must be shorter than %s budget %s", budgets[i-1].name, budgets[i-1].value, budgets[i].name, budgets[i].value)
		}
	}
}

func TestLocalHTTPClientDelegatesDeadlineToRequestContext(t *testing.T) {
	client := localHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("client timeout=%s, want caller-context authority", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("response header timeout=%s, want caller-context authority", transport.ResponseHeaderTimeout)
	}
}

func TestCommandResponseMutationOutcomeValidation(t *testing.T) {
	valid := []string{
		`{"exit_code":0,"applied":false}`,
		`{"exit_code":0,"applied":true,"outcome":"applied"}`,
		`{"exit_code":1,"applied":false,"outcome":"not_applied"}`,
		`{"exit_code":3,"applied":false,"outcome":"indeterminate"}`,
	}
	for _, raw := range valid {
		var response Response
		if err := decodeStrictResponse([]byte(raw), &response); err != nil {
			t.Fatalf("valid response %s: %v", raw, err)
		}
	}
	invalid := []string{
		`{"exit_code":0,"applied":true}`,
		`{"exit_code":0,"applied":false,"outcome":"applied"}`,
		`{"exit_code":1,"applied":true,"outcome":"applied"}`,
		`{"exit_code":1,"applied":true,"outcome":"not_applied"}`,
		`{"exit_code":0,"applied":false,"outcome":"not_applied"}`,
		`{"exit_code":3,"applied":true,"outcome":"indeterminate"}`,
		`{"exit_code":1,"applied":false,"outcome":"indeterminate"}`,
		`{"exit_code":1,"applied":false,"outcome":"unknown"}`,
	}
	for _, raw := range invalid {
		var response Response
		if err := decodeStrictResponse([]byte(raw), &response); err == nil {
			t.Fatalf("invalid response accepted: %s", raw)
		}
	}
}

func TestCommandHTTPStatusDeliveryClassification(t *testing.T) {
	definiteRejections := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusMethodNotAllowed,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusServiceUnavailable,
	}
	for _, status := range definiteRejections {
		if commandHTTPStatusMayHaveExecuted(status) {
			t.Fatalf("pre-execution status %d is ambiguous", status)
		}
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusTeapot} {
		if !commandHTTPStatusMayHaveExecuted(status) {
			t.Fatalf("unexpected post-dispatch status %d was treated as not delivered", status)
		}
	}
}
