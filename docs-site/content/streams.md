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

Lazy stream filters/maps, merge, and stream parameters/ownership transfer are
reserved future syntax. The shipped surface is direct sequential consumption,
explicit close/early stop, bounded collect/take, stdio plugin streams, and
command-adapter JSON-lines streams.

See the runnable [stream examples](https://github.com/DonaldMurillo/system-one-playground/tree/main/examples/sos/streams) project for
local streaming actions, Python and Node stdio producers, finite consumption,
early stop, explicit close, bounded collection, sampling, and terminal failure
handling.
