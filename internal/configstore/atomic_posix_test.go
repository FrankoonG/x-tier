//go:build unix

package configstore

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAtomicTempCreatedWithRequestedMode(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	for _, perm := range []fs.FileMode{0o600, 0o640} {
		t.Run(perm.String(), func(t *testing.T) {
			dir := t.TempDir()
			temp, err := createAtomicTemp(filepath.Join(dir, "config.json"), perm)
			if err != nil {
				t.Fatal(err)
			}
			tempPath := temp.Name()
			t.Cleanup(func() {
				_ = temp.Close()
				_ = os.Remove(tempPath)
			})
			tempInfo, err := temp.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if got, want := tempInfo.Mode().Perm(), perm.Perm(); got != want {
				t.Fatalf("temporary mode = %04o, want creation mode %04o", got, want)
			}
		})
	}
}

func TestAtomicWritePublishesRequestedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFileAtomic(path, []byte("config"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("published mode = %04o, want 0600", got)
	}
}
