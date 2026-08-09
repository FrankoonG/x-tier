package controlapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultAddr = "127.0.0.1:19090"

type Request struct {
	Args     []string `json:"args"`
	JSON     bool     `json:"json"`
	DryRun   bool     `json:"dry_run"`
	Revision int64    `json:"revision"`
}

type Response struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

func URL(addr string) string {
	if addr == "" {
		addr = DefaultAddr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + addr
}

func Execute(addr string, req Request) (Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(URL(addr)+"/v1/command", "application/json", bytes.NewReader(b))
	if err != nil {
		return Response{}, fmt.Errorf("control.unavailable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("control.http_status: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out Response
	if err := json.Unmarshal(body, &out); err != nil {
		return Response{}, err
	}
	return out, nil
}
