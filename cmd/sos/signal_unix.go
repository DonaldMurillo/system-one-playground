//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func commandSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		// Restore the platform's default handling so a second signal can force
		// termination if a producer ignores graceful cancellation.
		stop()
	}()
	return ctx, stop
}
