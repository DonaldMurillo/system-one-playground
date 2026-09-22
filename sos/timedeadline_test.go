package sos

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeadlineScopeExceededOnVirtualClock(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	scope := WithDeadlineScope(context.Background(), clock, 30*time.Second)
	defer scope.Cancel()
	if err := scope.Exceeded(); err != nil {
		t.Fatalf("fresh scope err = %v", err)
	}
	info := scope.Info()
	if !info.Established || info.Remaining != 30*time.Second || info.ClockKind != "virtual" {
		t.Fatalf("info = %+v", info)
	}
	// Work inside the block: a wait inheriting the child context deadline.
	waitErr := make(chan error, 1)
	go func() { waitErr <- clock.Sleep(scope.Context(), 60*time.Second) }()
	clock.Advance(30 * time.Second)
	select {
	case err := <-waitErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("inherited wait err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("inherited wait never noticed the deadline")
	}
	var typed *typedFailure
	failure := scope.Exceeded()
	if !errors.As(failure, &typed) || typed.kind != "DeadlineExceeded" {
		t.Fatalf("scope err = %#v", failure)
	}
	if typed.value["allowed"] != 30*time.Second || typed.value["elapsed"] != 30*time.Second {
		t.Fatalf("failure fields = %#v", typed.value)
	}
	scope.Cancel() // idempotent cleanup
}

func TestDeadlineScopeNotExceededWhenWorkFinishes(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	scope := WithDeadlineScope(context.Background(), clock, time.Minute)
	defer scope.Cancel()
	clock.Advance(10 * time.Second)
	if err := scope.Exceeded(); err != nil {
		t.Fatalf("err = %v", err)
	}
	scope.Cancel()
	// Explicit early cancellation is not a time failure.
	if err := scope.Exceeded(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scope err = %v", err)
	}
}

func TestNestedDeadlineUsesEarliest(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	outer := WithDeadlineScope(context.Background(), clock, 10*time.Second)
	defer outer.Cancel()
	// A child cannot extend its parent's remaining time.
	inner := WithDeadlineScope(outer.Context(), clock, 20*time.Second)
	defer inner.Cancel()
	innerInfo := inner.Info()
	if innerInfo.Deadline != outer.Info().Deadline {
		t.Fatalf("child deadline %v extended parent %v", innerInfo.Deadline, outer.Info().Deadline)
	}
	if innerInfo.Established {
		t.Fatal("child established a later deadline")
	}
	// An earlier child deadline wins.
	earlier := WithDeadlineScope(outer.Context(), clock, 5*time.Second)
	defer earlier.Cancel()
	if !earlier.Info().Established || earlier.Info().Remaining != 5*time.Second {
		t.Fatalf("earlier child info = %+v", earlier.Info())
	}
	// The inherited failure keeps the parent's original frame, not the
	// child's reinterpretation.
	clock.Advance(10 * time.Second)
	// Wait until the parent deadline propagates into the child context.
	select {
	case <-inner.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("child context never noticed the parent deadline")
	}
	err := inner.Exceeded()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inherited err = %v", err)
	}
	var typed *typedFailure
	if errors.As(err, &typed) {
		t.Fatal("inherited deadline was reinterpreted as the child's failure")
	}
}

func TestOperationTimeoutSharesDeadlineMachinery(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	operation := WithOperationTimeout(context.Background(), clock, 5*time.Second)
	defer operation.Cancel()
	clock.Advance(6 * time.Second)
	select {
	case <-operation.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("operation context never noticed the deadline")
	}
	var typed *typedFailure
	if err := operation.Exceeded(); !errors.As(err, &typed) || typed.kind != "DeadlineExceeded" {
		t.Fatalf("operation timeout err = %#v", err)
	}
}

func TestHostDeadlineScope(t *testing.T) {
	scope := WithDeadlineScope(context.Background(), HostClock{}, 20*time.Millisecond)
	defer scope.Cancel()
	time.Sleep(60 * time.Millisecond)
	var typed *typedFailure
	if err := scope.Exceeded(); !errors.As(err, &typed) || typed.kind != "DeadlineExceeded" {
		t.Fatalf("host scope err = %#v", err)
	}
	if elapsed, ok := typed.value["elapsed"].(time.Duration); !ok || elapsed < 20*time.Millisecond {
		t.Fatalf("elapsed = %#v", typed.value["elapsed"])
	}
}
