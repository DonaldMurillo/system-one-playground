# Time, timers, and schedules

Canonical, checked wording for reading the clock, waiting, repeating work,
calendar schedules, and deadlines. The full design is
[sysonescript-time-spec.md](sysonescript-time-spec.md); this page is the user
guide. Use `sos capabilities` to inspect clock and time-zone support on the
current target.

## Concepts

The language keeps six ideas distinct:

- a **timestamp** is one exact instant on the UTC timeline;
- a **duration** is elapsed monotonic time (`30 seconds`, `5 minutes`);
- a **date** is a calendar day without a time or zone;
- **local time** is a clock reading that names no instant until paired with a date and zone;
- a **time zone** is an IANA name such as `America/New_York` or `UTC`; and
- a **schedule** is a structured rule that produces future instants in a named zone.

An offset like `+05:30` is not a zone: it cannot describe daylight-saving
transitions. Calendar arithmetic (add a month) is separate from elapsed
arithmetic (add 30 minutes) because calendar units vary in length.

## Reading and computing

```sos
remember the current time called started
remember today's date in time zone "America/New_York" called today

make expires_at started plus 30 minutes
make elapsed finished minus started

find the time one calendar day after started
  in time zone "America/New_York"
  called tomorrow
```

Each statement reads the runtime clock exactly once; a captured timestamp never
changes. Timestamp plus duration is a timestamp; timestamp minus timestamp is a
signed duration. Calendar arithmetic keeps the local clock time and applies an
explicit policy for days that do not exist (`use the last day of the month` or
`fail`), so January 31 plus one month never silently lands in March.

## Waiting and timers

```sos
wait for 5 seconds
wait until deadline

stream one tick after 30 seconds called timer

stream a tick every 10 seconds called ticks

for each tick from ticks:
  call health.check
```

Repeating timers are anchored to their original schedule, so a ten-second timer
whose handler takes four seconds still fires at 00:00, 00:10, 00:20 — never at
00:00, 00:14, 00:28. Each delivery is a `TimeTick` record with `sequence`,
`scheduled_for`, `observed_at`, and `missed` fields. The first tick comes after
one full interval unless the source says `now and every`.

When a consumer falls behind, the default `combining missed ticks` policy
retains one pending tick and folds the rest into its `missed` count. Explicit
alternatives are `skipping missed ticks` and `catching up at most 3 ticks`;
there is no unbounded catch-up mode. `missed` describes only the schedule
positions combined into that delivery, not the timer's lifetime total.
The pending catch-up bound is shared by all timers in a run.

## Calendar schedules

```sos
stream scheduled times
  every weekday at 9:00
  in time zone "America/New_York"
  called ticks
```

Every calendar schedule names a zone. Daylight-saving gaps and overlaps get
explicit policy (`skip it` or `use the next valid time` for gaps; `run only at
the first occurrence` by default for overlaps). A schedule never depends on the
host locale. Cron expressions remain available for interoperability through
`std/time.schedule_from_cron`, which validates the expression and rejects
unsupported extensions instead of reinterpreting them. Cron schedules obey
the same explicit gap and overlap policies as readable calendar schedules.

Without durable state a schedule starts at `the next scheduled time`. With the
future state library you can `remember progress as "hourly-report"` and `run
once immediately if a scheduled time was missed`, bounded by
`catch up at most 3 missed scheduled times`. Checkpoints advance only after the
handler succeeds; exactly-once external effects stay the application's job.

## Deadlines

```sos
allow at most 30 seconds for:
  call reports.generate called report
  call files.write_text with "report.json", report called written
```

The block gets one child context measured from block entry. Everything created
inside — calls, streams, HTTP requests, waits — inherits it, and a child cannot
extend a parent's remaining time. When time runs out the runtime cancels the
scope, stops owned work, suppresses stale results, and fails with
`DeadlineExceeded` unless handled. A per-operation `timeout after 5 seconds`
limits one request; a scoped deadline limits the whole workflow; both inherit
any earlier parent deadline.

## Formatting and parsing

```sos
format timestamp as RFC3339 called text
format timestamp as an ISO date in time zone "UTC" called date_text
read timestamp from text as RFC3339 called timestamp
```

Named formats are RFC3339, RFC3339 with nanoseconds, ISO date, ISO local date
and time, and HTTP date. Parsing is strict: no locale guessing, no ambiguous
date order, no two-digit years. Custom layouts exist only through technical
`time.format` / `time.parse`.

## Virtual time

Test harnesses may `advance test time by 10 minutes` on a deterministic virtual
clock instead of sleeping. Advancing runs due timers in scheduled-time order
and drives waits, timer streams, stream transformations, deadlines, and calendar
schedules through the same runtime clock. Embedders inject `VirtualClock` into
the run; `VirtualStreamClock` is a separate low-level helper for testing a
stream transformation without starting a language run. Virtual time never
covers uncontrolled processes, file watchers, DNS, or sockets; tests use
adapters at those boundaries.

## Failures

Time operations fail with reserved typed failures: `InvalidDuration`,
`InvalidTimestamp`, `InvalidTimeZone`, `InvalidTimeFormat`, `InvalidSchedule`,
`DeadlineExceeded`, `TimerLimitExceeded`, and `ScheduleCheckpointFailed`. Run
cancellation stays fatal rather than catchable.

## Bundled time zones and build metadata

Native, WASI, and browser artifacts embed Go's IANA database (`time/tzdata`),
so calendar schedules behave identically on Linux, macOS, and Windows and never
depend on host zone files. Interpreted runs through the `sos` CLI share the
same guarantee, because the CLI links the embedded database; if you embed the
interpreter as a library, import `time/tzdata` yourself or ship a zone
database, since the interpreter package alone does not embed one.

Every build records what it bundled. `sos build` writes the provenance beside
a standalone binary as `<artifact>.timezone.json` (for `--output healthwatch`,
that is `healthwatch.timezone.json`); wasm-browser bundle targets carry it as
`timezone.json` inside the bundle archive. The manifest records the database
source, the IANA release identifier (for example `2025b`), a SHA-256 digest,
zone count, and the per-target clock capability matrix for native, WASI, and
browser targets on Linux, macOS, and Windows. Verify a recorded manifest
against the current database with the `timebundle` package
(`internal/timebundle`), whose `Check` rejects database, release, or zone-count
drift and whose `VerifyResolution` exercises DST transitions, half-hour
shiftes, and historical offset changes straight from the bundled bytes.

## Examples

See `examples/sos/time-basics` (clock, waits, timers, deadlines; runs in a few
seconds), `examples/sos/time-schedules` (calendar schedules and
daylight-saving policy; waits for the next scheduled local time), and
`examples/sos/time-service` (a fast single-check command plus a scheduled CLI
service with scoped deadlines).
