//go:build !windows

package cog

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// runManaged runs run until an interrupt or SIGTERM, which is how systemd
// and launchd stop a service.
func runManaged(ctx context.Context, run func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}
