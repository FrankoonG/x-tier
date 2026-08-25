//go:build windows

package configstore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCanonicalPathRejectsWin32AliasComponents(t *testing.T) {
	dir := t.TempDir()
	tests := []string{
		filepath.Join(dir, "xtier.json."),
		filepath.Join(dir, "xtier.json "),
		filepath.Join(dir, "xtier.json:stream"),
		filepath.Join(dir, "bad:name", "xtier.json"),
		filepath.Join(dir, "NUL.json"),
		filepath.Join(dir, "NUL .json"),
		filepath.Join(dir, "COM¹.json"),
		filepath.Join(dir, "missing.", "xtier.json"),
		filepath.Join(dir, "missing ", "xtier.json"),
		`C:`,
	}
	for _, path := range tests {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if canonical, err := CanonicalPath(path); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("CanonicalPath(%q) = %q, %v; want invalid path", path, canonical, err)
			}
		})
	}

	valid, err := CanonicalPath(filepath.Join(dir, "xtier.json"))
	if err != nil {
		t.Fatalf("canonicalize valid target: %v", err)
	}
	if invalid, err := CanonicalPath(filepath.Join(dir, "xtier.json.")); err == nil || invalid == valid {
		t.Fatalf("trailing-dot target = %q, %v; valid target = %q", invalid, err, valid)
	}
}

func TestCanonicalPathUsesExistingTargetFinalDOSPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MixedCase-XTier.json")
	if err := writeFileAtomic(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fromExactName, err := CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalPath(strings.ToLower(path))
	if err != nil {
		t.Fatal(err)
	}
	if got != fromExactName {
		t.Fatalf("lower-case canonical path = %q, exact-name canonical path = %q", got, fromExactName)
	}
	if filepath.Base(got) != filepath.Base(path) {
		t.Fatalf("canonical base = %q, want stored spelling %q", filepath.Base(got), filepath.Base(path))
	}
	assertOrdinaryDOSPath(t, got)
}

func TestCanonicalPathUsesExistingParentFinalDOSPath(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "RealParent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "ParentAlias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}

	got, err := CanonicalPath(filepath.Join(alias, "missing", "xtier.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := CanonicalPath(filepath.Join(realParent, "missing", "xtier.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
	assertOrdinaryDOSPath(t, got)
}

func TestNormalizeFinalDOSPathPrefixes(t *testing.T) {
	tests := map[string]string{
		`\\?\C:\Config\xtier.json`:        `C:\Config\xtier.json`,
		`\\?\UNC\server\share\xtier.json`: `\\server\share\xtier.json`,
		`\??\C:\Config\xtier.json`:        `C:\Config\xtier.json`,
		`\??\UNC\server\share\xtier.json`: `\\server\share\xtier.json`,
	}
	for input, want := range tests {
		got, err := normalizeFinalDOSPath(input)
		if err != nil {
			t.Fatalf("normalizeFinalDOSPath(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("normalizeFinalDOSPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCanonicalPathUnifiesShortAndLongNamesWhenAvailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "LongConfigurationName-XTier.json")
	if err := writeFileAtomic(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shortPath, err := windowsShortPath(path)
	if err != nil {
		t.Skipf("8.3 names unavailable: %v", err)
	}
	if strings.EqualFold(shortPath, path) {
		t.Skip("filesystem did not provide a distinct 8.3 name")
	}

	fromLong, err := CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	fromShort, err := CanonicalPath(shortPath)
	if err != nil {
		t.Fatal(err)
	}
	if fromShort != fromLong {
		t.Fatalf("short canonical path = %q, long canonical path = %q", fromShort, fromLong)
	}
}

func windowsShortPath(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 256)
	for {
		length, err := windows.GetShortPathName(name, &buffer[0], uint32(len(buffer)))
		if err != nil {
			return "", err
		}
		if length < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:length]), nil
		}
		buffer = make([]uint16, length+1)
	}
}

func assertOrdinaryDOSPath(t *testing.T, path string) {
	t.Helper()
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) {
		t.Fatalf("canonical path retains extended prefix: %q", path)
	}
}
