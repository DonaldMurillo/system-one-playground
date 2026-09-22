//go:build windows

package main

import (
	"context"
	"os"
	"os/signal"
)

func commandSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}
