package sos

import (
	"context"
	"sync"
	"time"
)

// TimeTick is the language-visible record delivered by timer streams.
// sequence begins at one; scheduled_for is the anchored target instant;
// observed_at is when the runtime admitted the tick; missed counts schedule
// positions combined into this delivery (zero for an on-time tick).
func newTimeTick(sequence int64, scheduledFor, observedAt time.Time, missed int64) map[string]any {
	return map[string]any{
		"sequence":      sequence,
		"scheduled_for": scheduledFor,
		"observed_at":   observedAt,
		"missed":        missed,
	}
}

// MissedTickPolicy controls delivery when a repeating timer's consumer falls
// behind. The default combines all missed positions into one tick. Skipping
// emits only the next future tick. Bounded catch-up replays at most Max
// historical ticks and then combines the remainder into the final one. There
// is deliberately no unbounded catch-up mode.
type MissedTickPolicy struct {
	Mode MissedTickMode
	Max  int64
}

type MissedTickMode int

const (
	MissedCombine MissedTickMode = iota
	MissedSkip
	MissedCatchUp
)

// TimerLimits bounds timers per run so timer state can never grow
// unboundedly. Zero values fall back to conservative defaults; exceeding a
// bound is a visible typed failure, never arbitrary eviction.
type TimerLimits struct {
	MaxActive         int
	MaxPendingCatchUp int64
}

const (
	defaultMaxActiveTimers   = 1024
	defaultMaxPendingCatchUp = 1024
)

func (l TimerLimits) active() int {
	if l.MaxActive <= 0 {
		return defaultMaxActiveTimers
	}
	return l.MaxActive
}

func (l TimerLimits) pendingCatchUp() int64 {
	if l.MaxPendingCatchUp <= 0 {
		return defaultMaxPendingCatchUp
	}
	return l.MaxPendingCatchUp
}

// TimerGovernor enforces the run-wide timer bounds: active timers per run and
// pending catch-up ticks. Every timer constructor draws from the same
// governor so exceeding a bound fails visibly and nothing is evicted.
type TimerGovernor struct {
	mu     sync.Mutex
	limits TimerLimits
	active int
}

// NewTimerGovernor creates the run-wide governor. Zero limits fall back to
// conservative defaults.
func NewTimerGovernor(limits TimerLimits) *TimerGovernor {
	return &TimerGovernor{limits: limits}
}

func (g *TimerGovernor) acquire(operation string) (release func(), err error) {
	maxActive := g.limits.active()
	g.mu.Lock()
	if g.active >= maxActive {
		g.mu.Unlock()
		return nil, timerLimitExceeded(maxActive, operation)
	}
	g.active++
	g.mu.Unlock()
	var released bool
	return func() {
		g.mu.Lock()
		if !released {
			released = true
			g.active--
		}
		g.mu.Unlock()
	}, nil
}

// OneShotTimer emits exactly one TimeTick then completes. Unlike a wait it
// can be inspected and canceled independently.
type OneShotTimer struct {
	clock   Clock
	when    time.Time
	deliver func() // releases the governor slot
	stopCh  chan struct{}
	mu      sync.Mutex
	stopped bool
}

// NewOneShotTimer arms a one-shot timer firing after d on the given clock.
func NewOneShotTimer(clock Clock, d time.Duration, governor *TimerGovernor) (*OneShotTimer, error) {
	clock = clockOrDefault(clock)
	if d < 0 {
		return nil, invalidDuration(d.String(), "timer delay must not be negative")
	}
	if governor == nil {
		governor = NewTimerGovernor(TimerLimits{})
	}
	release, err := governor.acquire("one-shot timer")
	if err != nil {
		return nil, err
	}
	return &OneShotTimer{clock: clock, when: clock.Now().Add(d), deliver: release, stopCh: make(chan struct{})}, nil
}

// Next blocks until the tick is due. It returns the tick once, then reports
// stream completion. Cancellation propagates the context error unchanged.
func (t *OneShotTimer) Next(ctx context.Context) (map[string]any, bool, error) {
	t.mu.Lock()
	stopped := t.stopped
	t.mu.Unlock()
	if stopped {
		return nil, false, nil
	}
	delay := t.when.Sub(t.clock.Now())
	if delay > 0 {
		timer := t.clock.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C():
		case <-t.stopCh:
			return nil, false, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		// Stop won the race while the wait ran: no tick after terminal state.
		return nil, false, nil
	}
	t.stopped = true
	close(t.stopCh)
	if t.deliver != nil {
		t.deliver()
	}
	return newTimeTick(1, t.when, t.clock.Now(), 0), true, nil
}

func (t *OneShotTimer) Snapshot() TimerSnapshot {
	t.mu.Lock()
	active := !t.stopped
	t.mu.Unlock()
	return TimerSnapshot{Kind: "one-shot", Clock: t.clock.Kind(), Next: t.when, Active: active}
}

// Stop cancels the timer before any tick is delivered; timers never emit
// after terminal state.
func (t *OneShotTimer) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.stopped = true
	close(t.stopCh)
	if t.deliver != nil {
		t.deliver()
	}
}

// TimerSnapshot is a non-consuming inspection record for tooling: it never
// advances a virtual clock, fires a timer, or consumes a tick.
type TimerSnapshot struct {
	Kind       string        `json:"kind"`
	Clock      string        `json:"clock"`
	Interval   time.Duration `json:"interval,omitempty"`
	Policy     string        `json:"policy,omitempty"`
	Next       time.Time     `json:"next,omitempty"`
	Sequence   int64         `json:"sequence"`
	Emitted    int64         `json:"emitted"`
	Missed     int64         `json:"missed"`
	Combined   int64         `json:"combined"`
	Skipped    int64         `json:"skipped"`
	CaughtUp   int64         `json:"caughtUp"`
	Active     bool          `json:"active"`
	AnchoredAt time.Time     `json:"anchoredAt,omitempty"`
}

type RepeatingTimer struct {
	clock    Clock
	interval time.Duration
	anchor   time.Time
	next     int64 // index of the next schedule position to deliver
	first    int64 // first deliverable position (0 when immediate, else 1)
	policy   MissedTickPolicy
	seq      int64
	emitted  int64
	missed   int64
	combined int64
	skipped  int64
	caughtUp int64
	// bounded catch-up policy, in schedule order, each with the count of
	// older positions combined into it (nonzero only on the final one).
	pendingCatchUp []pendingCatchUpTick
	mu             sync.Mutex
	stopped        bool
	stopCh         chan struct{}
	deliver        func()
	governor       *TimerGovernor
}

type pendingCatchUpTick struct {
	scheduled time.Time
	missed    int64
}

// NewRepeatingTimer creates an anchored timer with the given interval. When
// immediate is true the first tick is delivered right away ("now and every");
// otherwise the first tick occurs after one complete interval.
func NewRepeatingTimer(clock Clock, interval time.Duration, immediate bool, policy MissedTickPolicy, governor *TimerGovernor) (*RepeatingTimer, error) {
	clock = clockOrDefault(clock)
	if governor == nil {
		governor = NewTimerGovernor(TimerLimits{})
	}
	if interval <= 0 {
		return nil, invalidDuration(interval.String(), "timer interval must be positive")
	}
	if policy.Mode == MissedCatchUp && policy.Max <= 0 {
		return nil, invalidSchedule("bounded catch-up requires a positive tick bound")
	}
	if max := governor.limits.pendingCatchUp(); policy.Mode == MissedCatchUp && policy.Max > max {
		return nil, timerLimitExceeded(int(max), "pending catch-up ticks")
	}
	release, err := governor.acquire("repeating timer")
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	anchor := now
	next := int64(1)
	first := int64(1)
	if immediate {
		next, first = 0, 0
	}
	return &RepeatingTimer{
		clock: clock, interval: interval, anchor: anchor, next: next, first: first,
		policy: policy, stopCh: make(chan struct{}), deliver: release, governor: governor,
	}, nil
}

func (t *RepeatingTimer) position(k int64) time.Time {
	return t.anchor.Add(time.Duration(k) * t.interval)
}

// lastPassed returns the greatest schedule position whose instant is at or
// before now.
func (t *RepeatingTimer) lastPassed(now time.Time) int64 {
	k := int64(now.Sub(t.anchor) / t.interval)
	if k < 0 {
		k = 0
	}
	return k
}

// waitPosition waits until position k's instant has arrived.
func (t *RepeatingTimer) waitPosition(ctx context.Context, k int64) error {
	delay := t.position(k).Sub(t.clock.Now())
	if delay <= 0 {
		return nil
	}
	timer := t.clock.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case <-t.stopCh:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Next delivers the next tick according to the missed-tick policy. It returns
// (tick, more, error); more is false only after Stop.
func (t *RepeatingTimer) Next(ctx context.Context) (map[string]any, bool, error) {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return nil, false, nil
	}
	// Bounded catch-up first drains queued historical positions in order.
	if len(t.pendingCatchUp) > 0 {
		pending := t.pendingCatchUp[0]
		t.pendingCatchUp = t.pendingCatchUp[1:]
		t.seq++
		t.caughtUp++
		t.emitted++
		t.mu.Unlock()
		return newTimeTick(t.seq, pending.scheduled, t.clock.Now(), pending.missed), true, nil
	}
	k := t.next
	t.mu.Unlock()
	if err := t.waitPosition(ctx, k); err != nil {
		return nil, false, err
	}
	now := t.clock.Now()
	last := t.lastPassed(now)
	switch t.policy.Mode {
	case MissedCombine:
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.stopped {
			return nil, false, nil
		}
		if last > k {
			t.missed += last - k
			t.combined += last - k
			k = last
		}
		t.next = k + 1
		missed := k - t.first - t.seq
		if missed < 0 {
			missed = 0
		}
		t.seq++
		t.emitted++
		return newTimeTick(t.seq, t.position(k), now, missed), true, nil
	case MissedSkip:
		// Skipping emits only the next future tick: every position beyond
		// the one waited for that passed while the consumer was busy is
		// dropped and counted. A tick caught exactly on time is delivered,
		// never skipped.
		for {
			if last > k {
				t.mu.Lock()
				t.skipped += last - k + 1
				t.mu.Unlock()
				k = last + 1
			}
			if err := t.waitPosition(ctx, k); err != nil {
				return nil, false, err
			}
			now = t.clock.Now()
			last = t.lastPassed(now)
			if last <= k {
				break
			}
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.stopped {
			return nil, false, nil
		}
		t.next = k + 1
		t.seq++
		t.emitted++
		return newTimeTick(t.seq, t.position(k), now, 0), true, nil
	case MissedCatchUp:
		// Queue up to Max historical positions ending at the latest passed
		// one; anything older is combined into the final queued tick.
		t.mu.Lock()
		if t.stopped {
			t.mu.Unlock()
			return nil, false, nil
		}
		t.next = last + 1
		oldest := k
		if last-k+1 > t.policy.Max {
			oldest = last - t.policy.Max + 1
		}
		combined := oldest - k
		if combined > 0 {
			t.missed += combined
			t.combined += combined
		}
		for i := oldest; i <= last; i++ {
			miss := int64(0)
			if i == last && combined > 0 {
				miss = combined
			}
			t.pendingCatchUp = append(t.pendingCatchUp, pendingCatchUpTick{scheduled: t.position(i), missed: miss})
		}
		t.mu.Unlock()
		return t.Next(ctx)
	}
	return nil, false, invalidSchedule("unknown missed-tick policy")
}

// Stop cancels the timer before reporting completion; callbacks cannot emit
// after terminal state.
func (t *RepeatingTimer) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.stopped = true
	close(t.stopCh)
	if t.deliver != nil {
		t.deliver()
	}
}

// Snapshot exposes non-consuming inspection data for tooling and traces.
func (t *RepeatingTimer) Snapshot() TimerSnapshot {
	policy := "combining missed ticks"
	switch t.policy.Mode {
	case MissedSkip:
		policy = "skipping missed ticks"
	case MissedCatchUp:
		policy = "catching up at most " + itoa(t.policy.Max) + " ticks"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return TimerSnapshot{
		Kind: "repeating", Clock: t.clock.Kind(), Interval: t.interval,
		Policy: policy, Next: t.position(t.next), Sequence: t.seq,
		Emitted: t.emitted, Missed: t.missed, Combined: t.combined,
		Skipped: t.skipped, CaughtUp: t.caughtUp, Active: !t.stopped,
		AnchoredAt: t.anchor,
	}
}
