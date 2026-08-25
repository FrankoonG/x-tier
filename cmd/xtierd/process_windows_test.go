//go:build windows

package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestXTierdGracefulCtrlBreak(t *testing.T) {
	if os.Getenv("XTIERD_SIGNAL_HELPER") == "1" {
		os.Args = []string{
			"xtierd",
			"--config", os.Getenv("XTIERD_SIGNAL_CONFIG"),
			"--control", "127.0.0.1:0",
		}
		main()
		return
	}

	configPath := filepath.Join(t.TempDir(), "xtier.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestXTierdGracefulCtrlBreak$")
	cmd.Env = append(os.Environ(),
		"XTIERD_SIGNAL_HELPER=1",
		"XTIERD_SIGNAL_CONFIG="+configPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = cmd.Process.Kill()
			<-wait
		}
	})

	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "READY ") {
				ready <- line
				return
			}
		}
		ready <- ""
	}()

	select {
	case line := <-ready:
		if line == "" {
			select {
			case err := <-wait:
				exited = true
				t.Fatalf("xtierd exited before readiness: %v stderr=%s", err, stderr.String())
			default:
				t.Fatalf("xtierd closed stdout before readiness")
			}
		}
	case err := <-wait:
		exited = true
		t.Fatalf("xtierd exited before readiness: %v stderr=%s", err, stderr.String())
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		err := <-wait
		exited = true
		t.Fatalf("xtierd readiness timeout: %v stderr=%s", err, stderr.String())
	}

	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-wait:
		exited = true
		if err != nil {
			t.Fatalf("xtierd did not exit cleanly after Ctrl+Break: %v stderr=%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		err := <-wait
		exited = true
		t.Fatalf("xtierd did not exit after Ctrl+Break: %v stderr=%s", err, stderr.String())
	}
}
