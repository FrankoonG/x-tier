package configstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRevisionAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Revision = 1
	cfg.Node.NodeID = "A"
	cfg.Node.DisplayName = "alpha"
	cfg.Node.Role = "thin"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg.Revision = 2
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save second: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Revision != 2 {
		t.Fatalf("revision = %d", got.Revision)
	}
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected backup file")
	}
}

func TestWithLockRejectsExistingLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path+".lock", []byte("busy"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WithLock(path, func() error { return nil })
	if err == nil {
		t.Fatal("expected lock error")
	}
}

func TestValidateRejectsUnknownProfileReference(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.NodeID = "A"
	cfg.Peers = []PeerConfig{{
		Name:          "B",
		NodeID:        "B",
		Direction:     "outbound",
		GatewayAddr:   "b:19080",
		XrayProfileID: "missing",
		Enabled:       true,
		RendrCapable:  true,
	}}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected unknown profile error")
	}
}
