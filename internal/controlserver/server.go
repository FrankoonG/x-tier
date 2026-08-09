package controlserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/FrankoonG/x-tier/internal/cli"
	"github.com/FrankoonG/x-tier/internal/controlapi"
)

type Server struct {
	httpServer *http.Server
	listener   net.Listener
	configPath string
}

func Start(ctx context.Context, addr, configPath string) (*Server, error) {
	if addr == "" {
		addr = controlapi.DefaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{listener: ln, configPath: configPath}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/command", s.handleCommand)
	mux.HandleFunc("/v1/health", s.handleHealth)
	s.httpServer = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The caller observes startup failures synchronously through net.Listen.
		}
	}()
	return s, nil
}

func (s *Server) Addr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Close() error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "addr": s.Addr()})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req controlapi.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	args := []string{"--offline", "--config", s.configPath}
	if req.JSON {
		args = append(args, "--json")
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.Revision >= 0 {
		args = append(args, "--revision", strconv.FormatInt(req.Revision, 10))
	}
	args = append(args, req.Args...)
	var stdout, stderr bytes.Buffer
	code := cli.Run(args, &stdout, &stderr)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(controlapi.Response{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()})
}
