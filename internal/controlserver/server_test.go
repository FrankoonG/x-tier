package controlserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/FrankoonG/x-tier/internal/configstore"
	"github.com/FrankoonG/x-tier/internal/controlapi"
)

func TestServerExecutesCLIInsideDaemon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := configstore.DefaultConfig()
	cfg.Node = configstore.NodeConfig{NodeID: "A", DisplayName: "A", Role: "thin", RendrCapable: true}
	if err := configstore.Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := Start(ctx, "127.0.0.1:0", path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	resp, err := controlapi.Execute(srv.Addr(), controlapi.Request{
		Args: []string{"local", "identity", "rename", "daemon-a"},
		JSON: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", resp.ExitCode, resp.Stdout, resp.Stderr)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(resp.Stdout), &body); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, resp.Stdout)
	}
	if body["ok"] != true {
		t.Fatalf("unexpected stdout: %s", resp.Stdout)
	}
	after, err := configstore.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Node.DisplayName != "daemon-a" || after.Revision != 1 {
		t.Fatalf("daemon did not own write: name=%s rev=%d", after.Node.DisplayName, after.Revision)
	}
}
