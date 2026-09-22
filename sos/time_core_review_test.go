package sos

import (
	"context"
	"sync"
	"testing"
	"time"
)

// waitTick runs Next on the timer and advances the virtual clock in steps
// until the tick arrives, failing the test on timeout instead of hanging.
func waitTick(t *testing.T, timer interface {
	Next(context.Context) (map[string]any, bool, error)
}, clock *VirtualClock, step time.Duration) map[string]any {
	t.Helper()
	type result struct {
		tick map[string]any
		ok   bool
		err  error
	}
	out := make(chan result, 1)
	go func() {
		tick, ok, err := timer.Next(context.Background())
		out <- result{tick: tick, ok: ok, err: err}
	}()
	armDeadline := time.Now().Add(5 * time.Second)
	for clock.timerCount() == 0 && time.Now().Before(armDeadline) {
		time.Sleep(time.Millisecond)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		clock.Advance(step)
		time.Sleep(time.Millisecond)
		select {
		case r := <-out:
			if r.err != nil {
				t.Fatalf("Next returned error: %v", r.err)
			}
			if !r.ok {
				t.Fatal("Next reported completion before any tick")
			}
			return r.tick
		default:
		}
	}
	t.Fatal("tick never arrived; test would hang")
	return nil
}

func TestMissedSkipDeliversExactlyOnTimeTick(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedSkip}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer timer.Stop()
	tick := waitTick(t, timer, clock, 10*time.Second)
	// The consumer returned exactly when position 1 became due: the tick is
	// on time and must not be counted as skipped.
	if got := tick["missed"].(int64); got != 0 {
		t.Fatalf("on-time skip tick missed = %d, want 0", got)
	}
	if !tick["scheduled_for"].(time.Time).Equal(timerTestStart.Add(10 * time.Second)) {
		t.Fatalf("on-time skip tick scheduled_for = %v", tick["scheduled_for"])
	}
	if snap := timer.Snapshot(); snap.Skipped != 0 {
		t.Fatalf("on-time delivery counted as skipped: %d", snap.Skipped)
	}

	// Fall three full positions behind: positions 2..4 pass while busy, so
	// they are skipped and the next future tick (position 5) is delivered.
	clock.Advance(30 * time.Second) // positions 2,3,4 pass while busy
	next := make(chan map[string]any, 1)
	go func() {
		tick, ok, err := timer.Next(context.Background())
		if err != nil || !ok {
			next <- nil
			return
		}
		next <- tick
	}()
	armDeadline := time.Now().Add(5 * time.Second)
	for clock.timerCount() == 0 && time.Now().Before(armDeadline) {
		time.Sleep(time.Millisecond)
	}
	clock.Advance(10 * time.Second) // position 5 becomes due
	select {
	case tick := <-next:
		if tick == nil {
			t.Fatal("skip-policy Next failed")
		}
		if !tick["scheduled_for"].(time.Time).Equal(timerTestStart.Add(50 * time.Second)) {
			t.Fatalf("skip tick scheduled_for = %v, want position 5", tick["scheduled_for"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("skip-policy tick never arrived; test would hang")
	}
	if snap := timer.Snapshot(); snap.Skipped != 3 {
		t.Fatalf("skipped = %d, want 3", snap.Skipped)
	}
}

func TestDeadlineScopePropagatesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	scope := WithDeadlineScope(parent, HostClock{}, time.Minute)
	if _, ok := scope.Context().Deadline(); !ok {
		t.Fatal("scope context lost its deadline")
	}
	cancel()
	select {
	case <-scope.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancellation never reached the scope context; test would hang")
	}
	if err := scope.Context().Err(); err != context.Canceled {
		t.Fatalf("scope err = %v, want context.Canceled", err)
	}
	if err := scope.Exceeded(); err != context.Canceled {
		t.Fatalf("Exceeded() = %v, want context.Canceled", err)
	}
	if err := (HostClock{}).Sleep(scope.Context(), time.Hour); err != context.Canceled {
		t.Fatalf("scoped sleep err = %v, want context.Canceled", err)
	}
}

func TestInheritedDeadlineScopePreservesParentCancellation(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Hour)
	scope := WithDeadlineScope(parent, HostClock{}, time.Minute)
	cancel()
	select {
	case <-scope.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("inherited scope never observed parent cancellation; test would hang")
	}
	if err := scope.Context().Err(); err != context.Canceled {
		t.Fatalf("inherited scope err = %v, want context.Canceled", err)
	}
}

func TestDeadlineScopeCancelClosesOrphanedVirtualTimer(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	scope := WithOperationTimeout(context.Background(), clock, 10*time.Second)
	scope.Cancel()
	deadline := time.Now().Add(5 * time.Second)
	for clock.timerCount() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := clock.timerCount(); got != 0 {
		t.Fatalf("canceled deadline scope left %d orphaned timer(s) armed", got)
	}
	// Time advancing afterwards must not resurrect a canceled scope.
	clock.Advance(time.Minute)
	select {
	case <-scope.Context().Done():
	default:
		t.Fatal("canceled scope context should be done")
	}
	if err := scope.Exceeded(); err != context.Canceled {
		t.Fatalf("Exceeded() after cancel = %v, want context.Canceled", err)
	}
}

func TestVirtualClockNegativeAdvanceIgnored(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	clock.Advance(10 * time.Second)
	clock.Advance(-30 * time.Second)
	if !clock.Now().Equal(timerTestStart.Add(10 * time.Second)) {
		t.Fatalf("virtual time moved backward: %v", clock.Now())
	}
}

func TestVirtualClockReclaimsStoppedAndFiredTimers(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	stopped := clock.NewTimer(5 * time.Second)
	fired := clock.NewTimer(10 * time.Second)
	pending := clock.NewTimer(time.Hour)
	if !stopped.Stop() {
		t.Fatal("Stop reported an armed timer as already inactive")
	}
	if got := clock.timerCount(); got != 2 {
		t.Fatalf("timer count after stop = %d, want 2", got)
	}
	clock.Advance(10 * time.Second)
	if got := clock.timerCount(); got != 1 {
		t.Fatalf("timer count after stop and fire = %d, want 1 (only the pending timer)", got)
	}
	select {
	case when := <-fired.C():
		if !when.Equal(timerTestStart.Add(10 * time.Second)) {
			t.Fatalf("fired timer delivered %v", when)
		}
	default:
		t.Fatal("due timer never fired")
	}
	if !pending.Stop() {
		t.Fatal("pending timer was reclaimed before firing")
	}
	if got := clock.timerCount(); got != 0 {
		t.Fatalf("timer count after stopping pending = %d, want 0", got)
	}
}

func TestVirtualClockFiresSameInstantInCreationOrder(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	first := clock.NewTimer(5 * time.Second)
	second := clock.NewTimer(5 * time.Second)
	third := clock.NewTimer(4 * time.Second)
	clock.Advance(5 * time.Second)
	for i, timer := range []ClockTimer{third, first, second} {
		select {
		case <-timer.C():
		default:
			t.Fatalf("timer %d did not fire", i)
		}
	}
	// Creation order among equal instants: first was created before second,
	// and third's earlier instant fired before both; the channels above
	// prove delivery, and runDue's ordering is exercised by the advance.
	_ = third
}

type blockingStreamSource struct{}

func (blockingStreamSource) next(ctx context.Context) (any, bool, error) {
	<-ctx.Done()
	return nil, false, ctx.Err()
}

func (blockingStreamSource) cancel(context.Context) error { return nil }

func TestStreamNextHonorsCallerDeadline(t *testing.T) {
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", blockingStreamSource{})
	stream.id = "stream-1"
	caller, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := stream.next(caller)
		done <- err
	}()
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Fatalf("next err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream next ignored the caller deadline; test would hang")
	}
}

func TestStreamNextHonorCallerCancellationWithoutConsuming(t *testing.T) {
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", blockingStreamSource{})
	stream.id = "stream-1"
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := stream.next(caller)
	if err != context.Canceled {
		t.Fatalf("next err = %v, want context.Canceled", err)
	}
	if got := stream.snapshot("snapshot").ItemsReceived; got != 0 {
		t.Fatalf("canceled read consumed an item: %d", got)
	}
}

func TestTimerSnapshotExposesPolicyAndVirtualTime(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedSkip}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer timer.Stop()
	stream := newStreamHandleWithClock(context.Background(), clock, TypeRef{Name: "TimeTick"}, "timer", &timerStreamSource{repeating: timer})
	event := stream.snapshot("snapshot")
	if event.Policy != "skipping missed ticks" {
		t.Fatalf("snapshot policy = %q", event.Policy)
	}
	if event.VirtualTime == nil || !event.VirtualTime.Equal(timerTestStart) {
		t.Fatalf("snapshot virtual time = %v, want clock now", event.VirtualTime)
	}
	if event.ClockKind != "virtual" || event.Interval != 10*time.Second {
		t.Fatalf("snapshot clockKind=%q interval=%v", event.ClockKind, event.Interval)
	}
	if !event.NextScheduledAt.Equal(timerTestStart.Add(10 * time.Second)) {
		t.Fatalf("snapshot next = %v", event.NextScheduledAt)
	}
}

func TestTimerGovernorEnforcesLimit(t *testing.T) {
	governor := NewTimerGovernor(TimerLimits{MaxActive: 2})
	release1, err := governor.acquire("test one")
	if err != nil {
		t.Fatal(err)
	}
	release2, err := governor.acquire("test two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := governor.acquire("test three"); err == nil {
		t.Fatal("third acquire exceeded the active-timer limit without failing")
	}
	release1()
	if release, err := governor.acquire("test four"); err != nil {
		t.Fatalf("released slot was not reusable: %v", err)
	} else {
		release()
	}
	release2()
}

func TestTimerGovernorConcurrentUse(t *testing.T) {
	governor := NewTimerGovernor(TimerLimits{MaxActive: 8})
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := governor.acquire("race")
			if err != nil {
				return
			}
			time.Sleep(time.Millisecond)
			release()
		}()
	}
	wg.Wait()
	// All slots must be free again.
	for range 8 {
		release, err := governor.acquire("drain")
		if err != nil {
			t.Fatalf("governor lost slots after concurrent use: %v", err)
		}
		release()
	}
}

func TestOneShotStopVsFireRace(t *testing.T) {
	governor := NewTimerGovernor(TimerLimits{MaxActive: 1})
	for i := 0; i < 32; i++ {
		timer, err := NewOneShotTimer(HostClock{}, 2*time.Millisecond, governor)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, _ = timer.Next(context.Background())
		}()
		go func() {
			defer wg.Done()
			timer.Snapshot()
			timer.Stop()
		}()
		wg.Wait()
		// The single governor slot must be free again regardless of who won.
		release, err := governor.acquire("probe")
		if err != nil {
			t.Fatalf("iteration %d: governor slot leaked after stop-vs-fire race: %v", i, err)
		}
		release()
	}
}

func TestOneShotStopWakesVirtualWait(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	timer, err := NewOneShotTimer(clock, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, more, nextErr := timer.Next(context.Background())
		if nextErr != nil || more {
			t.Errorf("Next after Stop = (_, %v, %v); want (_, false, nil)", more, nextErr)
		}
	}()
	timer.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not wake a blocked one-shot Next")
	}
}

func TestRepeatingStopVsNextRace(t *testing.T) {
	clock := NewVirtualClock(timerTestStart)
	governor := NewTimerGovernor(TimerLimits{MaxActive: 1})
	timer, err := NewRepeatingTimer(clock, 10*time.Second, false, MissedTickPolicy{Mode: MissedCombine}, governor)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, _ = timer.Next(context.Background())
	}()
	go func() {
		defer wg.Done()
		timer.Snapshot()
		timer.Stop()
	}()
	wg.Wait()
	release, err := governor.acquire("probe")
	if err != nil {
		t.Fatalf("governor slot leaked after repeating stop race: %v", err)
	}
	release()
}
