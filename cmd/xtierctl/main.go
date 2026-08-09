package main

import (
	"os"

	"github.com/FrankoonG/x-tier/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
