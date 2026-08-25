//go:build !windows

package controlapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTokenRejectsGroupOrOtherPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.token")
	if _, err := CreateToken(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(path); err == nil {
		t.Fatal("ReadToken accepted group/other-readable permissions")
	}
}

func TestReadTokenRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.token")
	if _, err := CreateToken(realPath); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link.token")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadToken(linkPath); err == nil {
		t.Fatal("ReadToken accepted a symlink")
	}
}

func TestReadTokenRejectsHardlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.token")
	if _, err := CreateToken(target); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.token")
	if err := os.Link(target, alias); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	if _, err := ReadToken(target); err == nil {
		t.Fatal("ReadToken accepted a multiply linked token")
	}
	if _, err := ReadToken(alias); err == nil {
		t.Fatal("ReadToken accepted a hardlink alias")
	}
}
