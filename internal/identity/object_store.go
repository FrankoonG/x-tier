package identity

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/FrankoonG/x-tier/internal/statestore"
)

func CreateStore(store *statestore.Store) (*Identity, error) {
	seed, err := CreateStoreSeed(store)
	if err != nil {
		return nil, err
	}
	return FromSeed(seed)
}

func CreateStoreSeed(store *statestore.Store) (NodeSeed, error) {
	if store == nil {
		return NodeSeed{}, fs.ErrInvalid
	}
	seed, err := GenerateNodeSeed()
	if err != nil {
		return NodeSeed{}, err
	}
	payload, err := MarshalSeedEnvelope(seed)
	if err != nil {
		return NodeSeed{}, err
	}
	if err := store.CreateExclusive(statestore.IdentitySeed, payload); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return NodeSeed{}, errors.Join(ErrAlreadyExists, fs.ErrExist)
		}
		return NodeSeed{}, fmt.Errorf("create node seed object: %w", err)
	}
	return seed, nil
}

func LoadStore(store *statestore.Store) (*Identity, error) {
	seed, err := LoadStoreSeed(store)
	if err != nil {
		return nil, err
	}
	return FromSeed(seed)
}

func LoadStoreSeed(store *statestore.Store) (NodeSeed, error) {
	if store == nil {
		return NodeSeed{}, fs.ErrInvalid
	}
	payload, err := store.Read(statestore.IdentitySeed, maxSeedEnvelopeSize)
	if err != nil {
		return NodeSeed{}, fmt.Errorf("read node seed object: %w", err)
	}
	seed, err := UnmarshalSeedEnvelope(payload)
	if err != nil {
		return NodeSeed{}, errors.Join(ErrInvalidSeedEnvelope, err)
	}
	return seed, nil
}
