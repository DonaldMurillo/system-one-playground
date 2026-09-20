# Streams and long-running operations

Status: proposed after typed failures and results

This proposal adds bounded, cancellable, single-consumer streams to
SysOneScript. A stream is an active relationship with a producer, not a slowly
arriving list. Its lifecycle, ownership, consumption, and termination are
therefore explicit in source.

Idiomatic stream syntax uses `stream` to open a producer and `from` to consume
it:

```sos
stream logs.follow with "payments" called events

for each event from events:
  show message of event
```

Collections continue to use `in`:

```sos
for each user in users:
  show name of user
```

## Goals

- Process finite or long-running values without buffering the entire result.
- Make active, one-shot behavior visible at opening and consumption sites.
- Keep memory bounded through protocol and runtime backpressure.
- Give every stream one clear owner and one terminal outcome.
- Cancel producers promptly when consumers stop, fail, finish, or leave scope.
- Integrate typed opening and terminal failures.
- Distinguish program-visible items from diagnostic progress.
- Support stdio plugins and existing CLI stdout safely.
- Preserve deterministic behavior in the interpreter, native builds, editors,
  and debuggers.

## Non-goals

The first version does not add general async/await, detached background jobs,
implicit fan-out, multicast streams, unbounded collection, automatic retries,
transparent reconnection, random access, stream rewinding, or debugger previews
that consume values.

## Declaring streaming actions

A streaming action declares an item type rather than one final result:

```sos
to follow_logs with service as text streaming LogEvent
  may fail with ServiceNotFound, ConnectionLost:

  # external or native implementation
```

Canonical grammar:

```text
stream-result := "streaming" Type
stream-action := "to" Name ["with" Parameters] stream-result
                 ["may fail with" FailureNames] ":"
```

An action cannot declare both `returning TYPE` and `streaming TYPE`. `streaming`
describes the item type, not a `list of TYPE` result. The action's declared
failures may occur while opening or after one or more items have been delivered.
Tooling records each failure's possible phase when known.

SOS package, standard-library, and external-module interfaces expose the same
streaming result metadata. Browser or host target restrictions remain part of
the operation signature.

## Opening a stream

```sos
stream follow_logs with "payments" called events
```

Qualified module actions are supported:

```sos
stream logs.follow with "payments" called events
```

Grammar:

```text
open-stream := "stream" ActionName ["with" Arguments] "called" Name
```

The statement:

1. validates arguments and capabilities;
2. starts or invokes the producer;
3. waits for the producer to confirm that the stream is open;
4. binds an owned stream handle; and
5. continues to the next statement.

It does not wait for the stream to finish. The producer may send only its
granted credit window and then pauses until consumption creates more capacity.

Opening failures attach to the stream statement normally:

```sos
stream logs.follow with service called events
  on failure ServiceNotFound using service, message:
    show "Cannot follow {service}: {message}"
    finish
```

If opening fails, no stream handle exists. A handler that continues must use the
typed-failure outcome rules; it cannot `recover with` a list. Replacing a failed
stream requires a future typed stream-recovery construction and is not implicit.

## Blocking and execution order

Opening blocks only until startup succeeds or fails. Consumption blocks the
current action until a terminal outcome:

```sos
stream logs.follow with "payments" called events
show "Stream opened"

for each event from events:
  show message of event

show "Stream finished"
```

Execution order is:

1. Open the producer.
2. Wait for open confirmation.
3. Print `Stream opened`.
4. Enter the consuming loop.
5. Wait for and process items sequentially.
6. Exit after completion, handled terminal failure, or intentional stop.
7. Print `Stream finished`.

The current action executes no statement below the consuming operation while it
is waiting for an item. External producers, explicitly parallel workers, editor
cancellation, and debugger control remain active.

Code may perform bounded setup after opening and before consuming:

```sos
stream logs.follow with "payments" called events
show "Preparing output"
make received 0

for each event from events:
  assign received received + 1
  show message of event
```

During setup the producer can fill only its bounded credit window. It cannot
grow an unbounded queue.

## Ownership and single consumption

A stream handle has exactly one owner and can be consumed once. This is invalid:

```sos
for each event from events:
  show event

for each event from events:
  show event
```

Diagnostic:

```text
events was already consumed
```

Ordinary assignment cannot copy or alias a stream:

```sos
make other events
```

The first version rejects this rather than implying ownership transfer. APIs
that accept a stream must declare a streaming parameter and take ownership
explicitly; the original binding then becomes consumed. General user-defined
stream parameters and return forwarding may be staged after direct consumption
but must preserve the same ownership model.

An owned stream must be consumed, explicitly closed, or transferred before its
scope exits. Typed code treats an abandoned stream as an error:

```text
stream events remains active
Consume it, close it, or pass ownership before leaving this scope
```

The runtime still cancels abandoned streams during error cleanup so diagnostics
never leak a process or connection.

## Explicit close and early stop

Before consumption, an owner can cancel explicitly:

```sos
stream logs.follow with service called events

when should_follow is false:
  close stream events
  finish
```

`close stream NAME` requests cancellation, waits within the shutdown deadline,
forces termination when required, and consumes the handle. It is idempotent only
for runtime cleanup; calling it twice in source is a checker error.

Inside the nearest stream loop:

```sos
for each event from events:
  show event

  when severity of event is "critical":
    stop reading

show "Stopped after the first critical event"
```

`stop reading` cancels the source, exits the nearest stream-consuming loop
successfully, and continues below it. It does not stop the application. It is
invalid outside a stream-consuming block.

`finish`, typed `fail`, `pass failure on`, fatal termination, or a body failure
while a stream is owned cancels that stream before control leaves its scope.
Cancellation does not roll back items or effects already processed.

## Consuming items

```sos
for each event from events:
  show message of event
```

Grammar:

```text
stream-loop := "for each" Name "from" Expression ":"
```

The source expression must be an unconsumed `stream of TYPE`. Each iteration
binds one validated item. Items are observed in producer sequence order. The next
item is not exposed until the current loop body completes.

Normal completion exits the loop and continues below it. A failure in the body
cancels the producer before propagating the body failure.

Terminal stream failures attach to the consuming operation:

```sos
for each event from events:
  save event
  on failure ConnectionLost called problem:
    show message of problem
    pass failure on
```

Items and side effects before a terminal failure remain. There is no transaction
and no automatic retry. Restarting may duplicate data unless an application
uses an explicit source cursor.

A consumer may accept partial processing:

```sos
for each event from events:
  append event to collected
  on failure ConnectionLost:
    recover

show "Continuing with partial data"
```

Bare `recover` treats the no-result loop as completed and continues. A terminal
handler otherwise follows typed-failure rules. `recover with` is invalid because
the loop itself does not owe a result.

## Bounded materialization

A stream becomes a list only through an explicit bound.

```sos
collect at most 1000 items from events called buffered
```

This consumes until normal completion. If item 1001 would arrive, the operation
cancels upstream and fails with a typed collection-limit failure. Terminal source
failures propagate. An unbounded `collect items from` form is invalid.

Sampling intentionally stops after a count:

```sos
take first 100 items from events called sample
```

If the stream completes first, the list is shorter. Reaching 100 cancels the
producer and succeeds. Therefore:

- `collect at most N` expects natural completion and rejects overflow.
- `take first N` intentionally truncates and cancels successfully.

Both consume ownership and bind ordinary reusable lists. `N` must be a positive
bounded integer validated before consumption begins.

## Streaming transformations

Transformations that can operate item-by-item remain lazy and bounded:

```sos
keep events where severity is "error"

for each event from events:
  show message of event
```

`keep` replaces the owned stream with a filtered stream pipeline; it does not
materialize. Predicate failure cancels upstream and propagates.

Bounded streaming map uses `from`:

```sos
map each event from events with at most 4 running called summaries:
  call summarize with event called summary
  finish with summary
```

Its result is another stream, not a list. Concurrency and buffering are bounded.
Output preserves source order even if workers finish out of order. A future
explicit mode may request completion order.

Operations requiring all values remain list-only: sort, group, reverse, last,
exact count, and random access. Diagnostics recommend bounded collection:

```text
sort requires a collection, but events is a stream
Collect it with an explicit limit first
```

The initial implementation may ship direct loops, bounded collection, and
sampling before lazy filters/maps, but it must reserve and document unsupported
syntax rather than silently materializing.

## Multiple streams and merging

Opening two streams makes two producers active:

```sos
stream logs.follow with "payments" called payment_events
stream logs.follow with "orders" called order_events
```

When one is consumed, the other may fill its bounded window and pause. Merely
opening multiple streams does not merge or consume them concurrently.

Explicit merge takes ownership:

```sos
merge payment_events, order_events called all_events

for each event from all_events:
  show event
```

Merged output uses arrival order with a deterministic sequence assigned by the
host at receipt. Cancellation or failure cancels every owned source unless a
future policy explicitly permits partial-source continuation. `merge` should be
implemented only after single-stream lifecycle semantics are stable.

## Backpressure

Every producer has a bounded credit window. Conceptually:

```text
host:   grant 16 items
plugin: emit items 1 through 16
host:   consumer releases 8 slots; grant 8 more
plugin: emit items 17 through 24
```

Credits represent permission to send complete items, not bytes. Message and
buffer byte limits apply independently. The host grants more credit only as
downstream capacity becomes available.

Guarantees:

- buffered item count and bytes remain bounded;
- fast producers eventually pause;
- slow consumers naturally slow producers;
- cancellation is not queued behind unlimited output; and
- pipeline stages propagate capacity upstream.

Plugins that exceed credit commit a protocol violation and are terminated. For
command adapters, OS pipes provide transport pressure while SOS maintains bounded
decoder queues and terminates the process when consumption ends.

## Progress is not stream data

Progress reports are diagnostic UI events, not program-visible items. A compiler
may report `Compiling 42 of 100` while still returning one final artifact. A log
follower emits actual `LogEvent` stream items.

Progress can appear in the CLI, VS Code progress UI and Output channel, Studio,
and traces. It does not affect program behavior and is not recorded as stream
data. A future explicit progress API may expose it, but no implicit conversion
is allowed.

## Stdio plugin protocol

The external-module JSON-RPC protocol adds stream messages. Opening returns a
handle without waiting for terminal completion. Conceptual methods/events:

```text
stream.open
stream.item
stream.credit
stream.end
stream.error
stream.cancel
```

Each stream carries a stream ID, originating request ID, item type, next sequence
number, credit balance, deadline, and terminal state. Items are ordered per
stream; separate streams may interleave only when the plugin declares sufficient
concurrency.

Representative flow:

```json
{"jsonrpc":"2.0","id":7,"method":"stream.open","params":{"action":"follow","arguments":{"service":"payments"},"credit":16}}
{"jsonrpc":"2.0","id":7,"result":{"streamId":"s1","itemType":"LogEvent"}}
{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":0,"value":{"message":"started"}}}
{"jsonrpc":"2.0","method":"stream.credit","params":{"streamId":"s1","credit":1}}
{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":0}}
```

Terminal error includes a declared failure envelope. Duplicate/missing/out-of-
order sequence numbers, sends beyond credit, items after termination, invalid
item types, and unknown stream IDs are protocol violations.

`stream.cancel` is acknowledged within the cancellation deadline. The host closes
or kills an unresponsive plugin under the external-module lifecycle policy.
There is no automatic reopen or retry.

## Command adapter streams

An existing CLI can expose stdout as a stream:

```toml
[action.result]
type = "stream of LogEvent"

[action.command]
program = "docker"
arguments = ["logs", "--follow", "${parameter.container}"]
stdout = "json-lines"
stderr = "diagnostic"
```

Each complete JSON line becomes one validated item. Invalid JSON or item shape is
a terminal decode failure. A partial final line is an error for JSON-lines. An
accepted exit code completes normally; another exit code becomes a declared
process failure. `stop reading`, scope cancellation, or fatal termination kills
the complete process tree after the configured grace period.

Per-item and bounded-buffer limits replace a lifetime total-output cap for a
legitimate long-running stream. Stderr remains separately bounded and diagnostic
unless the module interface models it explicitly.

## Typed failures and captured outcomes

Opening and terminal failures use the typed-failure specification. Fatal run
termination remains uncatchable. Stream failure does not invalidate items already
processed.

Capturing an entire stream as one `Result` is not allowed because no terminal
result exists until consumption. Callers instead handle open failure at `stream`
and terminal failure at the consuming operation. Bounded materialization can be
captured as an ordinary list-producing operation in a later general-operation
capture extension.

## Runners, builds, and targets

Runners track active streams, producer identity, item count, buffer occupancy,
credit, elapsed time, and terminal state. Cancellation closes every owned stream
before a run ends.

Native builds preserve the same lifecycle and limits. Browser-WASM may support
pure host-provided streams but cannot launch process-backed stdio/command streams.
WASI requires explicit host process/stream capabilities. Unsupported stream
operations fail at build time with module/action identity.

Generated artifacts embed item/failure signatures, target support, protocol
requirements, limits, and source maps. Build parity tests ensure an interpreted
and packaged program observe the same ordering and terminal outcomes.

## Language server, Studio, and VS Code

Required tooling includes:

- completion for streaming actions, `stream`, `from`, `close stream`, bounded
  collection, and `stop reading`;
- hover showing item type, failures, effects, target support, and ownership;
- diagnostics for abandoned, copied, reused, or invalidly transformed streams;
- ownership-aware rename/references and flow state;
- optional inlay state such as `stream of LogEvent`;
- debugger views that never consume future items;
- active-stream panels with explicit Cancel;
- Output/trace events for open, completion, cancellation, failure, and protocol
  violations; and
- progress UI separate from stream item display.

Debugger inspection may show:

```text
events
  state: active
  item type: LogEvent
  items received: 42
  items buffered: 3
  credit available: 13
  producer: logs.follow
```

Expanding a stream cannot request or consume an item. Item logging is opt-in and
redacted/truncated under existing trace policy.

## Diagnostics

Representative messages:

```text
events was already consumed
stream events remains active; consume it, close it, or transfer ownership
cannot copy stream events with make
stop reading requires an active stream loop
collect requires an explicit positive item limit
sort requires a collection, but events is a stream
logs.follow sent an item after stream s1 ended
logs.follow exceeded its stream credit
stream item 42 must be LogEvent; received text
external stream logs.follow is unavailable in browser-WASM builds
```

## Verification matrix

Coverage includes startup ordering, open failures, code between open/consume,
blocking below consumption, normal completion, exact single ownership, abandoned
cleanup, explicit close, early stop, body failure cancellation, typed terminal
failure and partial effects, recovery, bounded collect/take, lazy pipeline limits,
multiple paused streams, merge ordering, credit enforcement, byte/item limits,
malformed/out-of-order protocol messages, command JSON-lines and process trees,
progress separation, fatal cancellation/deadlines, interpreter/build parity, and
all LSP/Studio/VS Code/debugger surfaces.

Fixture plugins must include Python and Node producers with finite, infinite,
slow, fast, failing, cancellation-resistant, and malformed streams. Process-tree
termination runs on Linux, macOS, and Windows. Tests use no network.

## Implementation sequence

1. Add stream result types, opening syntax, ownership states, and checker flow.
2. Implement direct sequential consumption, close, early stop, and cleanup.
3. Add typed opening/terminal failure handling.
4. Add credit-based stdio protocol streams and fixture plugins.
5. Add command-adapter text/JSON-lines streams and process cancellation.
6. Add bounded collect/take and only then lazy filter/map pipelines.
7. Add explicit merge after single-stream semantics are stable.
8. Complete runners, builds, target checks, LSP, Studio, VS Code, and debugger.
9. Run cross-platform, backend-parity, build, lint, and end-to-end verification.

## Accepted design direction

- Streaming actions declare `streaming TYPE`.
- `stream ACTION ... called NAME` opens explicitly and waits only for startup.
- `for each ITEM from STREAM` consumes explicitly and blocks the current action.
- Collections retain `for each ITEM in COLLECTION`.
- Streams are single-owner and single-consumer.
- `close stream` cancels before consumption; `stop reading` cancels from a loop.
- Code below consumption waits for completion, handled failure, or early stop.
- Materialization always has an explicit bound.
- Backpressure is mandatory and credit-based for stdio plugins.
- Progress and stream items remain separate.
- No terminal failure is retried or reconnected automatically.
