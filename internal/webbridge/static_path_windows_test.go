//go:build windows

package webbridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsStaticPathValidation(t *testing.T) {
	for _, name := range []string{
		"app.js.",
		"app.js ",
		"index.html::$DATA",
		"C:/Windows/win.ini",
		"NUL",
		"con.txt",
		"COM1",
		"dir/PRN.css",
		"dir//app.js",
		"dir/../app.js",
		"question?.js",
	} {
		if validStaticPath(name) {
			t.Errorf("validStaticPath(%q)=true, want false", name)
		}
	}
	for _, name := range []string{"index.html", "assets/app.js", "com10.txt", "null.js"} {
		if !validStaticPath(name) {
			t.Errorf("validStaticPath(%q)=false, want true", name)
		}
	}
}

func TestWindowsStaticHandlerRejectsFilesystemAliases(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("private-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(staticDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	server := &Server{staticRoot: root}

	for _, target := range []string{
		"/app.js.",
		"/app.js%20",
		"/index.html::$DATA",
		"/C:/Windows/win.ini",
		"/NUL",
		"/con.txt",
		"/dir/PRN.css",
	} {
		t.Run(target, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			response := httptest.NewRecorder()
			server.serveStatic(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if response.Body.String() == "private-index" {
				t.Fatal("invalid request target exposed a static file")
			}
		})
	}
}
