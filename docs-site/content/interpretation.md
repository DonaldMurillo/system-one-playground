# Inspecting and pinning interpretations

SysOneScript keeps a canonical grammar and adds a constrained interpretation pass
before execution. Ordinary canonical programs remain local in every mode.
`sos check` and `sos fmt` stay deterministic; use `sos explain` to explicitly
request interpretation of sentence variants.

## Supported sentence forms

| Form | Canonical meaning |
| --- | --- |
| `retain tickets where status is "open"` | Keep matching records. |
| `remove tickets where status is "closed"` | Keep records that do not match. |
| `filter tickets where status is "open"` | Jev selects keep or remove from explicit candidates, or rejects the sentence. |
| `order tickets by priority highest first` | Sort descending; `lowest first` sorts ascending. |
| `collect tickets by team called teams` | Group by team. |
| `load "tickets.json" as json called tickets` | Read the specified file and representation. |
| `write tickets as json to "report.json"` | Save to the specified destination. |
| `group them by team called teams` | Resolve a visible collection reference. |
| `if age bigger 18 show "adult"` | `when age > 18:` followed by `show "adult"`. |
| `provided that score at least 10 display "ok"` | `when score >= 10:` followed by `show "ok"`. |

Unambiguous variants lower locally and report deterministic decisions. Competing
meanings or collection references use a constrained Jev Choice with an explicit
reject option and a minimum confidence of 0.8. Unfamiliar sentence shapes also
require Jev to accept or reject the host's bounded composition, even if typing
leaves one candidate. That score is not a guarantee of
correctness; inspect the selected canonical form. An ordinary variable actually
named `them` retains its ordinary binding. Reference tracking is conservative
around scopes; unsupported references fail rather than invent bindings.

The resolver never invents a file format, destination, or naming convention.
For dictionary-backed forms, it retrieves concepts from the compiled project
lexicon, maps those concepts only to registered language definitions, and
removes type-incompatible meanings before considering Jev. If one candidate
remains, lowering is local and consumes zero requests. Jev receives a bounded
Choice only when more than one valid meaning remains. Arbitrary prose and
open-ended generation of code remain outside this pipeline.

In semantic mode, declare a reusable criterion:

```text
criterion urgent:
  ask "Does this ticket describe an active service outage?"
  using message
  accept probability at least 0.85
  on uncertain discard

read "tickets.json" as json called tickets
keep urgent tickets
```

`ask`, `accept`, and `on uncertain` are required. `using` and a quoted `model`
are optional. The threshold must be greater than 0.5 and at most 1; the
uncertainty policy is `keep`, `discard`, or `stop`. Questions are literals with
no expression interpolation. Applying a criterion requires both interpretation
mode `semantic` and runtime judgment `semantic`; undeclared adjectives fail.

## Workflow

Select `assisted` interpretation in project TOML or file frontmatter:

```toml
version = 1
[interpretation]
mode = "assisted"
[budget.run]
requests = 8
timeout = "30s"
```

Inspect a program, then save the interpretation:

```sh
sos explain report.sos --save report.resolution.json
sos explain report.sos --locked report.resolution.json
sos run report.sos --resolution report.resolution.json
sos build report.sos --output report --resolution report.resolution.json
```

Turn a reviewed source file into deterministic canonical syntax with the same
validated lowering used by execution:

```sh
sos canonicalize report.sos          # preview on stdout
sos canonicalize report.sos --diff   # review a unified patch
sos canonicalize report.sos --line 8 # canonicalize one interpreted sentence
sos canonicalize report.sos --write  # atomic in-place rewrite
```

Studio exposes **Make all canonical** after successful analysis, and the VS
Code extension exposes **SysOneScript: Make File Canonical**. Both editor paths
apply one undoable whole-document edit.

VS Code reloads the exact analysis produced by Run into its code lenses, so it
shows confidence, attributed tokens, estimated cost, batching, and memoization
without issuing a second analysis request. The CLI prints the same per-decision
summary and can persist the run analysis with
`sos run --save-resolution analysis.json FILE`.

`explain` does not execute script effects. Its JSON includes canonical source,
source-line mapping, interpretation decisions, dictionary `matches`, diagnostics,
and request usage. Each match identifies the phrase, concept, and executable
language definition used by the lowering.
Studio and VS Code refresh semantic code lenses after Analyze or Run. A Jev-resolved line
shows its confidence, provider-reported input tokens, and the corresponding
published-rate estimate; deterministic resolutions explicitly show that they
used no Jev request. Per-line cost remains an estimate rather than account
billing, and unreported provider usage is labeled unavailable instead of zero.
Adjacent scope-neutral dictionary compositions are sent as up to 128 questions
in one provider request. Scope-changing or dependent statements form ordering
barriers. Batched lenses label the request tokens and estimate as shared across
the participating lines rather than presenting the batch total as per-line cost.
The editor process memoizes successful structural choices at confidence 0.95 or
higher in a bounded 1024-entry, 24-hour LRU. Keys include the model, semantic
versions, confidence policy, masked sentence, candidates, and visible type
context; literal contents are excluded. Rejects, failures, and lower-confidence
answers are never cached. Cache hits spend no request and are labeled
`memoized`; canonicalizing remains the durable, deterministic workflow.
Saving is explicit and only succeeds after analysis succeeds. Saving a resolution
writes that JSON file; it does not run the program. A resolution can contain
source text and judgment questions, so handle it like the source itself.

Locked analysis validates the saved interpretation against the source, semantic
policy, registry version, and model. It rejects stale or malformed records before
script effects and does not ask the provider to repair them. Narrowing externally configured request
or timeout limits does not invalidate a saved interpretation. Editing frontmatter
changes the source hash and requires a new resolution. Resolution files
are not signatures or proof of who approved a choice; keep them under the same
review and access controls as source code.

Compiled artifacts embed the original source, effective configuration, and
validated interpretation. Launching an artifact revalidates its interpretation
without a language-resolution request. Runtime judgments still require their
own budget and provider access, or a matching runtime answer recording.
`--resolution` pins interpretation; `--replay` replays runtime judgment answers.
These are separate mechanisms.

A saved interpretation pins its interpretation model. On `run`, `--model` selects
the runtime judgment model without changing the saved language choices.
On `explain --locked`, an explicitly supplied model must match the saved record.

## Go utilities

`sos.Analyze(ctx, source, AnalyzeOptions)` returns an `Analysis` and an error. An
unsuccessful analysis may still contain diagnostics and usage. Callers must check
the error before executing or saving its canonical source. The pass validates
the lowered program before execution.

`AnalyzeOptions.Config` supplies resolved configuration. `Budget` shares request
accounting with subsequent runtime work. `Bucket` attributes admissions to editor
or interpretation activity. `Saved` supplies a prior analysis and `Locked`
requires it. `sos.Run` applies analysis before script effects and returns it as
`Result.Analysis`; `Options.Resolution` and `Options.Locked` select saved use.

`sos.Evaluate(ctx, EvaluationRequest)` is the shared live provider gateway for
constrained Choice, Noul, and Score questions. Its budget is mandatory. It
reserves a request before dispatch, validates responses, and reports known token
usage. It makes no automatic retries; failed attempts retain their admission.

Request ceilings are counts, not currency estimates. Interpretation and runtime
share one total during a run; editor analysis is a separate explicit operation.
Persistent project allowances and monetary caps remain future work.

## Editor assistance

Studio provides an explicit Analyze action and an Interpretation panel showing
source, canonical forms, reasons, confidence, and usage. Applying a precise form
is an edit guarded by the document version and original source line. Editing the
document makes prior suggestions stale.

`POST /api/analyze` uses the existing Studio authentication. Its JSON request is
`{"source":"..."}`. The response contains `analysis`, `promoted`, and `reused`.
Studio keeps one successful analysis for the same source; Run can reuse a
compatible analysis. A canonical policy may be promoted to assisted for editor
suggestions only; such a result is not reused as permission to run assisted code.

The language server exposes the explicit custom request `sos/analyze` with
`{"text":"..."}` or `{"textDocument":{"uri":"..."}}` for an opened document.
Local diagnostics never trigger it. Editor assistance `off` denies these
requests before provider contact. On-demand analysis is capped at 32 requests
and 30 seconds, intersected with configured ceilings. The stdio implementation
handles requests sequentially; analysis may occupy that request loop until it
finishes or times out.

## Example CLIs

From the repository root:

```sh
sos explain examples/sos/team-report/main.sos --save /tmp/team-report.resolution.json
sos run examples/sos/team-report/main.sos --resolution /tmp/team-report.resolution.json -- examples/sos/team-report/team-tickets.json --output /tmp/team-reports
sos explain examples/sos/urgent-tickets/main.sos --save /tmp/urgent.resolution.json
sos run examples/sos/urgent-tickets/main.sos --resolution /tmp/urgent.resolution.json -- examples/sos/urgent-tickets/tickets.json
```

The report example groups open tickets by team and saves explicitly named files.
The urgent example declares a reusable criterion and judges each ticket through
the real Jev provider. It requires both semantic interpretation and semantic
runtime judgment permission. Its question, acceptance threshold, and uncertainty
policy are source code, not invented from the word `urgent`.

`sosbuild.BuildOptions.OnAnalysis` observes the completed analysis and any error,
including usage from failed interpretation. The CLI uses it to report build
requests and input tokens. Known raw-source target incompatibilities and invalid
runtime recording/replay options are rejected before paid interpretation; target
capabilities are checked again after lowering.

## Commands and selected analysis (0.2)

`Run` validates `Options.CommandPath` and inputs before interpretation. Fresh runs
analyze the selected leaf and statically reachable shared actions; unselected
commands and unused actions do not consume interpretation requests. Source lines
are preserved by blanking excluded constructions. Command declarations remain
local and canonical. `check`, `explain`, and `build` cover the whole application.

Saved analysis format is now version 5 and the registry is version 4; regenerate older saved
interpretations. Selected-source and whole-source hashes distinguish their
coverage. A selected result cannot stand in for a whole build or a different
command. Packaged applications validate the embedded whole result offline, then
execute the selected leaf. Runtime Jev judgments still require provider access.

Public Go helpers:

- `SelectCommand(program, argv)` returns `Path`, typed `Values`, `Help`, and `Usage`.
  Handle help/errors before calling `Run` with `CommandPath` and `Args`.
- `ValidateCommandInputs(program, path, args)` validates an editor/API invocation
  without effects. Paths contain child names, excluding the root name.
- `SelectedSource(program, path)` returns a line-preserving selected source for
  explicit analysis. It does not validate input values; call the validator first.

Studio's `/api/check` returns the command tree as `commands`; `/api/run` and
`/api/analyze` accept `commandPath` and `args`. Analysis cache identity includes
source, command, and inputs. LSP `sos/analyze` accepts optional `commandPath` and
`args`; omitting the path retains whole-document analysis. Source/input/command
edits invalidate displayed analysis. Neither client runs paid analysis on typing.
