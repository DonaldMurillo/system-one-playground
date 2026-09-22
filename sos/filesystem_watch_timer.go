//go:build !linux && !darwin && !windows

package sos

import (
	"sync"
	"time"
)

// Platforms without one of the native notification backends keep the
// original contract with a timer that emits periodic triggers. Everything
// downstream — reconciliation scans, diffing, batching, overflow — is shared
// with the native backends, so only the trigger source differs.

type timerWatchBackend struct {
	interval  time.Duration
	triggers  chan nativeWatchEvent
	stop      chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once
}

func newNativeWatchBackend(_ traversalSpec, interval time.Duration, _ map[string]watchEntry) (nativeWatchBackend, error) {
	backend := &timerWatchBackend{
		interval: interval,
		triggers: make(chan nativeWatchEvent, 1),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go backend.tick()
	return backend, nil
}

func (b *timerWatchBackend) tick() {
	defer close(b.stopped)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			return
		case b.triggers <- nativeWatchEvent{}:
		}
	}
}

func (b *timerWatchBackend) events() <-chan nativeWatchEvent { return b.triggers }

// sync is a no-op: polling observes every snapshot identity regardless of
// registration.
func (b *timerWatchBackend) sync(traversalSpec, map[string]watchEntry) error { return nil }

func (b *timerWatchBackend) close() error {
	b.closeOnce.Do(func() {
		close(b.stop)
		<-b.stopped
	})
	return nil
}
