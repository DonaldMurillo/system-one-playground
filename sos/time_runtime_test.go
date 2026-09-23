package sos

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func runWithVirtualClock(t *testing.T, source string, clock *VirtualClock) (<-chan error, *bytes.Buffer) {
	t.Helper()
	program, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatalf("parse diagnostics: %+v", diagnostics)
	}
	output := &bytes.Buffer{}
	done := make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), program, Options{Clock: clock, Stdout: output})
		done <- err
	}()
	return done, output
}

func advanceUntilDone(t *testing.T, clock *VirtualClock, done <-chan error, step time.Duration, attempts int) error {
	t.Helper()
	// The interpreter starts on another goroutine. Do not advance past its
	// deadline before it has actually registered the first timer: race builds
	// on shared CI runners can take longer than the old 10 ms total allowance.
	ready := time.NewTimer(2 * time.Second)
	defer ready.Stop()
	for clock.timerCount() == 0 {
		select {
		case err := <-done:
			return err
		case <-ready.C:
			t.Fatal("virtual-time run did not register a timer")
		case <-time.After(time.Millisecond):
		}
	}
	for range attempts {
		clock.Advance(step)
		select {
		case err := <-done:
			return err
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("virtual-time run did not complete")
	return nil
}

func TestRuntimeOneShotTimerUsesInjectedClock(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	done, output := runWithVirtualClock(t, `stream one tick after 5 seconds called timer
for each tick from timer:
  show tick.sequence
`, clock)
	if err := advanceUntilDone(t, clock, done, 5*time.Second, 10); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != "1" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRuntimeRepeatingTimerStopsOnlyOwnedStream(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	done, output := runWithVirtualClock(t, `stream a tick every 10 seconds called ticks
  combining missed ticks
for each tick from ticks:
  show tick.sequence
  stop stream ticks
show "complete"
`, clock)
	if err := advanceUntilDone(t, clock, done, 10*time.Second, 10); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "1\n") || !strings.Contains(got, "complete\n") {
		t.Fatalf("output = %q", got)
	}
}

func TestRuntimeScopedDeadlineSuppressesStaleBindings(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	done, _ := runWithVirtualClock(t, `allow at most 5 seconds for:
  wait for 10 seconds
  remember "stale" called result
`, clock)
	err := advanceUntilDone(t, clock, done, 5*time.Second, 10)
	var failure *typedFailure
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("want deadline failure, got %T %v", err, err)
	}
	_ = failure
}
