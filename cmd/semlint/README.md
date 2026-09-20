# semlint

A semantic linter. Each rule pairs a deterministic site pattern with one
judgment a regex cannot make, and every rule that applies to a function is
asked in the same request.

Ordinary linters answer questions with a shape: is there a try/catch, is this
identifier unused, does this file end in a newline. The rules here answer
questions without one:

- Does this guard actually keep the feature working, or does a throw still
  leave the element half-initialised?
- Is this comment still true, or does it promise an encoding step the code
  below stopped doing?
- Would two open tabs visibly disagree after one of them changed this value?
- Is the thing being written to storage a theme preference or a bearer token?

## Rules are data

Bundled rules live in [`internal/semcore/rules`](../../internal/semcore/rules),
shared by Go semlint and the SOS `std/source` library. Edit those JSON files
and rebuild to change the builtins; use `-rules-file` for project overrides.
Two sets ship with the linter:

- **default**, general purpose, covering Go and the JavaScript family
- **browser-storage**, for localStorage, sessionStorage, cookies and IndexedDB

```
semlint ./src                              # the default set
semlint -sets=all ./src                    # every shipped set
semlint -rules-file=./my-rules.json ./src  # your rules too
semlint -list                              # what would run, and from where
```

A rule in your own file that reuses a shipped rule's id replaces it, so a
project can retune a threshold or reword a question without forking anything.

```json
{
  "rules": [{
    "id": "error-swallowed",
    "severity": "warning",
    "why": "what goes wrong, in one or two sentences",
    "where": { "kind": "catch" },
    "ask": {
      "instructions": "the one judgment a regex cannot make",
      "yes": "what a yes means",
      "no": "what a no means",
      "threshold": 0.75
    },
    "message": "the finding text"
  }]
}
```

`where` takes a `pattern` for a literal regex, or a `kind` that resolves per
language: `function`, `catch`, `comment`, `return-error`. One definition using
`kind` compiles into a variant per language.

For a deterministic rule, give `check` instead of `ask`, with `line_has`,
`line_lacks`, `unit_lacks`, or `always`.

## Scope: files, or a diff

Files are scanned independently and the request unit is a **function**, not a
file, so cost scales with functions. Nothing is analysed across files:
`other_matching_lines_in_file` stops at the file boundary.

Whole-tree runs are usually the wrong default. Measured on gofastr:

| Scope | Units |
| --- | --- |
| `framework` package | 13,124 |
| what the last commit touched | 4 |

So pass a diff:

```
semlint -since=HEAD~1            # units the last commit touched
semlint -since=HEAD              # units changed in the working tree
semlint -diff=pr.patch           # a unified diff from a file
git diff | semlint -diff=-       # or from standard input
```

With a diff and no paths, the changed files are the paths. Only added and
changed lines count; a deletion leaves no code to judge. Untracked files count
in full, so a new file is not reviewed by nobody.

`-max-units` defaults to 400 and refuses anything larger, so a whole-tree run
is a deliberate choice rather than an accident.

## The two halves

Every rule has a `Selector` and either a `Check` or a `Question`.

`Selector` is a regex. It finds candidate sites for free, so scanning a whole
repository costs nothing and the model is only asked about what it found.

`Check` makes a rule fully deterministic. `Question` hands it to the model.
**Which half a rule belongs in is the discipline the framework enforces.** The
cookie-attribute rule was a model question until it reported SameSite missing
from a line reading `SameSite=Lax`, at p=0.81. Whether two tokens appear in a
string is decidable, so it is now a `Check` and is right every time. If you can
write the check, write the check.

## Context is part of the deterministic half

A function judged alone produces false positives that have nothing to do with
the model. Three separate ones disappeared once the extractor started supplying:

| Field | Why a rule needs it |
| --- | --- |
| `module_scope_declarations` | The key-building helper is never inside the function using it |
| `enclosing_scope` | A four-line helper hides the guard or listener its parent installs |
| `other_matching_lines_in_file` | A comment claiming "every call site does X" cannot be checked from one call site |

The last one also carries a rule: when the evidence needed to check a claim is
not shown, the rule must answer no. A rule that reports what it could not
verify is worse than one that stays quiet.

## Measuring it

Two fixtures. `defects.js` contains one deliberate defect per rule, written
with ordinary comments. `clean.js` implements the same features correctly.

Expectations live in `defects.expected.json`, never in the source. An earlier
version labelled each function with its defect id in a comment, which both
inflated recall and made the comment rule fire on seven functions instead of
one. Nothing in a linter's input should name the defect it is meant to find.

```
make semlint-fixtures   # recall against defects.js, precision against clean.js
make semlint-sites      # the deterministic pass alone, no API calls
```

Current state, browser-storage on the JavaScript fixtures: 12 of 12 planted
defects found, stable across repeated runs, with one finding on `clean.js`
(`storage-unbounded-growth`) on a function that genuinely has no eviction.

The general set has its own Go fixture, `general.go` with `general.expected.json`,
and currently finds 9 of 12 planted defects plus several real ones that were
never planted. Each rule carries a `_status` field recording what has actually
been measured about it. `silent-truncation` is disabled: phrased broadly it
produced five false positives on correct code, phrased narrowly it caught none
of the planted cases, and it needs a question separating an accidental cut from
a deliberate cap. `check-then-use-gap` is quiet but missed its one planted case.

Do not read a rule's presence as evidence that it works. Read its `_status`.

## Two bugs worth knowing about

Both were found by the Go fixture and both were in the deterministic half, not
the model:

- The unit extractor's block pattern was JavaScript-only, so no Go function
  ever matched and every unit fell back to a line window straddling function
  boundaries. Findings pointed at unrelated code and recall was 3 of 12.
- Site patterns were matched against comment-stripped lines, which made every
  rule whose site lives in a comment silently unmatchable.

Python is deliberately unsupported. Block boundaries are found by counting
braces, which says nothing about an indentation-delimited language. Adding it
means an indentation-aware extractor, not another entry in the profile table.

## Separation decides whether a rule can be trusted

Not the threshold. A rule whose defective and correct populations sit far apart
survives run-to-run drift, a change of batch size, and a change of which other
rules are active. A rule whose populations nearly touch cannot be rescued by any
threshold, because those effects are the same size as its signal.

```
make semlint-separation
```

It measures both populations against the labelled fixtures, with the whole rule
set active, and grades each rule:

| Verdict | Robust gap | Meaning |
| --- | --- | --- |
| ROBUST | 0.30 or more | clean separation with room for drift |
| MOSTLY | strict gap negative, robust gap wide | separates the bulk; occasional edge errors |
| WORKABLE | 0.15 to 0.30 | usable, recheck when the rule set changes |
| FRAGILE | under 0.15 | under the batching effect; report as a lead only |
| UNUSABLE | zero or less | tuning cannot fix this |

Two gaps are reported. The strict one is the lowest true reading minus the
highest false one, which is the honest worst case but is dominated by a single
outlier. The robust one ignores the worst tenth of each population. Thresholds
are placed in the middle of the robust gap, and every rule's `_status` records
what it measured.

**An incomplete truth file makes a sound rule look broken.** This happened three
times while building this. A rule answering correctly about a real defect the
fixture never labelled is scored as a false positive, and five rules were
briefly graded UNUSABLE for that reason alone. The sidecar must list every
genuine defect in the fixture, including ones discovered after it was written.
Matching is by unit, not a line window: functions sit closer together than any
sensible window, so a window counts a correct neighbour as the defect.

Two rules are FRAGILE by measurement and ship at hint severity on purpose:
`comment-contradicts-code` and `stale-marker`. Read them as leads.

## Across files

A unit is one function, and a rule can consult the rest of its file. A key
written in one module and read in another is invisible to both halves: each side
is correct on its own and only the pair is wrong.

`related_lines_in_other_files` closes that. The deterministic pass indexes every
site in the run by the distinctive identifiers on its line, so two lines sharing
a prefix constant or helper name find each other for free. Only the matching
lines travel, never whole files, capped at 25 and dropped first when a request
must be trimmed.

The `crossfile` fixture is a pair where each file is correct alone. Linted
together, both sides are flagged at about 0.95. Linted separately, the rule
reports nothing, because it is told to answer no when the other side is not
shown to it.

## Calibrate in the battery, never alone

**A question's answer shifts when other questions share the request.** Measured
on the same unit and the same question, changing only how many questions went
with it:

| Rule | Asked alone | In the battery of 12 | Shift |
| --- | --- | --- | --- |
| comment-contradicts-code | 0.81-0.88 | 0.68-0.74 | -0.12 |
| storage-key-not-namespaced | 0.97 | 0.96 | -0.01 |
| storage-holds-sensitive-data | 0.98 | 0.97 | -0.01 |

Confident judgments barely move. The mid-confidence one moved enough to cross
its threshold, and a threshold calibrated in isolation missed the defect on
every batched run. The documentation describes answers as independent; for
well-separated judgments that holds, but do not rely on it near a threshold.

So calibration runs with the whole active set:

```
semlint -calibrate -sets=all <defective file>
semlint -calibrate -sets=all <correct file>
```

`-calibrate` drops every threshold to 0.01 and reports raw probabilities. Read
both, then put the threshold in the gap. Take several runs: one reading of 0.86
on a case that actually sits at 0.83-0.85 produced a threshold of 0.80 that sat
on the band edge and lost the finding.

`storage-value-untrusted` scored 0.71-0.74 on the real defect and 0.11-0.28 on
every correct read. Its threshold was 0.70, sitting on the lower edge of the
true band, and run-to-run drift of 0.03 was enough to lose the finding. At 0.50
it is in the gap and stable.

`comment-contradicts-code` is the least separated rule in the battery. Measured
in the full battery: 0.69-0.72 on a false comment, 0.54 at the top of the
non-target range, threshold 0.61 in between. It still rests on a single true
example and occasionally fires on a dense multi-claim comment. Treat its
findings as leads.

## Large inputs

Three things can go wrong with size, and all three used to be silent or fatal.

**A unit that is too big no longer ends the run.** One generated file with a
35KB line used to abort everything with `max_tokens_exceeded`. Now the request
is retried with all optional context dropped and the code cut to a size certain
to fit; if that still fails the unit is skipped, reported by file and line, and
every other unit is unaffected.

**Context is trimmed in a fixed order**, dropping what rules need least first:
peer lines, then enclosing scope, then module scope, then the file header. The
token estimate that drives this is approximate on purpose. Measured on this
API, English prose runs about 5.7 characters per token and densely punctuated
code about 2.5, so no divisor is right for both. The estimate trims early and
the server's refusal catches the rest.

**Whatever was cut is named in the state.** `context_completeness` is either
`complete` or a description of exactly what is missing, and the rule compiler
appends a clause to every question telling the model to answer no when the
evidence it needs fell in the cut part. A function larger than the 220-line
unit cap becomes a line window, which is reported the same way. The run summary
counts these as units judged on partial context, so you can see which findings
rest on less evidence.

## Output

`-format=hints` is shaped for an agent's context: the line, what is wrong, and
what would go wrong, with the rule name last. `-format=json` for tooling. The
default is a `file:line [rule] message` line, matching what repolint emits.

`-severity=error` and `-min-confidence` raise the bar. For feeding a model,
raise it: a linter that cries wolf poisons the context it is trying to improve.

## Cost

Adding a rule costs a few hundred tokens, not another round trip. Over the
gofastr runtime, 12 rules across 54 sites in 11 files resolve in 15 requests
carrying 53 questions, about 1.3 seconds wall and 38k tokens. One request per
rule would have been 53 requests for the same answers.
