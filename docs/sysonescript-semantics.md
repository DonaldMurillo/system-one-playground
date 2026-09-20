# Jev as part of language interpretation

Status: accepted architectural direction; not implemented in 0.1.

See the [implementation plan](sysonescript-semantic-plan.md) for independent
editor/source/runtime controls, shared budgets, and delivery checkpoints.

## Intent

SysOneScript is a general scripting language whose understanding of source can
involve Jev. It is not limited to a fixed grammar with explicit AI calls added
on top. Jev may participate in compilation, interpretation, and runtime
decisions. The existing Go parser and interpreter provide the initial
execution foundation, not the limit of the language surface.

Canonical constructions give each operation a precise meaning. Sentence
variations and contextual references may resolve to those constructions with
Jev's help. Explicit `jev` wrappers remain available for granular control;
they are not required around every semantic feature.

This supersedes the initial design's rule that Jev must never participate in
grammar interpretation or reference resolution. The commands/inputs/actions
contract remains useful as the representation of resolved behavior.

## Three roles

1. **Sentence interpretation:** select a supported operation from possible
   meanings of a sentence.
2. **Context resolution:** bind references or modifiers to compatible values
   and operations in scope.
3. **Data judgment:** evaluate meaning that depends on values during execution.

Arithmetic, file I/O, and control-flow execution still have ordinary Go
implementations. Jev's deeper involvement is in determining the program that
those implementations execute, not replacing every calculation with a model.

## Proposed pipeline

```text
source + source locations
  -> structural parsing and candidate construction
  -> deterministic resolution where unambiguous
  -> Jev selection where semantic interpretation is needed
  -> validated, source-mapped resolved program
  -> execution with explicit runtime judgment nodes
```

The front end supplies candidates with stable identifiers, descriptions,
operand bindings, requirements, and effects. Jev's constrained Choice answers
select among candidates; it need not generate code or free-form ASTs. Include
an unsupported/ambiguous alternative and validate every selected construction.
A model preference cannot make an invalid binding or operation executable.

Candidates come from a construction registry and structural context. The
first experiment uses a small registry with finite sentence patterns and
compatible in-scope operands. Arbitrary sentences that cannot produce useful
candidates fail with a diagnostic; this is not unrestricted English execution.

## Example and boundaries

Proposed surface, not runnable today:

```text
read "tickets.json" as tickets
keep the urgent ones
group them by team
save each group in its own file
```

- The read construction needs a representation rule, such as an explicit
  `as json` or a documented extension-based default. Jev must not invent one.
- `ones` and `them` refer to compatible collections in context. Resolve clear
  references without a call; offer compatible alternatives to Jev when needed.
- `urgent` either names a declared criterion or denotes a runtime judgment.
  Its question and uncertainty policy must be represented in the resolved
  program. A build cannot decide which future records are urgent.
- `each group` means one write per group. Destination, format, naming, and
  collision behavior need declarations or documented defaults. If absent,
  report what is missing rather than inventing a path or overwrite permission.

Quoted strings and file contents are data. They must not introduce executable
constructions or alter interpretation policy. Dynamic judgment state is kept
separate from source, candidate descriptions, and compiler instructions.

## Compile-time and runtime separation

Interpretations depending only on source, declarations, and language policy
can be resolved during a build. Save the resolved program with source hashes,
language/registry version, model identity, candidate selection, and policy.
The artifact executes that saved interpretation without reinterpreting source
on each launch. It may still contain live runtime judgments.

Interpretation depending on runtime context needs a distinct runtime node
with bounded inputs and a declared policy. Do not silently retry a failed
canonical parse as a different program during execution.

Use separate budgets and traces for interpretation and runtime judgments.
Existing answer record/replay does not yet implement this compilation record.
Cache keys must cover source, relevant scope, registry, policy, model, and
prompt version. Cached interpretation is not proof of correctness.

## Ambiguity, controls, and tooling

- Resolve only supported, valid constructions. Uncertain or missing meaning
  produces a source-located diagnostic with alternatives or a precise form.
- Model probability/confidence is evidence for a policy, not a guarantee of
  correct interpretation. Select thresholds using measured examples.
- Precise canonical wording provides a way to remove an unwanted inference.
- Retain explicit wrappers for questions, models, thresholds, and failure
  policies. Syntax for controlling source interpretation is still to be designed.
- Studio should show what a sentence means: operation, bindings, defaults,
  effects, and why Jev was consulted. Users should be able to inspect the
  resolved program before execution.
- Basic editing diagnostics stay local. Semantic analysis runs explicitly or
  through cached results; do not make paid provider calls on every keystroke.
- Canonical-only programs remain usable offline. Semantic builds require the
  provider or a matching saved resolution; failures must not change meaning.

## First experiment

Implement a small resolver before expanding the full CLI feature set:

1. Register collection filtering, sorting, grouping, and explicit save forms
   with stable operation IDs, operand requirements, and canonical lowering.
2. Support a bounded family of sentence variations and collection references.
   Include examples with multiple plausible referents and unsupported wording.
3. Use the real existing Jev client to choose among valid candidates, including
   abstention. Keep the source spans and resolution trace.
4. Emit the resolved program for inspection. Validate all constructions before
   executing any effects; run accepted plans through the Go interpreter.
5. Save resolutions and verify replay without provider access. Changed source
   or policy must invalidate the saved resolution.
6. Evaluate held-out paraphrases, ambiguous sentences, quoted instruction-like
   text, and missing parameters. Measure selection accuracy, wrong confident
   selections, abstentions, calls per program, and cold/warm p50/p95 latency.
7. Compare execution with manually written canonical equivalents through CLI
   tests. Verify ambiguity and missing inputs cause no writes or runtime calls.

Do not assume individual-call speed makes whole-program interpretation cheap
or reliable. Use the experiment to choose resolution granularity, caching,
thresholds, and the next supported constructions.

## Questions to settle from the experiment

- How are reusable semantic criteria such as `urgent` declared?
- Which references can resolve implicitly, and how is context reset?
- Which defaults deserve language-level meaning versus project configuration?
- How does a user pin, inspect, or explicitly refresh a saved interpretation?
- Which interpretation controls belong in source, CLI flags, or a project file?

These are design questions, not reasons to postpone a bounded resolver.
