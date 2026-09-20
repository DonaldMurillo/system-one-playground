# semlint as a SysOneScript application

The reference Go executable stays at `cmd/semlint`. The source-scanning SOS application is `examples/sos/semlint/scan.sos`; the earlier `main.sos` prepared-unit example remains available.

## Application boundary

SOS owns CLI inputs, custom rule override selection, scope policy, parallel evaluation, threshold and severity policy, calibration answer collection, findings, skipped-unit reporting and process status. These decisions are readable in the script.

Go standard-library operations provide reusable mechanics:

- `std/files.discover` recursively lists regular files, with explicit exclusions.
- `std/source.rules` and `builtin` load validated rule definitions.
- `std/source.extract` works on a supplied path label and source text, yielding source units and real locations.
- `std/source.prepare` builds questions and bounded context, using the same source/rule machinery as Go semlint. It never calls Jev.
- `std/source.diff` and `touches` parse unified diffs and test source ranges.
- `std/process.run` runs Git with an argument array, without shell interpretation.
- `std/jev.questions` builds first-class batches; the language's `evaluate` statement makes the provider request.

There is no `semlint.run` wrapper hiding the Go application. Extraction and preparation return ordinary values; another SOS application can supply different policies.

## Parallel semantics

A bounded `map each … with at most … running` executes isolated iteration bindings. Workers return values, not mutations of a shared findings list. The runtime preserves input order when collecting results, shares the request budget, and propagates cancellation.

The semlint application uses `collecting failures` to receive `{ok, value, error}` envelopes. It retains successful results and reports failed units rather than presenting an incomplete scan as clean. The final findings order is file and line, independent of network completion order.

Batching and concurrency are separate. A unit with twelve questions is one request; eight concurrent units can mean eight requests each carrying a different batch. The runtime does not repartition the questions.

## Current parity

Supported: the bundled general and browser-storage rule sets, custom rules replacing bundled IDs, deterministic rules, source units, module/enclosing/peer/cross-file context, initial context trimming and explicit completeness, Noul and score thresholds, recursive discovery with default exclusions, diff unit filtering, bounded parallel requests, structured skipped-unit results, severity/confidence filters, raw answer calibration output, JSON/text/hints reporting, changed-line filtering, Git untracked files, one explicit disclosed large-input retry, labelled separation measurement and finding/incomplete exit status.

The SOS interface is intentionally not a flag-for-flag clone. Commands are `rules`, `sites`, `check`, and `separation`; options currently use SOS identifier spelling with underscores. One source root is accepted, rather than Go semlint's multiple positional paths. The SOS CLI's provider configuration, deadlines, budgets and record/replay apply instead of independent Go semlint flags.

The separation command prepares context separately for each file, matching the Go reference, and measures the full active question battery repeatedly, then joins answers to adjacent `*.expected.json` sidecars by rule ID and source unit bounds. It computes the same discrete 10th/90th percentile gap as the Go reference. Incomplete positive/negative populations produce `UNTESTED` with a null suggested threshold. Suggestions retain their numeric precision rather than rounding to two decimals. JSON report layout differs from Go's human separation table.

Provider usage remains available in the SOS runtime trace; the script report does not duplicate token, wall-time or p50 statistics. The SOS source scanner rejects file read errors as execution failures instead of silently dropping unreadable files. A canceled or exhausted run is a global failure, not a clean report with a few skipped workers. Raw `--calibrate` answers include every probability, including values below the Go calibration mode's 0.01 floor.

To match the Go reference's diff context, the application filters source units before cross-file preparation. Git scope includes untracked files in full; `--changed_lines_only` narrows tracked findings to exact changed lines. One large-input retry explicitly reduces context and declares its omissions. Every retry consumes a request-budget admission. The failure handler uses `rethrow` for other failures, preserving their typed provider status and code in skipped results; converting an error to `stop with error` would preserve only its message.

## Validation

`tests/e2e/semlint_rewrite_test.go` invokes the actual CLI against real source fixtures. It checks extraction locations, deterministic findings, diff selection, report filters, scope guards, controlled concurrent HTTP responses completing out of order, partial provider failures, Git changes plus untracked files, disclosed oversized retries, labelled separation populations, and a compiled executable after deleting its SOS source. These tests contact only local test servers and incur no Jev cost.

Run:

```sh
go test ./tests/e2e -run 'TestSemlintSOS' -count=1
```

Comparing live outputs alone is insufficient for parity: model results can vary. Compare selected sites, full prepared state, question batches and findings with fixed responses first, then evaluate live behavior separately.
