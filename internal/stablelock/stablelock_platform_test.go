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
