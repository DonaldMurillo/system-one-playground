# semlint

semlint combines deterministic code selectors with narrow Jev questions about meaning. It groups applicable questions by extracted function so several rules can share one request. Use it alongside ordinary linters; semantic findings are review leads, not proof that a program is correct or incorrect.

## Start offline

From the repository root:

```sh
go build -o bin/semlint ./cmd/semlint
bin/semlint -list
bin/semlint -sites-only -sets=all cmd/semlint/fixtures
```

Listing rules and inspecting candidate sites require no API calls. The shipped sets are `default` and `browser-storage`; `-sets=all` enables both. Extraction supports Go and the JavaScript family. Python is unsupported because the extractor relies on braces rather than indentation.

## Run semantic checks

Set `TYPESAFE_API_KEY` in the environment or the working directory's `.env`. These commands make provider requests:

```sh
bin/semlint -sets=all ./src
bin/semlint -since=HEAD -format=hints
bin/semlint -diff=review.patch -format=json
```

`-since=HEAD` selects working-tree changes; another Git ref selects changes against it. These commands require a Git checkout. `-diff=-` reads a unified diff from stdin. Added/changed lines select affected functions; `-changed-lines-only` additionally restricts reported finding lines.

The default cap is 400 units, with eight workers and a three-minute overall deadline. Use `-max-units`, `-workers` and `-timeout` deliberately. `-max-units=0` removes the guard. Unit counts are not a token or dollar cap. Failed requests and trimming can affect coverage; read the run summary.

## Write rules

```json
{
  "rules": [{
    "id": "error-swallowed",
    "severity": "warning",
    "why": "A failed operation may appear successful.",
    "where": {"kind": "catch"},
    "ask": {
      "instructions": "Does this handler hide a failure that its caller needs to handle?",
      "yes": "A relevant failure is swallowed without recovery or reporting.",
      "no": "The handler recovers, reports, or intentionally handles the failure.",
      "threshold": 0.75
    },
    "message": "Review whether this handler hides a relevant failure."
  }]
}
```

Load with `-rules-file=./my-rules.json`. Reusing a shipped ID overrides that rule. A selector can specify a literal regex `pattern` or a language-aware `kind`: `function`, `catch`, `comment`, or `return-error`. Deterministic rules use `check` instead of `ask`, with `line_has`, `line_lacks`, `unit_lacks`, or `always`.

## Context and limitations

The extractor can supply module declarations, enclosing scope and related lines within the file. A deterministic identifier index also supplies bounded related lines from other files in the same run. This is limited context sharing, not whole-program analysis.

Large inputs may be trimmed or skipped. Context completeness accompanies requests so rules can avoid conclusions when evidence is missing. Findings support text, JSON and compact hints; `-severity` and `-min-confidence` filter output.

## Evaluate rules

```sh
make semlint-fixtures
make semlint-separation
bin/semlint -calibrate -sets=all cmd/semlint/fixtures/defects.js
```

These commands consume provider requests. Calibration exposes probabilities; separation compares defective and correct populations across runs. Calibrate with the full active rule battery, and inspect each rule's `_status`. Historical fixture measurements in the source README are observations, not guaranteed accuracy on your code.
