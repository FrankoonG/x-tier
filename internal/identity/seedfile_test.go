package identity

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestCreateLoadAndNoOverwriteWithFSErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node-seed.json")

	created, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if created.NodeID() != loaded.NodeID() {
		t.Fatalf("loaded NodeID = %q, created %q", loaded.NodeID(), created.NodeID())
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Create() error = %v, want fs.ErrExist", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("second Create changed the existing seed envelope")
	}

	leftovers, err := filepath.Glob(filepath.Join(dir, ".node-seed.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files were not cleaned up: %v", leftovers)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("seed mode = %04o, want 0600", got)
		}
	}
}

func TestCreateIsAtomicUnderContention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node-seed.json")
	const workers = 12

	start := make(chan struct{})
	results := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := Create(path)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAlreadyExists) && errors.Is(err, fs.ErrExist):
		default:
			t.Fatalf("unexpected Create error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful creates = %d, want 1", succeeded)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("load winning identity: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".node-seed.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files were not cleaned up: %v", leftovers)
	}
}

func TestLoadRejectsMalformedEnvelopes(t *testing.T) {
	validSeed := strings.Repeat("A", 43)
	validPrefix := `{"version":1,"type":"xtier-node-seed","kdf":"hkdf-sha256","algorithm":"ed25519","seed":"`
	tests := map[string]string{
		"empty":             "",
		"array":             `[]`,
		"unknown field":     validPrefix + validSeed + `","extra":true}`,
		"duplicate field":   `{"version":1,"version":1,"type":"xtier-node-seed","kdf":"hkdf-sha256","algorithm":"ed25519","seed":"` + validSeed + `"}`,
		"missing field":     `{"version":1,"algorithm":"ed25519"}`,
		"future version":    strings.Replace(validPrefix, `"version":1`, `"version":2`, 1) + validSeed + `"}`,
		"unknown algorithm": strings.Replace(validPrefix, `ed25519`, `rsa`, 1) + validSeed + `"}`,
		"padded base64":     validPrefix + `AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`,
		"short seed":        validPrefix + `AA"}`,
		"trailing object":   validPrefix + validSeed + `"}{}`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "seed.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); !errors.Is(err, ErrInvalidSeedEnvelope) {
				t.Fatalf("Load() error = %v, want ErrInvalidSeedEnvelope", err)
			}
		})
	}
}

func TestLoadRejectsNonRegularAndInsecureFiles(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrInvalidSeedEnvelope) {
		t.Fatalf("Load(directory) error = %v, want ErrInvalidSeedEnvelope", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	path := filepath.Join(t.TempDir(), "seed.json")
	seed, err := GenerateNodeSeed()
	if err != nil {
		t.Fatal(err)
	}
	data, err := MarshalSeedEnvelope(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInsecureSeedFile) {
		t.Fatalf("Load(insecure file) error = %v, want ErrInsecureSeedFile", err)
	}
}

func TestLoadRejectsOversizedEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSeedEnvelopeSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrInvalidSeedEnvelope) {
		t.Fatalf("Load(oversized) error = %v, want ErrInvalidSeedEnvelope", err)
	}
}

func TestLoadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if _, err := Create(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := Load(link); !errors.Is(err, ErrInvalidSeedEnvelope) {
		t.Fatalf("Load(symlink) error = %v, want ErrInvalidSeedEnvelope", err)
	}
}

func TestCreateMakesPrivateParentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	path := filepath.Join(dir, "seed.json")
	if _, err := Create(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory mode = %04o, want 0700", got)
		}
	}
}
