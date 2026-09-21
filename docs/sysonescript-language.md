# SysOneScript language reference

Files use `.sos`.

This file describes implemented behavior. The named-record proposal is shipped
in the current language core; the design proposal remains the compatibility
and migration reference.

## Sentence grammar

One construction per line. Two-space indentation introduces a body; a colon marks block constructions. `#` begins a comment outside quoted text. Keywords are lowercase and fixed. Connector words inside strings are data, so `by jev "caused by overload"` is unambiguous.

- `called` names a result; `as` specifies a type or representation.
- `make name value` creates or rebinds a variable. `assign name value` and `set name value` require an existing variable.
- `show`, `print`, and `emit` are output aliases. These aliases are resolved deterministically. Jev-assisted semantic interpretation is a separate, configurable stage.
- `field of record` and `record.field` access the same field. `count of collection` counts items; `count of text` counts Unicode code points. `words of text` splits on whitespace.
- Double-quoted strings use JSON escapes and interpolate `{expression}`. Values include booleans, null, finite binary64 numbers, duration literals, lists, and records. Numbers are not arbitrary-precision integers. `integer` parameters require an integral number.
- Expressions support `+ - * / %`, `plus`, `minus`, `times`, comparisons, `is`, `is not`, `contains`, `and`, `or`, `not`, and parentheses. Boolean operators short-circuit. Branches require booleans; there is no implicit judgment-to-boolean conversion. Division by zero and incompatible evaluated operands fail.

```text
make total 0
make values [2, 4, 6]
for each value in values numbered from 1:
  assign total total + value
show "Total: {total}"
```

## Named records

Named records give reusable, closed structural shapes to ordinary SOS records.
Definitions are visible throughout their file or package, regardless of source
order, and exported definitions are available to importing programs by their
exported name.

```text
define User:
  name as text
  age as optional integer
  active as boolean

make donald as User with:
  name from "Donald"
  active from true

to greet with user as User using name:
  return "Hello {name}"
```

Required fields must be supplied exactly once and must be non-null. Optional
fields may be omitted or explicitly set to `null`; reading an omitted optional
field returns `null`. Unknown and duplicate fields are errors, and named
records reject undeclared fields at typed boundaries. JSON-origin records can
be passed directly when their complete shape matches. Nested named records and
`list of TYPE` fields are supported; recursive definitions are rejected.

Action parameters may use scalar, named-record, list, or `any` types:
`to greet with user as User, greeting as text:`. Typed arguments are validated
before the action body begins. Untyped parameters remain `any`. A single
named-record parameter may use `using field, other_field` to bind selected
fields at action entry; selected fields must exist and cannot collide with a
parameter or another selected field.

The idiomatic access form is `field of value`, including nested access such as
`name of manager of user`. Existing `value.field` access remains supported for
compatibility. Dots remain the normal spelling for imported module and standard
library action targets. The runtime keeps named records as ordinary immutable
record values, so JSON output contains no hidden type marker.

## Control flow and reusable actions

```text
to double with value:
  return value * 2

call double with 7 called answer
when answer >= 10:
  show "large"
otherwise:
  show "small"

repeat 3 times:
  show answer

while answer > 0:
  assign answer answer - 1
```

Declare actions before calling them. Arguments are comma-separated expressions. Actions receive local bindings and explicit return values; assignments inside them do not change the caller's variables. Loops restore their item/counter bindings when done. Other variables are dynamically typed and remain in the run environment. The checker validates constructions, names, delimiters, known named-record fields, and some literal conditions; runtime action boundaries validate declared types and data shapes.

## Files, collections, and schemas

```text
read "data.json" as json called rows
keep rows where status is "open"
sort rows by created descending
group rows by team called teams

take first 3 items from rows called sample
make report with:
  count from count of rows
  examples from sample
save report as json in "report.json"
show rows as table with id, status
```

`read` supports `json`, `text`, and `lines of json`. Relative paths resolve from the run's working directory. `find files under folder matching "*.jsonl" called logs` is nonrecursive, sorted by path, and yields `{path, name}` records. `read each log in logs as lines of json into entries` concatenates records in file/line order. A failure aborts the operation; it does not silently skip bad input.

`keep` and `sort` replace the named collection. `group` creates `{key, items}` records, preserving first occurrence order. Sort is stable. `first N items of collection` and `last N items of collection` can also appear in expressions.

```text
expect log-entry with:
  time as timestamp
  service as text
  message as text
require each entry in entries matches log-entry
```

Schema fields support text, timestamp, number, integer, boolean, and list. Timestamps must be RFC3339 with an offset. `remember now called started` captures time once; `started minus 2h` is a timestamp expression.

`create folder output if missing` creates directories. Saving defaults to exclusive creation: an existing file fails. `on existing replace` explicitly permits replacement. `save report as json under output named "{number}.json"` requires a filename without path separators. Runs are not filesystem transactions; earlier writes may remain after a later failure.

## Inline Jev

```text
keep tickets where jev "The customer needs immediate assistance"
```

The short form uses the whole item as state, threshold **0.8**, discards uncertainty, and propagates provider failures. Yes is `p_yes >= threshold`; no is `p_yes <= 1 - threshold`; the interval between them is uncertain.

```text
keep tickets where jev:
  ask "The customer is blocked from doing their work"
  using message, status
  model "jev-1.13"
  accept probability at least 0.85
  on uncertain discard
  on failure stop with "Unable to evaluate tickets"
```

`using` selects fields. Threshold must be greater than 0.5 and at most 1. Uncertain policies are `keep`, `discard`, or `stop`. Thresholds are policy choices, not guarantees of correctness. The CLI `--model` overrides the environment default; a block's `model` is the most specific override. Otherwise `TYPESAFE_DEFAULT_MODEL` or `jev-latest` is used.

For raw typed answers, all three primitives are available:

```text
judge message by jev "The request is urgent" called urgency
show urgency.p_yes

classify message by jev "Which team should handle this?" called team:
  "access": "Account or login problems"
  "billing": "Payments and invoices"
  "other": "Neither"
show team.value
show team.confidence

score message by jev "How urgent is this request?" called priority:
  0: "No urgency"
  1: "Some urgency"
  2: "Immediate assistance needed"
show priority.expected
```

Choice returns `value`, `confidence`, and `probabilities`. Score returns `expected`, `confidence`, and `probabilities`; levels are consecutive starting at zero. Noul returns `p_yes` only. Required response fields, ranges, and distributions are validated. No automatic retry occurs in the language provider, making the call ceiling count HTTP attempts directly. Judgments are sequential within a block; parallel maps can evaluate independent items concurrently.

## Named question batches

`evaluate state by jev using questions called answers` submits a record of named questions in one request. Pure `std/jev` constructors build questions from data. See [question batches](sysonescript-question-batches.md) for validation, budgets and replay.

## Failure handling and CLI parameters

```text
read "optional.json" as json called rows
  on failure make rows as empty list
  on success:
    show "Loaded rows"
```

An operation's handlers are indented under it, even if the operation has no colon. A failure sets the `error` text binding. `on success` runs only after successful execution. A failure inside a handler propagates. Cancellation always propagates.

Typed actions can declare a result and a closed set of domain failures:

```text
define failure InvalidCity:
  city as text

to weather with city as text returning text may fail with InvalidCity:
  when city is "":
    fail InvalidCity with "A city is required":
      city from city
  finish with city
```

Typed handlers use `on failure NAME`, bind the structured `failure` value with
`called`, and must explicitly `recover`, `finish`, `fail`, or `pass failure on`.
Use `capture ACTION ... called outcome` when the failure should become data;
`when succeeded of outcome` narrows `value` and `failure` to their valid
branches. `sos check FILE --json` reports diagnostics, action signatures, and
possible failure names for editor and build tooling.

## Commands and inputs

```text
command tickets:
  describe "Inspect support tickets"
  option source as file default "tickets.json"

  command list:
    describe "Show open tickets"
    option limit as integer default 10
    read source as json called tickets
    keep tickets where status is "open"
    take first limit items from tickets called selected
    show selected as table with id, team

  command export:
    argument destination as file
    read source as json called tickets
    save tickets as json in destination
      on existing stop
```

Use `sos run tickets.sos -- list --limit 4`, or `tickets list --limit 4`
on its standalone executable. Parent options work before or after the selected
child: `--source backlog.json list` and `list --source backlog.json` agree.
Only the selected leaf executes. Groups contain commands; leaves contain
executable statements. A CLI file cannot also execute top-level statements;
move those statements inside a leaf. Plain scripts without commands still work.
Shared actions and schemas can be declared at top level.

`argument` is positional and required. An `option` is required unless it has a
literal default. `switch` defaults to off and accepts `--judge` or
`--judge=false`. Types are text, number, integer, folder, file, duration, and
boolean. File/folder values are paths; operations validate existence. Finite
text choices use `option criterion as text choices "urgent", "all" default "urgent"`.
Duplicate inputs, unknown flags, and invalid values are usage errors. A second
`--` after the runner separator makes following script values positional, even
when they start with a dash.

`--help` (or `-h`) prints generated command help without required inputs,
execution, or Jev requests. Invoking a group alone also prints help. The Studio
command selector chooses a leaf and displays fields for its inputs and inherited
options. See [commands and actions](sysonescript-commands.md)
for the full contract and [tickets walkthrough](sysonescript-tickets.md) for a runnable example.

## Interpretation

Canonical source runs locally in every interpretation mode. Assisted and semantic
modes add a constrained pass before execution. Use `sos explain FILE` to inspect
its decisions, and `--save` to pin them for later runs and builds. The
[interpretation guide](sysonescript-interpretation.md) describes the workflow and
public Go APIs. Local `check` and `fmt` continue to use the canonical grammar.

## Runtime limits and recordings

Global/project TOML and optional `+++` TOML frontmatter configure per-run request
and timeout ceilings. See [configuration](sysonescript-configuration.md) for strict
validation, inheritance, explicit zero, and `sos config FILE`. Frontmatter is
configuration, never a semantic model call. Body diagnostics retain source lines.

Default limits: 100,000 execution steps, 100 Jev requests, 128 action calls deep, 16 MiB per input file/value, 100,000 nodes per evaluated value, and 64 value nesting levels. Studio adds a 30-second default deadline, 32-call ceiling, and 1 MiB output cap. These are operational limits, not an OS sandbox; run trusted scripts.

`--record answers.json` records judgment answers and metadata with source/state/question/model hashes. `--replay answers.json` uses that ordered record without contacting Jev; changed source, state, or call order fails. It does not replay filesystem writes safely or freeze time: external inputs must still match, and writes still execute. Model questions/answers can contain sensitive content; the API key is not recorded.

Replay consumes no new provider-request budget. Live admissions remain counted
after failures; usage distinguishes reported input tokens from unresolved usage.

Imports of arbitrary Go packages, open-ended semantic grouping, arbitrary prose, an `own file` naming convention, and a plain JavaScript backend are not implemented. Local modules and bounded parallel maps are available.

The Go utility `UsesJev(*Statement)` identifies canonical provider operations
using the same token boundaries as the checker and interpreter. Names such as
`jevscore` and quoted text containing `where jev` are ordinary data/expressions;
they do not make an operation a provider call or disqualify a WASM build.
`on uncertain` takes inline `keep`, `discard`, or `stop`; its block form is
rejected during checking rather than deferred until an uncertain answer occurs.

## Packages and editor services (0.3)

See [local packages and the first standard library](sysonescript-packages.md) for
`package`, `export`, `import`, qualified calls, and the typed standard operation
catalog. See [editor services](sysonescript-editor.md) for semantic colors, inferred
type hints, folding, completion, and offline auto-import.

## Parallel processing and host libraries (0.3)

See [parallel maps and failures](sysonescript-parallel.md), [I/O and data libraries](sysonescript-io.md), and [SOS semlint](sysonescript-semlint.md). Failure handlers retain the `error` text and expose a `failure` record; `rethrow` propagates the original typed error from the active handler.
