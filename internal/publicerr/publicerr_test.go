package publicerr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestTypedWrappedCodeIsRetainedWithoutSensitiveDetails(t *testing.T) {
	const secret = "c0dec0de-f00d-4bee-8bad-0123456789ab"
	err := fmt.Errorf("dataplane: compile: %w", Errorf("config.profile_invalid", "rejected %s", secret))
	if code := Code(err, "runtime.apply_failed"); code != "config.profile_invalid" {
		t.Fatalf("code = %q", code)
	}
	message := Message(err, "runtime.apply_failed")
	if strings.Contains(message, secret) || !strings.Contains(message, "credential details were redacted") {
		t.Fatalf("unsafe message = %q", message)
	}
}

func TestDiagnosticTextCannotForgeSemanticCode(t *testing.T) {
	err := errors.New(`open C:\config.commit_visible_and_resynced: access denied`)
	if code := Code(err, "domain.failed"); code != "domain.failed" {
		t.Fatalf("code = %q", code)
	}
}

func TestUnknownTextFallsBackWithoutEcho(t *testing.T) {
	const secret = "literal-password-without-a-marker"
	message := Message(errors.New("upstream rejected "+secret), "service.reload_failed")
	if message != "operation failed (service.reload_failed)" || strings.Contains(message, secret) {
		t.Fatalf("message = %q", message)
	}
}

func TestUntrustedCodeNamespaceFallsBack(t *testing.T) {
	if code := CodeText("attacker.value: detail", "domain.failed"); code != "domain.failed" {
		t.Fatalf("code = %q", code)
	}
}

func TestLastKnownGoodNamespaceIsRetained(t *testing.T) {
	err := Errorf("lastgood.revision_ahead_of_applied", "checkpoint is newer")
	if got := Code(err, "operation.failed"); got != "lastgood.revision_ahead_of_applied" {
		t.Fatalf("Code=%q", got)
	}
}

func TestRouteResolverNamespacesAreRetained(t *testing.T) {
	for _, code := range []string{"path.edge_disabled", "topology.local_missing"} {
		if got := NormalizeCode(code, "route.compile_failed"); got != code {
			t.Fatalf("NormalizeCode(%q)=%q", code, got)
		}
	}
}
