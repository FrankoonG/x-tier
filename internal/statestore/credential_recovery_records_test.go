package statestore

import (
	"bytes"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCredentialRecoveryRecordsCreateListAndRead(t *testing.T) {
	store := openTestStore(t, filepath.Join(canonicalTempDir(t), "config.json"))
	defer store.Close()

	forensicPayload := []byte("forensic payload")
	forensicName, err := store.CreateCredentialRecoveryForensic(forensicPayload)
	if err != nil {
		t.Fatal(err)
	}
	auditPayloads := [][]byte{[]byte("first audit"), []byte("second audit")}
	auditNames := make([]string, 0, len(auditPayloads))
	for _, payload := range auditPayloads {
		name, err := store.CreateCredentialRecoveryAudit(payload)
		if err != nil {
			t.Fatal(err)
		}
		auditNames = append(auditNames, name)
	}

	if !validCredentialRecoveryRecordName(credentialRecoveryForensicKind, forensicName) {
		t.Fatalf("generated forensic name is invalid: %q", forensicName)
	}
	listedNames, err := store.CredentialRecoveryRecordNames()
	if err != nil {
		t.Fatal(err)
	}
	wantNames := append([]string{forensicName}, auditNames...)
	sort.Strings(wantNames)
	if !equalStrings(listedNames, wantNames) {
		t.Fatalf("record names=%v, want %v", listedNames, wantNames)
	}

	forensic, err := store.ReadCredentialRecoveryRecord(forensicName, int64(len(forensicPayload)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(forensic, forensicPayload) {
		t.Fatalf("forensic payload=%q, want %q", forensic, forensicPayload)
	}
	for index, name := range auditNames {
		payload, err := store.ReadCredentialRecoveryRecord(name, int64(len(auditPayloads[index])))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, auditPayloads[index]) {
			t.Fatalf("audit payload=%q, want %q", payload, auditPayloads[index])
		}
	}
}

func TestCredentialRecoveryReadEnforcesKindNameAndCallerLimit(t *testing.T) {
	store := openTestStore(t, filepath.Join(canonicalTempDir(t), "config.json"))
	defer store.Close()

	payload := []byte("immutable evidence")
	name, err := store.CreateCredentialRecoveryForensic(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadCredentialRecoveryRecord(name, int64(len(payload)-1)); err == nil || !strings.Contains(err.Error(), "statestore.object_too_large") {
		t.Fatalf("undersized read limit error=%v, want object_too_large", err)
	}
	if _, err := store.ReadCredentialRecoveryRecord(name, 0); err == nil || !strings.Contains(err.Error(), "statestore.read_limit_invalid") {
		t.Fatalf("zero read limit error=%v, want read_limit_invalid", err)
	}

	for _, invalid := range []string{
		"../" + name,
		"..\\" + name,
		filepath.Join(filepath.VolumeName(filepath.Clean(string(filepath.Separator)+"outside")), "outside"),
		"credential-recovery.forensic.20260903T120000.000000000.00000000000000000000000000000000.json/child",
		"credential-recovery.forensic.20260903T120000.000000000.0000000000000000000000000000000G.json",
		"credential-recovery.forensic.20261303T120000.000000000.00000000000000000000000000000000.json",
		"credential-recovery.unknown.20260903T120000.000000000.00000000000000000000000000000000.json",
		"backup.20260903T120000.000000000.token.json",
	} {
		if _, err := store.ReadCredentialRecoveryRecord(invalid, 1024); err == nil || !strings.Contains(err.Error(), "statestore.credential_recovery_name_invalid") {
			t.Errorf("invalid name %q error=%v", invalid, err)
		}
	}
}

func TestCredentialRecoveryRecordsAreExcludedFromBackupsAndPruning(t *testing.T) {
	store := openTestStore(t, filepath.Join(canonicalTempDir(t), "config.json"))
	defer store.Close()

	forensicName, err := store.CreateCredentialRecoveryForensic([]byte("forensic"))
	if err != nil {
		t.Fatal(err)
	}
	auditName, err := store.CreateCredentialRecoveryAudit([]byte("audit"))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := store.CreateBackup([]byte("backup")); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := store.BackupNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 3 {
		t.Fatalf("backup names=%v, want three backup-only names", backups)
	}
	for _, name := range backups {
		if name == forensicName || name == auditName {
			t.Fatalf("credential recovery record leaked into backups: %q", name)
		}
	}
	if err := store.PruneBackups(0); err != nil {
		t.Fatal(err)
	}
	if backups, err = store.BackupNames(); err != nil || len(backups) != 0 {
		t.Fatalf("backups after prune=%v err=%v", backups, err)
	}
	if payload, err := store.ReadCredentialRecoveryRecord(forensicName, 64); err != nil || string(payload) != "forensic" {
		t.Fatalf("forensic after backup prune=%q err=%v", payload, err)
	}
	if payload, err := store.ReadCredentialRecoveryRecord(auditName, 64); err != nil || string(payload) != "audit" {
		t.Fatalf("audit after backup prune=%q err=%v", payload, err)
	}
}

func TestCredentialRecoveryRecordNamesIgnoreUnownedFiles(t *testing.T) {
	store := openTestStore(t, filepath.Join(canonicalTempDir(t), "config.json"))
	defer store.Close()

	if err := store.CreateExclusive(ControlToken, []byte("not evidence")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBackup([]byte("not evidence")); err != nil {
		t.Fatal(err)
	}
	records, err := store.CredentialRecoveryRecordNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("unowned files appeared as recovery records: %v", records)
	}
}

func TestCredentialRecoveryRecordConcurrentCreatesAreUnique(t *testing.T) {
	store := openTestStore(t, filepath.Join(canonicalTempDir(t), "config.json"))
	defer store.Close()

	const workers = 64
	start := make(chan struct{})
	names := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			<-start
			name, err := store.CreateCredentialRecoveryAudit([]byte("audit"))
			if err != nil {
				errorsSeen <- err
				return
			}
			names <- name
		}()
	}
	close(start)
	group.Wait()
	close(names)
	close(errorsSeen)

	for err := range errorsSeen {
		t.Errorf("concurrent create: %v", err)
	}
	unique := make(map[string]struct{}, workers)
	for name := range names {
		if _, exists := unique[name]; exists {
			t.Errorf("duplicate generated name %q", name)
		}
		unique[name] = struct{}{}
	}
	if len(unique) != workers {
		t.Fatalf("unique names=%d, want %d", len(unique), workers)
	}
	listed, err := store.CredentialRecoveryRecordNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != workers {
		t.Fatalf("listed audit records=%d, want %d", len(listed), workers)
	}
}

func TestCredentialRecoveryRecordNameGrammar(t *testing.T) {
	created := time.Date(2026, time.September, 3, 12, 34, 56, 123456789, time.FixedZone("offset", 10*60*60))
	nonce := make([]byte, credentialRecoveryNonceBytes)
	for index := range nonce {
		nonce[index] = byte(index)
	}
	name := credentialRecoveryRecordName(credentialRecoveryForensicKind, created, nonce)
	want := "credential-recovery.forensic.20260903T023456.123456789.000102030405060708090a0b0c0d0e0f.json"
	if name != want {
		t.Fatalf("record name=%q, want %q", name, want)
	}
	if !validCredentialRecoveryRecordName(credentialRecoveryForensicKind, name) {
		t.Fatalf("valid record name rejected: %q", name)
	}
	if validCredentialRecoveryRecordName(credentialRecoveryAuditKind, name) {
		t.Fatalf("record name accepted as wrong kind: %q", name)
	}
	if validCredentialRecoveryRecordName("unknown", name) {
		t.Fatal("unknown recovery record kind accepted")
	}
}

func TestCredentialRecoveryRecordReadRejectsMissingOwnedName(t *testing.T) {
	store := openTestStore(t, filepath.Join(canonicalTempDir(t), "config.json"))
	defer store.Close()

	name := "credential-recovery.audit.20260903T120000.000000000.00000000000000000000000000000000.json"
	if _, err := store.ReadCredentialRecoveryRecord(name, 64); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing owned record error=%v, want fs.ErrNotExist", err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
