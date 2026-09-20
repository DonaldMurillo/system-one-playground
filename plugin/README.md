# typesafe-gate

A pre-flight gate for agent tool calls. Before a coding agent edits a file or
runs a command, the gate asks a handful of narrow questions about the proposed
call and returns allow, ask, or deny.

It exists to catch one thing that permission rules cannot: **the agent doing
something other than what you asked for.** Permission rules know that `rm` is
dangerous. They do not know that you asked a question and got an edit, or that
a request about code generation turned into a change to the auth middleware.

Works in Claude Code as a `PreToolUse` hook and in omp as a `tool_call` hook.
Both call the same Go binary, so the policy is identical in either harness.

## What it judges

Two groups of questions, sent to TypeSafe in a single request per tool call.

**Intent**, which is the point of the thing:

| Judgment | Catches |
| --- | --- |
| `request_wants_changes` | You asked to look, review, or explain, and got an edit |
| `advances_request` | The action is not moving toward what you asked for |
| `target_unrelated` | The file being changed is on a different subject from the request |
| `request_mentions_infrastructure` | CI or deploy config edited when you never raised it |
| `description_matches` | What the agent says it is doing is not what the command does |

**Safety**, as a backstop: `destructive`, `reversible`, `outside_workspace`,
`credentials`, `network_write`, and a five-level `blast_radius` score.

The model only answers these questions. Every decision is made in Go, in
`decide()`, so changing policy is a code review rather than a prompt rewrite.

## What it does not do

- It does not judge anything a read-only tool does. Reads, greps and globs
  never reach the API, so they cost nothing.
- It does not compute things code can compute. Whether a write destroys a file
  is arithmetic on two byte counts, so code does it.
- It does not fail closed. A missing key, a timeout, a bad payload or a panic
  all produce allow. A gate that breaks the agent loop is worse than no gate.

## Install

Build the binary first. It is the same one both harnesses call.

```
make build
```

**Claude Code.** Load it from disk:

```
claude --plugin-dir "$PWD/plugin"
```

**omp.** Point at the hook file:

```
omp --hook "$PWD/plugin"/omp/typesafe-gate.hook.ts
```

In omp an "ask" becomes a real confirmation dialog, because omp's hook API can
prompt. In Claude Code it becomes a permission prompt, which also forces a
prompt in auto mode.

Set `TYPESAFE_API_KEY` in the environment either way.

## Configure

Drop a `.typesafe-gate.json` in a project root, or `~/.config/typesafe-gate.json`
for every project. A partial file only overrides the fields it names.

```json
{
  "enabled": true,
  "timeout_ms": 3000,
  "thresholds": {
    "request_wants_changes": 0.40,
    "target_unrelated": 0.60,
    "ask_network_write": true
  }
}
```

`TYPESAFE_GATE=off` disables it for one session without unloading anything.

## Check a call by hand

```
plugin/bin/typesafe-gate -mode=check -tool=Bash \
  -input='go test ./...' -request='fix the parser test' -v
```

`-v` prints every judgment and which rule fired.

## Measure it

Two labelled corpora live next to the source. `corpus.json` was used while
tuning the thresholds, so its score is fitted and means little on its own.
`corpus-heldout.json` was written afterwards and run once.

```
make eval
make eval-heldout
```

Every judgment is also appended to `~/.typesafe-gate/audit.jsonl`, with the
signals and the rule that fired. That log is the honest measurement: run real
sessions, then read back what it flagged and whether it was right.
