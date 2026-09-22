# Readable stream handling and flow control

Status: implemented

This specification adds bounded stream transformations and concurrent handling
policies to SysOneScript. It provides the practical behavior commonly described
with terms such as debounce, throttle, switch, merge, concat, exhaust, buffer,
and distinct-until-changed without making those terms the language's canonical
surface.

Canonical source describes observable behavior in ordinary English. Technical
names remain available through `std/streams`, documentation search, hover text,
external-module metadata, and accepted noncanonical aliases. Both surfaces use
one runtime implementation.

## Goals

- Make noisy, bursty, or long-running sources useful for automations.
- Keep memory, timers, keyed state, and concurrency explicitly bounded.
- Cancel obsolete work without pretending completed effects can be undone.
- Preserve stream ownership, backpressure, ordering, and typed failures.
- Give HTTP, filesystem watching, timers, processes, queues, and plugins one
  shared flow-control model.
- Keep canonical programs understandable without Rx or functional-programming
  vocabulary.
- Expose complete lifecycle state in CLI, Studio, VS Code, and the debugger.

## Non-goals

This specification does not add detached tasks, arbitrary user-defined
operators, implicit infinite queues, rollback of completed effects, unbounded
global uniqueness, hidden retries, or silent loss of work that owns an
acknowledgment obligation. It does not treat every list operation as a stream
operation or introduce function values solely to imitate another reactive API.

## Two surfaces, one meaning

Canonical:

```sos
wait for changes to be quiet for 500 milliseconds called settled_changes
```

Technical fallback:

```sos
call streams.debounce with changes, 500 milliseconds called settled_changes
```

Documentation, completion, generators, and canonicalization emit the canonical
form. Technical aliases are deterministic and incur no Jev request. Jev may
propose a canonical rewrite for unfamiliar prose, but the rewritten source is
checked before execution and becomes the durable program.

Every derived stream consumes ownership of its source. The original binding may
not be consumed, closed, or transformed again after a transformation succeeds.
The derived binding becomes the new single owner.

## Time model

Durations use the runtime's monotonic clock for waiting and elapsed-time
decisions. Wall-clock changes do not shorten or extend debounce, throttle,
batch, idle, or lifetime windows. Tests and embedders may provide a deterministic
clock; production defaults to the host monotonic clock.

A duration must be positive unless a construction explicitly permits zero.
Timer count is bounded. Canceling a derived stream cancels its timers before
upstream cleanup completes.

## Wait until activity settles

Technical concept: debounce.

```sos
wait for changes to be quiet for 500 milliseconds called settled_changes
```

Each item replaces the pending item and restarts the quiet timer. The latest
item is emitted only after no newer item arrives during the complete duration.

Keyed form:

```sos
wait for each path in changes to be quiet for 500 milliseconds
  with at most 1000 pending paths
  called settled_changes
```

Items with different keys have independent timers. The key expression must be
text, number, integer, boolean, timestamp, or another stable comparable scalar.
Exceeding the key bound fails with `StreamKeyLimitExceeded`; it never evicts an
unspecified key silently.

When upstream completes normally, every pending item is emitted in the stable
order of its last arrival and the derived stream completes. When upstream
fails, pending items are discarded and the failure propagates immediately.
Cancellation discards pending items and cancels upstream.

## Limit emission rate

Technical concept: throttle.

```sos
limit metrics to at most 10 each second
  keeping the first
  called limited_metrics
```

```sos
limit status_updates to one each second
  keeping the latest
  called limited_updates
```

`keeping the first` emits up to the allowance and drops later items during the
window. `keeping the latest` retains at most one suppressed item and emits it
when capacity becomes available. The policy is mandatory; there is no ambiguous
default.

Keyed form:

```sos
limit alerts for each account_id to 5 each minute
  keeping the first
  with at most 10000 accounts
  called limited_alerts
```

Rate windows begin with the first item for that source or key. Implementations
must not accumulate catch-up credit after idle periods beyond one complete
window's allowance. Upstream backpressure still applies independently of the
rate policy.

Dropped-item counts are observable in stream snapshots and traces. Dropping an
item that owns a response, acknowledgment, lock, or other obligation is invalid
unless an explicit rejection/disposal policy completes that obligation.

## Handle every item with bounded concurrency

Technical concepts: merge with a concurrency bound, or merge-map.

```sos
handle each request from requests
  with at most 32 at once:

  call application.handle with request
```

The handler creates at most the declared number of child contexts. Items begin
in source order when capacity is available; completion order is unconstrained.
Backpressure stops requesting source items when all handler slots and the
bounded admission queue are full.

When upstream completes, handling completes only after every admitted handler
finishes. A handler failure cancels upstream and sibling handlers, waits for
bounded cleanup, and propagates the failure unless the handler recovers it.

An owned source item must be completed, transferred, or explicitly rejected on
every reachable handler path. This rule covers HTTP response obligations,
message acknowledgments, leases, and similar capabilities.

## Handle items sequentially

Technical concept: concat or concat-map.

```sos
handle each job from jobs
  one at a time:

  call jobs.process with job
```

This is ordered, bounded handling. The next item is not requested until the
current handler finishes. It is distinct from an ordinary `for each` loop only
when the item has an owned completion obligation or the tooling needs handler
lifecycle semantics; the checker may recommend the simpler loop otherwise.

## Cancel obsolete in-flight work

Technical concepts: switch or switch-map.

```sos
handle only the newest query from queries:
  when a newer query arrives cancel the previous work

  call search.run with query called results
  show results
```

When a newer item arrives, the runtime:

1. cancels the previous handler's child context;
2. prevents its later result from becoming visible;
3. waits up to the handler cleanup deadline;
4. records cleanup failure or forced termination; and
5. starts the newest item.

If several items arrive during cleanup, only the newest waiting item is retained.
The retained queue is therefore bounded to one.

Keyed form:

```sos
handle only the newest job for each customer_id from jobs
  with at most 20 customers at once
  and at most 10000 known customers:

  call reports.generate with job
```

A newer item cancels only the active handler with the same key. The global
concurrency and key-cardinality bounds are both mandatory.

### Effect safety

Cancellation prevents future work; it does not undo effects already completed.
Every operation exposes effect and cancellation metadata:

- `pure`
- `read-only`
- `idempotent`
- `compensatable`
- `non-idempotent`
- `cancellation-safe` or `cancellation-delayed`

The checker rejects `handle only the newest` when a handler contains an owned
obligation without a replacement policy. It warns on non-idempotent effects and
requires explicit acknowledgment:

```sos
handle only the newest order from orders:
  when a newer order arrives cancel remaining work
  acknowledging completed effects are not reversed

  call payments.charge with order
```

This acknowledgment does not make the operation safe; it makes the chosen risk
visible and auditable.

## Finish active work and replace queued work

Technical concept: conflation or latest buffered item.

```sos
handle updates one at a time
  keeping only the latest waiting update:

  call dashboard.refresh with update
```

The active handler is never canceled. While it runs, at most one waiting item is
retained and replaced by newer arrivals. This is the preferred policy when work
has effects that should finish but intermediate refreshes are obsolete.

Dropped waiting items must not own unresolved obligations unless the source
declares and executes an explicit disposal policy.

## Ignore or reject arrivals while busy

Technical concept: exhaust or exhaust-map.

For disposable triggers:

```sos
handle one refresh request at a time
  ignoring new requests while busy:

  call dashboard.refresh
```

For owned requests:

```sos
handle one request at a time
  rejecting new requests with status 429 while busy:

  call reports.generate with request
```

`ignoring` is valid only for values without completion obligations. Otherwise a
typed rejection action is required and must successfully complete the item.

## Group items into bounded batches

Technical concepts: buffer and time window.

```sos
group events into batches of at most 100
  or after 5 seconds
  called batches
```

The timer starts when the first item enters an empty batch. A batch is emitted
when its count limit or time limit is reached, whichever happens first. Every
batch is a bounded list. Empty periodic batches are not emitted.

When upstream completes, a nonempty partial batch is emitted before completion.
When upstream fails, the partial batch is discarded and the failure propagates,
unless the source form explicitly requests delivery before failure.

Keyed batching:

```sos
group events for each customer_id into batches of at most 100
  or after 5 seconds
  with at most 10000 customers
  called customer_batches
```

The aggregate item bound is enforced in addition to the key bound. A stream of
streams is not produced; each output item is a finite list so ownership remains
simple.

## Ignore consecutive repeats

Technical concept: distinct-until-changed.

```sos
ignore consecutive duplicate statuses called status_changes
```

```sos
keep only changes in status from updates called status_changes
```

Only the immediately previous key is retained. This is bounded. Global
uniqueness is a different operation and is invalid without an explicit memory
or key-count bound.

Equality follows normal typed value equality. Records and lists require a
declared scalar key rather than hidden deep comparison.

## Require continued activity

Technical concept: idle timeout.

```sos
require an update at least every 30 seconds
  called monitored_updates
```

If the source produces no item during the duration, upstream is canceled and
the derived stream fails with `StreamIdleTimeout`. The timer restarts after each
emitted item. Waiting due solely to downstream backpressure does not count as
source idleness.

## Limit total listening time

Intentional bounded observation completes normally:

```sos
listen to updates for at most 10 minutes
  then stop normally
  called bounded_updates
```

A required deadline fails:

```sos
require updates to finish within 10 minutes
  called bounded_updates
```

The first form cancels upstream at the deadline and completes. The second fails
with `StreamDeadlineExceeded`. Both begin timing when the derived stream starts,
not when it is declared.

## Take a bounded sample

The existing construction remains canonical:

```sos
take first 100 items from events called sample
```

It consumes up to the bound, cancels upstream after the bound, and succeeds with
a possibly shorter list when upstream completes first.

## Filtering and projection

Canonical filtering is block-based rather than callback-based:

```sos
keep each event from events called critical_events:
  when severity of event is "critical":
    keep event
```

Canonical projection is also block-based:

```sos
take a value from each event in events called messages:
  use message of event
```

These forms create derived streams and execute their blocks once per item. The
filter block must keep at most once; the projection block must use exactly one
value on every successful path. Both inherit backpressure and cancellation.
Final grammar may refine the words `keep event` and `use`, but it must not expose
anonymous function syntax merely to match another library.

## Typed failures

The runtime provides reserved failures:

| Failure | Fields |
|---|---|
| `StreamKeyLimitExceeded` | `limit as integer`, `operation as text` |
| `StreamConcurrencyLimitExceeded` | `limit as integer` |
| `StreamIdleTimeout` | `idle_for as duration` |
| `StreamDeadlineExceeded` | `deadline as duration` |
| `StreamHandlerCleanupFailed` | `operation as text`, `reason as text` |
| `StreamObligationAbandoned` | `operation as text`, `item as optional text` |

Ordinary upstream typed failures retain their identity and gain a transformation
frame. Global cancellation and run-budget exhaustion remain fatal run outcomes.

## Backpressure and admission

Transformations request upstream items only when their complete bounded state
can accept them. A timer does not grant unlimited admission. Limits apply to:

- pending items;
- active handlers;
- waiting handlers;
- keyed state;
- items across partial batches; and
- bytes retained by pending values.

Every implementation uses the existing stream credit model. External adapters
cannot bypass transformation bounds by sending bursts beyond granted credit.

## Ordering

- Quiet waiting preserves the order of final arrival among emitted keys.
- Rate limiting preserves the order of items it retains.
- Sequential handling preserves source start and completion order.
- Concurrent handling preserves start order but not completion order.
- Latest-only handling exposes results only from the latest surviving handler.
- Batches preserve source order within each batch.
- Keyed operations preserve per-key order; no global cross-key completion order
  is promised unless the construction explicitly states one.

Tooling must show when a construction may reorder visible effects.

## HTTP and other owned items

An HTTP request owns one response obligation. A queue message may own an
acknowledgment. Such items may not be dropped, replaced, ignored, or canceled
without an explicit completion policy.

```sos
handle only the newest request for each account_id from requests
  canceling an older request with status 409:
```

```sos
limit requests to 100 each second
  rejecting excess requests with status 429
  called admitted_requests
```

The policy is part of the source and trace. A generic transformation that lacks
the domain-specific ability to complete an obligation is rejected statically.

## Technical aliases

`std/streams` exposes searchable technical operations that map to the same
runtime implementations:

| Technical operation | Canonical wording |
|---|---|
| `debounce` | `wait for ... to be quiet` |
| `throttle` | `limit ... to at most ... each ...` |
| `merge` | `handle each ... with at most ... at once` |
| `concat` | `handle each ... one at a time` |
| `switch_latest` | `handle only the newest ...` |
| `exhaust` | `handle one ... ignoring/rejecting new ... while busy` |
| `conflate` | `keeping only the latest waiting ...` |
| `batch` | `group ... into batches` |
| `distinct_consecutive` | `ignore consecutive duplicates` |
| `idle_timeout` | `require an item at least every ...` |
| `take_for` | `listen ... for at most ...` |

Editors accepting a technical alias offer a canonical rewrite. Aliases never
change runtime behavior or create a paid interpretation request.

## Tooling and observability

Stream snapshots, traces, Studio, VS Code, and the debugger expose:

- source and derived stream identities;
- canonical operation and technical name;
- active and waiting handlers;
- pending timers and nearest deadline;
- current and maximum key count;
- retained, emitted, dropped, replaced, and rejected item counts;
- current batch sizes;
- cancellation and cleanup state;
- effect-safety warnings; and
- terminal failure or completion reason.

Inspecting this state never requests an item, advances a timer, or consumes the
stream. Stop Stream targets the derived stream and propagates cancellation to
its owned source.

## Required verification

Implementation is complete only when tests cover:

- deterministic virtual-clock behavior at exact timer boundaries;
- completion, failure, and cancellation with pending debounce items or batches;
- first/latest throttling and keyed isolation;
- key, item, byte, timer, and concurrency bounds;
- upstream backpressure under slow consumers;
- latest-only cancellation during startup, execution, and cleanup;
- prevention of stale results after cancellation;
- idempotent and non-idempotent effect diagnostics;
- owned-item rejection and abandoned-obligation detection;
- ordering guarantees for every handling policy;
- independent simultaneous derived streams;
- interpreter and native-build parity;
- Studio, CLI, debugger, and VS Code observability; and
- race tests with cancellation, timer firing, and terminal events occurring
  concurrently.

## Delivery sequence

1. Add a deterministic runtime clock and derived-stream ownership model.
2. Implement quiet waiting, rate limiting, consecutive-change filtering, idle
   deadlines, and lifetime limits.
3. Implement bounded batching and keyed state limits.
4. Implement sequential and bounded concurrent owned-item handling.
5. Implement latest-only cancellation, conflation, and busy rejection.
6. Add effect/cancellation metadata and obligation checking.
7. Add canonical grammar, technical aliases, canonicalization, and editor help.
8. Add observability across CLI, Studio, VS Code, and the debugger.
9. Exercise filesystem, HTTP, timer, process, and plugin sources end to end.
10. Run race, native-build, cross-platform, and independent OMP reviews.
