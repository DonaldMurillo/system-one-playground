package sos

import (
	"strconv"
	"strings"
	"time"
)

// NonexistentPolicy decides what happens when a scheduled local time does not
// exist during a forward daylight-saving transition.
type NonexistentPolicy int

const (
	// NonexistentSkip skips the nonexistent local time (default).
	NonexistentSkip NonexistentPolicy = iota
	// NonexistentNextValid uses the next valid time after the transition.
	NonexistentNextValid
)

// AmbiguousPolicy decides what happens when a local time occurs twice during
// a backward transition.
type AmbiguousPolicy int

const (
	// AmbiguousFirst runs only at the first occurrence (default).
	AmbiguousFirst AmbiguousPolicy = iota
	// AmbiguousSecond selects the second occurrence.
	AmbiguousSecond
	// AmbiguousBoth runs at both occurrences.
	AmbiguousBoth
)

// SchedulePolicy carries the daylight-saving decisions as schedule metadata.
type SchedulePolicy struct {
	Nonexistent NonexistentPolicy
	Ambiguous   AmbiguousPolicy
}

// ScheduleRule is a checked structured calendar rule producing future
// instants in one named zone. Every rule requires UTC or an IANA zone; a
// schedule never silently depends on the host locale.
type ScheduleRule struct {
	Zone     *time.Location
	Hour     int // -1 means every hour, Minute still fixed
	Minute   int
	Weekdays [7]bool // all true when unrestricted
	Months   [13]bool
	Days     [32]bool // 1..31
	LastDay  bool
	Policy   SchedulePolicy
}

func trueArray(n int) []bool {
	a := make([]bool, n)
	for i := range a {
		a[i] = true
	}
	return a
}

// EveryHourAt fires each hour at the given minute.
func EveryHourAt(minute int, zone *time.Location, policy SchedulePolicy) (*ScheduleRule, error) {
	r, err := baseSchedule(zone, policy)
	if err != nil {
		return nil, err
	}
	if err := validHourMinute(-1, minute); err != nil {
		return nil, err
	}
	r.Hour, r.Minute = -1, minute
	return r, nil
}

// EveryDayAt fires each day at hour:minute local.
func EveryDayAt(hour, minute int, zone *time.Location, policy SchedulePolicy) (*ScheduleRule, error) {
	r, err := baseSchedule(zone, policy)
	if err != nil {
		return nil, err
	}
	if err := validHourMinute(hour, minute); err != nil {
		return nil, err
	}
	r.Hour, r.Minute = hour, minute
	return r, nil
}

// EveryWeekdayAt fires on the weekday at hour:minute local.
func EveryWeekdayAt(weekday time.Weekday, hour, minute int, zone *time.Location, policy SchedulePolicy) (*ScheduleRule, error) {
	r, err := EveryDayAt(hour, minute, zone, policy)
	if err != nil {
		return nil, err
	}
	for i := range r.Weekdays {
		r.Weekdays[i] = time.Weekday(i) == weekday
	}
	return r, nil
}

// MonthlyOn fires on the given day of each month at hour:minute local.
func MonthlyOn(day, hour, minute int, zone *time.Location, policy SchedulePolicy) (*ScheduleRule, error) {
	r, err := EveryDayAt(hour, minute, zone, policy)
	if err != nil {
		return nil, err
	}
	if day < 1 || day > 31 {
		return nil, invalidSchedule("month day must be between 1 and 31")
	}
	for i := range r.Days {
		r.Days[i] = i == day
	}
	return r, nil
}

func MonthlyLastDayAt(hour, minute int, zone *time.Location, policy SchedulePolicy) (*ScheduleRule, error) {
	r, err := EveryDayAt(hour, minute, zone, policy)
	if err != nil {
		return nil, err
	}
	for i := range r.Days {
		r.Days[i] = false
	}
	r.LastDay = true
	return r, nil
}

func baseSchedule(zone *time.Location, policy SchedulePolicy) (*ScheduleRule, error) {
	if zone == nil {
		return nil, invalidSchedule("calendar schedules require a named time zone")
	}
	r := &ScheduleRule{Zone: zone, Policy: policy}
	copy(r.Weekdays[:], trueArray(7))
	copy(r.Months[:], trueArray(13))
	copy(r.Days[:], trueArray(32))
	r.Days[0] = false
	return r, nil
}

func validHourMinute(hour, minute int) error {
	if hour < -1 || hour > 23 {
		return invalidSchedule("hour must be between 0 and 23")
	}
	if minute < 0 || minute > 59 {
		return invalidSchedule("minute must be between 0 and 59")
	}
	return nil
}

// NextAfter returns the next occurrence strictly after t, in schedule order.
// Under AmbiguousBoth a backward transition yields two instants for one local
// time. A rule that can never match (e.g., February 30) is a typed failure.
func (r *ScheduleRule) NextAfter(t time.Time) ([]time.Time, error) {
	local := t.In(r.Zone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, r.Zone)
	for range 800 {
		if r.dayMatches(day) {
			hours := []int{r.Hour}
			if r.Hour < 0 {
				hours = make([]int, 24)
				for h := range hours {
					hours[h] = h
				}
			}
			for _, h := range hours {
				instants, ok := r.occurrences(day, h)
				if !ok {
					continue
				}
				future := instants[:0]
				for _, instant := range instants {
					if instant.After(t) {
						future = append(future, instant)
					}
				}
				// Under AmbiguousBoth the earlier occurrence may already be
				// past: deliver whichever remain, in order.
				if len(future) > 0 {
					return future, nil
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return nil, invalidSchedule("no future occurrence exists for this rule")
}

func (r *ScheduleRule) dayMatches(day time.Time) bool {
	if !r.Weekdays[int(day.Weekday())] {
		return false
	}
	if !r.Months[int(day.Month())] {
		return false
	}
	if r.LastDay {
		return day.AddDate(0, 0, 1).Month() != day.Month()
	}
	return r.Days[day.Day()]
}

// occurrences resolves the scheduled local time on day under the DST
// policies. ok is false when the local time does not exist and the policy is
// to skip it.
func (r *ScheduleRule) occurrences(day time.Time, hour int) ([]time.Time, bool) {
	candidates, exists := resolveLocal(r.Zone, day.Year(), day.Month(), day.Day(), hour, r.Minute, 0, 0)
	if !exists {
		if r.Policy.Nonexistent == NonexistentSkip {
			return nil, false
		}
		next := nextValidLocal(r.Zone, day.Year(), day.Month(), day.Day(), hour, r.Minute)
		return []time.Time{next}, true
	}
	switch len(candidates) {
	case 1:
		return candidates, true
	case 2:
		switch r.Policy.Ambiguous {
		case AmbiguousFirst:
			return []time.Time{candidates[0]}, true
		case AmbiguousSecond:
			return []time.Time{candidates[1]}, true
		default:
			return candidates, true
		}
	}
	return nil, false
}
func resolveLocal(zone *time.Location, y int, m time.Month, d, hh, mm, ss, ns int) ([]time.Time, bool) {
	// Treat the desired wall clock as a UTC instant to probe offsets around
	// the transition: candidate = u - offset.
	u := time.Date(y, m, d, hh, mm, ss, ns, time.UTC)
	offEarly := offsetAt(zone, u.Add(-24*time.Hour))
	offLate := offsetAt(zone, u.Add(24*time.Hour))
	seen := make(map[int64]bool)
	var out []time.Time
	for _, off := range []int{offEarly, offLate} {
		cand := u.Add(-time.Duration(off) * time.Second).UTC()
		id := cand.Unix()
		if seen[id] {
			continue
		}
		seen[id] = true
		if localEquals(cand.In(zone), y, m, d, hh, mm, ss) {
			out = append(out, cand)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	if len(out) == 2 && out[0].After(out[1]) {
		out[0], out[1] = out[1], out[0]
	}
	return out, true
}

// offsetAt returns the UTC offset in seconds that zone applies at instant t.
func offsetAt(zone *time.Location, t time.Time) int {
	_, off := t.UTC().In(zone).Zone()
	return off
}

func localEquals(t time.Time, y int, m time.Month, d, hh, mm, ss int) bool {
	return t.Year() == y && t.Month() == m && t.Day() == d &&
		t.Hour() == hh && t.Minute() == mm && t.Second() == ss
}

// nextValidLocal returns the first instant at or after the desired local
// time whose local time exists, located by binary search over the offset
// transition surrounding it.
func nextValidLocal(zone *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	target := time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
	lo := target.Add(-48 * time.Hour)
	hi := target.Add(48 * time.Hour)
	// Find the first instant whose local wall reading is at or after the
	// desired (nonexistent) local time; wall readings are monotone in time.
	wall := func(t time.Time) time.Time {
		l := t.In(zone)
		return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
	}
	for range 60 {
		mid := lo.Add(hi.Sub(lo) / 2)
		if wall(mid).Before(target) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// CronSchedule is a validated five-field cron expression bound to one named
// zone. Unsupported extensions fail validation instead of being reinterpreted.
type CronSchedule struct {
	Minutes       [60]bool
	Hours         [24]bool
	Days          [32]bool
	Months        [13]bool
	Weekdays      [7]bool
	DomRestricted bool
	DowRestricted bool
	Zone          *time.Location
	Policy        SchedulePolicy
	expr          string
}

// ScheduleFromCron parses a standard five-field cron expression (minute hour
// day-of-month month day-of-week; * lists, ranges, and steps supported,
// three-letter names for months and weekdays) in a named zone. Seconds
// fields, @macros, L/W/# extensions, and years are rejected.
func ScheduleFromCron(expr, zoneName string, policy SchedulePolicy) (*CronSchedule, error) {
	zone, err := LoadZone(zoneName)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, invalidSchedule("cron requires exactly five fields")
	}
	s := &CronSchedule{Zone: zone, Policy: policy, expr: expr}
	ranges := []struct {
		out      []bool
		min, max int
		names    map[string]int
	}{
		{s.Minutes[:], 0, 59, nil},
		{s.Hours[:], 0, 23, nil},
		{s.Days[:], 1, 31, nil},
		{s.Months[:], 1, 12, monthNames},
		{s.Weekdays[:], 0, 6, weekdayNames},
	}
	for i, field := range fields {
		spec := ranges[i]
		if i == 2 && field != "*" {
			s.DomRestricted = true
		}
		if i == 4 && field != "*" {
			s.DowRestricted = true
		}
		for _, part := range strings.Split(field, ",") {
			if err := parseCronPart(part, spec.out, spec.min, spec.max, spec.names); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}

var weekdayNames = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}

func parseCronPart(part string, out []bool, min, max int, names map[string]int) error {
	if part == "" {
		return invalidSchedule("empty cron field")
	}
	for _, c := range part {
		if strings.ContainsRune("@LW#?/", c) && c != '/' {
			return invalidSchedule("unsupported cron extension in " + part)
		}
	}
	step := 1
	body := part
	if i := strings.Index(part, "/"); i >= 0 {
		var err error
		step, err = strconv.Atoi(part[i+1:])
		if err != nil || step <= 0 {
			return invalidSchedule("invalid step in " + part)
		}
		body = part[:i]
	}
	lo, hi := min, max
	if body != "*" {
		rangePart := body
		if i := strings.Index(body, "-"); i >= 0 {
			rangePart = body[:i]
			var err error
			hi, err = cronValue(body[i+1:], names)
			if err != nil {
				return err
			}
		} else if strings.Contains(part, "/") {
			// step without range means min..max/min, already defaulted
			rangePart = body
			hi = max
		} else {
			// single value: with a step it is value/max
			v, err := cronValue(body, names)
			if err != nil {
				return err
			}
			if strings.Contains(part, "/") {
				lo, hi = v, max
			} else {
				lo, hi = v, v
			}
		}
		var err error
		lo, err = cronValue(rangePart, names)
		if err != nil {
			return err
		}
		if lo < min || hi > max || lo > hi {
			return invalidSchedule("cron value out of range in " + part)
		}
	}
	for v := lo; v <= hi; v += step {
		out[v] = true
	}
	return nil
}

func cronValue(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, invalidSchedule("invalid cron value " + s)
	}
	return v, nil
}

// NextAfter returns the next cron occurrence strictly after t.
func (s *CronSchedule) NextAfter(t time.Time) ([]time.Time, error) {
	local := t.In(s.Zone)
	// Start scanning at the next minute boundary.
	minute := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, s.Zone)
	for range 60 * 24 * 800 {
		minute = minute.Add(time.Minute)
		if s.matches(minute) {
			return []time.Time{resolveCronMinute(s, minute)}, nil
		}
	}
	return nil, invalidSchedule("no future occurrence exists for cron " + s.expr)
}

func (s *CronSchedule) matches(m time.Time) bool {
	if !s.Minutes[m.Minute()] || !s.Hours[m.Hour()] || !s.Months[int(m.Month())] {
		return false
	}
	dom := s.Days[m.Day()]
	dow := s.Weekdays[int(m.Weekday())]
	if s.DomRestricted && s.DowRestricted {
		return dom || dow
	}
	return dom && dow
}

// resolveCronMinute applies the DST policies to a matched local minute.
func resolveCronMinute(s *CronSchedule, m time.Time) time.Time {
	candidates, exists := resolveLocal(s.Zone, m.Year(), m.Month(), m.Day(), m.Hour(), m.Minute(), 0, 0)
	if !exists {
		if s.Policy.Nonexistent == NonexistentSkip {
			// The matcher should have skipped this minute; fall back to the
			// next valid instant for safety.
			return nextValidLocal(s.Zone, m.Year(), m.Month(), m.Day(), m.Hour(), m.Minute())
		}
		return nextValidLocal(s.Zone, m.Year(), m.Month(), m.Day(), m.Hour(), m.Minute())
	}
	if len(candidates) == 2 && s.Policy.Ambiguous == AmbiguousSecond {
		return candidates[1]
	}
	return candidates[0]
}
