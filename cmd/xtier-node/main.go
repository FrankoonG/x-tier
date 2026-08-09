package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/controlserver"
	"github.com/FrankoonG/x-tier/internal/node"
)

type fileConfig struct {
	node.Config
	Echo         bool   `json:"echo"`
	Service      string `json:"service"`
	PayloadBytes int    `json:"payload_bytes"`
}

func main() {
	configPath := flag.String("config", "", "node JSON config path")
	coreConfigPath := flag.String("core-config", "", "node core config path for local control")
	controlAddr := flag.String("control", controlapi.DefaultAddr, "local control listen address; use off to disable")
	flag.Parse()
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "--config is required")
		os.Exit(2)
	}
	if *coreConfigPath == "" {
		*coreConfigPath = *configPath
	}
	b, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var cfg fileConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	n, err := node.Start(ctx, cfg.Config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer n.Close()
	var control *controlserver.Server
	if *controlAddr != "" && *controlAddr != "off" {
		control, err = controlserver.Start(ctx, *controlAddr, *coreConfigPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer control.Close()
	}
	service := cfg.Service
	if service == "" && cfg.Echo {
		service = "echo"
	}
	if service != "" {
		go serveService(ctx, n, service, cfg.PayloadBytes)
	}
	controlReady := ""
	if control != nil {
		controlReady = control.Addr()
	}
	fmt.Printf("READY id=%s gateway=%s rendr=%s service=%s control=%s\n", n.ID(), n.GatewayAddr(), n.RendrAddr(), service, controlReady)
	<-ctx.Done()
}

func serveService(ctx context.Context, n *node.Node, service string, payloadBytes int) {
	for {
		c, err := n.RendrListener().Accept(ctx)
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			switch service {
			case "sink":
				_, _ = io.Copy(io.Discard, c)
			case "source":
				_ = writePattern(c, payloadBytes)
				time.Sleep(2 * time.Second)
			default:
				_, _ = io.Copy(c, c)
			}
		}()
	}
}

func writePattern(w io.Writer, n int) error {
	if n <= 0 {
		n = 1 << 20
	}
	buf := make([]byte, 256<<10)
	var written int
	for written < n {
		for i := range buf {
			buf[i] = byte((written + i) % 251)
		}
		chunk := buf
		if rem := n - written; rem < len(chunk) {
			chunk = chunk[:rem]
		}
		m, err := w.Write(chunk)
		written += m
		if err != nil {
			return err
		}
	}
	return nil
}
