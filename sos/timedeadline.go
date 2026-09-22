package sos

import (
	"context"
	"errors"
	"sync"
	"time"
)

// clockDeadlineContext is a child context whose deadline fires on an
// injectable clock, so virtual time drives scoped deadlines exactly like
// host time. It exposes Deadline and Done like any context, so nested scopes
// inherit the earliest effective deadline naturally.
type clockDeadlineContext struct {
	parent   context.Context
	deadline time.Time
	done     chan struct{}
	mu       sync.Mutex
	err      error
}

func (c *clockDeadlineContext) Deadline() (time.Time, bool) {
	if pd, ok := c.parent.Deadline(); ok && pd.Before(c.deadline) {
		return pd, true
	}
	return c.deadline, true
}

func (c *clockDeadlineContext) Done() <-chan struct{} { return c.done }

func (c *clockDeadlineContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *clockDeadlineContext) Value(key any) any { return c.parent.Value(key) }

func (c *clockDeadlineContext) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
}

// DeadlineInfo reports the effective scoped deadline for tooling: the active
// deadline, which clock established it, and the remaining time. Inspection
// never advances a virtual clock.
type DeadlineInfo struct {
	Deadline    time.Time     `json:"deadline"`
	Allowed     time.Duration `json:"allowed"`
	Elapsed     time.Duration `json:"elapsed"`
	Remaining   time.Duration `json:"remaining"`
	ClockKind   string        `json:"clockKind"`
	Established bool          `json:"established"`
}

// DeadlineScope bounds one workflow ("allow at most ... for:"). The duration
// is measured once from block entry and never resets per statement. Every
// wait, stream, process, provider call, and HTTP request created inside
// inherits it; nested blocks use the earliest effective deadline, so a child
// can never extend its parent's remaining time.
type DeadlineScope struct {
	clock    Clock
	allowed  time.Duration
	entered  time.Time
	deadline time.Time
	ctx      *clockDeadlineContext
	// inherited marks scopes whose parent deadline was already earlier.
	inherited bool
}

// WithDeadlineScope derives a child context with a deadline measured from
// entry on clock. If the parent already carries an equal or earlier deadline,
// the parent wins and the scope reports it without establishing its own.
func WithDeadlineScope(parent context.Context, clock Clock, allowed time.Duration) *DeadlineScope {
	clock = clockOrDefault(clock)
	entered := clock.Now()
	deadline := entered.Add(allowed)
	scope := &DeadlineScope{clock: clock, allowed: allowed, entered: entered, deadline: deadline}
	if parentDeadline, ok := parent.Deadline(); ok && !parentDeadline.After(deadline) {
		// The parent deadline is earlier (or equal): keep it untouched so
		// the inherited failure preserves its original frame.
		scope.inherited = true
		scope.ctx = &clockDeadlineContext{parent: parent, deadline: parentDeadline, done: make(chan struct{})}
		// Mirror the parent's cancellation into the child Done channel.
		go func() {
			select {
			case <-parent.Done():
				scope.ctx.finish(parent.Err())
			case <-scope.ctx.done:
			}
		}()
		return scope
	}
	ctx := &clockDeadlineContext{parent: parent, deadline: deadline, done: make(chan struct{})}
	scope.ctx = ctx
	timer := clock.NewTimer(allowed)
	go func() {
		// Stop on every exit path so a canceled or parent-canceled scope
		// never leaves an orphaned timer armed on the clock.
		defer timer.Stop()
		select {
		case <-timer.C():
			ctx.finish(context.DeadlineExceeded)
		case <-ctx.done:
		case <-parent.Done():
			// Parent cancellation propagates into the scope with the
			// parent's original error and frame.
			ctx.finish(parent.Err())
		}
	}()
	return scope
}

// Context returns the child context inherited by everything inside the block.
func (s *DeadlineScope) Context() context.Context { return s.ctx }

// Cancel stops the scope early (for example when its block completed or an
// owned stream must stop). It does not create a failure.
func (s *DeadlineScope) Cancel() { s.ctx.finish(context.Canceled) }

// Exceeded reports nil while the scope is within budget. When the deadline
// passed it returns the DeadlineExceeded failure carrying the allowed and
// elapsed durations measured on the owning clock. An inherited parent
// deadline preserves its original error and frame.
func (s *DeadlineScope) Exceeded() error {
	if err := s.ctx.Err(); errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if s.inherited {
		// Parent deadline or cancellation: preserve the original error.
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		if s.clock.Now().After(s.ctx.deadline) {
			return context.DeadlineExceeded
		}
		return nil
	}
	elapsed := s.clock.Now().Sub(s.entered)
	if elapsed < s.allowed {
		// Canceled before the deadline: run cancellation, not a time failure.
		if s.ctx.Err() != nil {
			return context.Canceled
		}
		return nil
	}
	return deadlineExceededFailure(s.allowed, elapsed)
}

// Info returns the non-consuming snapshot for tooling and traces: the
// effective deadline, which scope established it, and remaining time.
func (s *DeadlineScope) Info() DeadlineInfo {
	now := s.clock.Now()
	deadline, allowed, established := s.deadline, s.allowed, true
	if s.inherited {
		deadline = s.ctx.deadline
		allowed = deadline.Sub(s.entered)
		established = false
	}
	return DeadlineInfo{
		Deadline: deadline, Allowed: allowed,
		Elapsed: now.Sub(s.entered), Remaining: deadline.Sub(now),
		ClockKind: s.clock.Kind(), Established: established,
	}
}

// WithOperationTimeout bounds one operation rather than a whole workflow.
// It shares the deadline machinery and inherits any earlier parent deadline;
// tooling distinguishes it from a workflow scope by caller context.
func WithOperationTimeout(parent context.Context, clock Clock, timeout time.Duration) *DeadlineScope {
	return WithDeadlineScope(parent, clock, timeout)
}
