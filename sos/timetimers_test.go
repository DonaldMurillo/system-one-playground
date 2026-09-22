package sos

import (
	"context"
	"errors"
	"testing"
	"time"
)

var timerTestStart = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func consumeTick(t *testing.T, timer interface {
	Next(context.Context) (map[string]any, bool, error)
}, clock *VirtualClock, wait time.Duration) map[string]any {
	t.Helper()
	type result struct {
		tick map[string]any
		err  error
	}
	out := make(chan result, 1)
	go func() {
		tick, _, err := timer.Next(context.Background())
		out <- result{tick, err}
	}()
	advanced := time.Duration(0)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case r := <-out:
			if r.err != nil {
				t.Fatalf("Next: %v", r.err)
			}
			return r.tick
		default:
		}
		step := 50 * time.Millisecond
		if advanced >= wait {
			step = time.Millisecond // keep probing past the target only
			// marginally so position math stays predictable
		} else if wait-advanced < step {
			step = wait - advanced
		}
		clock.Advance(step)
		advanced += step
		time.Sleep(time.Millisecond)
	}
	t.Fatal("tick never arrived")
	return nil
}

func TestOneShotTimerEmitsOnceThenCompletes(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewOneShotTimer(clock, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	tick := consumeTick(t, timer, clock, 30*time.Second)
	if tick["sequence"] != int64(1) {
		t.Fatalf("sequence = %v", tick["sequence"])
	}
	if tick["missed"] != int64(0) {
		t.Fatalf("missed = %v", tick["missed"])
	}
	if !tick["scheduled_for"].(time.Time).Equal(timerTestStart.Add(30 * time.Second)) {
		t.Fatalf("scheduled_for = %v", tick["scheduled_for"])
	}
	if !tick["observed_at"].(time.Time).Equal(timerTestStart.Add(30 * time.Second)) {
		t.Fatalf("observed_at = %v", tick["observed_at"])
	}
	// One tick later the stream is complete and nothing else emits.
	done := make(chan bool, 1)
	go func() {
		_, more, err := timer.Next(context.Background())
		if err != nil {
			t.Error(err)
		}
		done <- !more
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("one-shot timer reported more ticks")
		}
	case <-time.After(time.Second):
		t.Fatal("completion never reported")
	}
}

func TestRepeatingTimerStaysAnchoredDespiteSlowHandler(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedCombine}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Handler takes four seconds: 00:00 open, ticks must land on 00:10,
	// 00:20, 00:30 — never 00:00, 00:14, 00:28.
	for i := 1; i <= 3; i++ {
		tick := consumeTick(t, timer, clock, 10*time.Second)
		want := timerTestStart.Add(time.Duration(i) * 10 * time.Second)
		if !tick["scheduled_for"].(time.Time).Equal(want) {
			t.Fatalf("tick %d scheduled_for = %v; want %v", i, tick["scheduled_for"], want)
		}
		if tick["missed"] != int64(0) {
			t.Fatalf("tick %d missed = %v", i, tick["missed"])
		}
		// Simulate four seconds of handler work.
		clock.Advance(4 * time.Second)
	}
	snap := timer.Snapshot()
	if snap.Emitted != 3 || snap.Missed != 0 || snap.Active != true {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestRepeatingTimerImmediateFirstTick(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, true, MissedTickPolicy{Mode: MissedCombine}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tick := consumeTick(t, timer, clock, 0)
	if !tick["scheduled_for"].(time.Time).Equal(timerTestStart) {
		t.Fatalf("immediate tick scheduled_for = %v", tick["scheduled_for"])
	}
}

func TestRepeatingTimerCombinePolicy(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedCombine}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Consumer idle for 35 seconds: positions 10s, 20s, 30s passed. One
	// pending tick retains the latest position and reports two missed.
	clock.Advance(35 * time.Second)
	tick := consumeTick(t, timer, clock, 0)
	if !tick["scheduled_for"].(time.Time).Equal(timerTestStart.Add(30 * time.Second)) {
		t.Fatalf("scheduled_for = %v", tick["scheduled_for"])
	}
	if tick["missed"] != int64(2) {
		t.Fatalf("missed = %v; want 2", tick["missed"])
	}
	snap := timer.Snapshot()
	if snap.Missed != 2 || snap.Combined != 2 || snap.Emitted != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	// Next tick is the next future position (40s), not a replay.
	next := consumeTick(t, timer, clock, 5*time.Second)
	if !next["scheduled_for"].(time.Time).Equal(timerTestStart.Add(40 * time.Second)) {
		t.Fatalf("next scheduled_for = %v", next["scheduled_for"])
	}
}

func TestRepeatingTimerSkipPolicy(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedSkip}, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(35 * time.Second)
	// Skipping emits only the next future tick: 40s, with 10/20/30 skipped.
	tick := consumeTick(t, timer, clock, 5*time.Second)
	if !tick["scheduled_for"].(time.Time).Equal(timerTestStart.Add(40 * time.Second)) {
		t.Fatalf("scheduled_for = %v", tick["scheduled_for"])
	}
	if tick["missed"] != int64(0) {
		t.Fatalf("missed = %v", tick["missed"])
	}
	snap := timer.Snapshot()
	if snap.Skipped != 3 || snap.Emitted != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestRepeatingTimerBoundedCatchUpPolicy(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedCatchUp, Max: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Idle 35s: positions 10/20/30 passed. Catch up at most 2: replay 20 and
	// 30, combining the missed 10-position into the final delivered tick.
	clock.Advance(35 * time.Second)
	first := consumeTick(t, timer, clock, 0)
	if !first["scheduled_for"].(time.Time).Equal(timerTestStart.Add(20 * time.Second)) {
		t.Fatalf("first catch-up scheduled_for = %v", first["scheduled_for"])
	}
	second := consumeTick(t, timer, clock, 0)
	if !second["scheduled_for"].(time.Time).Equal(timerTestStart.Add(30 * time.Second)) {
		t.Fatalf("second catch-up scheduled_for = %v", second["scheduled_for"])
	}
	if second["missed"] != int64(1) {
		t.Fatalf("final catch-up missed = %v; want 1", second["missed"])
	}
	snap := timer.Snapshot()
	if snap.CaughtUp != 2 || snap.Missed != 1 || snap.Combined != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestRepeatingTimerCancellation(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedCombine}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := timer.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// Stopped timers never emit after terminal state.
	timer.Stop()
	clock.Advance(60 * time.Second)
	_, more, err := timer.Next(context.Background())
	if err != nil || more {
		t.Fatalf("stopped timer Next = %v, %v", more, err)
	}
	if timer.Snapshot().Active {
		t.Fatal("stopped timer still active")
	}
}

func TestTimerLimitsEnforced(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	governor := NewTimerGovernor(TimerLimits{MaxActive: 1})
	heldFirst, err := NewOneShotTimer(clock, time.Second, governor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRepeatingTimer(clock, time.Second, false, MissedTickPolicy{}, governor)
	var typed *typedFailure
	if !errors.As(err, &typed) || typed.kind != "TimerLimitExceeded" {
		t.Fatalf("err = %#v; want TimerLimitExceeded", err)
	}
	// Releasing the active slot admits a new timer.
	heldFirst.Stop()
	clock2 := NewVirtualClock(timerTestStart)
	held, err := NewOneShotTimer(clock2, time.Second, governor)
	if err != nil {
		t.Fatal(err)
	}
	held.Stop()
	if _, err := NewOneShotTimer(clock2, time.Second, governor); err != nil {
		t.Fatalf("released slot not reusable: %v", err)
	}
	// Catch-up bounds above the pending limit fail visibly.
	_, err = NewRepeatingTimer(clock2, time.Second, false, MissedTickPolicy{Mode: MissedCatchUp, Max: 5000}, NewTimerGovernor(TimerLimits{}))
	if !errors.As(err, &typed) || typed.kind != "TimerLimitExceeded" {
		t.Fatalf("catch-up bound err = %#v", err)
	}
	// Invalid intervals and policies fail validation.
	if _, err := NewRepeatingTimer(clock2, 0, false, MissedTickPolicy{}, nil); err == nil {
		t.Fatal("zero interval accepted")
	}
	if _, err := NewRepeatingTimer(clock2, time.Second, false, MissedTickPolicy{Mode: MissedCatchUp}, nil); err == nil {
		t.Fatal("unbounded catch-up accepted")
	}
}
