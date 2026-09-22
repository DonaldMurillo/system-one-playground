package sos

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func registerPure(module, name string, params []NativeParam, result, description string, fn func([]any) (any, error)) {
	if stdRegistry[module] == nil {
		stdRegistry[module] = map[string]NativeOp{}
	}
	stdRegistry[module][name] = NativeOp{Name: name, Params: params, Fn: fn}
	stdDocs[module+"."+name] = stdDoc{result, description, []string{"pure"}}
}
func stringList(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
func listIndex(value any, length int, allowEnd bool) (int, error) {
	n, ok := number(value)
	limit := length
	if allowEnd {
		limit++
	}
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || math.Trunc(n) != n || n >= float64(limit) {
		return 0, fmt.Errorf("index must be an integer within the collection bounds")
	}
	return int(n), nil
}
func init() {
	registerPure("std/list", "percentile", []NativeParam{{"numbers", "list"}, {"percent", "number"}}, "number", "Returns the sorted sample at floor(percent/100 * (count-1)); percent is 0..100 and the sample must be nonempty and finite.", func(args []any) (any, error) {
		input := args[0].([]any)
		if len(input) == 0 {
			return nil, fmt.Errorf("percentile requires a nonempty sample")
		}
		percent, _ := number(args[1])
		if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
			return nil, fmt.Errorf("percent must be from 0 through 100")
		}
		values := make([]float64, len(input))
		for i, v := range input {
			n, ok := number(v)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, fmt.Errorf("sample must contain finite numbers")
			}
			values[i] = n
		}
		sort.Float64s(values)
		return values[int(math.Floor(percent/100*float64(len(values)-1)))], nil
	})
	registerPure("std/text", "split", []NativeParam{{"text", "text"}, {"separator", "text"}}, "list", "Splits text at each separator; an empty separator splits Unicode code points.", func(args []any) (any, error) {
		return stringList(strings.Split(args[0].(string), args[1].(string))), nil
	})
	registerPure("std/text", "lines", []NativeParam{{"text", "text"}}, "list", "Splits LF or CRLF lines, preserving a final empty line.", func(args []any) (any, error) {
		return stringList(strings.Split(strings.ReplaceAll(args[0].(string), "\r\n", "\n"), "\n")), nil
	})
	registerPure("std/text", "slice", []NativeParam{{"text", "text"}, {"start", "number"}, {"end", "number"}}, "text", "Returns the half-open zero-based Unicode code-point range [start,end). Out-of-range indices fail.", func(args []any) (any, error) {
		r := []rune(args[0].(string))
		start, err := listIndex(args[1], len(r), true)
		if err != nil {
			return nil, err
		}
		end, err := listIndex(args[2], len(r), true)
		if err != nil {
			return nil, err
		}
		if end < start {
			return nil, fmt.Errorf("end must be at least start")
		}
		return string(r[start:end]), nil
	})
	registerPure("std/text", "replace", []NativeParam{{"text", "text"}, {"old", "text"}, {"new", "text"}}, "text", "Replaces every literal occurrence of old with new.", func(args []any) (any, error) {
		return strings.ReplaceAll(args[0].(string), args[1].(string), args[2].(string)), nil
	})
	registerPure("std/text", "matches", []NativeParam{{"text", "text"}, {"pattern", "text"}}, "list", "Finds Go/RE2 regex matches; each record contains text, byteStart/byteEnd (zero-based), line/column (one-based code points), and groups.", regexMatches)
	registerPure("std/list", "at", []NativeParam{{"list", "list"}, {"index", "number"}}, "any", "Reads a zero-based list element; out-of-range indices fail.", func(args []any) (any, error) {
		values := args[0].([]any)
		i, err := listIndex(args[1], len(values), false)
		if err != nil {
			return nil, err
		}
		return values[i], nil
	})
	registerPure("std/list", "flatten", []NativeParam{{"list", "list"}}, "list", "Flattens one list level into a new list; every input element must be a list.", func(args []any) (any, error) {
		out := []any{}
		for _, v := range args[0].([]any) {
			list, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("flatten requires a list of lists")
			}
			out = append(out, list...)
		}
		return out, nil
	})
	registerPure("std/record", "get", []NativeParam{{"record", "record"}, {"key", "text"}}, "any", "Reads a dynamic record field; a missing field is an error.", func(args []any) (any, error) {
		v, ok := args[0].(map[string]any)[args[1].(string)]
		if !ok {
			return nil, fmt.Errorf("record has no field %q", args[1])
		}
		return v, nil
	})
	registerPure("std/record", "set", []NativeParam{{"record", "record"}, {"key", "text"}, {"value", "any"}}, "record", "Returns a new record with the named field set; does not mutate the original.", func(args []any) (any, error) {
		out := map[string]any{}
		for k, v := range args[0].(map[string]any) {
			out[k] = v
		}
		out[args[1].(string)] = args[2]
		return out, nil
	})
	registerPure("std/record", "keys", []NativeParam{{"record", "record"}}, "list", "Returns record keys sorted lexicographically.", func(args []any) (any, error) {
		keys := []string{}
		for k := range args[0].(map[string]any) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return stringList(keys), nil
	})
}

// stdTimeClock is the single runtime clock behind every language-visible
// timing operation in std/time. Embedders and test harnesses may replace it
// with a virtual clock; each operation reads it at most once per call so a
// captured instant never changes (exact-once now).
var stdTimeClock Clock = HostClock{}

func registerTime(name, description, effect string, params []NativeParam, result string, failures []string, targets []string, fn func([]any) (any, error)) {
	if stdRegistry["std/time"] == nil {
		stdRegistry["std/time"] = map[string]NativeOp{}
	}
	if targets == nil {
		targets = []string{"native", "wasm", "wasip1"}
	}
	effects := []string{}
	if effect != "" {
		effects = append(effects, effect)
	}
	stdRegistry["std/time"][name] = NativeOp{Name: name, Params: params, Result: result, Fn: fn, PossibleFailures: failures, Targets: targets, Description: description, Effects: effects}
	stdDocs["std/time."+name] = stdDoc{result, description, effects}
}

// registerTimeCtx registers a context-aware std/time operation so waits
// inherit the run context's cancellation and parent deadline.
func registerTimeCtx(name, description, effect string, params []NativeParam, result string, failures []string, targets []string, fn func(context.Context, Clock, []any) (any, error)) {
	registerTime(name, description, effect, params, result, failures, targets, nil)
	op := stdRegistry["std/time"][name]
	op.Fn = nil
	op.ContextFn = func(ctx context.Context, opts Options, args []any) (any, error) {
		return fn(ctx, clockOrDefault(opts.Clock), args)
	}
	stdRegistry["std/time"][name] = op
}

func bindRuntimeClock(name string, fn func(Clock, []any) (any, error)) {
	op := stdRegistry["std/time"][name]
	op.ContextFn = func(_ context.Context, opts Options, args []any) (any, error) {
		return fn(clockOrDefault(opts.Clock), args)
	}
	stdRegistry["std/time"][name] = op
}

// durationArgument accepts an elapsed-duration value or a readable/compact
// duration literal, so canonical English and technical calls share one strict
// parser. Negative or overflowing values are typed InvalidDuration failures.
func durationArgument(v any) (time.Duration, error) {
	switch d := v.(type) {
	case time.Duration:
		if d < 0 {
			return 0, invalidDuration(d.String(), "durations are nonnegative elapsed quantities")
		}
		return d, nil
	case string:
		return ParseDurationValue(d)
	}
	return 0, invalidDuration(fmt.Sprint(v), "expected a duration value or duration text")
}

// timestampArgument accepts a timestamp value or strictly RFC3339 text.
func timestampArgument(v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return time.Time{}, invalidTimestamp(t, "expected RFC3339 text")
		}
		return parsed, nil
	}
	return time.Time{}, invalidTimestamp(fmt.Sprint(v), "expected a timestamp value or RFC3339 text")
}

func zoneArgument(name string) (*time.Location, error) {
	if name == "" {
		return nil, nil
	}
	return LoadZone(name)
}

// timeInspect renders one calendar inspection record in a named zone.
func timeInspect(t time.Time, loc *time.Location) any {
	if loc != nil {
		t = t.In(loc)
	}
	return map[string]any{
		"year": float64(t.Year()), "month": float64(int(t.Month())), "day": float64(t.Day()),
		"weekday": t.Format("Monday"), "hour": float64(t.Hour()), "minute": float64(t.Minute()),
		"second": float64(t.Second()), "nanosecond": float64(t.Nanosecond()),
		"offset": t.Format("-07:00"), "abbreviation": t.Format("MST"),
	}
}

var calendarUnitNames = map[string]struct{ years, months, days int }{
	"day": {0, 0, 1}, "week": {0, 0, 7}, "month": {0, 1, 0}, "year": {1, 0, 0},
}

func calendarArgument(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || n < 1 {
			return 0, invalidSchedule("calendar count must be a positive whole number")
		}
		return int(n), nil
	case string:
		if parsed, err := strconv.Atoi(n); err == nil && parsed >= 1 {
			return parsed, nil
		}
	}
	return 0, invalidSchedule("calendar count must be a positive whole number")
}

// timeAddCalendar advances t by calendar units in a named zone, keeping the
// local clock time. Invalid month days use the declared policy: "last day"
// (default) clamps to the month end; "fail" raises a typed InvalidSchedule.
func timeAddCalendar(t time.Time, count int, unit, zone, policy string) (time.Time, error) {
	name := strings.TrimSuffix(strings.ToLower(unit), "s")
	step, ok := calendarUnitNames[name]
	if !ok {
		return time.Time{}, invalidSchedule("calendar units are days, weeks, months, or years; " + unit + " is not an elapsed duration")
	}
	if policy != "" && policy != "last day" && policy != "fail" {
		return time.Time{}, invalidSchedule("invalid-day policy must be \"last day\" or \"fail\"")
	}
	loc, err := zoneArgument(zone)
	if err != nil {
		return time.Time{}, err
	}
	if loc != nil {
		t = t.In(loc)
	} else {
		loc = t.Location()
	}
	if step.days != 0 {
		return t.AddDate(0, 0, step.days*count), nil
	}
	base := t.In(loc)
	hour, minute, second := base.Clock()
	year, month, day := base.Date()
	year += step.years * count
	month += time.Month(step.months * count)
	// Normalize into the target month before resolving the day.
	year += int(month-1) / 12
	month = time.Month(int(month-1)%12 + 1)
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > lastDay {
		if policy == "fail" {
			return time.Time{}, invalidSchedule(fmt.Sprintf("day %d does not exist in %s %d", day, month, year))
		}
		day = lastDay
	}
	return time.Date(year, month, day, hour, minute, second, base.Nanosecond(), loc), nil
}

func init() {
	registerTime("now", "Reads the runtime clock exactly once and returns the current instant. A captured timestamp never changes.", "clock", nil, "timestamp", nil, nil, func(args []any) (any, error) {
		return stdTimeClock.Now(), nil
	})
	bindRuntimeClock("now", func(clock Clock, _ []any) (any, error) { return clock.Now(), nil })
	registerTime("today", "Returns today's local midnight in a named zone, reading the runtime clock exactly once.", "clock", []NativeParam{{"zone", "text"}}, "timestamp", []string{"InvalidTimeZone"}, nil, func(args []any) (any, error) {
		loc, err := LoadZone(args[0].(string))
		if err != nil {
			return nil, err
		}
		now := stdTimeClock.Now().In(loc)
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), nil
	})
	bindRuntimeClock("today", func(clock Clock, args []any) (any, error) {
		loc, err := LoadZone(args[0].(string))
		if err != nil {
			return nil, err
		}
		now := clock.Now().In(loc)
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), nil
	})
	registerTimeCtx("sleep", "Waits for one nonnegative duration on the monotonic runtime clock. Waiting is cancelable and inherits the parent deadline.", "clock", []NativeParam{{"duration", "any"}}, "any", []string{"InvalidDuration"}, []string{"native", "wasip1"}, func(ctx context.Context, clock Clock, args []any) (any, error) {
		d, err := durationArgument(args[0])
		if err != nil {
			return nil, err
		}
		return nil, clock.Sleep(ctx, d)
	})
	registerTimeCtx("wait_until", "Resolves a timestamp once, then waits monotonically for the remaining duration. A past timestamp completes immediately.", "clock", []NativeParam{{"timestamp", "any"}}, "any", []string{"InvalidTimestamp"}, []string{"native", "wasip1"}, func(ctx context.Context, clock Clock, args []any) (any, error) {
		target, err := timestampArgument(args[0])
		if err != nil {
			return nil, err
		}
		return nil, waitClockUntil(ctx, clock, target)
	})
	registerTime("after", "Returns the instant one duration after the current runtime-clock reading; this is the deadline of a one-shot timer.", "clock", []NativeParam{{"duration", "any"}}, "timestamp", []string{"InvalidDuration"}, nil, func(args []any) (any, error) {
		d, err := durationArgument(args[0])
		if err != nil {
			return nil, err
		}
		return stdTimeClock.Now().Add(d), nil
	})
	bindRuntimeClock("after", func(clock Clock, args []any) (any, error) {
		d, err := durationArgument(args[0])
		if err != nil {
			return nil, err
		}
		return clock.Now().Add(d), nil
	})
	registerTime("parse_duration", "Parses one duration literal in readable (\"500 milliseconds\") or compact (\"500ms\") form. Strict: negative values, unknown units, and overflow are typed InvalidDuration failures.", "pure", []NativeParam{{"text", "text"}}, "duration", []string{"InvalidDuration"}, nil, func(args []any) (any, error) {
		return ParseDurationValue(args[0].(string))
	})
	registerTime("format_duration", "Renders a duration in canonical readable words; generated source never uses compact form.", "pure", []NativeParam{{"duration", "any"}}, "text", []string{"InvalidDuration"}, nil, func(args []any) (any, error) {
		d, err := durationArgument(args[0])
		if err != nil {
			return nil, err
		}
		return FormatDurationWords(d), nil
	})
	registerTime("format", "Formats a timestamp in a named format (RFC3339, RFC3339Nano, ISO date, ISO local date and time, HTTP date) or custom Go layout, in a named zone. An empty zone keeps the timestamp's own offset.", "pure", []NativeParam{{"timestamp", "any"}, {"format", "text"}, {"zone", "text"}}, "text", []string{"InvalidTimestamp", "InvalidTimeFormat", "InvalidTimeZone"}, nil, func(args []any) (any, error) {
		t, err := timestampArgument(args[0])
		if err != nil {
			return nil, err
		}
		return FormatTimestampValue(t, args[1].(string), args[2].(string))
	})
	registerTime("parse", "Parses text strictly in a named format (or custom Go layout) and zone. Never guesses locale, date order, missing zone, or two-digit-year century; failures are typed.", "pure", []NativeParam{{"text", "text"}, {"format", "text"}, {"zone", "text"}}, "timestamp", []string{"InvalidTimeFormat", "InvalidTimestamp", "InvalidTimeZone"}, nil, func(args []any) (any, error) {
		return ParseTimestampValue(args[0].(string), args[1].(string), args[2].(string))
	})
	registerTime("add_calendar", "Advances a timestamp by calendar days, weeks, months, or years in a named zone, keeping the local clock time. Invalid month days use the policy: \"last day\" (default) or \"fail\".", "pure", []NativeParam{{"timestamp", "any"}, {"count", "any"}, {"unit", "text"}, {"zone", "text"}, {"policy", "text"}}, "timestamp", []string{"InvalidTimestamp", "InvalidTimeZone", "InvalidSchedule"}, nil, func(args []any) (any, error) {
		t, err := timestampArgument(args[0])
		if err != nil {
			return nil, err
		}
		count, err := calendarArgument(args[1])
		if err != nil {
			return nil, err
		}
		policy := ""
		if args[4] != nil {
			policy, _ = args[4].(string)
		}
		return timeAddCalendar(t, count, args[2].(string), args[3].(string), policy)
	})
	registerTime("inspect", "Returns the calendar parts of a timestamp in a named zone: year, month, day, weekday, hour, minute, second, nanosecond, UTC offset, and zone abbreviation.", "pure", []NativeParam{{"timestamp", "any"}, {"zone", "text"}}, "record", []string{"InvalidTimestamp", "InvalidTimeZone"}, nil, func(args []any) (any, error) {
		t, err := timestampArgument(args[0])
		if err != nil {
			return nil, err
		}
		loc, err := zoneArgument(args[1].(string))
		if err != nil {
			return nil, err
		}
		return timeInspect(t, loc), nil
	})
	registerTime("schedule_from_cron", "Validates a five-field cron expression in a named zone and returns its next occurrence after the current runtime-clock reading. Unsupported cron extensions fail rather than being reinterpreted.", "clock", []NativeParam{{"expression", "text"}, {"zone", "text"}}, "timestamp", []string{"InvalidSchedule", "InvalidTimeZone"}, nil, func(args []any) (any, error) {
		loc, err := LoadZone(args[1].(string))
		if err != nil {
			return nil, err
		}
		schedule, err := parseCronSchedule(args[0].(string))
		if err != nil {
			return nil, err
		}
		return schedule.next(stdTimeClock.Now().In(loc), loc), nil
	})
	bindRuntimeClock("schedule_from_cron", func(clock Clock, args []any) (any, error) {
		schedule, err := ScheduleFromCron(args[0].(string), args[1].(string), SchedulePolicy{})
		if err != nil {
			return nil, err
		}
		next, err := schedule.NextAfter(clock.Now())
		if err != nil {
			return nil, err
		}
		return next[0], nil
	})
}

// reservedTimeFailures are the typed time failures from the specification.
// They merge into the checker's builtin visibility so handlers can match the
// reserved kinds without a local declaration.
func reservedTimeFailures() map[string]*FailureDef {
	return map[string]*FailureDef{
		"InvalidDuration": {Name: "InvalidDuration", Fields: []RecordField{
			{Name: "value", Type: TypeRef{Name: "text"}}, {Name: "reason", Type: TypeRef{Name: "text"}},
		}},
		"InvalidTimestamp": {Name: "InvalidTimestamp", Fields: []RecordField{
			{Name: "value", Type: TypeRef{Name: "text"}}, {Name: "reason", Type: TypeRef{Name: "text"}},
		}},
		"InvalidTimeZone": {Name: "InvalidTimeZone", Fields: []RecordField{
			{Name: "zone", Type: TypeRef{Name: "text"}},
		}},
		"InvalidTimeFormat": {Name: "InvalidTimeFormat", Fields: []RecordField{
			{Name: "value", Type: TypeRef{Name: "text"}}, {Name: "format", Type: TypeRef{Name: "text"}},
		}},
		"InvalidSchedule": {Name: "InvalidSchedule", Fields: []RecordField{
			{Name: "reason", Type: TypeRef{Name: "text"}},
		}},
		"DeadlineExceeded": {Name: "DeadlineExceeded", Fields: []RecordField{
			{Name: "allowed", Type: TypeRef{Name: "duration"}}, {Name: "elapsed", Type: TypeRef{Name: "duration"}},
		}},
		"TimerLimitExceeded": {Name: "TimerLimitExceeded", Fields: []RecordField{
			{Name: "limit", Type: TypeRef{Name: "integer"}}, {Name: "operation", Type: TypeRef{Name: "text"}},
		}},
		"ScheduleCheckpointFailed": {Name: "ScheduleCheckpointFailed", Fields: []RecordField{
			{Name: "identity", Type: TypeRef{Name: "text"}}, {Name: "reason", Type: TypeRef{Name: "text"}},
		}},
	}
}

// --- cron compatibility parsing (five fields, no extensions) ---

type cronField struct {
	values     map[int]bool
	restricted bool
}

type cronSchedule struct {
	minute, hour, dom, month, dow cronField
}

var cronMonthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var cronDowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func parseCronValue(token string, low, high int, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(token)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(token)
	if err != nil || v < low || v > high {
		return 0, fmt.Errorf("value %q outside range %d-%d", token, low, high)
	}
	return v, nil
}

func parseCronField(field string, low, high int, names map[string]int, allowSevenAsLow bool) (cronField, error) {
	out := cronField{values: map[int]bool{}}
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return out, fmt.Errorf("empty list element")
		}
		step := 1
		if slash := strings.IndexByte(part, '/'); slash >= 0 {
			s, err := strconv.Atoi(part[slash+1:])
			if err != nil || s < 1 {
				return out, fmt.Errorf("step %q must be a positive integer", part[slash+1:])
			}
			step = s
			part = part[:slash]
		}
		begin, end := low, high
		switch {
		case part == "*":
			// full range
		case strings.Contains(part, "-"):
			bounds := strings.SplitN(part, "-", 2)
			var err error
			if begin, err = parseCronValue(bounds[0], low, high, names); err != nil {
				return out, err
			}
			if end, err = parseCronValue(bounds[1], low, high, names); err != nil {
				return out, err
			}
			if begin > end {
				return out, fmt.Errorf("range %s descends", part)
			}
		default:
			v, err := parseCronValue(part, low, high, names)
			if err != nil {
				return out, err
			}
			begin, end = v, v
			if step == 1 {
				out.values[v] = true
				out.restricted = true
				continue
			}
			end = high
		}
		for v := begin; v <= end; v += step {
			out.values[v] = true
			out.restricted = true
		}
	}
	if allowSevenAsLow && out.values[7] {
		out.values[0] = true
	}
	return out, nil
}

func parseCronSchedule(expression string) (*cronSchedule, error) {
	fields := strings.Fields(expression)
	if len(fields) == 1 && strings.HasPrefix(fields[0], "@") {
		return nil, invalidSchedule("cron nicknames such as " + fields[0] + " are unsupported extensions; use five fields")
	}
	if len(fields) != 5 {
		return nil, invalidSchedule("cron expressions have exactly five fields: minute hour day-of-month month day-of-week")
	}
	schedule := &cronSchedule{}
	var err error
	if schedule.minute, err = parseCronField(fields[0], 0, 59, nil, false); err != nil {
		return nil, invalidSchedule("minute: " + err.Error())
	}
	if schedule.hour, err = parseCronField(fields[1], 0, 23, nil, false); err != nil {
		return nil, invalidSchedule("hour: " + err.Error())
	}
	if schedule.dom, err = parseCronField(fields[2], 1, 31, nil, false); err != nil {
		return nil, invalidSchedule("day of month: " + err.Error())
	}
	if schedule.month, err = parseCronField(fields[3], 1, 12, cronMonthNames, false); err != nil {
		return nil, invalidSchedule("month: " + err.Error())
	}
	if schedule.dow, err = parseCronField(fields[4], 0, 7, cronDowNames, true); err != nil {
		return nil, invalidSchedule("day of week: " + err.Error())
	}
	return schedule, nil
}

func sortedCronValues(field cronField) []int {
	values := make([]int, 0, len(field.values))
	for v := range field.values {
		values = append(values, v)
	}
	sort.Ints(values)
	return values
}

// next finds the first occurrence strictly after t, day-stepped so the search
// is bounded by five years. When both day fields are restricted, standard
// cron OR semantics apply.
func (s *cronSchedule) next(t time.Time, loc *time.Location) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	for range 366 * 5 {
		if !s.month.values[int(day.Month())] {
			day = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
			continue
		}
		dayMatch := s.dom.values[day.Day()]
		dowMatch := s.dow.values[int(day.Weekday())]
		dayOK := dayMatch && dowMatch
		if s.dom.restricted && s.dow.restricted {
			dayOK = dayMatch || dowMatch
		}
		if !dayOK {
			day = day.AddDate(0, 0, 1)
			continue
		}
		for _, hour := range sortedCronValues(s.hour) {
			for _, minute := range sortedCronValues(s.minute) {
				candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
				if candidate.After(t) {
					return candidate
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}
}
func regexMatches(args []any) (any, error) {
	text := args[0].(string)
	re, err := regexp.Compile(args[1].(string))
	if err != nil {
		return nil, err
	}
	indices := re.FindAllStringSubmatchIndex(text, 100001)
	if len(indices) > 100000 {
		return nil, fmt.Errorf("regex exceeds 100000 matches")
	}
	out := []any{}
	line, column, position := 1, 1, 0
	for _, m := range indices {
		for position < m[0] {
			r, size := utf8.DecodeRuneInString(text[position:])
			position += size
			if r == '\n' {
				line++
				column = 1
			} else {
				column++
			}
		}
		groups := []any{}
		for i := 2; i < len(m); i += 2 {
			if m[i] < 0 {
				groups = append(groups, nil)
			} else {
				groups = append(groups, text[m[i]:m[i+1]])
			}
		}
		out = append(out, map[string]any{"text": text[m[0]:m[1]], "byteStart": float64(m[0]), "byteEnd": float64(m[1]), "line": float64(line), "column": float64(column), "groups": groups})
	}
	return out, nil
}
