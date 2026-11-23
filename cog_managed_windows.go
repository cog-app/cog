//go:build windows

package cog

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"golang.org/x/sys/windows/svc"
)

// runManaged runs run until Ctrl+C, or, when the Windows service manager
// started the process, until it asks the service to stop.
func runManaged(ctx context.Context, run func(context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("ask whether this is a service: %w", err)
	}
	if !isService {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
		return run(ctx)
	}
	h := &serviceHandler{ctx: ctx, run: run}
	// The name is ignored for a service in a process of its own.
	if err := svc.Run("", h); err != nil {
		return fmt.Errorf("run as a service: %w", err)
	}
	return h.err
}

type serviceHandler struct {
	ctx context.Context
	run func(context.Context) error
	err error
}

func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	s <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case err := <-done:
			h.err = err
			s <- svc.Status{State: svc.StopPending}
			if err != nil || h.ctx.Err() != nil {
				// Stopped by an error, or by the restart an update asks
				// for (which ends h.ctx): a non-zero exit code is a
				// failure, after which the recovery actions setup
				// configured start the service again.
				return true, 1
			}
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				h.err = <-done
				return false, 0
			}
		}
	}
}
