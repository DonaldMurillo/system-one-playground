package sos

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPureCollectionTransforms(t *testing.T) {
	percentile := stdRegistry["std/list"]["percentile"].Fn
	sample := []any{9.0, 1.0, 5.0, 3.0}
	got, err := percentile([]any{sample, 50.0})
	if err != nil || got != 3.0 {
		t.Fatalf("percentile: %v %v", got, err)
	}
	if !reflect.DeepEqual(sample, []any{9.0, 1.0, 5.0, 3.0}) {
		t.Fatal("mutated sample")
	}
	if _, err := percentile([]any{[]any{}, 50.0}); err == nil {
		t.Fatal("empty accepted")
	}
	slice := stdRegistry["std/text"]["slice"].Fn
	got, err = slice([]any{"aé日z", 1, 3})
	if err != nil || got != "é日" {
		t.Fatalf("slice: %v %v", got, err)
	}
	if _, err := slice([]any{"text", 3.0, 2.0}); err == nil {
		t.Fatal("reversed bounds accepted")
	}
	original := map[string]any{"a": 1.0}
	got, err = stdRegistry["std/record"]["set"].Fn([]any{original, "a", 2.0})
	if err != nil || original["a"] != 1.0 || got.(map[string]any)["a"] != 2.0 {
		t.Fatalf("record set: %v %v", got, err)
	}
}

// --- std/time language-facing operations ---

func timeOp(t *testing.T, name string) NativeOp {
	t.Helper()
	op, ok := stdRegistry["std/time"][name]
	if !ok {
		t.Fatalf("std/time.%s is not registered", name)
	}
	return op
}

func TestStdTimeDurationParsing(t *testing.T) {
	op := timeOp(t, "parse_duration")
	cases := []struct {
		in   string
		want string
	}{
		{"500 milliseconds", "500ms"},
		{"30 seconds", "30s"},
		{"1 hour 30 minutes", "1h30m0s"},
		{"500ms", "500ms"},
		{"2h", "2h0m0s"},
		{"7 days", "168h0m0s"},
	}
	for _, c := range cases {
		got, err := op.Fn([]any{c.in})
		if err != nil || got.(time.Duration).String() != c.want {
			t.Errorf("parse_duration(%q) = %v, %v; want %s", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"-5 seconds", "1 month", "5 foos", "", "9223372036854775808 seconds"} {
		_, err := op.Fn([]any{bad})
		var typed *typedFailure
		if err == nil || !errors.As(err, &typed) || typed.kind != "InvalidDuration" {
			t.Errorf("parse_duration(%q) error = %v; want typed InvalidDuration", bad, err)
		}
	}
}

func TestStdTimeStrictParseFormat(t *testing.T) {
	parse, format := timeOp(t, "parse"), timeOp(t, "format")
	got, err := parse.Fn([]any{"2026-01-02T03:04:05Z", "RFC3339", "UTC"})
	if err != nil || got.(time.Time).UTC().Format(time.RFC3339) != "2026-01-02T03:04:05Z" {
		t.Fatalf("parse RFC3339 = %v, %v", got, err)
	}
	if _, err := parse.Fn([]any{"02/01/2026", "RFC3339", "UTC"}); err == nil {
		t.Fatal("strict parsing must reject locale-style dates")
	}
	// Invalid UTF-8 fails rather than being silently reinterpreted.
	if _, err := parse.Fn([]any{string([]byte{0xff, 0xfe}) + "T00:00:00Z", "RFC3339", "UTC"}); err == nil {
		t.Fatal("invalid UTF-8 must fail parsing")
	}
	if _, err := parse.Fn([]any{"2026-01-02T03:04:05", "RFC3339", "UTC"}); err == nil {
		t.Fatal("missing zone must fail")
	}
	text, err := format.Fn([]any{"2026-01-02T03:04:05Z", "ISO date", "UTC"})
	if err != nil || text.(string) != "2026-01-02" {
		t.Fatalf("format ISO date = %v, %v", text, err)
	}
	if _, err := format.Fn([]any{"2026-01-02T03:04:05Z", "ISO date", "Nowhere/Central"}); err == nil {
		t.Fatal("unknown zone must fail")
	}
	var typed *typedFailure
	_, err = format.Fn([]any{"2026-01-02T03:04:05Z", "ISO date", "Nowhere/Central"})
	if !errors.As(err, &typed) || typed.kind != "InvalidTimeZone" {
		t.Fatalf("zone error = %v; want typed InvalidTimeZone", err)
	}
}

func TestStdTimeNowReadsClockExactlyOnce(t *testing.T) {
	start := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	clock := NewVirtualClock(start)
	original := stdTimeClock
	stdTimeClock = clock
	defer func() { stdTimeClock = original }()
	op := timeOp(t, "now")
	first, err := op.Fn(nil)
	if err != nil || !first.(time.Time).Equal(start) {
		t.Fatalf("now = %v, %v; want the virtual start instant", first, err)
	}
	clock.AdvanceTo(start.Add(time.Minute))
	second, _ := op.Fn(nil)
	if !second.(time.Time).Equal(start.Add(time.Minute)) {
		t.Fatalf("now after advance = %v", second)
	}
	// The first capture never changes: exact-once semantics.
	if !first.(time.Time).Equal(start) {
		t.Fatal("captured timestamp changed")
	}
}

func TestStdTimeTodayLocalMidnight(t *testing.T) {
	start := time.Date(2026, 3, 1, 20, 30, 0, 0, time.UTC)
	clock := NewVirtualClock(start)
	original := stdTimeClock
	stdTimeClock = clock
	defer func() { stdTimeClock = original }()
	op := timeOp(t, "today")
	got, err := op.Fn([]any{"Pacific/Honolulu"})
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	midnight := got.(time.Time)
	if midnight.Hour() != 0 || midnight.Minute() != 0 || midnight.Day() != 1 || midnight.Location().String() != "Pacific/Honolulu" {
		t.Fatalf("today = %v; want local midnight", midnight)
	}
}

func TestStdTimeCalendarArithmetic(t *testing.T) {
	op := timeOp(t, "add_calendar")
	// January 31 plus one month clamps to the last day by default.
	got, err := op.Fn([]any{"2026-01-31T09:00:00Z", 1.0, "month", "UTC", ""})
	if err != nil {
		t.Fatalf("add_calendar: %v", err)
	}
	if got.(time.Time).Format("2006-01-02") != "2026-02-28" {
		t.Fatalf("last-day clamp = %v", got)
	}
	// The same operation with policy "fail" is a typed InvalidSchedule.
	_, err = op.Fn([]any{"2026-01-31T09:00:00Z", 1.0, "month", "UTC", "fail"})
	var typed *typedFailure
	if !errors.As(err, &typed) || typed.kind != "InvalidSchedule" {
		t.Fatalf("fail policy error = %v; want typed InvalidSchedule", err)
	}
	// Calendar arithmetic respects the local clock time across DST.
	got, err = op.Fn([]any{"2026-03-07T09:00:00Z", 1.0, "day", "America/New_York", ""})
	if err != nil {
		t.Fatalf("calendar day: %v", err)
	}
	if got.(time.Time).In(time.FixedZone("EST", -5*3600)).Format("15:04") != "04:00" && got.(time.Time).Format("15:04") != "04:00" {
		t.Fatalf("calendar day keeps local clock time: %v", got)
	}
	// Months are not durations.
	if _, err = op.Fn([]any{"2026-01-31T09:00:00Z", 1.0, "fortnight", "UTC", ""}); err == nil {
		t.Fatal("unknown calendar unit must fail")
	}
}

func TestStdTimeCronSchedule(t *testing.T) {
	op := timeOp(t, "schedule_from_cron")
	got, err := op.Fn([]any{"0 9 * * mon-fri", "America/New_York"})
	if err != nil {
		t.Fatalf("schedule_from_cron: %v", err)
	}
	next := got.(time.Time)
	if next.Hour() != 9 || next.Minute() != 0 {
		t.Fatalf("next occurrence = %v; want 09:00 local", next)
	}
	if wd := int(next.Weekday()); wd == 0 || wd == 6 {
		t.Fatalf("next occurrence %v falls on a weekend", next)
	}
	for _, bad := range []string{"@daily", "0 9 * * * *", "60 * * * *", "0 9 * * jan#2"} {
		_, err := op.Fn([]any{bad, "UTC"})
		var typed *typedFailure
		if !errors.As(err, &typed) || typed.kind != "InvalidSchedule" {
			t.Errorf("cron %q error = %v; want typed InvalidSchedule", bad, err)
		}
	}
}

func TestStdTimeReservedFailuresVisible(t *testing.T) {
	visible, problems := visibleFailuresFrom(map[string]*FailureDef{}, nil, "test")
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	for _, kind := range []string{"InvalidDuration", "InvalidTimestamp", "InvalidTimeZone", "InvalidTimeFormat", "InvalidSchedule", "DeadlineExceeded", "TimerLimitExceeded", "ScheduleCheckpointFailed"} {
		if visible[kind] == nil {
			t.Errorf("reserved failure %s is not visible", kind)
		}
	}
	// Reserved kinds cannot be redeclared locally.
	_, problems = visibleFailuresFrom(map[string]*FailureDef{"InvalidDuration": {Name: "InvalidDuration"}}, nil, "test")
	if len(problems) == 0 {
		t.Error("redeclaring a reserved time failure must be a problem")
	}
}

func TestTimeStatementLowering(t *testing.T) {
	source := "import \"std/time\"\nremember the current time called started\nremember now called again\nwait for 5 seconds\nwait until started\nformat started as RFC3339 called text\nformat started as RFC3339 with nanoseconds called precise\nformat started as an ISO date in time zone \"UTC\" called day\nread timestamp from \"2026-01-02T03:04:05.123456789Z\" as RFC3339 with nanoseconds called precise_parsed\nread timestamp from \"2026-01-02T03:04:05Z\" as RFC3339 called parsed\nfind the time one calendar month after started in time zone \"UTC\" called next\n"
	p, ds := ParseWithVocabulary(source, nil)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	want := []string{
		"import \"std/time\"",
		"call time.now called started",
		"call time.now called again",
		"call time.sleep with \"5 seconds\"",
		"call time.wait_until with started",
		"call time.format with started, \"RFC3339\", \"\" called text",
		"call time.format with started, \"RFC3339Nano\", \"\" called precise",
		"call time.format with started, \"ISO date\", \"UTC\" called day",
		"call time.parse with \"2026-01-02T03:04:05.123456789Z\", \"RFC3339Nano\", \"\" called precise_parsed",
		"call time.parse with \"2026-01-02T03:04:05Z\", \"RFC3339\", \"\" called parsed",
		"call time.add_calendar with started, 1, \"month\", \"UTC\", \"\" called next",
	}
	if len(p.Statements) != len(want) {
		t.Fatalf("statements = %d; want %d", len(p.Statements), len(want))
	}
	for i, w := range want {
		if p.Statements[i].Text != w {
			t.Errorf("statement %d = %q; want %q", i, p.Statements[i].Text, w)
		}
	}
}

func TestTimeEnglishRemembersDistinguishFromOrdinary(t *testing.T) {
	// A remember of an ordinary expression is untouched by time lowering.
	p, ds := ParseWithVocabulary("remember \"later\" called note", nil)
	if len(ds) != 0 || p.Statements[0].Text != "remember \"later\" called note" || p.Statements[0].Kind != "remember" {
		t.Fatalf("ordinary remember was rewritten: %q %v", p.Statements[0].Text, ds)
	}
}
