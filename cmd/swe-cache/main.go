package main

import (
	"os"

	"github.com/ndianabasi/swe-cache-manager/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
