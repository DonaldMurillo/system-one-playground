package sos

import (
	"errors"
	"testing"
	"time"
)

func TestAddCalendarDayKeepsClockTimeAcrossDST(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	// 2026-03-07 12:00 EST + 1 calendar day = 2026-03-08 12:00 EDT: same
	// wall clock, different offset (elapsed 23 hours).
	start := time.Date(2026, 3, 7, 17, 0, 0, 0, time.UTC) // 12:00 EST
	got, err := AddCalendar(start, ny, CalendarDay, 1, MonthOverflowLastDay, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC) // 12:00 EDT
	if !got.Equal(want) {
		t.Fatalf("calendar day = %v; want %v", got, want)
	}
	if got.Sub(start) != 23*time.Hour {
		t.Fatalf("elapsed = %v; want 23h", got.Sub(start))
	}
}

func TestAddCalendarMonthOverflowPolicies(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	jan31 := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC) // 05:00 NY

	clamped, err := AddCalendar(jan31, ny, CalendarMonth, 1, MonthOverflowLastDay, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-02-28 05:00 EST = 10:00 UTC.
	if !clamped.Equal(time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("last-day policy = %v", clamped)
	}

	_, err = AddCalendar(jan31, ny, CalendarMonth, 1, MonthOverflowFail, SchedulePolicy{})
	if err == nil {
		t.Fatal("overflow accepted")
	}
	var typed *typedFailure
	if !errors.As(err, &typed) {
		t.Fatalf("overflow error = %#v", err)
	}
}

func TestAddCalendarLeapYear(t *testing.T) {
	feb29 := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	got, err := AddCalendar(feb29, time.UTC, CalendarYear, 1, MonthOverflowLastDay, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2025, 2, 28, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("leap year = %v", got)
	}
	// January 31 + 1 month in a leap year lands on February 29.
	jan31 := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)
	got, err = AddCalendar(jan31, time.UTC, CalendarMonth, 1, MonthOverflowLastDay, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("leap month = %v", got)
	}
}

func TestAddCalendarWeek(t *testing.T) {
	start := time.Date(2026, 9, 21, 8, 30, 0, 0, time.UTC)
	got, err := AddCalendar(start, time.UTC, CalendarWeek, 2, MonthOverflowLastDay, SchedulePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 10, 5, 8, 30, 0, 0, time.UTC)) {
		t.Fatalf("two weeks = %v", got)
	}
	if _, err := AddCalendar(start, nil, CalendarDay, 1, MonthOverflowLastDay, SchedulePolicy{}); err == nil {
		t.Fatal("nil zone accepted")
	}
	if _, err := AddCalendar(start, time.UTC, CalendarDay, -1, MonthOverflowLastDay, SchedulePolicy{}); err == nil {
		t.Fatal("negative count accepted")
	}
}

func TestCalendarInspection(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	// 2026-09-21 14:30 UTC is 10:30 EDT on a Monday.
	ts := time.Date(2026, 9, 21, 14, 30, 45, 250, time.UTC)
	fields := CalendarFields(ts, ny)
	if fields["year"] != int64(2026) || fields["month"] != int64(9) || fields["day"] != int64(21) {
		t.Fatalf("date fields = %v", fields)
	}
	if fields["weekday"] != "Monday" {
		t.Fatalf("weekday = %v", fields["weekday"])
	}
	if fields["hour"] != int64(10) || fields["minute"] != int64(30) || fields["second"] != int64(45) {
		t.Fatalf("time fields = %v", fields)
	}
	if fields["nanosecond"] != int64(250) {
		t.Fatalf("nanosecond = %v", fields["nanosecond"])
	}
	if fields["utc_offset"] != "-04:00" || fields["abbreviation"] != "EDT" {
		t.Fatalf("zone fields = %v", fields)
	}
	if fields["date"] != "2026-09-21" || fields["clock_time"] != "10:30:45" {
		t.Fatalf("display fields = %v", fields)
	}
}

func TestLocalDayBoundaries(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	ts := time.Date(2026, 9, 21, 14, 0, 0, 0, time.UTC) // 10:00 EDT
	start := StartOfLocalDay(ts, ny)
	if !start.Equal(time.Date(2026, 9, 21, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("start of day = %v", start)
	}
	end := EndOfLocalDay(ts, ny)
	if end.In(ny).Format("15:04:05.999999999") != "23:59:59.999999999" {
		t.Fatalf("end of day = %v", end)
	}
	if !end.Add(time.Nanosecond).Equal(start.AddDate(0, 0, 1)) {
		t.Fatalf("end boundary = %v", end)
	}
}

func TestTodayInZoneReadsClockOnce(t *testing.T) {
	clock := NewVirtualClock(time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC))
	today := TodayInZone(clock, mustZone(t, "Asia/Tokyo"))
	// 03:00 UTC is already the next day (12:00 JST) in Tokyo.
	if today.In(mustZone(t, "Asia/Tokyo")).Day() != 21 || today.Hour() != 0 {
		t.Fatalf("today in Tokyo = %v", today)
	}
	clock.Advance(48 * time.Hour)
	// The captured date never changes.
	if today.In(mustZone(t, "Asia/Tokyo")).Day() != 21 {
		t.Fatalf("captured date changed: %v", today)
	}
}

func TestSignedElapsed(t *testing.T) {
	a := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	b := a.Add(-90 * time.Minute)
	if SignedElapsed(a, b) != 90*time.Minute {
		t.Fatal("forward difference wrong")
	}
	if SignedElapsed(b, a) != -90*time.Minute {
		t.Fatal("signed difference wrong")
	}
}
