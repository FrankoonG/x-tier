package statestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestStoreObjectsAreIsolatedByConfigName(t *testing.T) {
	dir := canonicalTempDir(t)
	first := openTestStore(t, filepath.Join(dir, "a"))
	defer first.Close()
	second := openTestStore(t, filepath.Join(dir, "a.control-token"))
	defer second.Close()
	if first.ConfigKey() == second.ConfigKey() {
		t.Fatal("distinct config names share a state key")
	}
	if err := first.CreateExclusive(ControlToken, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := second.CreateExclusive(ControlToken, []byte("second")); err != nil {
		t.Fatal(err)
	}
	left, err := first.Read(ControlToken, 64)
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.Read(ControlToken, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, []byte("first")) || !bytes.Equal(right, []byte("second")) {
		t.Fatalf("isolated state changed: first=%q second=%q", left, right)
	}
}

func TestObjectNamesPreserveEnumerationAndMapToOwnedFiles(t *testing.T) {
	tests := []struct {
		object Object
		value  Object
		name   string
	}{
		{ConfigLock, 1, "config.lock"},
		{DaemonLock, 2, "daemon.lock"},
		{ControlToken, 3, "control-token.v1"},
		{WebToken, 4, "web-token.v1"},
		{IdentitySeed, 5, "identity-seed.v1.json"},
		{LastKnownGood, 6, "last-known-good.json"},
		{PeerCredentialQuarantineLedger, 7, "peer-credential-quarantines.v1.json"},
		{ConfigRevisionHighWater, 8, "config-revision-high-water.v1.json"},
		{RejectedConfig, 9, "rejected-config.json"},
		{PreMigrationConfig, 10, "pre-migration-config.json"},
	}
	for _, test := range tests {
		if test.object != test.value {
			t.Errorf("object %q value=%d, want %d", test.name, test.object, test.value)
		}
		name, err := objectName(test.object)
		if err != nil {
			t.Errorf("objectName(%d): %v", test.object, err)
			continue
		}
		if name != test.name {
			t.Errorf("objectName(%d)=%q, want %q", test.object, name, test.name)
		}
	}
	if _, err := objectName(0); err == nil {
		t.Fatal("objectName accepted zero Object")
	}
	if _, err := objectName(PreMigrationConfig + 1); err == nil {
		t.Fatal("objectName accepted Object after the known enumeration")
	}
}

func TestPeerCredentialQuarantineLedgerSecureReadWrite(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()

	initial := []byte(`{"version":1,"peers":{"peer-a":{"reason":"credential-mismatch"}}}`)
	if err := store.CreateExclusive(PeerCredentialQuarantineLedger, initial); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExclusive(PeerCredentialQuarantineLedger, []byte("overwrite")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second exclusive create error=%v, want fs.ErrExist", err)
	}
	if _, err := store.Read(PeerCredentialQuarantineLedger, int64(len(initial)-1)); err == nil {
		t.Fatal("ledger read ignored size limit")
	}
	payload, err := store.Read(PeerCredentialQuarantineLedger, int64(len(initial)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, initial) {
		t.Fatalf("initial ledger=%q, want %q", payload, initial)
	}

	replacement := []byte(`{"version":1,"peers":{}}`)
	if err := store.Replace(PeerCredentialQuarantineLedger, replacement); err != nil {
		t.Fatal(err)
	}
	payload, err = store.Read(PeerCredentialQuarantineLedger, int64(len(replacement)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, replacement) {
		t.Fatalf("replacement ledger=%q, want %q", payload, replacement)
	}
}

func TestReplaceReportsUnknownOutcomeWhenDirectorySyncFailsAfterPublish(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()
	if err := store.Replace(ConfigRevisionHighWater, []byte("before")); err != nil {
		t.Fatal(err)
	}

	name, err := objectName(ConfigRevisionHighWater)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected directory sync failure")
	err = replaceInRootWithSync(
		store.state,
		store.stateDir,
		name,
		[]byte("after"),
		func(*os.File) error { return injected },
	)
	if !errors.Is(err, ErrCommitOutcomeUnknown) || errors.Is(err, injected) {
		t.Fatalf("replace error=%v, want only ErrCommitOutcomeUnknown classification", err)
	}
	payload, readErr := store.Read(ConfigRevisionHighWater, 64)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(payload, []byte("after")) {
		t.Fatalf("published payload=%q, want after", payload)
	}
}

func TestStoreRejectsReservedConfigPath(t *testing.T) {
	dir := canonicalTempDir(t)
	for _, path := range []string{
		filepath.Join(dir, DirectoryName),
		filepath.Join(dir, DirectoryName, "nested", "config.json"),
	} {
		if _, err := Open(path); err == nil {
			t.Fatalf("reserved config path accepted: %s", path)
		}
	}
}

func TestStoreRejectsStateDirectorySymlink(t *testing.T) {
	dir := canonicalTempDir(t)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, DirectoryName)); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, err := Open(filepath.Join(dir, "config.json")); err == nil || !errors.Is(err, ErrInsecureState) {
		t.Fatalf("state directory symlink error=%v, want ErrInsecureState", err)
	}
}

func TestStoreRemainsBoundAfterStateDirectoryRename(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()
	if err := store.CreateExclusive(ControlToken, []byte("bound")); err != nil {
		t.Fatal(err)
	}
	oldState := filepath.Dir(mustDiagnosticPath(t, store, ControlToken))
	renamedState := filepath.Join(filepath.Dir(oldState), "state-renamed")
	if err := renameOpenDirectoryForTest(oldState, renamedState); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(oldState, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := store.Read(ControlToken, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "bound" {
		t.Fatalf("store followed rebound state path: %q", payload)
	}
	if _, err := os.Stat(filepath.Join(oldState, "control-token.v1")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacement state path was touched: %v", err)
	}
}

func TestCreateExclusiveDoesNotOverwriteAndReplaceIsAtomic(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()
	if err := store.CreateExclusive(WebToken, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExclusive(WebToken, []byte("second")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second exclusive create error=%v, want fs.ErrExist", err)
	}
	if err := store.Replace(WebToken, []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	payload, err := store.Read(WebToken, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "replacement" {
		t.Fatalf("replacement payload=%q", payload)
	}
}

func TestStoreRejectsSymlinkedObject(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()

	target := filepath.Join(dir, "outside-secret")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	objectPath := mustDiagnosticPath(t, store, ControlToken)
	if err := os.Symlink(target, objectPath); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	if err := store.CreateExclusive(ControlToken, []byte("replacement")); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("exclusive create over symlink error=%v, want ErrInsecureState", err)
	}
	if _, err := store.Read(ControlToken, 64); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("symlinked object error=%v, want ErrInsecureState", err)
	}
}

func TestStoreRejectsHardlinkedObject(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()
	if err := store.CreateExclusive(ControlToken, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	objectPath := mustDiagnosticPath(t, store, ControlToken)
	if err := os.Link(objectPath, filepath.Join(filepath.Dir(objectPath), "unexpected-link")); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if err := store.CreateExclusive(ControlToken, []byte("replacement")); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("exclusive create over hardlink error=%v, want ErrInsecureState", err)
	}
	if _, err := store.Read(ControlToken, 64); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("hardlinked object error=%v, want ErrInsecureState", err)
	}
}

func TestConcurrentExclusivePublicationHasOneWinner(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()

	const workers = 32
	start := make(chan struct{})
	results := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for index := range workers {
		go func() {
			defer group.Done()
			<-start
			results <- store.CreateExclusive(WebToken, []byte(strconv.Itoa(index)))
		}()
	}
	close(start)
	group.Wait()
	close(results)

	winners := 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, fs.ErrExist):
		default:
			t.Errorf("exclusive publication error=%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("exclusive publication winners=%d, want 1", winners)
	}
	payload, err := store.Read(WebToken, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(string(payload)); err != nil {
		t.Fatalf("published payload=%q is not one complete contender", payload)
	}
}

func TestConfigOperationsRemainBoundAfterParentRename(t *testing.T) {
	root := canonicalTempDir(t)
	live := filepath.Join(root, "live")
	old := filepath.Join(root, "old")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(live, "config.json")
	store := openTestStore(t, path)
	defer store.Close()
	if err := store.CreateConfigExclusive([]byte("old-parent")); err != nil {
		t.Fatal(err)
	}
	if err := renameOpenDirectoryForTest(live, old); err != nil {
		if !openAncestorRenameBlockedForTest(err) {
			t.Fatal(err)
		}
		payload, readErr := store.ReadConfig(64)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(payload) != "old-parent" {
			t.Fatalf("blocked parent rename changed config binding: %q", payload)
		}
		return
	}
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new-parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceConfig([]byte("updated-old-parent")); err != nil {
		t.Fatal(err)
	}
	oldPayload, err := os.ReadFile(filepath.Join(old, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	newPayload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldPayload) != "updated-old-parent" || string(newPayload) != "new-parent" {
		t.Fatalf("config store followed rebound parent: old=%q new=%q", oldPayload, newPayload)
	}
}

func TestConcurrentFirstOpenPublishesOneValidManifest(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "config.json")
	const workers = 16
	stores := make(chan *Store, workers)
	errorsSeen := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(workers)
	for range workers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			store, err := Open(path)
			if err != nil {
				errorsSeen <- err
				return
			}
			stores <- store
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(stores)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("concurrent open: %v", err)
	}
	count := 0
	var key string
	for store := range stores {
		count++
		if key == "" {
			key = store.ConfigKey()
		} else if store.ConfigKey() != key {
			t.Errorf("concurrent stores disagree on config key: %q %q", key, store.ConfigKey())
		}
		if err := store.Close(); err != nil {
			t.Errorf("close concurrent store: %v", err)
		}
	}
	if count != workers {
		t.Fatalf("opened stores=%d, want %d", count, workers)
	}
}

func TestBackupPruningOccursAfterPublicationAndTemporaryRecoveryIsExplicit(t *testing.T) {
	dir := canonicalTempDir(t)
	store := openTestStore(t, filepath.Join(dir, "config.json"))
	defer store.Close()
	for _, payload := range []string{"one", "two", "three"} {
		if _, err := store.CreateBackup([]byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	names, err := store.BackupNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("backup count=%d, want 3", len(names))
	}
	if payload, err := store.ReadBackup(names[0], 16); err != nil || len(payload) == 0 {
		t.Fatalf("read backup payload=%q err=%v", payload, err)
	}
	if _, err := store.ReadBackup("../config.json", 16); err == nil {
		t.Fatal("ReadBackup accepted a non-owned name")
	}
	if err := store.PruneBackups(2); err != nil {
		t.Fatal(err)
	}
	names, err = store.BackupNames()
	if err != nil || len(names) != 2 {
		t.Fatalf("pruned backups=%v err=%v", names, err)
	}
	temporaryName := ".tmp-owned-fixture"
	temporaryFile, err := openSecureRootFile(store.state, temporaryName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := temporaryFile.Write([]byte("stale")); err != nil {
		t.Fatal(err)
	}
	if err := temporaryFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverTemporaryFiles(); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(filepath.Dir(mustDiagnosticPath(t, store, ControlToken)), temporaryName)
	if _, err := os.Stat(temporary); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temporary file survived recovery: %v", err)
	}
}

func TestManifestParserRejectsMalformedDocuments(t *testing.T) {
	valid := `{"version":2,"kind":"xtier-private-state","config_key":"key","store_id":"00000000000000000000000000000000"}`
	for name, payload := range map[string]string{
		"duplicate": `{"version":2,"version":2,"kind":"xtier-private-state","config_key":"key","store_id":"00000000000000000000000000000000"}`,
		"unknown":   `{"version":2,"kind":"xtier-private-state","config_key":"key","store_id":"00000000000000000000000000000000","future":true}`,
		"trailing":  valid + `{}`,
		"missing":   `{"version":2,"kind":"xtier-private-state","config_key":"key"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeManifest([]byte(payload)); err == nil {
				t.Fatalf("ambiguous manifest accepted: %s", payload)
			}
		})
	}
}

func TestManifestParserIsStrictForOtherwiseValidDocuments(t *testing.T) {
	value := manifest{
		Version: manifestVersion, Kind: manifestKind, ConfigKey: "key",
		StoreID: "00000000000000000000000000000000",
	}
	valid, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManifest(valid); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	duplicate := bytes.Replace(valid, []byte(`"version":2`), []byte(`"version":2,"version":2`), 1)
	unknown := bytes.Replace(valid, []byte(`}`), []byte(`,"future":true}`), 1)
	versionField := []byte(strconv.Quote("version") + ":2")
	duplicate = bytes.Replace(valid, versionField, append(append([]byte(nil), versionField...), append([]byte{','}, versionField...)...), 1)
	unknown = append(append(bytes.TrimSuffix(valid, []byte{'}'}), ','), []byte(strconv.Quote("future")+":true}")...)
	if bytes.Equal(duplicate, valid) || bytes.Equal(unknown, valid) {
		t.Fatal("strictness fixtures did not modify the valid manifest")
	}
	for name, payload := range map[string][]byte{
		"duplicate": duplicate,
		"unknown":   unknown,
		"trailing":  append(append([]byte(nil), valid...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeManifest(payload); err == nil {
				t.Fatalf("ambiguous manifest accepted: %s", payload)
			}
		})
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(dir)
}

func mustDiagnosticPath(t *testing.T, store *Store, object Object) string {
	t.Helper()
	path, err := store.DiagnosticPath(object)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
