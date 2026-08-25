package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/FrankoonG/x-tier/internal/controlapi"
	"github.com/FrankoonG/x-tier/internal/daemon"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("xtierd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", os.Getenv("XTIER_CONFIG"), "config path")
	controlAddr := fs.String("control", controlapi.DefaultAddr, "loopback control listen address")
	webAddr := fs.String("web", os.Getenv("XTIER_WEB_ADDR"), "optional loopback web listen address")
	webRoot := fs.String("web-root", os.Getenv("XTIER_WEB_ROOT"), "built frontend directory served by --web")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "xtierd does not accept positional arguments")
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "--config is required")
		return 2
	}
	if *webRoot != "" && *webAddr == "" {
		fmt.Fprintln(stderr, "--web-root requires --web")
		return 2
	}
	d, err := daemon.Start(ctx, daemon.Options{
		ConfigPath:  *configPath,
		ControlAddr: *controlAddr,
		WebAddr:     *webAddr,
		WebRoot:     *webRoot,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "READY control=%s", d.Addr())
	if d.WebAddr() != "" {
		fmt.Fprintf(stdout, " web=%s", d.WebAddr())
	}
	fmt.Fprintf(stdout, " config=%s\n", d.ConfigPath())
	if err := d.Wait(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
