# Parallel maps and failures

Use a parallel map when independent items can be processed concurrently:

```text
make inputs [1, 2, 3]
map each item in inputs with at most 8 running called results:
  make doubled item * 2
  return doubled
show results
```

`map each item in inputs with at most workers running called results:` is fixed
language syntax. The worker limit is an integer from 1 through 128; the input
must be a list with at most 100,000 items. An empty input returns an empty list.
Each iteration must `return` a value. `number` is the one-based input position.

## Isolation and ordering

Go workers share a queue of input positions. Each iteration receives a deep copy
of the surrounding JSON values and its input item, so changing a local list or
record cannot modify the parent or another worker's bindings. Return values are
collected in input order, regardless of completion order.

Stdout and stderr from workers are buffered and flushed in input order after the
workers finish. Their combined buffered output across the run is limited to
16 MiB. They are not live progress streams. Embedders can use `OnTrace` for live
provider events: callback invocations are serialized, but arrive in completion
order. Final trace collections are grouped by input order.

This isolates language values, not external effects. Concurrent writes to the
same file or subprocess operations on the same repository can still interfere.
Choose distinct output paths or perform those effects after collecting results.
Standard input is unavailable inside a parallel map. Nested parallel maps are
rejected, including maps reached through actions called by a worker.

## Budgets, cancellation and batching

All workers share the run's provider request budget, request-count limit, step
limit and cancellation context. They reserve requests before dispatch, so eight
workers cannot each spend the full run allowance independently. A timeout,
explicit exit, budget or execution-limit failure cancels the map. The runtime
waits for workers to finish before returning; context-aware provider calls and
subprocesses receive cancellation. A blocking host operation must cooperate for
prompt cancellation.

A batch remains a single provider request. Eight workers can evaluate eight
units concurrently, each containing its own question batch. Parallelism does not
merge questions between units or change their thresholds.

## Collecting failures

By default, an unhandled worker error cancels the map. To inspect ordinary
per-item failures while allowing other items to finish:

```text
import "std/files" as files
make paths ["present.txt", "missing.txt"]
map each filename in paths with at most 4 running called outcomes collecting failures:
  call files.read with filename called text
  return text
show outcomes
```

The result contains one record per input:

```json
{"ok": true, "value": "file contents", "error": null}
```

A failed result has `ok: false`, `value: null`, and an `error` record with
`kind`, `message`, `retryable`, and numeric `status`. Provider errors can also
include `code`. Kinds distinguish runtime, provider, connection, timeout,
canceled, budget, limit, and input-too-large failures (`input_too_large`).
These fields describe failures; they do not automatically retry a request.

Cancellation, budget exhaustion, execution limits, explicit exit, and replay
integrity errors remain fatal even when collecting failures. Failure collection
is not a way to bypass run limits.

Handlers receive the legacy `error` text and a structured `failure` record with
these same fields. Use `rethrow` inside a handler to propagate the original
failure, preserving its provider status and kind instead of converting it into
a new text error. Calling `rethrow` outside a handler is an error.

## Record and replay

Parallel judgments are recorded with a logical path containing the enclosing
map location, map invocation sequence and input position. Matching follows this
identity rather than network completion order. Collected output stays ordered
when requests finish in a different order, or the worker count changes.

Ordinary provider failures are recorded too, so replay can reproduce collected
failure outcomes without another provider call.

Keep the source and inputs compatible with the recording. Missing or unused
judgments fail replay validation. Runtime recordings remain separate from saved
semantic interpretation decisions, and can contain sensitive source inputs.
