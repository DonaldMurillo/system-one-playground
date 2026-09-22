# Time, timers, schedules, and deadlines

Status: implemented

This specification defines instants, durations, calendar values, waiting,
repeating timer streams, calendar schedules, deadlines, time zones, formatting,
and deterministic virtual time. It consolidates existing timestamp and duration
behavior behind one runtime clock and provides the timing foundation used by
stream handling, HTTP, processes, and long-running services.

Canonical source describes timing behavior in ordinary English. `std/time`
provides technical fallback operations for tooling, external modules, and
advanced use. Both surfaces use the same checked implementation.

## Goals

- Make elapsed waits, repeating work, and calendar schedules readable.
- Keep timers, pending ticks, catch-up, and retained schedule state bounded.
- Prevent repeating timers from drifting with handler duration.
- Make missed ticks and restart behavior visible rather than accidental.
- Handle time zones and daylight-saving transitions deterministically.
- Propagate cancellation and parent deadlines through timed work.
- Make all language-visible timing testable with one virtual clock.
- Preserve interpreter, native-build, Studio, VS Code, debugger, and CLI parity.

## Non-goals

This specification does not provide distributed clock synchronization,
transactional exactly-once scheduling, a holiday/business-calendar database,
detached jobs, durable scheduling without an explicit state store, or virtual
time for uncontrolled operating-system processes and public networks. Cron is a
compatibility input, not the canonical language.

## Time concepts

SysOneScript keeps these concepts distinct:

- `timestamp`: one exact instant on the UTC timeline;
- `duration`: elapsed monotonic time;
- `date`: a calendar date without a time or zone;
- `local time`: a clock reading without an instant until paired with a date and
  named time zone;
- `time zone`: an IANA zone with historical and future offset rules; and
- `schedule`: a rule that produces future instants in a named zone.

An offset such as `+05:30` is not a time zone and cannot represent daylight-
saving transitions. Calendar arithmetic is not elapsed-duration arithmetic.

## Reading the current time

Canonical forms:

```sos
remember the current time called started
remember today's date in time zone "America/New_York" called today
```

Compatibility form:

```sos
remember now called started
```

Technical fallback:

```sos
call time.now called started
call time.today with "America/New_York" called today
```

Each statement reads the runtime clock exactly once. A captured timestamp never
changes. Tooling and traces distinguish actual runtime time from a virtual test
clock.

## Duration values

Existing readable duration literals remain canonical:

```sos
500 milliseconds
30 seconds
5 minutes
2 hours
7 days
```

Compact forms such as `500ms`, `30s`, and `2h` remain accepted technical input,
but generated source uses words. Durations are finite elapsed quantities.
Negative or overflowing duration values fail validation unless an operation
explicitly accepts signed differences.

Calendar months and years are not durations because their elapsed length varies.

## Waiting

```sos
wait for 5 seconds
wait until deadline
```

Waiting is cancelable and inherits the current context deadline. Waiting until a
past timestamp completes immediately. `wait for` uses the monotonic clock;
`wait until` resolves its timestamp once, then waits monotonically for the
remaining duration so wall-clock adjustments do not unpredictably restart it.

Technical fallbacks are `time.sleep` and `time.wait_until`.

## One-shot timer streams

```sos
stream one tick after 30 seconds called timer
```

The stream emits one `TimeTick` and completes. It differs from `wait` because it
can be transformed, canceled independently, inspected, or eventually selected
alongside another stream.

## Repeating timer streams

```sos
stream a tick every 10 seconds called ticks

for each tick from ticks:
  call health.check
```

Repeating timers are anchored to their original schedule. If handling a tick
takes four seconds, a ten-second timer remains scheduled at 00:00, 00:10, 00:20,
and 00:30 rather than drifting to 00:00, 00:14, 00:28, and 00:42.

The first tick occurs after one complete interval unless source explicitly says:

```sos
stream a tick now and every 10 seconds called ticks
```

## TimeTick

```sos
define TimeTick:
  sequence as integer
  scheduled_for as timestamp
  observed_at as timestamp
  missed as integer
```

`sequence` begins at one. `scheduled_for` is the anchored target instant;
`observed_at` is when the runtime admitted the tick. `missed` counts schedule
positions combined into this delivery and is zero for an on-time ordinary tick.

## Slow consumers and missed ticks

Timer streams never create an unbounded queue. The canonical default is:

```sos
stream a tick every 10 seconds called ticks
  combining missed ticks
```

When the consumer falls behind, the runtime retains one pending tick and reports
the number of additional schedule positions in `missed`.

Alternative explicit policies:

```sos
stream a tick every 10 seconds called ticks
  skipping missed ticks
```

```sos
stream a tick every 10 seconds called ticks
  catching up at most 3 ticks
```

Skipping emits only the next future tick. Bounded catch-up emits at most the
declared number of historical ticks, then combines any additional misses into
the final emitted tick. There is no unbounded catch-up mode.

Stream snapshots expose missed, combined, skipped, and caught-up counts.

## Calendar schedules

Canonical forms:

```sos
stream scheduled times
  every weekday at 9:00
  in time zone "America/New_York"
  called ticks
```

```sos
stream scheduled times
  every Monday at 8:00
  in time zone "Europe/London"
  called ticks
```

```sos
stream scheduled times
  on the first day of each month at 00:15
  in time zone "UTC"
  called ticks
```

Every calendar schedule requires a named zone. The grammar supports explicit
minutes, hours, weekdays, month days, months, and combinations whose next
occurrence can be calculated without model interpretation.

Source-order and grammar parsing are deterministic. Jev may help canonicalize
unfamiliar wording, but the saved schedule is a checked structured rule.

## Cron compatibility

`std/time.schedule_from_cron` accepts a validated cron expression and named time
zone for interoperability. Cron is not emitted by completion, examples, or
generators. Tooling displays the corresponding English schedule when it can do
so without losing meaning.

Unsupported cron extensions fail rather than being silently reinterpreted.

## Daylight-saving transitions

For a local scheduled time that does not exist during a forward transition, the
default is:

```sos
when a scheduled local time does not exist
  skip it
```

Alternative:

```sos
when a scheduled local time does not exist
  use the next valid time
```

For a local time that occurs twice during a backward transition, the default is:

```sos
when a scheduled local time occurs twice
  run only at the first occurrence
```

Alternatives may select the second occurrence or both occurrences. These
policies are part of schedule metadata and traces. A schedule never silently
depends on the host locale.

## Startup and missed calendar schedules

Without durable progress, a schedule starts at the next occurrence after the
stream opens:

```sos
start with the next scheduled time
```

It cannot know which occurrences were missed while the process was stopped.
Durable catch-up requires a checkpoint identity and the future state library:

```sos
stream scheduled times
  every hour
  remembering progress as "hourly-report"
  run once immediately if a scheduled time was missed
  called ticks
```

Additional explicit policy:

```sos
catch up at most 3 missed scheduled times
```

Checkpoint updates occur only after the owning handler reports successful
completion. Exactly-once external effects are not implied; idempotency remains
the application's responsibility.

## Scoped deadlines

```sos
allow at most 30 seconds for:
  call reports.generate called report
  call files.write_text with "report.json", report called written
```

The block receives one child context with a deadline measured from block entry.
Every operation, stream, process, provider call, HTTP request, and wait created
inside inherits it. The duration does not reset for each statement.

When the deadline expires, the runtime:

1. cancels the child context;
2. stops owned streams and child processes;
3. waits for bounded cleanup;
4. prevents stale child results from becoming visible; and
5. fails with `DeadlineExceeded` unless handled.

Nested blocks use the earliest effective deadline. A child cannot extend its
parent's remaining time.

## Operation timeout versus workflow deadline

```sos
get JSON from endpoint called result
  timeout after 5 seconds
```

limits one operation. A scoped deadline limits a complete workflow. Both inherit
an earlier parent deadline. Tooling reports the effective deadline and which
scope established it.

## Elapsed arithmetic

```sos
make expires_at started plus 30 minutes
make elapsed finished minus started
```

Timestamp plus or minus duration yields a timestamp. Timestamp minus timestamp
yields a signed duration. Elapsed arithmetic uses exact timeline instants and is
independent of display time zones.

## Calendar arithmetic

```sos
find the time one calendar day after started
  in time zone "America/New_York"
  called tomorrow
```

Supported units are calendar days, weeks, months, and years. The operation keeps
the local clock time where possible and resolves it using explicit daylight-
saving policy.

For invalid month days, the caller selects a policy:

```sos
when the day does not exist
  use the last day of the month
```

or:

```sos
when the day does not exist
  fail
```

There is no hidden overflow from January 31 into March.

## Calendar inspection

Readable operations expose:

- date in a named zone;
- local clock time in a named zone;
- year, month, day, weekday, hour, minute, second, and nanosecond;
- start and end of local day;
- UTC offset and zone abbreviation; and
- comparison and signed elapsed difference.

Examples:

```sos
find the weekday of timestamp in time zone "UTC" called weekday
find the start of its day in time zone "America/New_York" called day_start
```

## Parsing and formatting

Named formats are canonical:

```sos
format timestamp as RFC3339 called text
format timestamp as an ISO date in time zone "UTC" called date_text
read timestamp from text as RFC3339 called timestamp
```

Supported named formats include RFC3339, RFC3339 with nanoseconds, ISO date,
ISO local date and time, and HTTP date.

Custom layouts are available through technical `std/time` operations:

```sos
call time.format with timestamp, layout, zone called text
call time.parse with text, layout, zone called timestamp
```

Parsing is strict. It never guesses locale, date order, missing zone, or
two-digit-year century. Invalid or ambiguous values fail with typed failures.

## Time zones

IANA names such as `America/New_York`, `Europe/London`, `Asia/Tokyo`, and `UTC`
are canonical. Native artifacts bundle the time-zone database used at build
time, record its version in build metadata, and do not silently vary according
to host zone-file availability.

Fixed offsets may be used for parsing and display where explicitly accepted,
but calendar schedules require `UTC` or an IANA zone.

## Runtime clock

All language-visible timing uses one clock interface with equivalent operations
for:

- current instant;
- one-shot timer;
- anchored repeating timer; and
- cancelable waiting.

Direct host-clock calls are forbidden in language semantics, stream
transformations, HTTP deadlines, process deadlines, and service scheduling.
Telemetry may use a separate host clock only when it cannot affect program
behavior and is labeled accordingly.

## Virtual time for tests

Tests and embedding hosts may provide a deterministic virtual clock. A test host
can advance it without sleeping:

```sos
advance test time by 10 minutes
```

This construction is available only in test harnesses, never ordinary
production programs. Advancing time runs due timers in deterministic
scheduled-time and creation order until no newly due work remains.

Virtual time drives:

- waits;
- one-shot and repeating timers;
- quiet waiting and rate limiting;
- batching;
- idle and total-lifetime limits;
- scoped deadlines; and
- calendar schedules.

It does not virtualize uncontrolled process execution, operating-system file
watchers, DNS, sockets, or public providers. Tests use adapters at those
boundaries.

## Timer ownership and limits

Every timer or schedule stream is single-owner and follows ordinary stream
cleanup. Runtime policy bounds active timers per run, active keyed timers per
transformation, pending catch-up ticks, and retained bytes. Exceeding a bound
fails visibly; timers are never evicted arbitrarily.

Stopping a timer stream cancels its timer before reporting completion. Timer
callbacks cannot emit after terminal state.

## Typed failures

The runtime provides reserved failures:

| Failure | Fields |
|---|---|
| `InvalidDuration` | `value as text`, `reason as text` |
| `InvalidTimestamp` | `value as text`, `reason as text` |
| `InvalidTimeZone` | `zone as text` |
| `InvalidTimeFormat` | `value as text`, `format as text` |
| `InvalidSchedule` | `reason as text` |
| `DeadlineExceeded` | `allowed as duration`, `elapsed as duration` |
| `TimerLimitExceeded` | `limit as integer`, `operation as text` |
| `ScheduleCheckpointFailed` | `identity as text`, `reason as text` |

Run cancellation remains a fatal outcome rather than a catchable time failure.
An inherited parent deadline preserves its original failure and context frame.

## Technical operations

`std/time` exposes searchable operations corresponding to canonical forms:

| Technical operation | Canonical wording |
|---|---|
| `now` | `remember the current time` |
| `today` | `remember today's date in time zone ...` |
| `sleep` | `wait for ...` |
| `wait_until` | `wait until ...` |
| `after` | `stream one tick after ...` |
| `every` | `stream a tick every ...` |
| `schedule` | `stream scheduled times ...` |
| `with_deadline` | `allow at most ... for:` |
| `format` | `format timestamp as ...` |
| `parse` | `read timestamp from text ...` |
| `add_calendar` | `find the time ... calendar ... after ...` |

Editors accepting technical wording offer a canonical rewrite. These operations
never require Jev.

## Tooling and observability

CLI, Studio, VS Code, traces, and the debugger expose:

- clock kind: host or virtual;
- current virtual time where applicable;
- timer identity and owner;
- interval or structured schedule;
- time zone and daylight-saving policy;
- next scheduled instant;
- emitted, missed, combined, skipped, and caught-up tick counts;
- active scoped deadline and remaining time;
- checkpoint identity and last completed schedule; and
- terminal completion, cancellation, or typed failure.

Inspection never advances a virtual clock, fires a timer, or consumes a tick.
Stop Stream targets one timer or schedule without stopping the complete run.

## Targets and reproducibility

Elapsed timers work on native, WASI, and browser hosts with a monotonic clock.
Calendar schedules require a bundled or host-declared time-zone database.
Build manifests record time-zone database provenance (source, IANA release,
digest, zone count) and supported clock capabilities: standalone binaries
carry `<artifact>.timezone.json` beside them, and wasm-browser bundles carry
`timezone.json` inside the bundle archive.

Replay and semantic-decision recordings do not freeze time. Tests requiring
repeatability provide a virtual clock and explicit starting instant.

## Required verification

Implementation is complete only when tests cover:

- duration parsing, overflow, negative values, and readable/compact forms;
- exact once-only reads of current time;
- wait cancellation and parent deadline inheritance;
- anchored repeating timers without handler-duration drift;
- combine, skip, and bounded catch-up behavior under backpressure;
- exact timer-boundary races among item, cancellation, and completion;
- calendar schedules across zones, leap years, month ends, and historical
  offset transitions;
- nonexistent and repeated daylight-saving local times under every policy;
- restart behavior with and without durable checkpoints;
- nested scoped deadlines and stale-result suppression;
- strict parsing and formatting, including invalid UTF-8 and ambiguous input;
- bundled time-zone behavior independent of host files;
- deterministic virtual-clock ordering and timer creation during callbacks;
- timer/key/count/byte limits and cleanup;
- interpreter and native-build parity;
- CLI, Studio, debugger, and VS Code observability; and
- Linux, macOS, Windows, WASI, and browser capability diagnostics.

## Delivery sequence

1. Introduce the shared runtime clock and migrate language-visible host-clock
   calls without changing existing behavior.
2. Implement instants, dates, durations, zones, strict parsing, formatting, and
   elapsed/calendar arithmetic.
3. Implement cancelable waits, one-shot timers, and anchored repeating streams.
4. Implement missed-tick policies, limits, tracing, and tooling snapshots.
5. Implement structured calendar schedules and daylight-saving policies.
6. Implement scoped deadlines and migrate HTTP, process, provider, and external
   module timing to the shared clock/context model.
7. Implement virtual time and deterministic timer-driven tests.
8. Add optional durable schedule checkpoints after the state contract exists.
9. Add canonical grammar, technical aliases, canonicalization, and complete
   CLI/service examples.
10. Run native-build, cross-platform, race, virtual-time, and independent OMP
    reviews before declaring the specification implemented.
