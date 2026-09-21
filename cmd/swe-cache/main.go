package main

import (
	"os"

	swecache "github.com/ndianabasi/swe-cache-manager"
	"github.com/ndianabasi/swe-cache-manager/internal/cli"
)

func main() {
	os.Exit(cli.RunWithReadme(os.Args[1:], os.Stdout, os.Stderr, swecache.Readme))
}
