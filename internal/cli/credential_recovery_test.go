package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/maintenancelease"
	"github.com/FrankoonG/x-tier/internal/route"
	"github.com/FrankoonG/x-tier/internal/statestore"
	"github.com/FrankoonG/x-tier/internal/xraycredential"
)

func TestCredentialRecoveryCLIRequiresLocalOfflineBoundary(t *testing.T) {
	configPath, managementPath, _ := seedCLIMissingCredentialLedgerRecovery(t, true)
	command := []string{
		"--config", configPath,
		"--json",
		"--dry-run",
		"local", "config", "recover-missing-credential-ledger",
		"--management-credential-file", managementPath,
	}
	if code, output := runCLI(t, command...); code == 0 ||
		jsonField(t, output, "error_code") != "config.credential_recovery_requires_offline" {
		t.Fatalf("online code=%d output=%s", code, output)
	}
	command = append([]string{"--offline"}, command...)
	if code, output := runDaemonCLI(t, command...); code == 0 ||
		jsonField(t, output, "error_code") != "config.credential_recovery_local_only" {
		t.Fatalf("daemon code=%d output=%s", code, output)
	}
	if !CommandMutates([]string{"local", "config", "recover-missing-credential-ledger"}) {
		t.Fatal("credential recovery was not classified as a mutation")
	}
}

func TestCredentialRecoveryCLIDryRunAndApply(t *testing.T) {
	configPath, managementPath, rawCredential := seedCLIMissingCredentialLedgerRecovery(t, true)
	base := []string{
		"--offline", "--config", configPath, "--json",
		"local", "config", "recover-missing-credential-ledger",
		"--management-credential-file", managementPath,
	}
	dryArgs := append([]string{"--offline", "--config", configPath, "--json", "--dry-run"}, base[4:]...)
	code, output := runCLI(t, dryArgs...)
	if code != 0 {
		t.Fatalf("dry-run code=%d output=%s", code, output)
	}
	if strings.Contains(output, rawCredential) {
		t.Fatal("dry-run output exposed the raw VLESS credential")
	}
	confirmation := decodeCLIRecoveryConfirmation(t, output)
	if confirmation.CredentialEvidenceUnverified || confirmation.Mode != configstore.CredentialRecoveryModeProtectedSourceUnion {
		t.Fatalf("confirmation=%+v", confirmation)
	}
	confirmationPath := writeProtectedCLIJSON(t, confirmation)
	applyArgs := append(append([]string(nil), base...), "--confirmation-file", confirmationPath)
	code, output = runCLI(t, applyArgs...)
	if code != 0 {
		t.Fatalf("apply code=%d output=%s", code, output)
	}
	response := decodeJSON(t, output)
	if response["ok"] != true || response["dry_run"] != false {
		t.Fatalf("apply response=%#v", response)
	}
	store := openCLIStore(t, configPath)
	recovered, err := configstore.LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Peers[0].Enabled || !configstore.IsPeerCredentialQuarantined(recovered.Peers[0]) || recovered.NodeInbound[0].Enabled {
		t.Fatalf("recovered peer=%+v inbound=%+v", recovered.Peers[0], recovered.NodeInbound[0])
	}
	if records, err := store.CredentialRecoveryRecordNames(); err != nil || len(records) != 3 {
		t.Fatalf("records=%v err=%v", records, err)
	}
}

func TestCredentialRecoveryCLIUnverifiedRequiresExactAcceptance(t *testing.T) {
	configPath, managementPath, _ := seedCLIMissingCredentialLedgerRecovery(t, false)
	dryArgs := []string{
		"--offline", "--config", configPath, "--json", "--dry-run",
		"local", "config", "recover-missing-credential-ledger",
		"--management-credential-file", managementPath,
	}
	code, output := runCLI(t, dryArgs...)
	if code != 0 {
		t.Fatalf("dry-run code=%d output=%s", code, output)
	}
	confirmation := decodeCLIRecoveryConfirmation(t, output)
	if !confirmation.CredentialEvidenceUnverified {
		t.Fatalf("confirmation=%+v", confirmation)
	}
	confirmationPath := writeProtectedCLIJSON(t, confirmation)
	applyArgs := []string{
		"--offline", "--config", configPath, "--json",
		"local", "config", "recover-missing-credential-ledger",
		"--management-credential-file", managementPath,
		"--confirmation-file", confirmationPath,
	}
	if code, output = runCLI(t, applyArgs...); code == 0 ||
		jsonField(t, output, "error_code") != "config.credential_recovery_acceptance_required" {
		t.Fatalf("unaccepted code=%d output=%s", code, output)
	}
	applyArgs = append(applyArgs, "--accept-missing-credential-evidence")
	if code, output = runCLI(t, applyArgs...); code != 0 {
		t.Fatalf("accepted code=%d output=%s", code, output)
	}
	store := openCLIStore(t, configPath)
	recovered, err := configstore.LoadStoreExisting(store)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Peers[0].Enabled || recovered.Peers[0].DisabledCause != configstore.PeerCredentialEvidenceUnverifiedCause {
		t.Fatalf("peer=%+v", recovered.Peers[0])
	}
}

func TestCredentialRecoveryCLIRejectsWrongAuthAndHeldDaemonLease(t *testing.T) {
	configPath, managementPath, _ := seedCLIMissingCredentialLedgerRecovery(t, true)
	args := func(path string) []string {
		return []string{
			"--offline", "--config", configPath, "--json", "--dry-run",
			"local", "config", "recover-missing-credential-ledger",
			"--management-credential-file", path,
		}
	}
	wrongCredentialPath := filepath.Join(t.TempDir(), "wrong-credential.token")
	if _, err := controlapi.CreateToken(wrongCredentialPath); err != nil {
		t.Fatal(err)
	}
	if code, output := runCLI(t, args(wrongCredentialPath)...); code == 0 ||
		jsonField(t, output, "error_code") != "config.credential_recovery_auth_failed" {
		t.Fatalf("wrong auth code=%d output=%s", code, output)
	}
	lease, err := maintenancelease.Acquire(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if code, output := runCLI(t, args(managementPath)...); code == 0 ||
		jsonField(t, output, "error_code") != "config.credential_recovery_daemon_running" {
		t.Fatalf("held lease code=%d output=%s", code, output)
	}
}

func TestCredentialRecoveryCLIRejectsInsecureOrMalformedConfirmation(t *testing.T) {
	configPath, managementPath, _ := seedCLIMissingCredentialLedgerRecovery(t, true)
	insecure := filepath.Join(t.TempDir(), "confirmation.json")
	if err := os.WriteFile(insecure, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(insecure, 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--offline", "--config", configPath, "--json",
		"local", "config", "recover-missing-credential-ledger",
		"--management-credential-file", managementPath,
		"--confirmation-file", insecure,
	}
	if code, output := runCLI(t, args...); code == 0 ||
		jsonField(t, output, "error_code") != "config.credential_recovery_confirmation_invalid" {
		t.Fatalf("insecure confirmation code=%d output=%s", code, output)
	}
	malformed := writeProtectedCLIBytes(t, []byte("{} {}\n"))
	args[len(args)-1] = malformed
	if code, output := runCLI(t, args...); code == 0 ||
		jsonField(t, output, "error_code") != "config.credential_recovery_confirmation_invalid" {
		t.Fatalf("malformed confirmation code=%d output=%s", code, output)
	}
}

func seedCLIMissingCredentialLedgerRecovery(t *testing.T, withEvidence bool) (string, string, string) {
	t.Helper()
	configPath, err := filepath.Abs(filepath.Join(t.TempDir(), "xtier.json"))
	if err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Clean(configPath)
	store, err := statestore.Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rawCredential := "66ad4540-b58c-4ad2-9926-ea63445a9b57"
	cfg := configstore.DefaultConfig()
	cfg.Revision = 4
	cfg.XrayProfiles["vless"] = configstore.XrayProfile{ID: "vless", Kind: "vless", VLESS: &configstore.VLESSProfile{
		UUID: rawCredential, Transport: "tcp", Security: "none", AllowInsecurePlaintext: true,
	}}
	cfg.XrayProfiles["socks"] = configstore.XrayProfile{ID: "socks", Kind: "socks", SOCKS: &configstore.SOCKSProfile{
		Username: "terminal", Password: "test-entry-secret",
	}}
	cfg.Peers = []configstore.PeerConfig{{
		Name: "B", NodeID: "node-b", Direction: route.DirectionOutbound,
		GatewayAddr: "127.0.0.1:2443", XrayProfileID: "vless", Enabled: true,
	}}
	cfg.NodeInbound = []configstore.InboundConfig{{
		Kind: "socks", Purpose: "user", Listen: "127.0.0.1:1080", Enabled: true,
		XrayProfileID: "socks", ExitPeer: "B",
	}}
	if withEvidence {
		fingerprint, err := xraycredential.VLESSFingerprint(rawCredential)
		if err != nil {
			t.Fatal(err)
		}
		cfg.PeerCredentialQuarantines = []configstore.PeerCredentialQuarantine{{
			CredentialFingerprint: fingerprint,
			PeerNodeIDs:           []string{"node-b"},
			Reason:                configstore.PeerCredentialCollisionReason,
		}}
		cfg.Peers[0].Enabled = false
		cfg.Peers[0].DisabledCause = configstore.PeerCredentialQuarantineCause
		cfg.NodeInbound[0].Enabled = false
		cfg.NodeInbound[0].DisabledCause = configstore.PeerCredentialQuarantineInboundCause
	}
	if err := configstore.SaveStore(store, cfg); err != nil {
		t.Fatal(err)
	}
	if err := configstore.SaveStoreLastKnownGood(store, cfg); err != nil {
		t.Fatal(err)
	}
	if withEvidence {
		active := cfg
		active.Revision--
		active.PeerCredentialQuarantines = nil
		active.Peers = append([]configstore.PeerConfig(nil), cfg.Peers...)
		active.Peers[0].Enabled = true
		active.Peers[0].DisabledCause = ""
		active.NodeInbound = append([]configstore.InboundConfig(nil), cfg.NodeInbound...)
		active.NodeInbound[0].Enabled = true
		active.NodeInbound[0].DisabledCause = ""
		payload, err := configstore.EncodeDocument(active)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceConfig(payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := controlapi.CreateStoreToken(store, statestore.ControlToken); err != nil {
		t.Fatal(err)
	}
	managementPath, err := store.DiagnosticPath(statestore.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(statestore.PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	if !withEvidence {
		if err := store.ReplaceConfig([]byte("{\n")); err != nil {
			t.Fatal(err)
		}
	}
	return configPath, managementPath, rawCredential
}

func decodeCLIRecoveryConfirmation(t *testing.T, output string) configstore.MissingCredentialLedgerRecoveryConfirmation {
	t.Helper()
	var envelope struct {
		Recovery struct {
			Confirmation configstore.MissingCredentialLedgerRecoveryConfirmation `json:"confirmation"`
		} `json:"recovery"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Recovery.Confirmation
}

func writeProtectedCLIJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return writeProtectedCLIBytes(t, append(payload, '\n'))
}

func writeProtectedCLIBytes(t *testing.T, payload []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "protected.json")
	if err := configstore.Save(path, configstore.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
