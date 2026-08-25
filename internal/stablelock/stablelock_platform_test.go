//go:build linux || windows

package stablelock

import "testing"

func TestAcquireRejectsSecondLeaseAndReleases(t *testing.T) {
	first, err := Acquire("test", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire("test", t.Name()); err == nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("stable ownership name was acquired twice")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Acquire("test", t.Name())
	if err != nil {
		t.Fatalf("released stable ownership name was not reusable: %v", err)
	}
	_ = third.Close()
}

func TestAcquirePathIdentityKeyRejectsSameObjectThroughDifferentNames(t *testing.T) {
	objectKey := t.Name() + "-object"
	first, err := AcquirePathIdentityKey("test-path-key", t.Name()+"-first", objectKey)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := AcquirePathIdentityKey("test-path-key", t.Name()+"-second", objectKey); err == nil {
		_ = second.Close()
		t.Fatal("same filesystem object was acquired through two external names")
	}
}
