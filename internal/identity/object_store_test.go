package identity

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

func TestObjectStoreIdentityPersistsAndNeverOverwrites(t *testing.T) {
	store, err := statestore.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := CreateStore(store)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if created.NodeID() != loaded.NodeID() || created.Public().PublicKey != loaded.Public().PublicKey {
		t.Fatal("loaded object identity differs from the created identity")
	}
	if _, err := CreateStore(store); !errors.Is(err, ErrAlreadyExists) || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second create error=%v", err)
	}
}

func TestObjectStoreIdentityRejectsMalformedEnvelope(t *testing.T) {
	store, err := statestore.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateExclusive(statestore.IdentitySeed, []byte(`{"seed":"not-valid"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(store); !errors.Is(err, ErrInvalidSeedEnvelope) {
		t.Fatalf("malformed envelope error=%v", err)
	}
}
