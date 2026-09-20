# TOML configuration and request budgets

Global settings, nearest-project settings, and `+++` TOML frontmatter control
interpretation, runtime judgments, editor assistance, request ceilings, and
timeouts. Daily project allowances and monetary limits are not implemented.

## Discovery and inspection

Global configuration is `sysonescript/config.toml` inside the OS user configuration
directory (Go `os.UserConfigDir`). Set `SOS_CONFIG_HOME` to a directory containing
`config.toml` to select a different global configuration directory.

Project discovery starts at the run working directory and walks upward to the
nearest `sos.toml`. It does not merge every ancestor project. Relative paths in
the script still resolve against the run working directory, not the config file.

```sh
go run ./cmd/sos config examples/sos/count.sos
```

`config FILE` prints effective settings and their origins as JSON without
executing the script or contacting Jev. `timeout_ns` is a duration in nanoseconds;
zero means no configured timeout. Settings default to on-demand editor assistance,
canonical interpretation, explicit runtime judgments, and 100 live requests.
Studio also enforces its existing 32-call and 30-second run defaults. Invalid/nonpositive CLI
`--timeout` values and overflowing numeric durations are usage errors.

## Supported settings

Every supplied configuration document requires `version = 1`.

```toml
version = 1

[editor]
assistance = "on-demand"

[interpretation]
mode = "canonical"

[runtime]
judgment = "explicit"

[budget.run]
requests = 20
timeout = "15s"
```

Allowed editor modes are `off`, `on-demand`, and `automatic`. Paid analysis is
explicitly requested through Analyze; `automatic` currently behaves as
`on-demand`. Ordinary diagnostics stay local. Interpretation modes are
`canonical`, `assisted`, and `semantic`; runtime modes are `deny`, `explicit`,
and `semantic`. Canonical code needs no interpretation requests in any mode.
See [interpretation workflows](sysonescript-interpretation.md) for inspecting and
pinning permitted sentence variants.

Only `[budget.run]` request and timeout limits are currently supported. Unknown
keys, duplicate keys, negative requests, missing/unsupported versions, and
nonpositive or invalid timeouts fail. Config documents and frontmatter are
limited to 64 KiB. Secrets remain in the existing environment setup and must not
be put in source frontmatter.

## File overrides

```text
+++
version = 1

[budget.run]
requests = 0
+++

make total 2 + 3
show total
```

The delimiter is a line containing exactly three plus signs. A header is optional
and belongs at the beginning of the file, with an optional UTF-8 BOM. A missing
closing delimiter fails. Source diagnostics retain body line numbers, and the
formatter preserves the header text. Frontmatter cannot set editor preferences,
including an empty editor table. Configuration never invokes Jev.

Unknown keys and invalid values carry positions into the editor diagnostics,
including dotted keys and inline tables. UTF-8 BOMs are also accepted on scripts
without frontmatter.

Preferences resolve from defaults, global, project, and file. Request/time
ceilings take the minimum of explicitly supplied limits. Omitted fields inherit;
zero requests denies live calls but permits ordinary code and recorded-answer
replay. A configured runtime `deny` also prevents live provider calls and cannot
be overridden by a later file preference. Replay still validates its recorded
answers and executes ordinary script effects.

Existing positive `--max-calls` and `--timeout` CLI flags can further constrain a
run. They cannot increase configured ceilings. Use TOML `requests = 0` for an
explicit zero allowance; legacy CLI `--max-calls` remains positive-only.

## Accounting and packaging

All live language judgments reserve a request before dispatch. A failed or
cancelled attempt does not refund that reservation. Successful responses record
reported input-token usage; other completions retain unknown usage. No automatic
retries occur. Runtime `Result.Usage`, Studio's run response, and CLI stderr
report usage; replay consumes no new provider request. These counters are local
to a run, not a provider-account balance or a persistent daily allowance.

The Go `RequestBudget` API can share one concurrency-safe total among editor,
interpretation, and runtime buckets. Bucket allowances cannot bypass the total.
The interpreter, semantic resolver, and editor use this admission API. A run
shares its total between interpretation and runtime; each explicit editor
analysis has a separate bounded budget. `EffectiveConfig` exposes resolution without execution;
`Options.Config` supplies a resolved policy and `Options.Budget` shares accounting.

Native and WASM builds embed the effective configuration from the build working
directory along with the script and its validated interpretation. At execution they use that captured policy,
not newly discovered host configuration. Existing environment runtime knobs may
further reduce limits. `SOS_MAX_CALLS=0` denies live calls; invalid or negative
values fail. Supplied `SOS_TIMEOUT` values must be positive durations (or integer
seconds); overflow and nonpositive values fail instead of disabling the timeout. Source/config policy is not an OS sandbox. The bundled
compiler includes config sources and pinned dependency metadata; compilation
needs the TOML dependency available in Go's module cache or via module download.

Development builders use all source packages and module metadata from the live
checkout; released trimpath builders use all of them from the embedded snapshot.
Run `go generate ./internal/sosbuild` before rebuilding a release CLI. Captured
configuration includes provenance paths from the build machine, not credentials.

Future persistent project accounting and money reservation
are tracked in the [semantic toolchain plan](sysonescript-semantic-plan.md).

See [library vocabulary and the searchable dictionary](sysonescript-vocabulary.md)
for open imports, parent aliases, and project-wide library configuration.
