package sos

import (
	"errors"
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadZone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestScheduleWeekdayAtNine(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	rule, err := EveryWeekdayAt(time.Monday, 8, 0, mustZone(t, "Europe/London"), SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	// Sunday 2026-09-20 23:00 UTC: next Monday 8:00 London.
	from := time.Date(2026, 9, 20, 23, 0, 0, 0, time.UTC)
	next, err := rule.NextAfter(from)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC) // 08:00 London = 07:00 UTC (BST)
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("next = %v; want %v", next, want)
	}
	_ = ny
}

func TestScheduleEveryHour(t *testing.T) {
	rule, err := EveryHourAt(15, time.UTC, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 21, 9, 16, 0, 0, time.UTC)
	next, err := rule.NextAfter(from)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 21, 10, 15, 0, 0, time.UTC)
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("next = %v; want %v", next, want)
	}
}

func TestScheduleFirstOfMonth(t *testing.T) {
	rule, err := MonthlyOn(1, 0, 15, time.UTC, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 1, 0, 15, 0, 0, time.UTC)
	next, err := rule.NextAfter(from)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 1, 0, 15, 0, 0, time.UTC)
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("next = %v; want %v", next, want)
	}
}

func TestScheduleLeapYearMonthEnd(t *testing.T) {
	// Feb 29 exists only in leap years: restrict the rule to February.
	rule, err := MonthlyOn(29, 12, 0, time.UTC, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	for m := range rule.Months {
		rule.Months[m] = time.Month(m) == time.February
	}
	from := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	next, err := rule.NextAfter(from)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("next = %v; want %v", next, want)
	}
}

func TestScheduleNonexistentLocalTimePolicies(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	// US DST 2026: forward transition Sunday 2026-03-08 02:00 -> 03:00 EST->EDT.
	skip, err := EveryDayAt(2, 30, ny, SchedulePolicy{Nonexistent: NonexistentSkip})
	if err != nil {
		t.Fatal(err)
	}
	next, err := skip.NextAfter(time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// 02:30 does not exist on Mar 8; skipping lands on Mar 9 02:30 EDT.
	want := time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC)
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("skip policy next = %v; want %v", next, want)
	}

	nextValid, err := EveryDayAt(2, 30, ny, SchedulePolicy{Nonexistent: NonexistentNextValid})
	if err != nil {
		t.Fatal(err)
	}
	next, err = nextValid.NextAfter(time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// The next valid time after nonexistent 02:30 is 03:00 EDT = 07:00 UTC.
	want = time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("next-valid policy next = %v; want %v", next, want)
	}
}

func TestScheduleAmbiguousLocalTimePolicies(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	// Backward transition Sunday 2026-11-01 02:00 -> 01:00 EDT->EST: local
	// 01:30 occurs twice.
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	first, err := EveryDayAt(1, 30, ny, SchedulePolicy{Ambiguous: AmbiguousFirst})
	if err != nil {
		t.Fatal(err)
	}
	next, err := first.NextAfter(base)
	if err != nil {
		t.Fatal(err)
	}
	// First occurrence: EDT offset -4 => 05:30 UTC.
	if len(next) != 1 || !next[0].Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) {
		t.Fatalf("first occurrence = %v", next)
	}

	second, err := EveryDayAt(1, 30, ny, SchedulePolicy{Ambiguous: AmbiguousSecond})
	if err != nil {
		t.Fatal(err)
	}
	next, err = second.NextAfter(base)
	if err != nil {
		t.Fatal(err)
	}
	// Second occurrence: EST offset -5 => 06:30 UTC.
	if len(next) != 1 || !next[0].Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("second occurrence = %v", next)
	}

	both, err := EveryDayAt(1, 30, ny, SchedulePolicy{Ambiguous: AmbiguousBoth})
	if err != nil {
		t.Fatal(err)
	}
	next, err = both.NextAfter(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 || !next[0].Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) ||
		!next[1].Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("both occurrences = %v", next)
	}
}

func TestScheduleHistoricalOffsetTransition(t *testing.T) {
	// Historical zone data: London's BST double gap in 1971-10-31 changed at
	// 02:00; simpler historical check: US went year-round DST briefly in
	// 1974-1975. Verify the schedule uses the historical offset on
	// 1974-01-06 (DST started that winter): New York was UTC-4.
	ny := mustZone(t, "America/New_York")
	rule, err := EveryDayAt(9, 0, ny, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	next, err := rule.NextAfter(time.Date(1974, 1, 5, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(1974, 1, 6, 13, 0, 0, 0, time.UTC) // 09:00 at UTC-4
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("historical next = %v; want %v", next, want)
	}
}

func TestScheduleValidationFailures(t *testing.T) {
	if _, err := EveryDayAt(24, 0, time.UTC, SchedulePolicy{}); err == nil {
		t.Fatal("hour 24 accepted")
	}
	if _, err := EveryDayAt(9, 60, time.UTC, SchedulePolicy{}); err == nil {
		t.Fatal("minute 60 accepted")
	}
	if _, err := MonthlyOn(0, 9, 0, time.UTC, SchedulePolicy{}); err == nil {
		t.Fatal("day 0 accepted")
	}
	if _, err := EveryDayAt(9, 0, nil, SchedulePolicy{}); err == nil {
		t.Fatal("nil zone accepted")
	}
}

func TestCronSchedule(t *testing.T) {
	s, err := ScheduleFromCron("0 9 * * MON-FRI", "America/New_York", SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	// Friday 2026-09-25 13:00 UTC (09:00 NY passed): next is Monday 09:00 NY.
	next, err := s.NextAfter(time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 28, 13, 0, 0, 0, time.UTC)
	if len(next) != 1 || !next[0].Equal(want) {
		t.Fatalf("cron next = %v; want %v", next, want)
	}

	step, err := ScheduleFromCron("*/15 * * * *", "UTC", SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	next, err = step.NextAfter(time.Date(2026, 9, 21, 12, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !next[0].Equal(time.Date(2026, 9, 21, 12, 15, 0, 0, time.UTC)) {
		t.Fatalf("step cron next = %v", next[0])
	}
}

func TestCronRejectsExtensions(t *testing.T) {
	bad := []string{
		"* * * * * *",   // six fields (seconds)
		"@daily",        // macros
		"0 0 L * *",     // L extension
		"0 0 1W * *",    // W extension
		"0 0 * * MON#1", // # extension
		"0 0 * * 2027",  // year field inside dow
		"* * * *",       // four fields
		"61 * * * *",    // out of range
		"* 25 * * *",    // out of range
		"* * 0 * *",     // day zero
	}
	for _, expr := range bad {
		_, err := ScheduleFromCron(expr, "UTC", SchedulePolicy{})
		if err == nil {
			t.Errorf("cron %q accepted", expr)
			continue
		}
		var typed *typedFailure
		if !errors.As(err, &typed) || typed.kind != "InvalidSchedule" {
			t.Errorf("cron %q error = %#v; want InvalidSchedule", expr, err)
		}
	}
	if _, err := ScheduleFromCron("* * * * *", "+05:30", SchedulePolicy{}); err == nil {
		t.Fatal("fixed offset accepted for cron zone")
	}
}
