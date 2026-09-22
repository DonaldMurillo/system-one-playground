package sos

import (
	"context"
	"time"
)

// Clock is the single timing boundary for all language-visible behavior:
// current instants, one-shot timers, and cancelable waiting. Host and virtual
// (deterministic test) implementations are interchangeable, so no language
// semantics may call the host clock directly.
type Clock interface {
	// Now returns the current instant on this clock.
	Now() time.Time
	// NewTimer arms a one-shot timer for d on this clock.
	NewTimer(d time.Duration) ClockTimer
	// Sleep blocks until d elapsed on this clock, the context is canceled, or
	// the context deadline passes. A canceled context returns ctx.Err().
	Sleep(ctx context.Context, d time.Duration) error
	// Kind reports "host" or "virtual" for tooling and traces.
	Kind() string
}

// ClockTimer is a cancelable one-shot timer armed by a Clock.
type ClockTimer interface {
	// C delivers the scheduled instant exactly once when the timer fires.
	C() <-chan time.Time
	// Stop prevents delivery and reports whether the timer was still armed.
	Stop() bool
}

// HostClock reads the operating-system wall clock and monotonic timers.
type HostClock struct{}

func (HostClock) Now() time.Time { return time.Now() }

func (HostClock) NewTimer(d time.Duration) ClockTimer {
	return &hostTimer{t: time.NewTimer(d)}
}

func (HostClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := (HostClock{}).NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		// Inherited parent deadlines and cancellation keep their original
		// error and frame; they are not reinterpreted as time failures.
		return ctx.Err()
	}
}

func (HostClock) Kind() string { return "host" }

type hostTimer struct {
	t *time.Timer
}

func (h *hostTimer) C() <-chan time.Time { return h.t.C }

func (h *hostTimer) Stop() bool { return h.t.Stop() }

// waitClockUntil waits monotonically for the remaining distance to a target
// instant already resolved once from this clock. Waiting until a past
// timestamp completes immediately.
func waitClockUntil(ctx context.Context, clock Clock, target time.Time) error {
	remaining := target.Sub(clock.Now())
	if remaining <= 0 {
		return nil
	}
	return clock.Sleep(ctx, remaining)
}

// clockOrDefault returns the host clock for nil so embedders can omit one.
func clockOrDefault(clock Clock) Clock {
	if clock == nil {
		return HostClock{}
	}
	return clock
}
