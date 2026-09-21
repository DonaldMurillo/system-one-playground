# SysOneScript implementation and release notes

Version 0.2.0, 2026-09-19.

## Command release 0.2

Nested command groups and executable leaves replace metadata-only command blocks.
The runner and packaged binaries share input validation/help; Studio renders a
command selector and inherited input fields. Run validates inputs before Jev,
then analyzes the selected leaf and reachable actions. Saved interpretation
format/registry 2 requires regeneration of older records. Repository examples
were updated directly; there is no per-file grammar version switch.

See the [tickets walkthrough](sysonescript-tickets.md). Its first release uses JSON
files; streaming, modules, and persistent project/currency allowances remain
planned. Analysis still uses line-preserving selected-source hashes; explicit
module graph/coverage provenance is a later roadmap stage.

Validation for this increment: full Go suite/vet, core/editor/config race tests,
frontend lint/helper tests/build, browser command/input flows, desktop tests/vet
and pinned Wails build, plus trimpath snapshot builds outside the checkout and
native execution after source removal. Provider behavior is covered through the
real client against controlled HTTP fixtures; no live paid evaluation was run
for this increment. Existing WASM packaging cases run in the Go end-to-end suite.

## Components

- `sos/`: source-mapped sentence parser, offline checks, expression evaluator, interpreter, existing TypeSafe client adapter, strict ordered judgment recordings, a constrained semantic resolver, and reusable criteria.
- `cmd/sos/`: run/check/fmt/build/lsp/version effective `config FILE`, and `explain FILE` commands. Script arguments are shared with generated standalone executables.
- `sosconfig/`: strict TOML parsing, global/project discovery, file frontmatter, preference origins, and intersected per-run ceilings. `sos/budget.go` provides concurrency-safe request admission and usage snapshots.
- `internal/sosbuild/`: Go executable/WASM packaging. Generated `sources.zip` bundles `sos`, `typesafe`, and `sosconfig` sources plus pinned module metadata, excluding tests and credentials. Run `go generate ./internal/sosbuild` after core edits. Release CLI builds use `-trimpath`, and can build scripts without the original checkout. The TOML dependency must be cached or downloadable at build time. Artifacts capture the build's effective configuration and validated interpretation.
- `internal/soslsp/`: stdio JSON-RPC LSP. Full-document synchronization, diagnostics, keyword completion, hover, name-based definition lookup, formatting, and explicit `sos/analyze` semantic assistance. Definition lookup is local and lexical, not a complete scope/type engine.
- `internal/studio/` and `studio/`: authenticated local HTTP host and locally bundled Monaco. LSP requests use immutable document snapshots over an HTTP bridge. Diagnostics discard stale responses. The explicit Analyze action displays interpretations and usage; compatible cached interpretations can be reused by Run. Native and browser clients share the assets and runtime.
- `desktop/`: Wails app in a separate module. This keeps Wails out of the CLI's dependency graph. Its resolved dependencies require Go 1.25; normal Go toolchain auto-selection handles it. Native Open/Save/Folder use OS dialogs. Choose the project folder to load `.env` when launching from Finder.

## Artifact boundaries

The native build is an executable containing the reference interpreter and program, not a direct optimizing code generator. Browser WASM includes Go support JavaScript and an HTML runner. WASI uses a separate host ABI. Pure scripts were executed under both target runtimes; model host adapters are not implemented in WASM, so live Jev constructs are rejected there. A separate JavaScript emitter remains future work.

Studio currently executes through the Go core in-process, with cancellation and execution/output limits. It is for trusted local scripts, not multi-tenant sandboxing. Browser services bind loopback and reject unrelated Hosts/Origins. The native Wails `wails://wails` origin is explicitly supported. Credentials stay in the Go process.

## Validation

- `go test ./...` covers the public CLI, standalone/WASM builds, language flows, LSP protocol sessions, and Studio HTTP handlers.
- `SOS_LIVE_TEST=1 go test ./tests/e2e -run TestLiveJevAndReplay -v` calls the real provider, then replays with the endpoint set to an unreachable address.
- All three live primitives were exercised through `examples/sos/primitives.sos`.
- Native, `js/wasm`, and `wasip1/wasm` count programs were executed with matching output.
- Browser verification used Playwright against the running bundled editor: run, live trace, and edit-triggered diagnostics. Native verification confirmed launch, script output, and OS Save dialog.
- The desktop package is locally self-signed for development, not notarized for public distribution. macOS ARM64 is the desktop platform validated here; other desktop platforms require their native packaging/testing lanes.

## OMP collaboration

For the TOML/request-budget slice, OMP implemented budget primitives and independent
CLI acceptance tests. The initial config worker timed out without files; the
parent implemented configuration and integrated runtime, packaging, usage reporting,
and Studio. Follow-up OMP tasks use the user's requested 45-minute limits.
Acceptance testing completed successfully. New checks include strict configuration,
ceilings, original source lines, relocation of native artifacts, HTTP failure usage,
zero-cost replay, race detection, header fuzzing, and browser frontmatter execution.
Persistent daily allowances, money enforcement, and semantic interpretation remain
planned; unsupported budget keys fail explicitly.

The subsequent OMP read-only audit completed and found a real mixed-source
packaging bug. The builder now selects a complete live checkout or a complete
embedded snapshot; a CLI regression test deliberately keeps a stale archive
while live code requires a new config symbol. Additional fixes cover positioned
configuration-value errors, exact Jev predicate boundaries, unsupported uncertainty
blocks, BOM handling, zero-admission budget reporting, and clearer replay mismatch
messages. The complete Go suite, race checks, fuzzing, and native build pass after
these fixes. The audit's observation about nonpositive CLI timeouts was already
addressed and is pinned by regression tests.

OMP tasks implemented the CLI/build subsystem, LSP, and Studio/Wails shell in separate file areas. The parent implemented the language core and integrated the results. The LSP and Studio tasks reached their bounded execution deadlines; the parent completed their test failures and UI integration. A later bounded OMP review did not produce a report, so no independent audit result is claimed.

Integration fixes included argument-parser advancement, default quoting, standalone error formatting, UTF-16 conversion, quoted connector parsing, stale diagnostics, explicit `by jev`, native host/CSP handling, real LSP bridge, and native file dialogs. See `agent-notes.md` for reusable guidance.


## Semantic increment verification

- OMP tasks use 45-minute limits: resolver implementation, editor integration,
  independent semantic acceptance, and integration review. See
  [interpretation workflows](sysonescript-interpretation.md) for the shipped surface.
- The live criterion example used 4 requests / 1,189 reported input tokens.
  A real keep-or-remove discrimination selected keep at 0.96 confidence using
  1 request / 602 input tokens; saved execution then needed zero new requests.
  These are observations from one run, not cost or accuracy guarantees.
- Saved pure-source interpretation ran offline as native, browser-WASM, and WASI
  artifacts. The generated browser host has a regression for Go deadline timers
  firing after main exits.
- The browser Analyze/Apply/Run/Save flow was exercised against the real core,
  with an indented sentence and trailing comment; `.sos` download preserved both.
  Failed-analysis HTTP tests retain known token usage and unknown timeout usage.

## Refreshing a running Studio

Desktop builds now run the shared frontend build before embedding assets. Use
`make sos-desktop` or `go tool wails build` from `desktop/`. Rebuilding does not
update an already-running desktop process or browser server. Save work before
restarting, or use `open -n desktop/build/bin/sos-studio.app` on macOS to open a
fresh instance while preserving the older window. Version 0.2.0 appears in the
app metadata and the editor status bar. Command controls appear for command
scripts; the greeting example includes two selectable commands.

## SysOneScript product identity

The language and Go core are `sos`, the compiler/runner is `bin/sos`, and the
editor is `bin/sos-studio` or `desktop/build/bin/sos-studio.app`. Script files use
`.sos`; project config is `sos.toml`. Global config uses the OS configuration
folder's `sysonescript/config.toml`, with `SOS_CONFIG_HOME` as its override.
Artifact knobs are `SOS_MODEL`, `SOS_MAX_CALLS`, `SOS_TIMEOUT`, `SOS_RECORD`, and
`SOS_REPLAY`; live-test opt-in is `SOS_LIVE_TEST`. Monaco's language ID is `sos`,
and the optional LSP method is `sos/analyze`.

Jev remains the underlying provider and explicit judgment keyword. Credentials
remain `TYPESAFE_API_KEY`; provider models and the TypeSafe Go client retain their
names. No legacy product aliases are installed. Configure existing workflows
with the names above when upgrading.

## 2026-09-21 - External process modules stay offline until invocation
- Scope: external-modules
- Trigger: Adding stdio/command plugins without weakening project trust or reproducible builds.
- Approach: Parse and type-check TOML offline; intersect capability layers; key persistent clients per run; bundle exact target artifacts with an authoritative checksummed manifest.
- Evidence: `go test ./...`, `go vet ./...`, `pnpm --pm-on-fail=ignore --dir studio run build`, and `pnpm --pm-on-fail=ignore --dir vscode run test`.
- Next time: Preserve the offline discovery boundary and add adversarial lifecycle tests before expanding the protocol.
- Status: active
