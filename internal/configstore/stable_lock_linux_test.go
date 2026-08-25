//go:build linux

package configstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateCASStableLockSurvivesLockFileRenameAndPinsParent(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	old := filepath.Join(root, "old")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(live, "config.json")
	cfg := DefaultConfig()
	cfg.Node.NodeID = testLegacyNodeID
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := UpdateCAS(path, 0, func(candidate *Config) error {
			close(entered)
			<-release
			candidate.Node.DisplayName = "old-parent"
			return nil
		})
		done <- err
	}()
	<-entered
	if err := os.Rename(path+".lock", path+".lock.renamed"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(live, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	newCfg := DefaultConfig()
	newCfg.Node.NodeID = testLegacyNodeID
	newCfg.Node.DisplayName = "new-parent"
	if err := Save(path, newCfg); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(old, "config.json")
	if _, err := UpdateCAS(oldPath, 0, func(candidate *Config) error {
		candidate.Node.DisplayName = "must-not-run"
		return nil
	}); err == nil || !strings.Contains(err.Error(), "config.locked") {
		t.Fatalf("second CAS while stable ownership held error = %v", err)
	}
	if _, err := UpdateCAS(path, 0, func(candidate *Config) error {
		candidate.Node.DisplayName = "must-not-run-by-name"
		return nil
	}); err == nil || !strings.Contains(err.Error(), "config.locked") {
		t.Fatalf("second CAS through rebound text path error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	oldCfg, err := Load(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	currentCfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if oldCfg.Revision != 1 || oldCfg.Node.DisplayName != "old-parent" {
		t.Fatalf("pinned old config = %+v", oldCfg)
	}
	if currentCfg.Revision != 0 || currentCfg.Node.DisplayName != "new-parent" {
		t.Fatalf("replacement config was touched = %+v", currentCfg)
	}
}
