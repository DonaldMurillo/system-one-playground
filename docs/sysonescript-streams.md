# Streams and long-running operations

SysOneScript streams are bounded, cancellable, and single-consumer. They model
an active producer rather than a list that happens to arrive slowly. Opening,
ownership, consumption, and cancellation are therefore visible in source.

Rebuilding over an existing bundled directory requires native atomic directory
exchange (macOS, or Linux amd64/arm64 on a supporting filesystem). Other
platforms preserve the existing bundle and require a new output path; the
builder never removes a live bundle to imitate atomic replacement.

## Declare, open, and consume

A streaming action declares its item type with `streaming`:

```sos
to follow with service as text streaming LogEvent
  may fail with ServiceNotFound, ConnectionLost:
  send {"message": "connected", "service": service}
  send {"message": "waiting for updates", "service": service}
  finish
```

`send VALUE` emits one typed item and pauses when the consumer has not granted
capacity. It is valid only inside a streaming action. Local producers start
lazily when consumption begins, so setup after `stream` is deterministic.

Open the producer with `stream`, then consume it with `from`:

```sos
stream logs.follow with "payments" called events
show "Stream opened"

for each event from events:
  show message of event

show "Stream finished"
```

Opening waits for startup confirmation, not completion. The loop processes one
validated item at a time in producer order and blocks the current action until
normal completion, handled terminal failure, or intentional cancellation.
Collections continue to use `for each value in values`.

The called name owns the stream. It can be consumed exactly once and cannot be
copied with `make`. Before leaving its scope, code must consume or close it:

```sos
close stream events
```

Inside the nearest stream loop, `stop reading` cancels the producer, exits the
loop successfully, and continues below it. `finish`, a propagated failure, or
fatal cancellation also closes every owned producer before control leaves its
scope. Expanding a stream in the debugger reports its state and counters; it
never requests or consumes a future item.

## Typed failures and partial effects

Opening failures attach to `stream`; failures after opening attach to the
consuming operation. Items and effects already observed remain visible.

```sos
stream logs.follow with service called events
  on failure ServiceNotFound using service, message:
    show "Cannot follow {service}: {message}"
    finish

for each event from events:
  save event
  on failure ConnectionLost called problem:
    show message of problem
    recover
```

Bare `recover` accepts the partial work and continues. A stream loop does not
produce a result, so `recover with` is invalid there. Fatal run termination is
not catchable and still cancels the producer.

## Bounded lists

Materializing a stream always requires a positive bound:

```sos
collect at most 1000 items from events called buffered
take first 100 items from events called sample
```

`collect at most` expects natural completion; item 1001 cancels upstream and
fails with the built-in `StreamLimitExceeded` failure, exposing integer
`limit` and `received` fields. `take first` intentionally cancels
after the requested sample and succeeds, or returns a shorter list when the
producer completes first. Neither construction creates an unbounded buffer.

## External producers and backpressure

External stdio actions use `stream.open`, `stream.item`, `stream.credit`,
`stream.end`, `stream.error`, and `stream.cancel`. Credit counts complete items;
the producer must pause when credit reaches zero. Item count, item bytes, and
decoded-buffer bytes are bounded independently. Sequence gaps, invalid item
types, sends beyond credit, and items after a terminal message are protocol
violations and terminate the producer.

Command adapters can expose `stream of TYPE` from JSON-lines stdout. Each
complete line is decoded and type-checked as one item. Invalid JSON, an invalid
item shape, a partial final line, or a rejected process exit becomes a terminal
stream failure. Early stop, explicit close, and run cancellation terminate the
complete process tree after the configured grace period. Stderr remains a
separately bounded diagnostic channel.

Command streams can expose conventional typed terminal failures when the action
declares the exact failure name and its definition accepts an empty payload:

- `StreamDecodeFailure` covers invalid JSON, a partial final JSON line, or an
  item that does not match the declared stream item type.
- `ProcessFailure` covers stdout read failures and process exits outside the
  action's accepted exit codes.
- `StreamTimeout` covers the action deadline expiring while the stream is open.

If the matching name is not declared, or its definition requires payload
fields, the condition remains an ordinary terminal runtime error. These
failures never discard items or effects already observed by the consumer.

Progress remains separate from stream data. The runtime does not convert plugin
diagnostics or UI progress into program-visible items. Studio and VS Code show
runtime-owned stream identity, producer, item type, received/buffered counts,
credit, elapsed time, terminal failures, and lifecycle history without reading
an item. **Stop stream** ends only that producer and lets execution continue
below its stream loop; the ordinary run Stop action still cancels the whole run.
Editor inspection travels a loopback control channel whose failure policy —
environment-only authentication, bounded frames and connections, and counted
lifecycle-event drops — is documented in [editor language
services](sysonescript-editor.md).

Lazy stream filters/maps, merge, and stream parameters/ownership transfer are
reserved future syntax. The shipped surface is direct sequential consumption,
explicit close/early stop, bounded collect/take, stdio plugin streams, and
command-adapter JSON-lines streams.

## Flow control and derived streams

Bounded stream transformations and concurrent handling policies are specified in
the approved [readable stream handling and flow-control
design](sysonescript-stream-handling-spec.md). This section documents the
implemented runtime and editor behavior.

Every policy has two surfaces with one meaning and one runtime implementation:
canonical English wording, and a searchable technical name in `std/streams`
(`debounce`, `throttle`, `merge`, `concat`, `switch_latest`, `exhaust`,
`conflate`, `batch`, `distinct_consecutive`, `idle_timeout`, `take_for`).
Technical aliases are deterministic, never create a paid interpretation request,
and editors offer a canonical rewrite. Documentation, completion, and
canonicalization emit the canonical form. Handler operations (`merge`,
`concat`, `switch_latest`, `exhaust`, and `conflate`) require their canonical
block form so the checker can inspect effects and obligation completion; a
bodyless technical call is rejected instead of silently draining the stream.

### Quiet waiting and rate limiting

`wait for changes to be quiet for 500 milliseconds called settled_changes`
(debounce) restarts a quiet timer on every item and emits only the latest item
after the complete duration passes with no newer arrival. The keyed form keeps
one independent timer per key with an explicit pending-key bound; exceeding it
fails with `StreamKeyLimitExceeded` rather than silently evicting a key.

`limit metrics to at most 10 each second keeping the first` (throttle) emits up
to the allowance per window and drops later items; `keeping the latest` retains
at most one suppressed item and emits it when capacity returns. The keep policy
is mandatory — there is no ambiguous default — and idle periods do not
accumulate catch-up credit. Dropped-item counts are observable in snapshots and
traces, and dropping an item that owns a response, acknowledgment, or lease is
invalid unless an explicit rejection or disposal policy completes that
obligation.

### Bounded concurrent and sequential handling

`handle each request from requests with at most 32 at once` (merge-map) runs at
most the declared number of child handlers; items start in source order,
complete in any order, and backpressure stops admission when handler slots and
the bounded queue are full. A handler failure cancels upstream and siblings,
waits for bounded cleanup, and propagates. Runtime embedders can choose that
bound with `HandleWithBoundConcurrencyCleanup`; the simpler helper uses five
seconds and reports `StreamHandlerCleanupFailed` on expiry. `handle each ... one at a time`
(concat-map) is the ordered, capacity-one form; the checker may recommend a
plain `for each` loop when no owned obligation or handler lifecycle is needed.
For HTTP request streams, cancellation also completes the failed request and
every active sibling with a bounded service-unavailable response, even when a
handler ignores its canceled context until the cleanup deadline.

### Canceling, finishing, and rejecting while busy

`handle only the newest query from queries` (switch_latest) cancels the previous
handler's child context on each newer item, prevents stale results from becoming
visible, waits up to a cleanup deadline, and records cleanup failure. The
waiting queue is bounded to one item. Because cancellation prevents future work
but never undoes completed effects, the checker rejects it when a handler owns
an obligation without a replacement policy, and requires an explicit
`acknowledging completed effects are not reversed` marker on non-idempotent
effects.

If cleanup times out after a newer owned item is pulled but before its handler
starts, the replacement policy settles that item and the run reports the
cleanup failure. Cancellation/rejection policy lines describe the policy;
only the handler's executable statements count toward response completion.
If a handler fails or the run is canceled, active owned siblings and any item
pulled but not yet delivered are settled through their declared policy.

`handle updates one at a time keeping only the latest waiting update` (conflate)
never cancels the active handler and retains at most one newer waiting item.
`handle one request at a time ignoring new requests while busy` (exhaust) is
valid only for values without completion obligations; owned requests require a
typed rejection such as `rejecting new requests with status 429 while busy`.

### Batching, filtering, and projection

`group events into batches of at most 100 or after 5 seconds called batches`
(batch) starts the timer on the first item of an empty batch, emits whichever
limit fires first, never emits empty periodic batches, and emits a nonempty
partial batch on normal upstream completion. Keyed batching adds the key bound
and derives the aggregate item bound as batch size times maximum keys.

`ignore consecutive duplicate statuses` (distinct-until-changed) retains only
the immediately previous key; global uniqueness is a different, invalid-without-
a-bound operation. Records and lists must use `keep only changes in <field>
from <stream>` to declare the scalar comparison key; they are never compared
deeply by implication. Canonical filtering and projection are block-based:

```sos
keep each event from events called critical_events:
  when severity of event is "critical":
    keep event
```

```sos
take a value from each event in events called messages:
  use message of event
```

### Time bounds and lifetimes

All durations use the monotonic clock; wall-clock changes never shorten or
extend a window. `require an update at least every 30 seconds` (idle_timeout)
cancels upstream and fails with `StreamIdleTimeout` when the source stays idle;
backpressure waiting does not count as idleness. `listen to updates for at most
10 minutes then stop normally` (take_for) completes normally at the deadline,
while `require updates to finish within 10 minutes` fails with
`StreamDeadlineExceeded`. The existing `take first 100 items from events`
remains the canonical bounded sample.

### Ownership, ordering, and failures

Every derived stream consumes ownership of its source: after a transformation
succeeds, the original binding may not be consumed, closed, or transformed
again; the derived binding is the new single owner. Canceling a derived stream
cancels its timers before upstream cleanup. Backpressure is enforced through the
same stream credit model everywhere — pending items, active and waiting
handlers, keyed state, partial-batch items, retained bytes, and timer count are
all bounded, and external adapters cannot bypass these bounds.

Ordering per policy: quiet waiting and rate limiting preserve the order of what
they emit, sequential handling preserves start and completion order, concurrent
handling preserves start order only, latest-only handling exposes only the
surviving handler's results, and batches preserve source order within each
batch. Keyed operations preserve per-key order without promising global
cross-key completion order.

Reserved failures are `StreamKeyLimitExceeded`, `StreamConcurrencyLimitExceeded`,
`StreamIdleTimeout`, `StreamDeadlineExceeded`, `StreamHandlerCleanupFailed`, and
`StreamObligationAbandoned`. Ordinary upstream failures keep their identity and
gain a transformation frame.

See the runnable [stream examples](https://github.com/DonaldMurillo/system-one-playground/tree/main/examples/sos/streams) project for
local streaming actions, Python and Node stdio producers, finite consumption,
early stop, explicit close, bounded collection, sampling, terminal failure
handling, and a multi-producer flow-control program that combines in-language
and Node producers in one run.
