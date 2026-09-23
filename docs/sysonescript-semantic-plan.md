# Semantic toolchain implementation plan

Status: proposed implementation plan following the accepted architectural direction.
No functionality in this document is implied to be shipped.

Implementation progress: the [configuration reference](sysonescript-configuration.md)
and [interpretation guide](sysonescript-interpretation.md) document the implemented
subset: TOML layers, shared request accounting, constrained sentence resolution,
reusable criteria, saved interpretations, captured artifact policy, and explicit
editor analysis. Persistent project/monetary accounting, automatic background
assistance, and the broader grammar below remain planned.

## Outcome

Jev helps the underlying toolchain understand, explain, and execute SysOneScript.
Source does not need a `jev` wrapper for every internal use. One discrimination
engine supports the compiler, interpreter, and LSP; independently configured
policies govern each consumer. Every provider request passes through one
budget authority, including retries and editor assistance.

The first release demonstrates a bounded semantic language, inspectable
interpretations, predictable costs, and matching interpreted/packaged behavior.
It does not attempt arbitrary English, a package registry, or new host permissions.

## Current foundation

- `sos/`: canonical parser/checker, interpreter, explicit provider judgments,
  per-run call limit, answer tracing, and runtime answer record/replay.
- `typesafe/`: real Jev client and response usage including input tokens.
  Verify current provider accounting and pricing before monetary enforcement;
  a code comment is not a billing contract.
- `internal/soslsp/`: local diagnostics and editor features.
- `internal/studio/` and `studio/src/`: Go server, Monaco workbench, trace UI.
- `internal/sosbuild/`: native/WASM packaging with embedded interpreter sources.

Today `MaxCalls <= 0` selects a default. The new configuration must distinguish
omitted limits from zero: zero denies calls; omitted means inherit. Adapt the
legacy API explicitly instead of silently changing its semantics.

## Configuration model

Use TOML throughout: global `config.toml` in the OS user configuration directory's
`sysonescript` folder, nearest project `sos.toml`, and optional `+++` TOML frontmatter
at the beginning of a script. Version the schema. Frontmatter uses the same
schema but cannot set editor preferences. Credentials remain in the existing
environment configuration. The TOML parser dependency needs explicit approval.

Expose independent controls rather than a single intelligence slider:

| Dimension | Values | Meaning |
| --- | --- | --- |
| Editor assistance | off, on-demand, automatic | Whether and when editor analysis may call Jev |
| Source interpretation | canonical, assisted, semantic | Exact forms; supported paraphrases/references; additionally implicit runtime judgment constructions |
| Runtime judgment | deny, explicit, semantic | No live judgments; explicit calls only; also judgments introduced by semantic resolution |
| Resolution reuse | locked, cached, refresh | Require saved interpretation; reuse or resolve; deliberately re-resolve |

If source resolves to a runtime judgment disallowed by the runtime policy,
report a diagnostic. Never downgrade it to an ordinary condition. Rich editor
assistance may be paired with canonical-only compilation and runtime denial.

Ship named presets as shortcuts, with expanded settings visible. Preserve
0.1 behavior by default: canonical source, explicit runtime calls, on-demand
editor assistance. A semantic preset enables the richer source mode and its
runtime nodes. Automatic paid editor assistance requires an explicit setting.

Resolve preferences from defaults, project configuration, then invocation
overrides, with file frontmatter between project and invocation settings.
Editor preferences belong to the user/editor. Unknown fields are errors;
frontmatter is parsed deterministically and preserves body source locations. Budgets and capability
ceilings are intersections: source wrappers can narrow them, never raise them.
Changing source meaning invalidates a saved interpretation; changing a spending
ceiling only changes whether required work is permitted.

## Shared discrimination engine

Introduce a Go package with interfaces independent of Monaco and transport:

- **Construction registry:** stable IDs, finite patterns, argument slots,
  compatible types/shapes, effects, canonical lowering, and diagnostics.
- **Analysis context:** source span, visible symbols, known shapes, discourse
  references, declared criteria, and registry/language versions.
- **Discrimination request:** category (construction, reference, semantic
  predicate), valid candidate IDs, context, and effective policy.
- **Resolution:** selected candidate and bindings, unresolved requirements,
  provenance, source/context hashes, and any runtime judgment nodes.
- **Analysis result:** diagnostics, alternatives, resolution records, usage,
  and state such as resolved, ambiguous, unsupported, or budget-exhausted.

Resolve exact/unambiguous cases locally. Use Jev Choice for constrained choices
where needed, including abstention. Completeness and validity are deterministic
checks; Jev may help explain missing meaning but cannot waive requirements.
Keep candidate generation separate from selection so evaluations can diagnose
whether the right meaning was absent or the wrong candidate was selected.

Generate user explanations from selected constructions, bindings, missing slots,
and trace metadata. Do not assume the provider generates prose explanations or
free-form code. Quoted text and runtime data cannot modify compiler instructions.

Lower into a versioned resolved representation before execution. Initially an
adapter may target the existing canonical program; validate the adapter and
retain original source spans. Do not turn cached text fragments into unchecked
executable source.

## Readable failure sentences (proposed)

Exact aliases alone will not make SysOneScript approachable to someone who does
not know its grammar. In particular, a beginner should not have to name a
failure type merely to handle any failure from the preceding operation. The
implemented catch-all is `on failure:`; a typed `on failure InvalidHttpBody:`
is an optional narrowing, not the default requirement. Neither `on error:` nor
`with failure ...` is currently accepted syntax.

The semantic writing layer should consider phrasing such as the following
**proposed, non-executable example**:

```text
read the JSON body of request as Incoming and call it incoming
if that fails, tell the client the body was invalid
```

Resolution must use the immediately preceding operation, its possible failure
kinds, visible bindings, and the surrounding HTTP request context to retrieve
bounded, registered constructions. `if that fails` can refer to a catch-all
handler without inventing a failure type. `on error:` may be added as a
deterministic synonym for `on failure:`, but that alias alone is not the
semantic-matching feature. Other unfamiliar sentence shapes may need a bounded
Jev choice or acceptance check under the configured interpretation policy.

The resolver must distinguish **displaying a message in run output** from
**sending an HTTP response**. It must also make the handler outcome explicit:
recover with a valid replacement result, pass the failure onward, or terminate
the action. It must never lower a bare `show` and then continue into code that
uses a result the failed operation did not produce. If intent, response status,
or control flow is unclear, report alternatives and ask for a choice; do not
silently guess. In an HTTP request handler, an unhandled invalid-body failure
already receives the server's bounded 4xx fallback and the listener continues.

Editor analysis should show the chosen canonical form, bindings, confidence,
provenance, and request usage before a user applies the precise form. Exact
registered wording should resolve locally at zero provider cost; unfamiliar
wording or ambiguity follows the bounded semantic pipeline above. The final
lowered program must pass the canonical checker, and a saved or packaged
interpretation must remain pinned to that reviewed meaning.

## Budget authority and usage ledger

Track editor, interpretation, and runtime spending separately and against an
overall project ceiling. A request must fit both its bucket and every enclosing
limit. Expose request count, duration/deadline, and optional money limits.

Use explicit scopes: per analysis/build/run and a persistent project allowance
for a configured period (initially UTC day). Show reset times. Document that
these are local project limits, not a provider-wide account cap. Standalone
executables enforce their embedded per-run ceilings; they cannot claim to share
a cross-machine project allowance without a connected budget service.

Admission lifecycle:

1. Check deadline, policy, and applicable remaining budgets.
2. Atomically reserve one request and, where possible, a conservative charge
   bound before dispatch. Concurrent callers cannot reserve the same balance.
3. Dispatch via the existing provider client with cancellation and request ID.
4. Reconcile reported usage, retain audit metadata, and release only amounts
   known not to be billable. Dispatch failures with unknown billing keep their
   reservation as unresolved usage; cancellation is not assumed to be free.

Use integer monetary units or exact decimal parsing, not floating point. Store
pricing source/version and distinguish reserved, reported, estimated, and unknown
amounts. If a reliable upper charge bound is unavailable, refuse strict monetary
mode or offer an explicitly estimated mode chosen by the user. Never present a
token estimate as a guaranteed monetary cap. Pricing verification is a delivery
checkpoint, not a hardcoded assumption in this plan.

Implement durable local journaling and cross-process serialization for the
project allowance; crash recovery retains unconfirmed reservations. Decide the
portable locking mechanism during implementation. A separate dependency, if
needed, requires approval. Do not ship an in-memory counter labeled a persistent
or shared cap. Deduplicate identical in-flight analysis requests and charge one
reservation; subscribers cancelling must not cancel work still needed by others.

No automatic retries initially. Any later retry requires fresh admission.
Cache hits have zero new provider usage and display original versus current
cost separately. Shared analysis is attributed to the initiating consumer;
reuse is shown without charging it again.

On exhaustion: pause editor assistance, fail unresolved compilation, and raise
a distinct runtime budget failure. Existing failure handling may handle that
failure explicitly; it cannot convert it to a fabricated model answer or obtain
more requests. Ordinary local diagnostics remain available.

## Resolutions, builds, and editor behavior

Persist versioned resolution records separately from runtime answer recordings.
Keys cover source, relevant scope, construction registry, model, prompt version,
and semantic policy. Build artifacts embed the validated resolved program and
provenance. They do not reinterpret source on launch. Live data judgments remain
runtime nodes with their own budgets and policies.

LSP exposes local diagnostics immediately. On-demand semantic analysis comes
first. Add automatic mode only after cache, debouncing, document-version checks,
and budget tests pass. Stale responses never publish diagnostics, edits, or
executable resolutions for a newer document.

Offer hover interpretation, missing-slot diagnostics, and code actions that
expand a sentence to a precise form. Code actions apply only against the
document version analyzed. A suggestion has no execution authority. Builds may
reuse a compatible validated editor resolution, but never merely trust UI text.
Locked mode fails on a mismatch instead of silently selecting a new meaning.

Studio displays the effective modes, interpretation trace, remaining budgets,
and attributed usage. CLI gains semantic check/explain and usage reporting;
exact flags are to be settled alongside existing runner argument handling.

## Delivery sequence and verification gates

### 1. Policy and provider gateway

Add configuration validation, admission control, usage records, and one gateway
used by existing runtime judgments before adding new model-assisted features.
Deliver per-operation limits first, then persistent project accounting; label
available scopes honestly throughout development.

Gate: zero-call policy sends no request; concurrent callers cannot exceed the
cap; unknown-billing failures retain reservations; daily boundaries, crash
recovery, and restart do not erase spent/reserved usage. Existing runtime and
record/replay CLI tests continue to pass.

### 2. Inspectable sentence resolver

Register filtering, sorting, grouping, and explicit save forms. Support a bounded
set of paraphrases and collection references. Add a source-located explain result
and feed validated resolutions into the existing interpreter. Missing save
destinations/naming remain diagnostics.

Gate: canonical scripts make zero interpretation calls; supported paraphrases
execute like canonical equivalents; ambiguous or incomplete programs perform
no writes and no runtime judgments. All interpretation calls are accounted for.

### 3. Saved interpretations and build parity

Add resolution storage, reuse/refresh/locked policies, and artifact embedding.
Keep runtime answer recording a distinct feature. Regenerate bundled compiler
sources after core changes.

Gate: locked builds run offline when they contain no runtime judgments; source,
scope, or policy mismatches fail clearly; interpreted and native execution agree.
Verify deterministic resolved programs on existing WASM targets. Live Jev WASM
support remains a separate host-integration task.

### 4. LSP and Studio assistance

Add on-demand analysis, hover interpretation, actionable diagnostics, canonical
expansion, and usage display. Then add optional debounced automatic analysis.

Gate: opening a document with on-demand mode makes no provider call; stale
results cannot overwrite fresh analysis; accepting an edit checks its version;
budget exhaustion preserves local editing and diagnostics. Exercise the public
LSP protocol and Monaco UI rather than only internal helper tests.

### 5. Implicit runtime semantics

Define reusable criteria before supporting phrases such as `urgent tickets`.
Initially require a declared criterion with question, state selection, and
uncertainty policy; unsupported undeclared adjectives produce suggestions, not
silently invented questions. Design the criterion syntax as part of this phase.

Gate: the resolved program exposes the exact runtime question and policy;
explicit-only runtime mode rejects implicit judgment nodes; semantic mode
executes them through the same gateway, tracing, and replay mechanisms.

### 6. One substantial CLI and migration

Apply the planned commands/inputs/actions contract to a ticket-management CLI
with list, triage, and export. Migrate `.sos` files and executable naming to
`.sos`/`sos`, including examples, help, Monaco, LSP, packaging, and documentation.
Provide explicit compatibility/deprecation behavior rather than accidental aliases.

Gate: one documented workflow runs from Studio, the script runner, and a native
artifact with inspectable meaning and consistent budget/failure behavior.

## Evaluation and release criteria

Create a versioned corpus with expected operations, bindings, runtime nodes,
or expected abstention. Keep held-out cases separate from prompt tuning. Include
paraphrases, competing references, negation, missing operands, quoted misleading
instructions, unsupported sentences, and cost exhaustion.

Report candidate coverage, correct resolution, wrong accepted resolution,
abstention, cold/warm p50/p95 latency, requests, and usage per program. Do not
collapse these into a single confidence score. Set numerical acceptance targets
after a baseline run; require zero wrong accepted effectful interpretations in
the release regression corpus (a test gate, not a universal guarantee).

Use the real provider for opted-in live evaluations and saved real responses
for offline reproduction. Accounting/concurrency tests may exercise transport
failure fixtures; they do not substitute a fake semantic provider for the product.
No model use is required for documentation, canonical tests, or ordinary lint.

## Work split once implementation starts

- Core owner: construction registry, analysis context, resolutions, and lowering.
- Budget owner: configuration, gateway, journal, and accounting tests.
- Tooling owner: LSP/Studio integration against agreed analysis contracts.
- Integration owner: real-provider corpus, artifact parity, CLI migration, and
  public documentation. Use OMP for bounded parallel tasks after the contracts
  are fixed; do not let separate agents invent incompatible policy schemas.

Start with phases 1 and 2 as the first vertical slice. Broader language features
and cosmetic editor changes should not delay proving useful interpretation at
an observable, bounded cost.
