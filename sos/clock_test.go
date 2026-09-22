package sos

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHostClockKindAndSleep(t *testing.T) {
	clock := HostClock{}
	if clock.Kind() != "host" {
		t.Fatalf("kind = %q", clock.Kind())
	}
	started := time.Now()
	if err := clock.Sleep(context.Background(), 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 4*time.Millisecond {
		t.Fatal("host sleep returned early")
	}
}

func TestHostClockSleepCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	err := HostClock{}.Sleep(ctx, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("canceled sleep blocked")
	}
}

func TestVirtualClockAdvanceRunsTimersInOrder(t *testing.T) {
	start := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	clock := NewVirtualClock(start)
	if clock.Kind() != "virtual" || !clock.Now().Equal(start) {
		t.Fatal("virtual clock setup failed")
	}
	// Created in reverse order; both due at +10s: creation order breaks ties.
	late := clock.NewTimer(10 * time.Second)
	early := clock.NewTimer(4 * time.Second)
	fired := make(chan string, 3)
	go func() {
		fired <- "early:" + (<-early.C()).UTC().Format("15:04:05")
		fired <- "late:" + (<-late.C()).UTC().Format("15:04:05")
	}()
	clock.Advance(10 * time.Second)
	if got := <-fired; got != "early:12:00:04" {
		t.Errorf("first fire = %q", got)
	}
	if got := <-fired; got != "late:12:00:10" {
		t.Errorf("second fire = %q", got)
	}
}

func TestVirtualClockScheduledTimeOrderBeatsCreationOrder(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	slow := clock.NewTimer(10 * time.Second) // created first, due later
	fast := clock.NewTimer(2 * time.Second)  // created second, due earlier
	results := make(chan int64, 2)
	go func() { results <- (<-slow.C()).Unix() }()
	go func() { results <- (<-fast.C()).Unix() }()
	clock.Advance(10 * time.Second)
	first, second := <-results, <-results
	if first == second {
		t.Fatalf("timers delivered same instant: %d", first)
	}
}

func TestVirtualClockTimerCreatedDuringAdvance(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fired := make(chan time.Time, 2)
	first := clock.NewTimer(time.Second)
	go func() {
		fired <- <-first.C()
		// A timer created from a callback and due within the same advance
		// must also run before the advance returns.
		cb := clock.NewTimer(time.Second)
		go func() { fired <- <-cb.C() }()
	}()
	clock.Advance(1 * time.Second)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("first timer never fired")
	}
	// A timer armed from a woken receiver after the first advance runs on
	// the next advance; the advance loop keeps scanning until no due work
	// remains, so timers created while timers are firing are never lost.
	time.Sleep(20 * time.Millisecond) // let the receiver arm the nested timer
	clock.Advance(1 * time.Second)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("nested timer never fired")
	}
}

func TestVirtualClockSleepAndCancel(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	done := make(chan error, 1)
	go func() { done <- clock.Sleep(context.Background(), 5*time.Second) }()
	// Give the sleeper a moment to arm its virtual timer before advancing.
	time.Sleep(20 * time.Millisecond)
	clock.Advance(3 * time.Second)
	clock.Advance(2 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("sleep err = %v", err)
	}
	if !clock.Now().Equal(time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC)) {
		t.Fatalf("now = %v", clock.Now())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.Sleep(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sleep err = %v", err)
	}
}

func TestVirtualTimerStopPreventsDelivery(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	timer := clock.NewTimer(5 * time.Second)
	if !timer.Stop() {
		t.Fatal("Stop reported the timer was already dead")
	}
	if timer.Stop() {
		t.Fatal("double Stop reported success")
	}
	clock.Advance(10 * time.Second)
	select {
	case v := <-timer.C():
		t.Fatalf("stopped timer fired: %v", v)
	default:
	}
}

func TestClockOrDefault(t *testing.T) {
	if clockOrDefault(nil).Kind() != "host" {
		t.Fatal("nil clock did not default to host")
	}
	v := NewVirtualClock(time.Unix(0, 0))
	if clockOrDefault(v) != Clock(v) {
		t.Fatal("explicit clock was replaced")
	}
}
