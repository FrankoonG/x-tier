package statestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLegacyMigrationCopiesOnlyUnambiguousPrivateState(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()

	legacy := map[string][]byte{
		store.configLeaf + ".control-token":                 []byte("  " + strings.Repeat("a", 64) + "\r\n"),
		store.configLeaf + ".web-token":                     []byte(strings.Repeat("b", 64) + "\n"),
		store.configLeaf + legacyPeerCredentialLedgerSuffix: []byte("{\"version\":1,\"quarantines\":[]}\n"),
		store.configLeaf + ".last-good":                     []byte(`{"revision":7}`),
		store.configLeaf + ".bak.20260826T010203.123456789": []byte("backup-one"),
		store.configLeaf + ".bak.notes":                     []byte("backup-neighbor"),
	}
	for name, payload := range legacy {
		writeSecureRootFileForMigrationTest(t, store.parent, name, payload)
	}
	writeSecureRootFileForMigrationTest(t, store.parent, store.configLeaf+".lock", []byte("do-not-copy"))
	seed := []byte(`{"version":1,"seed":"exact"}` + "\n")
	writeLegacySeedForMigrationTest(t, store, seed)

	seedCalls := 0
	ledgerCalls := 0
	options := LegacyMigrationOptions{
		Identity: "node-a",
		ValidateIdentitySeed: func(identity string, payload []byte) error {
			seedCalls++
			if identity != "node-a" || !bytes.Equal(payload, seed) {
				t.Fatalf("seed validator identity=%q payload=%q", identity, payload)
			}
			payload[0] = 'x'
			return nil
		},
		ValidatePeerCredentialLedger: func(payload []byte) error {
			ledgerCalls++
			if !bytes.Equal(payload, legacy[store.configLeaf+legacyPeerCredentialLedgerSuffix]) {
				t.Fatalf("ledger validator payload=%q", payload)
			}
			payload[0] = 'x'
			return nil
		},
	}
	result, err := store.MigrateLegacy(options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources != 4 || result.Published != 4 || result.AlreadyComplete {
		t.Fatalf("migration result=%+v", result)
	}
	if seedCalls != 1 || ledgerCalls != 1 {
		t.Fatalf("validator calls: seed=%d ledger=%d", seedCalls, ledgerCalls)
	}

	assertStoreObjectForMigrationTest(t, store, ControlToken, legacy[store.configLeaf+".control-token"])
	assertStoreObjectForMigrationTest(t, store, WebToken, legacy[store.configLeaf+".web-token"])
	assertStoreObjectForMigrationTest(t, store, IdentitySeed, seed)
	assertStoreObjectForMigrationTest(
		t,
		store,
		PeerCredentialQuarantineLedger,
		legacy[store.configLeaf+legacyPeerCredentialLedgerSuffix],
	)
	if _, err := store.Read(LastKnownGood, maxLegacyConfigSize); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ambiguous last-known-good was imported: %v", err)
	}
	backups, err := store.BackupNames()
	if err != nil || len(backups) != 0 {
		t.Fatalf("ambiguous backups were imported: names=%v err=%v", backups, err)
	}
	for name, want := range legacy {
		got, readErr := readFromRoot(store.parent, name, maxLegacyConfigSize)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("legacy source %q changed: got=%q err=%v", name, got, readErr)
		}
	}

	result, err = store.MigrateLegacy(options)
	if err != nil || !result.AlreadyComplete || result.Sources != 4 || result.Published != 0 {
		t.Fatalf("idempotent migration result=%+v err=%v", result, err)
	}
}

func TestCompletedLegacyMigrationDoesNotRepublishDeletedCredentialLedger(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	source := store.configLeaf + legacyPeerCredentialLedgerSuffix
	writeSecureRootFileForMigrationTest(t, store.parent, source, []byte("{\"version\":1,\"quarantines\":[]}\n"))
	options := LegacyMigrationOptions{ValidatePeerCredentialLedger: func([]byte) error { return nil }}
	if _, err := store.MigrateLegacy(options); err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := store.DiagnosticPath(PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	result, err := store.MigrateLegacy(options)
	if err != nil || !result.AlreadyComplete || result.Published != 0 {
		t.Fatalf("completed migration result=%+v err=%v", result, err)
	}
	if _, err := store.Read(PeerCredentialQuarantineLedger, 1024); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("completed migration silently recreated ledger: %v", err)
	}
}

func TestLegacyReceiptWithoutLedgerEntryPermanentlyIgnoresAdjacentSidecar(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	sourcePayload := []byte("{\"version\":1,\"quarantines\":[{\"legacy\":true}]}\n")
	targetPayload := []byte("{\"version\":1,\"quarantines\":[]}\n")
	writeSecureRootFileForMigrationTest(
		t,
		store.parent,
		store.configLeaf+legacyPeerCredentialLedgerSuffix,
		sourcePayload,
	)
	if err := store.Replace(PeerCredentialQuarantineLedger, targetPayload); err != nil {
		t.Fatal(err)
	}
	validatorCalls := 0
	result, err := store.MigrateLegacy(LegacyMigrationOptions{
		ValidatePeerCredentialLedger: func([]byte) error {
			validatorCalls++
			return errors.New("retired sidecar must not be consulted")
		},
	})
	if err != nil || !result.AlreadyComplete || result.Published != 0 || validatorCalls != 0 {
		t.Fatalf("old receipt migration result=%+v validator_calls=%d err=%v", result, validatorCalls, err)
	}
	assertStoreObjectForMigrationTest(t, store, PeerCredentialQuarantineLedger, targetPayload)

	ledgerPath, err := store.DiagnosticPath(PeerCredentialQuarantineLedger)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	result, err = store.MigrateLegacy(LegacyMigrationOptions{
		ValidatePeerCredentialLedger: func([]byte) error {
			validatorCalls++
			return errors.New("retired sidecar must not be consulted")
		},
	})
	if err != nil || !result.AlreadyComplete || result.Published != 0 || validatorCalls != 0 {
		t.Fatalf("deleted target migration result=%+v validator_calls=%d err=%v", result, validatorCalls, err)
	}
	if _, err := store.Read(PeerCredentialQuarantineLedger, 1024); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old receipt republished deleted ledger: %v", err)
	}
}

func TestLegacyMigrationRejectsInvalidCredentialLedger(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	writeSecureRootFileForMigrationTest(
		t,
		store.parent,
		store.configLeaf+legacyPeerCredentialLedgerSuffix,
		[]byte("invalid"),
	)
	injected := errors.New("invalid ledger")
	_, err := store.MigrateLegacy(LegacyMigrationOptions{
		ValidatePeerCredentialLedger: func([]byte) error { return injected },
	})
	if !errors.Is(err, ErrInvalidLegacyState) || !errors.Is(err, injected) {
		t.Fatalf("invalid ledger error=%v", err)
	}
}

func TestLegacyMigrationPublishesEmptyVersionFence(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()

	result, err := store.MigrateLegacy(LegacyMigrationOptions{})
	if err != nil || result.Sources != 0 || result.Published != 0 || result.AlreadyComplete {
		t.Fatalf("first empty migration result=%+v err=%v", result, err)
	}
	intentPath, completePath, err := store.LegacyMigrationReceiptPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{intentPath, completePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("empty migration did not publish %s: %v", path, err)
		}
	}
	result, err = store.MigrateLegacy(LegacyMigrationOptions{})
	if err != nil || !result.AlreadyComplete || result.Sources != 0 || result.Published != 0 {
		t.Fatalf("completed empty migration result=%+v err=%v", result, err)
	}
}

func TestLegacyMigrationRecoversFromIntentAndPartialPublication(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	controlName := store.configLeaf + ".control-token"
	webName := store.configLeaf + ".web-token"
	control := []byte(strings.Repeat("a", 64))
	web := []byte(strings.Repeat("b", 64))
	writeSecureRootFileForMigrationTest(t, store.parent, controlName, control)
	writeSecureRootFileForMigrationTest(t, store.parent, webName, web)

	options := LegacyMigrationOptions{}
	plan, err := store.buildLegacyMigrationPlan(options)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := jsonMarshalLineForMigrationTest(plan.intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := createExclusiveInRoot(store.state, store.stateDir, legacyMigrationIntentName, intent); err != nil {
		t.Fatal(err)
	}
	first := plan.intent.Entries[0]
	if _, err := store.publishLegacyEntry(first, plan.payloads[first.Source]); err != nil {
		t.Fatal(err)
	}

	result, err := store.MigrateLegacy(options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Published != 1 || result.AlreadyComplete {
		t.Fatalf("recovery result=%+v", result)
	}
	assertStoreObjectForMigrationTest(t, store, ControlToken, control)
	assertStoreObjectForMigrationTest(t, store, WebToken, web)
}

func TestLegacyMigrationFailsClosedOnDifferentV2Object(t *testing.T) {
	tests := []struct {
		name    string
		object  Object
		prepare func(*testing.T, *Store)
		options LegacyMigrationOptions
	}{
		{
			name: "token", object: ControlToken,
			prepare: func(t *testing.T, store *Store) {
				writeSecureRootFileForMigrationTest(t, store.parent, store.configLeaf+".control-token", []byte(strings.Repeat("a", 64)))
			},
		},
		{
			name: "identity-seed", object: IdentitySeed,
			prepare: func(t *testing.T, store *Store) {
				writeLegacySeedForMigrationTest(t, store, []byte("legacy"))
			},
			options: LegacyMigrationOptions{
				Identity: "node-a", ValidateIdentitySeed: func(string, []byte) error { return nil },
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newLegacyMigrationStore(t)
			defer store.Close()
			test.prepare(t, store)
			if err := store.CreateExclusive(test.object, []byte("v2")); err != nil {
				t.Fatal(err)
			}
			if _, err := store.MigrateLegacy(test.options); !errors.Is(err, ErrLegacyMigrationConflict) {
				t.Fatalf("conflicting migration error=%v, want ErrLegacyMigrationConflict", err)
			}
			assertStoreObjectForMigrationTest(t, store, test.object, []byte("v2"))
		})
	}
}

func TestLegacyMigrationRejectsSourceDriftAfterCompletion(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	name := store.configLeaf + ".control-token"
	first := []byte(strings.Repeat("a", 64))
	writeSecureRootFileForMigrationTest(t, store.parent, name, first)
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	replaceLegacySourceForMigrationTest(t, store, name, []byte(strings.Repeat("b", 64)))
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("source drift error=%v, want ErrLegacyMigrationConflict", err)
	}
	assertStoreObjectForMigrationTest(t, store, ControlToken, first)
}

func TestLegacyMigrationRejectsNewSourceAfterEmptyFence(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	writeSecureRootFileForMigrationTest(
		t, store.parent, store.configLeaf+".control-token", []byte(strings.Repeat("a", 64)),
	)
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("new legacy source error=%v, want ErrLegacyMigrationConflict", err)
	}
}

func TestLegacyMigrationAllowsSourceRemovalAndTargetEvolution(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	name := store.configLeaf + ".control-token"
	writeSecureRootFileForMigrationTest(t, store.parent, name, []byte(strings.Repeat("a", 64)))
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(store.configPath), name)); err != nil {
		t.Fatal(err)
	}
	evolved := []byte(strings.Repeat("b", 64))
	if err := store.Replace(ControlToken, evolved); err != nil {
		t.Fatal(err)
	}
	result, err := store.MigrateLegacy(LegacyMigrationOptions{})
	if err != nil || !result.AlreadyComplete || result.Published != 0 {
		t.Fatalf("completed migration result=%+v err=%v", result, err)
	}
	assertStoreObjectForMigrationTest(t, store, ControlToken, evolved)
}

func TestLegacyMigrationRepairsMissingCompletedSeed(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	seed := []byte("legacy-seed")
	writeLegacySeedForMigrationTest(t, store, seed)
	options := LegacyMigrationOptions{
		Identity: "node-a", ValidateIdentitySeed: func(string, []byte) error { return nil },
	}
	if _, err := store.MigrateLegacy(options); err != nil {
		t.Fatal(err)
	}
	seedPath, err := store.DiagnosticPath(IdentitySeed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(seedPath); err != nil {
		t.Fatal(err)
	}
	result, err := store.MigrateLegacy(options)
	if err != nil || !result.AlreadyComplete || result.Published != 1 {
		t.Fatalf("seed repair result=%+v err=%v", result, err)
	}
	assertStoreObjectForMigrationTest(t, store, IdentitySeed, seed)
}

func TestLegacyMigrationRejectsMissingCompletedSeedWithoutSource(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	writeLegacySeedForMigrationTest(t, store, []byte("legacy-seed"))
	options := LegacyMigrationOptions{
		Identity: "node-a", ValidateIdentitySeed: func(string, []byte) error { return nil },
	}
	if _, err := store.MigrateLegacy(options); err != nil {
		t.Fatal(err)
	}
	seedTarget, _ := store.DiagnosticPath(IdentitySeed)
	if err := os.Remove(seedTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(store.configPath), "keystore", "node-seed.v1.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateLegacy(options); !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("missing seed error=%v, want ErrLegacyMigrationConflict", err)
	}
}

func TestLegacyMigrationRejectsCompletionReceiptTampering(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	writeSecureRootFileForMigrationTest(
		t, store.parent, store.configLeaf+".control-token", []byte(strings.Repeat("a", 64)),
	)
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); err != nil {
		t.Fatal(err)
	}
	payload, err := readFromRoot(store.state, legacyMigrationCompleteName, maxManifestSize)
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(payload, []byte(legacyMigrationCompleteKind), []byte("tampered-completion-kind"), 1)
	if err := replaceInRoot(store.state, store.stateDir, legacyMigrationCompleteName, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); !errors.Is(err, ErrLegacyMigrationConflict) {
		t.Fatalf("tampered completion error=%v, want ErrLegacyMigrationConflict", err)
	}
}

func TestLegacyMigrationTreatsInvalidUnambiguousStateAsPresent(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *Store)
		options LegacyMigrationOptions
	}{
		{
			name: "empty-token",
			prepare: func(t *testing.T, store *Store) {
				writeSecureRootFileForMigrationTest(t, store.parent, store.configLeaf+".control-token", []byte(" \r\n\t"))
			},
		},
		{
			name:    "invalid-seed",
			prepare: func(t *testing.T, store *Store) { writeLegacySeedForMigrationTest(t, store, []byte("seed")) },
			options: LegacyMigrationOptions{
				Identity:             "node-a",
				ValidateIdentitySeed: func(string, []byte) error { return errors.New("wrong owner") },
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newLegacyMigrationStore(t)
			defer store.Close()
			test.prepare(t, store)
			if _, err := store.MigrateLegacy(test.options); !errors.Is(err, ErrInvalidLegacyState) {
				t.Fatalf("migration error=%v, want ErrInvalidLegacyState", err)
			}
			intent, complete, _ := store.LegacyMigrationReceiptPaths()
			for _, path := range []string{intent, complete} {
				if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("invalid source produced receipt %s: %v", path, err)
				}
			}
		})
	}
}

func TestLegacyMigrationIgnoresAmbiguousSeedAndConfigNeighbors(t *testing.T) {
	store := newLegacyMigrationStore(t)
	defer store.Close()
	seed := []byte("unasserted-shared-seed")
	writeLegacySeedForMigrationTest(t, store, seed)
	neighbor := []byte(`{"schema_version":1,"revision":0,"node":{},"system":{}}`)
	writeSecureRootFileForMigrationTest(t, store.parent, store.configLeaf+".control-token", neighbor)
	result, err := store.MigrateLegacy(LegacyMigrationOptions{
		IsConfigDocument: func(payload []byte) bool { return bytes.Equal(payload, neighbor) },
	})
	if err != nil || result.Sources != 0 || result.Published != 0 {
		t.Fatalf("ambiguous migration result=%+v err=%v", result, err)
	}
	if _, err := store.Read(IdentitySeed, maxLegacySeedSize); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ambiguous seed was imported: %v", err)
	}
	if _, err := store.Read(ControlToken, maxLegacyTokenSize); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("neighboring config was imported as token: %v", err)
	}
}

func TestLegacyMigrationRejectsUnsafeSources(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		store := newLegacyMigrationStore(t)
		defer store.Close()
		target := filepath.Join(filepath.Dir(store.configPath), "outside-token")
		if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(filepath.Dir(store.configPath), store.configLeaf+".control-token")
		if err := os.Symlink(target, legacy); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("symlink migration error=%v, want ErrInsecureState", err)
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		store := newLegacyMigrationStore(t)
		defer store.Close()
		name := store.configLeaf + ".control-token"
		writeSecureRootFileForMigrationTest(t, store.parent, name, []byte(strings.Repeat("a", 64)))
		path := filepath.Join(filepath.Dir(store.configPath), name)
		if err := os.Link(path, path+".alias"); err != nil {
			t.Skipf("hardlink unavailable: %v", err)
		}
		if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("hardlink migration error=%v, want ErrInsecureState", err)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode test")
		}
		store := newLegacyMigrationStore(t)
		defer store.Close()
		name := store.configLeaf + ".control-token"
		writeSecureRootFileForMigrationTest(t, store.parent, name, []byte(strings.Repeat("a", 64)))
		if err := os.Chmod(filepath.Join(filepath.Dir(store.configPath), name), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.MigrateLegacy(LegacyMigrationOptions{}); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("broad-mode migration error=%v, want ErrInsecureState", err)
		}
	})
}

func newLegacyMigrationStore(t *testing.T) *Store {
	t.Helper()
	dir := canonicalTempDir(t)
	return openTestStore(t, filepath.Join(dir, "config.json"))
}

func writeSecureRootFileForMigrationTest(t *testing.T, root *os.Root, name string, payload []byte) {
	t.Helper()
	file, err := openSecureRootFile(root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func replaceLegacySourceForMigrationTest(t *testing.T, store *Store, name string, payload []byte) {
	t.Helper()
	if err := os.Remove(filepath.Join(filepath.Dir(store.configPath), name)); err != nil {
		t.Fatal(err)
	}
	writeSecureRootFileForMigrationTest(t, store.parent, name, payload)
}

func writeLegacySeedForMigrationTest(t *testing.T, store *Store, payload []byte) {
	t.Helper()
	if err := createSecureRootDirectory(store.parent, "keystore"); err != nil {
		t.Fatal(err)
	}
	root, err := store.parent.OpenRoot("keystore")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := secureRootDirectory(root, filepath.Join(filepath.Dir(store.configPath), "keystore"), true); err != nil {
		t.Fatal(err)
	}
	writeSecureRootFileForMigrationTest(t, root, "node-seed.v1.json", payload)
}

func assertStoreObjectForMigrationTest(t *testing.T, store *Store, object Object, want []byte) {
	t.Helper()
	got, err := store.Read(object, maxLegacyConfigSize)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("object %d=%q, want %q", object, got, want)
	}
}

func jsonMarshalLineForMigrationTest(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}
