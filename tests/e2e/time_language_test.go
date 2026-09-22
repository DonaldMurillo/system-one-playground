package e2e

import (
	"strings"
	"testing"
)

// Canonical English time statements and std/time technical fallbacks share one
// checked implementation. These runs exercise the language-facing surface end
// to end through the CLI: exact-once now, waiting, strict parsing and
// formatting, calendar arithmetic, and typed failure exposure.

func TestTimeEnglishAndTechnicalFormsAgree(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/time"

remember the current time called started
call time.now called technical
wait for 1 millisecond
call time.sleep with "1 millisecond"
format started as ISO date in time zone "UTC" called day_text
read timestamp from "2026-01-02" as ISO date in time zone "UTC" called parsed
format parsed as RFC3339 called parsed_text
show day_text
show parsed_text
`
	path := writeScript(t, dir, "time.sos", source)
	out, err, code := runCLI(t, dir, "run", path)
	if code != 0 {
		t.Fatalf("code %d out %q error %s", code, out, err)
	}
	if !strings.Contains(out, "2026-01-02") || !strings.Contains(out, "2026-01-02T00:00:00Z") {
		t.Fatalf("missing expected time output: %q", out)
	}
}

func TestTimeTodayAndCalendarArithmetic(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/time"

remember today's date in time zone "Pacific/Honolulu" called today
format today as ISO date in time zone "Pacific/Honolulu" called today_text
call time.inspect with "2026-03-31T12:00:00Z", "America/New_York" called parts
find the time one calendar month after "2026-01-31T09:00:00Z" in time zone "UTC" called clamped
format clamped as ISO date in time zone "UTC" called clamped_text
show today_text
show clamped_text
show parts
`
	path := writeScript(t, dir, "calendar.sos", source)
	out, err, code := runCLI(t, dir, "run", path)
	if code != 0 {
		t.Fatalf("code %d out %q error %s", code, out, err)
	}
	// Invalid month days clamp to the last day of the month by default.
	if !strings.Contains(out, "2026-02-28") {
		t.Fatalf("missing clamped calendar date: %q", out)
	}
}

func TestTimeStrictParsingFailsTyped(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/time"

read timestamp from "02/01/2026" as RFC3339 in time zone "UTC" called parsed
show parsed
`
	path := writeScript(t, dir, "strict.sos", source)
	_, err, code := runCLI(t, dir, "run", path)
	if code == 0 {
		t.Fatalf("strict parsing must reject ambiguous date order")
	}
	if !strings.Contains(err, "InvalidTimeFormat") && !strings.Contains(err, "does not match") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestTimeInvalidDurationIsTypedFailure(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/time"

call time.parse_duration with "-5 seconds" called d
show d
`
	path := writeScript(t, dir, "duration.sos", source)
	_, err, code := runCLI(t, dir, "run", path)
	if code == 0 {
		t.Fatalf("negative durations must fail validation")
	}
	if !strings.Contains(err, "InvalidDuration") && !strings.Contains(err, "invalid duration") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestTimeTypedFailureHandlerRecovers(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/time"

call time.parse with "not a timestamp", "RFC3339", "UTC" called parsed
  on failure InvalidTimeFormat:
    recover with "2020-01-01T00:00:00Z"
show parsed
`
	path := writeScript(t, dir, "handler.sos", source)
	out, err, code := runCLI(t, dir, "run", path)
	if code != 0 || !strings.Contains(out, "2020-01-01") {
		t.Fatalf("code %d out %q err %s", code, out, err)
	}
}

func TestTimeWaitUntilPastTimestampCompletes(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/time"

wait until "2020-01-01T00:00:00Z"
show "done"
`
	path := writeScript(t, dir, "past.sos", source)
	out, err, code := runCLI(t, dir, "run", path)
	if code != 0 || !strings.Contains(out, "done") {
		t.Fatalf("code %d out %q err %s", code, out, err)
	}
}

func TestTimeCronScheduleNextOccurrence(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/time"

call time.schedule_from_cron with "0 9 * * mon-fri", "America/New_York" called next
format next as RFC3339 in time zone "America/New_York" called local_next
show local_next
`
	path := writeScript(t, dir, "cron.sos", source)
	out, err, code := runCLI(t, dir, "run", path)
	if code != 0 {
		t.Fatalf("code %d out %q err %s", code, out, err)
	}
	if !strings.Contains(out, "T09:00:00") || !strings.Contains(out, "-05:00") && !strings.Contains(out, "-04:00") {
		t.Fatalf("unexpected next occurrence: %q", out)
	}
}
