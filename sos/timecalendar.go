package sos

import "time"

// CalendarUnit is a calendar arithmetic unit. Calendar months and years vary
// in elapsed length and are never durations.
type CalendarUnit int

const (
	CalendarDay CalendarUnit = iota
	CalendarWeek
	CalendarMonth
	CalendarYear
)

// MonthOverflowPolicy resolves calendar arithmetic landing on a day that
// does not exist in the target month (January 31 + 1 month).
type MonthOverflowPolicy int

const (
	// MonthOverflowLastDay uses the last day of the target month (default).
	MonthOverflowLastDay MonthOverflowPolicy = iota
	// MonthOverflowFail fails instead of silently overflowing into the next
	// month; there is no hidden overflow from January 31 into March.
	MonthOverflowFail
)

// AddCalendar finds the time n calendar units after t in zone, keeping the
// local clock time where possible and resolving daylight-saving transitions
// with the explicit policies. It uses exact local calendar arithmetic, not
// elapsed-duration arithmetic.
func AddCalendar(t time.Time, zone *time.Location, unit CalendarUnit, n int, overflow MonthOverflowPolicy, policy SchedulePolicy) (time.Time, error) {
	if zone == nil {
		return time.Time{}, invalidSchedule("calendar arithmetic requires a named time zone")
	}
	if n < 0 {
		return time.Time{}, invalidSchedule("calendar arithmetic requires a non-negative count")
	}
	local := t.In(zone)
	switch unit {
	case CalendarDay, CalendarWeek:
		if unit == CalendarWeek {
			n *= 7
		}
		target := local.AddDate(0, 0, n)
		return resolveAdjusted(zone, target, policy)
	case CalendarMonth, CalendarYear:
		years, months := 0, n
		if unit == CalendarYear {
			years, months = n, 0
		}
		y, m := local.Year()+years, int(local.Month())+months
		for m > 12 {
			m -= 12
			y++
		}
		day := local.Day()
		lastDay := lastDayOfMonth(y, time.Month(m))
		if day > lastDay {
			if overflow == MonthOverflowFail {
				return time.Time{}, invalidTimestamp(
					itoa(int64(y))+"-"+pad2(int64(m)),
					"day "+itoa(int64(day))+" does not exist in this month")
			}
			day = lastDay
		}
		target := time.Date(y, time.Month(m), day, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
		return resolveAdjusted(zone, target, policy)
	}
	return time.Time{}, invalidSchedule("unknown calendar unit")
}

// resolveAdjusted converts a desired local wall clock (expressed as a UTC
// timestamp carrying the intended y/m/d h:m:s) back to a real instant in
// zone, applying nonexistent and ambiguous policies.
func resolveAdjusted(zone *time.Location, desiredLocal time.Time, policy SchedulePolicy) (time.Time, error) {
	candidates, exists := resolveLocal(zone, desiredLocal.Year(), desiredLocal.Month(), desiredLocal.Day(),
		desiredLocal.Hour(), desiredLocal.Minute(), desiredLocal.Second(), desiredLocal.Nanosecond())
	if !exists {
		if policy.Nonexistent == NonexistentSkip {
			// Calendar arithmetic must still produce an instant; skipping is
			// a schedule-only policy. Use the next valid local time.
			return nextValidLocal(zone, desiredLocal.Year(), desiredLocal.Month(), desiredLocal.Day(),
				desiredLocal.Hour(), desiredLocal.Minute()), nil
		}
		return nextValidLocal(zone, desiredLocal.Year(), desiredLocal.Month(), desiredLocal.Day(),
			desiredLocal.Hour(), desiredLocal.Minute()), nil
	}
	if len(candidates) == 2 && policy.Ambiguous == AmbiguousSecond {
		return candidates[1], nil
	}
	return candidates[0], nil
}

func lastDayOfMonth(year int, month time.Month) int {
	// The day before the first of next month.
	firstOfNext := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return firstOfNext.AddDate(0, 0, -1).Day()
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// CalendarFields exposes calendar inspection: date, local clock time, year,
// month, day, weekday, hour, minute, second, nanosecond, UTC offset, and zone
// abbreviation in one named zone.
func CalendarFields(t time.Time, zone *time.Location) map[string]any {
	local := t.In(zone)
	_, offset := local.Zone()
	abbreviation := zoneAbbreviation(local)
	return map[string]any{
		"year": int64(local.Year()), "month": int64(local.Month()),
		"day": int64(local.Day()), "weekday": weekdayName(local.Weekday()),
		"hour": int64(local.Hour()), "minute": int64(local.Minute()),
		"second": int64(local.Second()), "nanosecond": int64(local.Nanosecond()),
		"date":           local.Format("2006-01-02"),
		"clock_time":     local.Format("15:04:05"),
		"utc_offset":     formatOffset(offset),
		"abbreviation":   abbreviation,
		"offset_seconds": int64(offset),
	}
}

func zoneAbbreviation(local time.Time) string {
	name, _ := local.Zone()
	return name
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return sign + pad2(int64(seconds/3600)) + ":" + pad2(int64(seconds%3600/60))
}

func weekdayName(d time.Weekday) string {
	return [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}[d]
}

// StartOfLocalDay returns midnight at the start of t's local day in zone.
func StartOfLocalDay(t time.Time, zone *time.Location) time.Time {
	local := t.In(zone)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
}

// EndOfLocalDay returns the last representable nanosecond of t's local day.
func EndOfLocalDay(t time.Time, zone *time.Location) time.Time {
	return StartOfLocalDay(t, zone).AddDate(0, 0, 1).Add(-time.Nanosecond)
}

// TodayInZone reads the clock once and returns the start of today's local
// day in zone. The captured date never changes afterwards.
func TodayInZone(clock Clock, zone *time.Location) time.Time {
	return StartOfLocalDay(clock.Now(), zone)
}

// SignedElapsed computes the signed difference a minus b as an exact timeline
// measurement, independent of display zones. It is the one operation that
// explicitly accepts signed differences.
func SignedElapsed(a, b time.Time) time.Duration {
	return a.Sub(b)
}
