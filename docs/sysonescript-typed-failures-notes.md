# Typed-failure implementation notes

These notes record the implementation seams for the proposed typed-failures
surface. The language core remains the authority; Studio, LSP, and vocabulary
clients consume its parsed metadata instead of maintaining duplicate registries.

## 2026-09-21 - Final OMP boundary review
- Scope: semantic-provider, CLI, standalone-build, debugger, VS Code
- Trigger: Whole-branch OMP review found data-boundary and client-contract drift.
- Approach: Redact provider collection origins, save only successful analyses, fail closed on `.env`, and separate live filesystem paths from embedded logical paths.
- Evidence: `go test ./...`, `go test -race ./sos`, plus Studio and VS Code test/build scripts.
- Next time: Review every new analysis field at provider, persistence, artifact, and editor boundaries before shipping.
- Status: active

## What worked

- `FailureDef` reuses named-record field validation while reserving host-owned
  fields (`kind`, `message`, `retryable`, `status`, `code`, and `frames`).
- `typedFailure` travels through the existing error/handler path, so legacy
  `on failure`, `failure`, `return`, and `rethrow` behavior stays compatible.
- Action metadata is projected from `parseActionDecl`; reachable local calls
  are added to `possibleFailures` and sorted for stable JSON/tooling output.
- Captured outcomes are runtime-created closed records with `succeeded`,
  `value`, and `failure`; fatal runtime errors are not converted to outcomes.
- Standalone builds must reject non-empty semantic diagnostics before compiling;
  otherwise payload-field failures can compile into artifacts that fail only at
  runtime. Generated runners use the same structured failure rendering as the
  CLI.
- Package-local action calls must keep the current module's failure registry;
  restoring the entry program's registry makes nested package failures appear
  as unknown at runtime.
- Contract propagation must inspect qualified calls, sentence calls, and
  transitive local calls. A `pass failure on` handler is propagation, not
  handling, and typed control-flow checks must prove every branch terminates.
- `Result` narrowing is represented with internal `ResultSuccess` and
  `ResultFailure` checker types; keep the branch state scoped to `when` and
  its paired `otherwise`, then restore the enclosing environment.
- Generated artifact and CLI checks are separate acceptance surfaces: run
  `go generate ./internal/sosbuild` before artifact tests, and keep
  `sos check --json` metadata covered at the CLI boundary.
- Wrapped `returning` and `may fail with` action headers must be normalized by
  both the parser and semantic preanalysis. Sharing that normalization keeps a
  canonical declaration deterministic and prevents accidental Jev requests.
- Parallel runtimes must clone the checker/runtime type map as well as the
  value environment; sharing that mutable map creates races between workers.
- Module failure frames use logical package-relative source identities. Never
  embed a build host's absolute checkout path in a standalone artifact.
- Vocabulary and LSP catalogs expose imported record and failure definitions
  from the same resolved scope used by the checker, including deterministic
  collision diagnostics.

## Follow-up seams

- Standard-library and external/plugin operations still need first-class
  declared failure metadata and protocol validation.
- Full path-sensitive `Result` narrowing, standard-library/provider/plugin
  failure declarations, command-boundary call graphs, debugger failure
  inspection, and native/browser/WASI metadata manifests need a later pass over
  the shared artifact representation.
- The canonical parser currently expects an action signature on one logical
  source line; formatter-generated wrapped declarations should be normalized
  before parsing if multiline signatures are introduced.

## Verification heuristic

When changing this area, run the focused `sos` typed-failure tests first, then
the full Go test/vet suite. Regenerate `internal/sosbuild/sources.zip` before
artifact checks so embedded runtime sources cannot lag the checkout.
