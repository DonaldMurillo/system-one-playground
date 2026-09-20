# SysOneScript: useful multi-command CLIs

Status: the first file-based command slice is implemented in 0.2.0. Later stages
remain planned. This roadmap extends the
[command design](sysonescript-commands.md) and the implemented
[semantic pipeline](sysonescript-interpretation.md). See the [tickets walkthrough](sysonescript-tickets.md) for the shipped example.

Completed: command trees, shared typed input/help selection, selected execution
and interpretation, reachable shared actions, saved-resolution validation, Studio
command/input selection, and acceptance coverage for runner/native parity.
Local package and editor work is tracked in the [0.3 package guide](sysonescript-packages.md)
and [editor guide](sysonescript-editor.md). Streaming, full graph/coverage provenance,
and persistent costs remain future work. The release target below includes those later capabilities.

## Release target

Build and package one useful `tickets` application:

```sh
tickets import incoming.json --output tickets.jsonl
tickets triage --criterion urgent < tickets.jsonl > urgent.jsonl
tickets report --by team --output ./reports < urgent.jsonl
```

`import` validates and normalizes a JSON array to JSON Lines. `triage` selects
from explicitly declared criteria, judges records sequentially, and emits the
retained records. `report` produces deterministic team summaries and explicitly
named files. No implicit filename, overwrite permission, or judgment question is
invented. Initial `--criterion` and `--by` choices are finite declared sets;
unknown values fail before reading the stream or making provider calls.

Build this incrementally: first file-based commands in one source file, then
stdin/stdout streaming, then extraction of shared actions and criteria into
modules. Every stage must remain a runnable application.

## Decisions to carry into implementation

### Clean language model and command structure

- This is a young language: breaking syntax and internal API changes are allowed.
  Replace the old command model directly; do not maintain parallel grammars,
  a legacy execution path, or a per-file language-version opt-in.
- Update repository examples, tests, editor support, and public documentation
  together. Give obsolete command layouts an actionable diagnostic where
  practical; compatibility shims are not a release requirement.
- A CLI has one root command; a group contains child commands, a leaf contains
  executable statements. Top-level actions, criteria, imports, and schemas are
  declarations; no implicit top-level or parent setup execution. Plain scripts
  without commands remain a deliberate supported form of general scripting.
- Keep TOML schema versioning separate from internal compiler/interpretation
  format versions. Neither requires supporting multiple source grammars.
- Parent options/switches are inherited; no input shadowing or duplicate sibling
  names. Groups have no positional inputs. Required inputs, literal defaults,
  type validation, finite text choices, descriptions, and generated help are
  part of the first command milestone.
- Inherited options work before or after the selected child; child options follow
  its name. Reject duplicates. Preserve an explicit script-level `--` for
  positional values starting with a dash. Keep runner flags separate from script
  arguments using `sos run FILE -- ...`.
- Help bypasses missing required values and runs no body or provider request.
  Unknown commands/flags remain usage errors. Calling a group alone prints help.
- Exit codes remain 0 success, 1 execution failure, 2 usage error. Explicit custom
  exit codes, short-option bundles, and platform-specific interrupt codes are
  later extensions.

### Where Jev participates

Command names, input declarations, module paths, and help structure are canonical
and resolved locally. Jev interprets permitted sentences inside executable
bodies and evaluates declared runtime criteria. It cannot invent CLI options or
imports.

The run pipeline is:

1. Read source/declarations and resolve the local module graph.
2. Select the command and validate its inputs, or return help/usage diagnostics.
3. Analyze the selected body and its statically reachable actions/criteria under
   the effective policy and one shared request budget.
4. Validate the lowered program before executing its effects.
5. Execute with the remaining budget and report usage on stderr.

Declaration/graph errors anywhere block the application. Noncanonical bodies of
unselected commands cause no interpretation requests during a run. Whole-project
`check` remains local; full build analysis resolves every included command so
packaged help and execution never reinterpret source at launch. Explanations
must state whether they cover one selected command or the whole application.

Version saved interpretations to include compiler semantics, command selection or
whole-project coverage, source-file identities/hashes, import edges, semantic
policies, registry/prompt versions, and interpretation model. Reject incompatible
old records with a regeneration diagnostic; no legacy validator is required.
A selected-command record cannot silently
stand in for a full-build record. All source diagnostics gain file identity as
well as line/column; traces must identify the original module and command.

### Streams and shell composition

- Add explicit stdin input and JSON/JSONL output formats; stdout contains data,
  stderr contains diagnostics, progress, and usage. No implicit pretty-printing
  in a JSONL pipeline.
- Keep existing `read` materialized behavior. Introduce a separate stream source
  (proposed `stream stdin as lines of json called tickets`) and a single-pass
  stream value. Do not silently change existing list semantics.
- Filtering, per-record validation/judgment, and JSONL emission work incrementally
  with bounded buffering and natural backpressure. Sequential provider calls are
  the first implementation; no hidden prefetch or retries.
- Sorting and grouping require explicit materialization with item/byte limits.
  A stream cannot be consumed twice. Define and test early termination, reader
  ownership, cancellation, malformed/oversized records, and broken pipes.
- An emitted prefix may remain when a later record fails or budget is exhausted.
  Return failure and retain usage; do not claim streaming output is transactional.
- Studio uses explicit input text/file selection, not process stdin. Start with a
  bounded input panel and existing capped output; live streaming UI is later.
- Native CLI is the first streaming target. WASM targets must explicitly reject
  unsupported host input rather than acquiring accidental host access.

### Local modules

The earlier local-file proposal below is superseded by directory packages and logical
module identities in [the package guide](sysonescript-packages.md). Remote dependency
resolution and cross-package criteria remain later work.

- Relative local source files only; no package downloads or arbitrary Go imports.
  Import paths resolve from the importing file, while script data paths continue
  to resolve from the invocation's working directory.
- Imports have explicit namespaces and exports for actions, criteria, and schemas.
  Private names stay private; no wildcard imports or caller-variable capture.
  Freeze exact import/export grammar before implementing this milestone.
- Importing runs no effects. Reject cycles and duplicate exports; normalize file
  identities, including symlinks, before cycle/deduplication checks. Bound graph
  depth, file count, and total source bytes.
- Load each module once. No module-global mutable executable initialization.
  Pass values to actions explicitly and retain lexical definition-site lookup.
- Host/project/entry policy establishes authority. Module declarations cannot
  raise request/time ceilings or enable judgments forbidden by the entry policy.
  A module's stricter restriction may narrow execution; all calls share the same
  operation budget. Editor preferences do not propagate from imported source.
- Builds embed the entire validated source graph and resolution. Standalone
  execution must not need the original module files or rediscover host settings.

### Costs

Continue enforcing per-operation request/time ceilings, counting failures and
cancellations, and separating interpretation/editor/runtime usage. Display that
builds may resolve several commands and consume interpretation requests.

Persistent project budgets are a separate milestone: stable project identity,
atomic cross-process reservations, durable ledgers, UTC accounting periods,
crash reconciliation, and no automatic refund of unknown billed attempts. CLI,
Studio, and build should share that ledger when operating in the same project.
Portable artifacts must not pretend to share the build machine's allowance;
choose an explicit local project identity or run-only accounting on the host.

Monetary ceilings follow a provider-accounting investigation. Verify versioned
pricing and a defensible maximum charge before promising a hard dollar cap.
If a safe upper bound is unavailable, label estimates as estimates and continue
to enforce request limits. No new storage dependency is approved by this plan.

## Build sequence and acceptance gates

| Stage | Deliverable | Gate before moving on |
| --- | --- | --- |
| 0. Language contract/evaluation baseline | One command model, updated fixtures, semantic regression corpus, explicit list of intentional breaks | New expected behavior is specified; obsolete layouts and incompatible saved records fail clearly; ambiguous and unsupported cases have expected refusal outcomes. |
| 1. Command tree | Reworked parser/AST, updated examples, offline declaration validation, shared selection/help/input parser, file-based tickets commands | Runner and native binary agree on help, selection, inherited options, `--`, input errors, and exit codes; zero calls/writes on help and invalid inputs; only selected body executes. |
| 2. Semantic command integration | Selected-command analysis, full-build resolution, file/command provenance, saved-record coverage | Unselected commands consume no calls during run; changed source/coverage invalidates saved use; whole-app artifact runs all commands offline for interpretation. |
| 3. Pipes and streaming | stdin injection, JSONL reader/writer, single-pass operations, explicit bounded materialization | Real shell pipeline works; malformed records, slow sinks, cancellation, broken pipes, and budget exhaustion are tested; large input memory stays within the configured buffer/materialization limits. |
| 4. Local modules | Namespaces/exports, graph loader, module policy, graph-aware saved resolution and packaging | Imported code executes only when called; cycles/shadowing/private access fail; changing a dependency invalidates resolution; packaged application works after source tree removal. |
| 5. Tooling and release | Studio command picker/input panel, cross-file LSP navigation/diagnostics, examples and breaking-change notes | Changing file/command/input invalidates stale UI results; no paid keystroke analysis; native desktop and applicable WASM targets pass packaging checks. |
| 6. Persistent costs | Project request ledger, then independently validated monetary accounting | Concurrent processes cannot overspend; interrupted/unknown attempts remain reserved; currency limits ship only after upper-bound accounting is demonstrated. |

Tooling follows each stage's API as it stabilizes; stage 5 is the release gate,
not a requirement to defer all editor work until the end. Stage 6 can ship
separately from the useful CLI release.

## Evaluation and verification

Maintain a corpus of source, mode, context, allowed canonical outcomes, and
expected refusals. Cover synonymous forms, pronoun binding/shadowing, module
boundaries, quoted instruction-like text, missing destinations, policy denial,
low confidence, saved-record tampering, and original file/line attribution.
Report accepted-correct, accepted-wrong, refused, request count, reported/unknown
usage, and latency separately. Refusal is preferable to an incorrect accepted
operation. No new accepted-wrong case in the release corpus; this is a regression
gate, not a claim of universal correctness.

Default CI uses deterministic transport fixtures with the real client and no
external calls. Opt-in capped live evaluations record model/prompt/registry
versions and may accept several explicitly valid outcomes. Do not add a fake
product provider or require one exact probabilistic score.

Prefer CLI/native-artifact end-to-end tests for user-visible behavior; use focused
unit/race tests for graph, stream, parser, and budget invariants. Finish with Go
tests/vet/race checks, frontend lint/build, Playwright command/input flows,
`go tool wails build` in desktop, and packaged execution from an unrelated working
directory. Add streaming resource measurements rather than only small fixtures.

## OMP implementation arrangement

Use the user's requested `--max-time 45m` for each bounded task. Do not start
workers against unsettled shared interfaces or let multiple workers edit the
same core files.

For each stage, the parent first fixes contracts and file ownership, then runs:

1. Engine worker: that stage's parser/runtime/loader slice and focused invariants.
2. Tooling worker: CLI/Studio/LSP consumers of the agreed contract, in separate files.
3. Acceptance worker: independently specified end-to-end cases and, after the
   implementation settles, a review of policy, spending, and side-effect order.

The parent owns integration, full verification, documentation, release artifacts,
and follow-up defects. Later stages wait for the earlier acceptance gate; do not
attempt commands, streams, modules, and the cost ledger in one worker wave.

The first implementation task is stages 0–1: replace the old command structure,
update the repository to the new contract, and ship `tickets import/triage/report` with file inputs. That
is the smallest complete next release slice.
