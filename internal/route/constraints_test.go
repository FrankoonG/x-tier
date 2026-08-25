package route

import (
	"encoding/json"
	"strings"
	"testing"
)

func testTransitScope() TransitReleaseScope {
	return TransitReleaseScope{
		Stream: []TransitExecutionCandidate{
			{Mode: TransitModeForward, Backend: TransitBackendKernelNftables},
			{Mode: TransitModeForward, Backend: TransitBackendUserspaceStream},
			{Mode: TransitModeRelay, Backend: TransitBackendProtocol},
		},
		Packet: []TransitExecutionCandidate{
			{Mode: TransitModeForward, Backend: TransitBackendKernelNftables},
			{Mode: TransitModeForward, Backend: TransitBackendUserspacePacket},
			{Mode: TransitModeForward, Backend: TransitBackendRoutedUnderlay},
			{Mode: TransitModeRelay, Backend: TransitBackendProtocol},
		},
	}
}

func TestTransitNormalizeOmittedEqualsAuto(t *testing.T) {
	omitted, err := (TransitConstraint{}).Normalize(SessionKindStream, testTransitScope())
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := (TransitConstraint{RequestedPolicy: TransitPolicyAuto}).Normalize(SessionKindStream, testTransitScope())
	if err != nil {
		t.Fatal(err)
	}
	if omitted.RequestedPolicy != TransitPolicyAuto {
		t.Fatalf("policy = %q", omitted.RequestedPolicy)
	}
	if got, want := len(omitted.ExecutionPreference), len(explicit.ExecutionPreference); got != want {
		t.Fatalf("omitted candidates = %d, explicit = %d", got, want)
	}
	for i := range omitted.ExecutionPreference {
		if omitted.ExecutionPreference[i] != explicit.ExecutionPreference[i] {
			t.Fatalf("candidate %d differs: %+v != %+v", i, omitted.ExecutionPreference[i], explicit.ExecutionPreference[i])
		}
	}
}

func TestTransitNormalizePolicyDoesNotCrossFallbackBoundary(t *testing.T) {
	forward, err := (TransitConstraint{RequestedPolicy: TransitPolicyForward}).Normalize(SessionKindStream, testTransitScope())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range forward.ExecutionPreference {
		if candidate.Mode != TransitModeForward {
			t.Fatalf("forward policy included %+v", candidate)
		}
	}
	relay, err := (TransitConstraint{RequestedPolicy: TransitPolicyRelay}).Normalize(SessionKindStream, testTransitScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(relay.ExecutionPreference) != 1 || relay.ExecutionPreference[0] != (TransitExecutionCandidate{Mode: TransitModeRelay, Backend: TransitBackendProtocol}) {
		t.Fatalf("relay preference = %+v", relay.ExecutionPreference)
	}
}

func TestTransitNormalizeOrderedRequiresFrozenScope(t *testing.T) {
	c := TransitConstraint{
		RequestedPolicy: TransitPolicyOrdered,
		ExecutionPreference: []TransitExecutionCandidate{
			{Mode: TransitModeForward, Backend: TransitBackendUserspacePacket},
		},
	}
	_, err := c.Normalize(SessionKindStream, testTransitScope())
	if err == nil || !strings.Contains(err.Error(), "route.transit_not_in_release_scope") {
		t.Fatalf("error = %v", err)
	}
}

func TestTransitNormalizeRejectsEmptyReleaseScope(t *testing.T) {
	_, err := (TransitConstraint{}).Normalize(SessionKindStream, TransitReleaseScope{})
	if err == nil || !strings.Contains(err.Error(), "route.transit_release_scope_empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestTransitCandidateRejectsModeBackendConfusion(t *testing.T) {
	candidate := TransitExecutionCandidate{Mode: TransitModeForward, Backend: TransitBackendProtocol}
	if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "route.transit_backend_mode_mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompiledRouteJSONUsesStableSnakeCase(t *testing.T) {
	data, err := json.Marshal(CompiledRoute{Leaves: []RouteLeafDescriptor{{
		ID:                        "lane-1",
		Generation:                7,
		ExpectedRuntimeInstanceID: "runtime-1",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"\"resolved_paths\"", "\"expected_runtime_instance_id\"", "\"generation\":7"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("JSON %s does not contain %s", text, expected)
		}
	}
	if strings.Contains(text, "ExpectedRuntimeInstanceID") {
		t.Fatalf("Go field leaked into JSON: %s", text)
	}
}
