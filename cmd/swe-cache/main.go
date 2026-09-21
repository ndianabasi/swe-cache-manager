package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	swecache "github.com/ndianabasi/swe-cache-manager"
	"github.com/ndianabasi/swe-cache-manager/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.RunWithReadmeContext(ctx, os.Args[1:], os.Stdout, os.Stderr, swecache.Readme))
}
