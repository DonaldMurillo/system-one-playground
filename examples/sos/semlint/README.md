# semlint implemented in SOS

The Go semlint executable remains the reference implementation. `scan.sos` is the SOS application: it selects rules, discovers source files, chooses changed units, evaluates question batches in a bounded parallel map, filters findings, and decides the process status. Both applications share the deterministic source extractor and rule compiler through `internal/semcore`.

## Start offline

From this directory:

```sh
sysone check scan.sos
sysone run scan.sos -- rules
sysone run scan.sos -- sites --root fixtures --sets none --rules_file fixtures/rules.json
sysone run scan.sos -- check --root fixtures --sets none --rules_file fixtures/rules.json --no_fail
```

The fixture uses a deterministic rule: no API key, request or payment is needed. `sites` returns source units, context, questions and deterministic findings without contacting Jev. `rules` lists the active definitions. The files passed to `--rules_file` and `--diff_file` resolve from the process working directory; `--root` controls source discovery.

## Semantic source checks

```sh
sysone run scan.sos -- check --root ../../.. --workers 8 --max_units 100
sysone run scan.sos -- check --root /path/to/repo --since HEAD --workers 4
sysone run scan.sos -- check --root /path/to/repo --diff_file changes.patch
sysone build scan.sos --output semlint-sos
./semlint-sos check --root /path/to/repo
```

Semantic checks require `TYPESAFE_API_KEY`. Each unit's applicable questions share one Jev request; `--workers` limits simultaneous units. The normal SOS run budget applies across every worker. Increase limits deliberately after examining `sites`; a worker count is not a request allowance.

Use `--sets default`, `browser-storage`, `all`, or `none`. A custom rule with the same ID replaces the bundled definition. `--only rule-id` selects one rule. SOS parameter names currently use underscores, so these options intentionally use `--rules_file`, `--max_units`, `--diff_file`, `--min_confidence` and `--no_fail`.

## Output and failures

`check` writes one JSON record with `findings`, `skipped` and `units`. Findings are sorted by file and line regardless of completion order. An individual failed unit is reported in `skipped`; successful units still contribute findings. Deterministic findings survive semantic failures.

Exit status is 0 for a complete run without findings, 1 when findings remain, and 2 when units were skipped or the application rejects the scope. `--no_fail` suppresses finding/skip status changes, while invalid arguments and execution failures still fail. `--severity warning|error` and `--min_confidence` filter the final report. `--calibrate` includes every semantic answer before those report filters so raw Noul probabilities or score expectations can be inspected; the separate `separation` command measures repeated labelled/clean populations.

This is a trusted local source-scanning program. It can read source under `--root`, execute Git for `--since`, and send selected source/context to Jev when checking semantic rules. Check the offline `sites` output to inspect exactly what would be sent. Recordings contain that context too.

## The parallelism

```text
map each unit in units with at most workers running called results collecting failures:
  call judge_unit with unit, calibrate called findings
  return findings
```

`--format text` provides familiar file/line finding lines; `--format hints` adds consequences. `--changed_lines_only` restricts findings to changed lines, while ordinary diff mode checks entire touched units. Git scope includes untracked source files.

On a provider `max_tokens_exceeded` response, the worker explicitly retries once with reduced context and disclosed omissions. The retry consumes another admission from the shared run budget; other errors are collected without automatic retries.

## Separation measurement

```sh
sysone run scan.sos -- separation --root /path/to/labelled-fixtures --sets all --runs 3 --workers 4
```

Place `name.expected.json` next to each `name.js` or `name.go`, with `{ "expect": [{ "rule": "rule-id", "line": 12 }] }`. A matching rule is positive when its unit contains the labelled line. Every unlabelled matching unit is the correct population; absent sidecars therefore mean all cases are labelled correct. Keep the truth complete. Corrupt JSON fails before judgments. Deterministic checks are excluded from semantic populations.

The command emits true/false readings, strict and robust gaps, a suggested midpoint, and a verdict. It never rewrites your rule files. Incomplete populations receive `UNTESTED` and a null suggestion; gaps use the same discrete percentiles as Go semlint. Failed evaluations produce skipped entries and status 2.

Each iteration has isolated bindings. `results` follows input order; it contains success or error envelopes. There is no shared mutable findings list inside the workers. The application flattens and filters results afterward. Stop/deadline cancels the map and the runtime waits for admitted workers to finish.

## Prepared-unit entry point

`main.sos` remains the smaller original example, consuming `units.json` directly:

```sh
sysone run main.sos -- units.json
```

It runs sequentially, emits a findings array, and does not set finding-based exit status. Keep it for learning the question-batch API; use `scan.sos` for the repository workflow.

See [the implementation and parity guide](../../../docs/sysonescript-semlint.md) for supported behavior and remaining differences from Go semlint.
