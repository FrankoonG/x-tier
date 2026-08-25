//go:build linux || windows

package statestore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/FrankoonG/x-tier/internal/stablelock"
)

func TestStableIdentityKeyConflictsWithLegacyPathDerivedKey(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "config.json")
	store := openTestStore(t, path)
	defer store.Close()
	key, err := store.StableIdentityKey()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := stablelock.AcquirePathIdentity("test-store-key-compat", path+"-legacy-name", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if current, err := stablelock.AcquirePathIdentityKey("test-store-key-compat", path+"-current-name", key); err == nil {
		_ = current.Close()
		t.Fatal("Store identity key did not conflict with the legacy path-derived object key")
	}
}

func TestStableIdentityKeyRemainsBoundAfterParentRebind(t *testing.T) {
	root := canonicalTempDir(t)
	live := filepath.Join(root, "live")
	moved := filepath.Join(root, "moved")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(live, "config.json")
	store := openTestStore(t, path)
	defer store.Close()
	before, err := store.StableIdentityKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := renameOpenDirectoryForTest(live, moved); err != nil {
		if openAncestorRenameBlockedForTest(err) {
			t.Skipf("platform blocked open-parent rename: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := store.StableIdentityKey()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("stable identity changed after parent rename: before=%q after=%q", before, after)
	}
	replacement := openTestStore(t, path)
	defer replacement.Close()
	replacementKey, err := replacement.StableIdentityKey()
	if err != nil {
		t.Fatal(err)
	}
	if replacementKey == before {
		t.Fatalf("rebound parent reused old stable identity %q", before)
	}
}
