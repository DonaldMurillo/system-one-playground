package sos

import (
	"context"
	"sync"
	"time"
)

// VirtualClock is a deterministic clock for test harnesses and embedding
// hosts. Advancing it runs due timers in scheduled-time order, breaking ties
// by creation order, until no newly due work remains. Timers created while
// callbacks fire are considered by the same advance call.
//
// A VirtualClock must not be used by ordinary production programs; it exists
// so waits, timers, rate limiting, batching, lifetime limits, scoped
// deadlines, and calendar schedules are all testable without sleeping.
type VirtualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*virtualTimer
	seq    uint64
}

// NewVirtualClock starts deterministic time at an explicit instant. Tests
// requiring repeatability always provide one.
func NewVirtualClock(start time.Time) *VirtualClock {
	return &VirtualClock{now: start}
}

func (v *VirtualClock) Now() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.now
}

func (v *VirtualClock) Kind() string { return "virtual" }

type virtualTimer struct {
	clock   *VirtualClock
	when    time.Time
	seq     uint64
	ch      chan time.Time
	stopped bool
	fired   bool
	// fireMu serializes fire-once delivery against Stop.
	fireMu sync.Mutex
}

func (v *VirtualClock) NewTimer(d time.Duration) ClockTimer {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seq++
	t := &virtualTimer{
		clock: v,
		when:  v.now.Add(d),
		seq:   v.seq,
		ch:    make(chan time.Time, 1),
	}
	v.timers = append(v.timers, t)
	return t
}

func (t *virtualTimer) C() <-chan time.Time { return t.ch }

func (t *virtualTimer) Stop() bool {
	t.fireMu.Lock()
	if t.fired || t.stopped {
		t.fireMu.Unlock()
		return false
	}
	t.stopped = true
	t.fireMu.Unlock()
	// Reclaim the timer immediately so stopped timers (for example the one
	// arming a rolled-back or canceled deadline scope) never linger.
	t.clock.drop(t)
	return true
}

func (v *VirtualClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := v.NewTimer(d).(*virtualTimer)
	select {
	case <-timer.ch:
		v.drop(timer)
		return nil
	case <-ctx.Done():
		timer.Stop()
		v.drop(timer)
		return ctx.Err()
	}
}

// Advance moves virtual time forward by d and runs every timer that becomes
// due, in scheduled-time then creation order, including timers created by
// callbacks during the advance. Each fired timer receives its scheduled
// instant. Negative advances are ignored: virtual time never moves backward.
func (v *VirtualClock) Advance(d time.Duration) {
	if d < 0 {
		return
	}
	v.mu.Lock()
	v.now = v.now.Add(d)
	v.mu.Unlock()
	v.runDue()
}

// AdvanceTo moves virtual time to an absolute instant and runs due timers.
func (v *VirtualClock) AdvanceTo(t time.Time) {
	v.mu.Lock()
	if t.After(v.now) {
		v.now = t
	}
	v.mu.Unlock()
	v.runDue()
}

func (v *VirtualClock) runDue() {
	for {
		v.mu.Lock()
		var next *virtualTimer
		for _, t := range v.timers {
			t.fireMu.Lock()
			inactive := t.stopped || t.fired
			t.fireMu.Unlock()
			if inactive || t.when.After(v.now) {
				continue
			}
			if next == nil || t.when.Before(next.when) || (t.when.Equal(next.when) && t.seq < next.seq) {
				next = t
			}
		}
		if next == nil {
			v.mu.Unlock()
			return
		}
		when := next.when
		v.mu.Unlock()

		next.fireMu.Lock()
		if next.stopped || next.fired {
			next.fireMu.Unlock()
			continue
		}
		next.fired = true
		// Deliver the scheduled instant, matching host timer semantics of
		// reporting when the timer was due rather than when the receiver ran.
		next.ch <- when
		next.fireMu.Unlock()
		// A fired timer will never run again; reclaim it right away.
		v.drop(next)
	}
}

// drop reclaims stopped or fired timers so long runs cannot grow unboundedly.
func (v *VirtualClock) drop(t *virtualTimer) {
	v.mu.Lock()
	defer v.mu.Unlock()
	kept := v.timers[:0]
	for _, existing := range v.timers {
		if existing != t {
			kept = append(kept, existing)
		}
	}
	v.timers = kept
}

// timerCount reports how many timers the clock still retains; it exists for
// tests that verify stopped and fired timers are reclaimed.
func (v *VirtualClock) timerCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.timers)
}
