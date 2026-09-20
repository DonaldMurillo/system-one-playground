# Typed failures and results

Status: proposed after named records

This proposal gives SysOneScript actions explicit, inspectable failure
contracts while preserving readable default propagation. It evolves the
existing `on failure`, `failure`, and `rethrow` behavior instead of adding an
unrelated exception system.

The idiomatic control-flow vocabulary is:

```text
finish
finish with VALUE
fail FAILURE with MESSAGE
pass failure on
recover
recover with VALUE
```

Existing `return VALUE` and `rethrow` forms remain valid compatibility syntax.
Documentation, completion, and generated source prefer the idiomatic forms.

## Goals

- Declare the expected failures an action may produce.
- Verify failure propagation through the action call graph.
- Expose possible outcomes to editors, runners, builders, help, debuggers, and
  external-module tooling.
- Keep successful calls concise; handle failures only where useful.
- Distinguish recoverable operation failures from fatal run termination.
- Require explicit recovery when a failed operation owed a result.
- Allow failures to become data for tests, batches, and reporting.
- Preserve identity and frames when passing a failure onward.

## Non-goals

This does not add class exceptions, inheritance, implicit retries, unchecked
suppression, resumable execution inside a failed operation, process-crash
recovery, or mandatory result unwrapping after ordinary calls.

## Failure definitions

```sos
define failure InvalidCity:
  city as text

define failure WeatherUnavailable:
  retry_after as optional duration
```

Failure definitions follow named-record field, package, and export rules. Each
failure also has host-owned common fields:

| Field | Type | Meaning |
|---|---|---|
| `kind` | text | Exact declared name or stable built-in kind |
| `message` | text | Safe human-readable explanation |
| `retryable` | boolean | Information only; never triggers a retry |
| `status` | optional integer | Provider/process/protocol status when relevant |
| `code` | optional text | Stable subsystem code when relevant |
| `frames` | list | Read-only action/module/source frames |

User fields cannot use reserved common names. User code can read common fields
but cannot construct or modify `frames`. Locally created failures default to
non-retryable. External operations may provide validated instance metadata.

## Producing failures

```sos
fail InvalidCity with "A city is required":
  city from city
```

With no payload fields, omit the block:

```sos
fail WeatherUnavailable with "The weather service is unavailable"
```

The message must be text. Payload construction uses named-record rules:
required fields must exist, optional fields may be absent/null, and unknown,
duplicate, or mistyped fields fail checking or execution.

`fail` immediately ends the current action through its failure path. Code after
an unconditional failure is unreachable. A handler may deliberately transform
one failure into another:

```sos
call weather.current with city called report
  on failure NetworkUnavailable called original:
    fail WeatherUnavailable with message of original
```

The replacement gains a transformation frame but does not silently inherit the
original payload or retryability.

## Successful completion and action signatures

```sos
finish
finish with report
```

`finish` completes with no value. `finish with` evaluates once and completes
with that value. Typed actions declare their result and expected failures:

```sos
to fetch_weather with city as text returning WeatherReport
  may fail with InvalidCity, WeatherUnavailable:

  when city is "":
    fail InvalidCity with "A city is required":
      city from city

  call weather.current with city called report
    on failure NetworkUnavailable called original:
      fail WeatherUnavailable with message of original

  finish with report
```

Canonical grammar:

```text
action := "to" Name ["with" Parameters] ["returning" Type]
          ["may fail with" FailureNames] ":"
```

The formatter may wrap before `may fail with`; parsing treats the wrapped lines
as one declaration. Legacy actions may omit annotations. New exported actions
and generated interfaces should declare them.

The checker verifies every successful path. A value-returning action cannot use
bare `finish`; a no-result action cannot use `finish with`. `return VALUE` has
the same semantics as `finish with VALUE` and remains non-idiomatic compatibility
syntax.

## Closed failure contracts

`may fail with` is a closed set of expected catchable failures. The checker
computes failures from explicit `fail`, called actions, standard operations,
external-module definitions, and handler recovery/transformation.

Every possible catchable failure must be handled locally or declared. If
`fetch_weather` can produce `InvalidCity` and `WeatherUnavailable`, this is an
error:

```sos
to create_report with city as text returning WeatherReport
  may fail with InvalidCity:

  call fetch_weather with city called report
  finish with report
```

Diagnostic:

```text
create_report may pass WeatherUnavailable on
Handle it or add it to "may fail with"
```

Normal calls need no local handler. A declared failure automatically propagates
through an enclosing compatible signature. At a command boundary, it becomes a
structured run failure.

## Typed handlers

```sos
call fetch_weather with city called report
  on failure InvalidCity using city, message:
    show "Could not load {city}: {message}"
    finish

  on failure WeatherUnavailable called problem:
    recover with cached_report

  on failure:
    pass failure on
```

Rules:

- Typed handlers match exact kinds; there is no inheritance.
- Handlers are checked in source order.
- A kind may be handled once for an operation.
- General `on failure:` matches remaining catchable failures and must be last.
- `called NAME` binds the complete typed failure.
- `using FIELDS` binds selected common or declared fields.
- `called` and `using` are mutually exclusive initially.
- Unknown fields, duplicates, unreachable handlers, and collisions are errors.
- Failure inside a handler propagates outward, not to a sibling handler.

Legacy implicit `error` text and `failure` records remain available in untyped
handlers. Typed handlers should prefer explicit bindings.

## Handler outcomes

A handler makes its outcome explicit on every path.

### Recover

```sos
call fetch_weather with city called report
  on failure WeatherUnavailable:
    recover with cached_report

show summary of report
```

`recover with VALUE` handles the failure, validates and supplies the missing
operation result, then continues after the operation. A no-result operation uses
bare `recover`. Recovery never resumes inside the failed operation and never
rolls back effects already performed.

### Pass the active failure onward

```sos
on failure WeatherUnavailable called problem:
  show message of problem
  pass failure on
```

`pass failure on` exits via the existing failure path while preserving kind,
payload, metadata, identity, and original frames and adding the current frame.
It is valid only in an active handler. `rethrow` remains an exact non-idiomatic
alias.

### Other outcomes

A handler may instead `finish`, `finish with`, create another typed `fail`, or
`stop with`. Any branch that reaches the end without `recover`, `pass failure
on`, `finish`, `fail`, or `stop` is an incomplete-handler error. This prevents a
failed result binding from remaining undefined.

## Fatal run termination

These are not domain outcomes and cannot be caught, recovered, or captured:

- user cancellation;
- overall run deadline;
- run/request budget exhaustion;
- execution and memory safety limits;
- replay-integrity failure;
- host/runtime corruption; and
- forced process termination after cancellation/shutdown limits.

They always reach the run boundary. An operation-specific timeout may be a
catchable declared failure when it occurs inside the overall deadline. Operation
metadata and tooling display that distinction. `stop with` remains explicit app
termination, not a typed domain failure.

## Capturing failures as result data

Normal code uses calls and handlers. Code that needs an outcome as data uses
`capture`:

```sos
capture fetch_weather with city called outcome

when succeeded of outcome:
  show summary of value of outcome
otherwise:
  show message of failure of outcome
```

Qualified actions work too:

```sos
capture weather.current with city called outcome
```

Grammar:

```text
capture := "capture" ActionName ["with" Arguments] "called" Name
```

The checker represents the value as an intrinsic closed outcome type:

```text
Result<SuccessType, FailureA | FailureB | ...>
```

Readable fields:

| Field | Success | Failure |
|---|---|---|
| `succeeded` | true | false |
| `value` | action result | null |
| `failure` | null | typed failure |

The runtime alone creates result values, so contradictory states are impossible.
`capture` catches declared catchable failures only; fatal termination still
propagates. It does not retry, roll back, or hide effects. Traces record when a
failure becomes data.

The checker narrows after `when succeeded of outcome` and its `otherwise`.
Reading `value` in a known failed branch or `failure` in a known successful
branch is an error. Without narrowing, those fields are optional.

## Packages, built-ins, and external modules

Packages export failures like named records. External TOML lists failure kinds
and payloads per action. Plugin protocol errors map to declared kind, message,
payload, and validated metadata. An undeclared plugin failure is a protocol
contract violation, not a dynamic domain failure.

Standard operations expose the same signature metadata. Existing generic
runtime/provider kinds migrate to built-in failure definitions without losing
`kind`, `message`, `retryable`, `status`, `code`, or recording identity. Bugs,
malformed plugin output, and invariant violations remain fatal runtime failures.

## Runners and builders

Command boundaries collect every reachable expected failure. Commands need not
handle all failures merely to compile. Their generated metadata contains the
complete possible-failure catalog.

When a typed failure reaches a boundary, runners display its kind, message,
payload, status/code/retryability, and action/module/source frames. Exit behavior
stays: success 0, unhandled execution failure 1, CLI usage failure 2.

Native, browser-WASM, and WASI artifacts embed action signatures, failure
definitions/docs, standard/external failure metadata, and source maps. Builds
fail when an exported typed action can produce an undeclared catchable failure.
Behavior and rendering must match the interpreter.

## Language server and editor experience

Hover shows result and failure signatures:

```text
fetch_weather(city as text) -> WeatherReport

May fail with:
  InvalidCity
    city as text
  WeatherUnavailable
    retry_after as optional duration
```

Required support:

- completion after `may fail with`, `fail`, and typed `on failure`;
- completion for payload and `using` fields;
- hover, definition, references, and rename for failure types/fields;
- call-site possible-failure information;
- optional configurable `may fail: ...` inlay hints;
- quick fixes to propagate a failure or add a handler;
- narrowing for captured outcomes;
- dedicated semantic tokens and symbols/icons;
- debugger inspection of active/captured failures and frames; and
- a call-hierarchy failure-propagation view.

Possible-failure hints are informational, not noisy Problems entries. Contract
violations remain errors. `sos check --json`, editor analysis, CLI/MCP operation
descriptions, and build metadata return stable `possibleFailures`. `sos explain`
can show the call path by which each failure reaches an action or command.

## Diagnostics

Representative messages:

```text
create_report may pass WeatherUnavailable on; handle it or declare it
InvalidCity requires field city as text
recover with requires WeatherReport; received text
report has no value after failure; recover, finish, fail, or pass failure on
on failure InvalidCity is unreachable after the general failure handler
pass failure on requires an active failure handler
plugin action weather.current returned undeclared failure RateLimited
cannot capture fatal run cancellation
```

Declaring but not currently producing a failure is informational, not an error,
because public contracts may intentionally remain stable.

## Compatibility and migration

- Existing `on failure`, implicit `error`, and `failure` bindings continue.
- `return VALUE` aliases `finish with VALUE`.
- `rethrow` aliases `pass failure on`.
- Formatters preserve the author's compatibility syntax.
- Completion, generated examples, and docs use idiomatic forms.
- Existing runtime/provider failures map to built-in typed definitions.
- Untyped actions continue dynamically. Strict declaration checking starts when
  an action adopts `returning`/`may fail with` or policy requires typed exports.
- Saved analyses and generated artifacts increment format identity when failure
  signatures become part of it.

## Verification matrix

Coverage includes definitions/exports/reserved fields, failure construction,
successful completion contracts, direct/transitive/transformed/recovered/passed
failures, typed/general handlers, incomplete paths, scalar/record/list/no-result
recovery, uncatchable fatal termination, captured-result narrowing, package and
plugin metadata, undeclared plugin failures, command rendering/exit status,
backend parity, check/explain/MCP/build metadata, editor/debugger behavior, and
compatibility for `return`, `rethrow`, and legacy handlers.

## Implementation sequence

1. Add failure definitions and action result/failure signatures to parser and
   module metadata.
2. Implement `finish`, `fail`, and call-graph propagation checking.
3. Add typed matching, binding, `recover`, and `pass failure on` while preserving
   legacy handlers.
4. Add intrinsic captured results and branch narrowing.
5. Migrate built-in runtime/provider failures.
6. Add external-module declaration and protocol validation.
7. Add runner/build metadata and consistent boundary rendering.
8. Complete LSP, Studio, VS Code, debugger, CLI/MCP, and generated help.
9. Run backend parity, compatibility, build, lint, and end-to-end tests.

## Accepted design direction

- `define failure Name:` is distinct from ordinary data.
- Actions declare result types and closed expected-failure sets.
- Compatible declared failures propagate automatically.
- `finish`, `fail`, `recover`, and `pass failure on` are idiomatic.
- `return` and `rethrow` remain valid non-idiomatic aliases.
- Recovery explicitly supplies an owed result.
- Fatal termination cannot be caught or captured.
- Captured results are explicit and opt-in.
- Language services, runners, and builders expose possible failures as stable
  structured metadata.
