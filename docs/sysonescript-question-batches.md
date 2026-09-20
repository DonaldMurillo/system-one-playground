# First-class questions and Jev batches

Questions are ordinary SOS records. Construct them with pure `std/jev` operations, read them from JSON, pass them into actions or collect them in lists. Constructing questions never calls the provider.

```text
import "std/jev" as jev
call jev.noul with "Is this urgent?" called urgent
call jev.choice with "Which team?", {"platform": "Service reliability", "other": "Another team"} called team
make questions with:
  urgent from urgent
  team from team
make message "Checkout is unavailable"
evaluate message by jev using questions called answers
show answers.urgent.p_yes
show answers.team.value
```

`evaluate STATE by jev using QUESTIONS called NAME` is fixed language syntax. It sends every named question against the same state in **one HTTP request**. Runtime judgment policy applies. A batch consumes one request admission, not one admission per question. Every attempt is counted, including failed responses; there are no automatic retries. Questions are limited to 1–128 per batch; this is not a monetary spending cap.

## Question data

A question record has `type`, `instructions`, and optional `criteria`. Unknown fields are errors. Supported types are `noul`, `choice`, and `score`. Instructions are required; empty text is rejected. Choice needs at least two labels. Score needs at least two ordered levels. Explicit Noul criteria must contain `true` and `false` descriptions.

```json
{
  "outage": {
    "type": "noul",
    "instructions": "Does this describe an active outage?",
    "criteria": {"true": "Service is currently unavailable", "false": "No current outage is established"}
  }
}
```

## Standard operations

| Operation | Arguments | Result |
| --- | --- | --- |
| `jev.noul` | instructions | Noul question record |
| `jev.choice` | instructions, criteria record | Choice question record |
| `jev.score` | instructions, levels list | Score question record |
| `jev.questions` | list of `{id, question}` entries | Named question record; duplicate/empty IDs fail |
| `jev.answer` | answers record, text ID | Named answer; missing IDs fail |

These operations are pure and support native/WASM targets. The live `evaluate` operation is rejected on WASM, consistently with other live Jev operations.

`jev.questions` is useful inside loops that assemble rules dynamically. `jev.answer` retrieves an answer when the rule ID comes from data rather than a literal property name.

## Results and validation

Each ID maps to the same normalized answer shape used by individual judgments: Noul `p_yes`; Choice `value`, `confidence`, `probabilities`; Score `expected`, `confidence`, `probabilities`. Missing, extra, wrong-kind or malformed answers fail the entire batch. There are no silently accepted partial results.

Trace records one request with the named answer map and reported input-token usage. Record/replay includes the entire batch in the source/state/question/model fingerprint. Changing questions invalidates replay; replayed nested answers are validated and consume no new provider requests.

## SOS semlint milestone

`examples/sos/semlint/main.sos` implements sequential evaluation and threshold filtering over prepared units. Its JSON fixture includes two rules against one code unit. This is the first rewrite milestone, not a replacement for the Go semlint executable: it does not yet discover source files, extract functions, select rules from regexes, read Git diffs, trim context, run workers or preserve semlint's CI exit-code contract.

The source-scanning implementation now lives in `examples/sos/semlint/scan.sos`; see [SOS semlint](sysonescript-semlint.md). It adds source/diff primitives, context handling, bounded concurrency and CLI reporting while keeping Go semlint as the reference. The prepared-unit example remains intentionally small.
