# Repository issue assistant

A multi-file CLI for developer issue intake, normalization, optional Jev triage,
and per-area reports. The input is a local JSON export; it does not contact GitHub,
run git, inspect a live repository, or change source code. Those integrations can
feed this CLI through the same JSON contract later.

## Project layout

- `main.sos`: command interface, validation, filesystem writes, and Jev policy.
- `operations/normalize.sos`: exported normalization action using `std/text`.
- `operations/report.sos`: exported grouping/counting action in the same package.
- `fixtures/issues.json`: five developer issues; four open, one closed.
- `sos.toml`: module identity and a per-run request/timeout budget.

Each issue requires integer `id` and `priority`, plus text `area`, `status`, and
`message`. Import trims whitespace, lowercases area/status, and preserves IDs and
priority. Triage selects open issues. Report creates numbered JSON files in stable
alphabetical area order, each containing its area, count, and issue records.

## Offline workflow

Build `bin/sos` from the repository root if necessary:

```sh
go build -o bin/sos ./cmd/sos
cd examples/sos/repo-assistant
../../../bin/sos check main.sos
../../../bin/sos run main.sos -- --help
../../../bin/sos run main.sos -- import fixtures/issues.json --output issues.json
../../../bin/sos run main.sos -- triage issues.json --criterion all --output selected.json
../../../bin/sos run main.sos -- report selected.json --output reports
```

Expected report counts: **build 1, docs 1, runtime 2**. The closed issue (104) is
excluded. No API key or model requests are needed for this sequence. Existing
output files are protected: choose new destinations to repeat a workflow.

## Jev workflow

Set `TYPESAFE_API_KEY` in the launching environment (or Studio's project
environment settings), then run:

```sh
../../../bin/sos run main.sos -- triage issues.json --criterion blocking --output blocking.json
../../../bin/sos run main.sos -- report blocking.json --output blocking-reports
```

`blocking` is the default criterion. It asks Jev whether each open issue describes
an actively broken build, exploitable security flaw, data loss, or a regression
blocking users. The threshold is 0.85; uncertain results are discarded, and
provider failures stop execution before saving selected issues. Selection can vary
with model responses. The fixture needs at most four runtime judgment requests;
the project budget allows twelve requests per run with a thirty-second timeout.
These are request/time limits, not a monetary spending cap.

This example deliberately uses fixed language grammar for command dispatch and
collection operations. Jev judges issue content at runtime; it is not needed to
interpret the command syntax. Secrets are never part of this example or its
compiled source graph.

## Generate a standalone CLI

From the example directory:

```sh
../../../bin/sos build main.sos --output repo-assistant
./repo-assistant --help
./repo-assistant import fixtures/issues.json --output native-issues.json
./repo-assistant triage native-issues.json --criterion all --output native-selected.json
./repo-assistant report native-selected.json --output native-reports
```

The binary embeds the module source and local package graph. It can run after
moving it away from this project; input/output files remain external, and live
Jev still requires the runtime API key. An installed `sysone` launcher can wrap
these same commands; see the Studio CLI documentation for its alias interface.

## Verification and implementation notes

`go test ./tests/e2e -run 'TestRepoAssistantProjectWorkflow|TestCommandImportsStillResolveAndValidate' -count=1`
runs the checked-in files through both the runner and a native executable after
deleting its source project. It checks normalization, closed-issue exclusion,
report counts/order, help, invalid choices, and overwrite protection. An HTTP
canary ensures the offline workflow makes no provider requests.

CLI entry files may contain top-level imports alongside command/schema/action
declarations; import resolution still validates their targets before execution.
Library operations receive explicit arguments, avoiding hidden caller variables.

## Open as a Studio project

From the repository root:

```sh
bin/sysone --project examples/sos/repo-assistant open studio
```

Select `main.sos`, choose a command and fill its inputs. Use the file tree to
inspect the local package. Settings configures the project's Jev key. Save edits,
then **Build CLI** generates a native executable. The same project can be driven
through `sysone --project examples/sos/repo-assistant mcp` by an MCP client.
