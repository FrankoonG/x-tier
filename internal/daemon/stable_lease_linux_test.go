//go:build linux

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/statestore"
)

func TestDaemonStableLeaseSurvivesLockRenameAndParentRebind(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	old := filepath.Join(root, "old")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(live, "config.json")
	cfg := configstore.DefaultConfig()
	cfg.Node.DisplayName = "old-parent"
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	first, err := Start(context.Background(), Options{ConfigPath: path, ControlAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	lockPath, err := first.stateStore.DiagnosticPath(statestore.DaemonLock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(lockPath, lockPath+".renamed"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(live, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := configstore.DefaultConfig()
	replacement.Node.DisplayName = "new-parent"
	if err := configstore.Save(path, replacement); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(old, "config.json")
	if _, err := Start(context.Background(), Options{ConfigPath: oldPath, ControlAddr: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "daemon.already_running") {
		t.Fatalf("second daemon after parent rebind error = %v", err)
	}
	if _, err := Start(context.Background(), Options{ConfigPath: path, ControlAddr: "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "daemon.already_running") {
		t.Fatalf("second daemon through rebound text path error = %v", err)
	}
	status, err := first.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Revision != 0 {
		t.Fatalf("first daemon drifted to replacement config: %+v", status)
	}
}
