# Gate, playground and experiments

These tools share the Go client but serve different jobs. They are part of the same repository as semlint, SysOneScript and Studio.

## Agent tool-call gate

`cmd/gate` implements a pre-flight policy gate. Hook adapters in `plugin/` support Claude Code and omp. The model supplies narrow judgments; Go code combines them into allow, ask or deny decisions.

```sh
make build
claude --plugin-dir "$PWD/plugin"
omp --hook "$PWD/plugin/omp/typesafe-gate.hook.ts"
```

Run from the repository root, with `TYPESAFE_API_KEY` set in the launching environment. The build target writes `plugin/bin/typesafe-gate`. These are separate agent-launch commands, not commands to run inside a SysOneScript program.

The gate considers request intent, whether the target is related, whether the description matches the proposed action, and safety factors such as destructive operations, credentials and network writes. Read-only tool calls bypass model evaluation.

**It fails open.** Missing credentials, timeouts, bad payloads and panics allow the call. It is an advisory supplement to the agent's normal permissions, not a security boundary.

Configuration comes from `.typesafe-gate.json` in the project or `~/.config/typesafe-gate.json`; partial configuration overrides selected fields:

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

`make eval` uses the fitted corpus. `make eval-heldout` uses the separate held-out corpus. Both make live judgments; a fitted score does not establish unseen-case reliability.

## API playground

`cmd/playground` contains seven executable scenarios: primitives, structured state, confidence, fan-out, composite questions, selection and errors/metadata.

```sh
go run ./cmd/playground 01
go run ./cmd/playground 03
```

The argument is a substring filter. Omitting it runs all scenarios. These are live API demonstrations, not offline tests; they read the key from the environment or working-directory `.env`.

## Experiments

`cmd/experiments` explores calibration, sensitivity, guardrails, option scaling, throughput, budgets, staged decisions and model-derived features.

```sh
go run ./cmd/experiments e3
go run ./cmd/experiments e7
```

Experiments make real requests. Heavy throughput/sustained-load cases are skipped by default, but `-heavy` or naming a heavy experiment exactly enables them. Do not treat them as normal verification commands. Results depend on the model, input distribution, request grouping and provider behavior.

## Repository map

| Directory | Responsibility |
| --- | --- |
| `typesafe/` | Go HTTP client and typed questions/answers |
| `cmd/semlint/` | Semantic linter, rule sets and fixtures |
| `cmd/gate/`, `plugin/` | Agent policy gate and hook adapters |
| `cmd/playground/`, `cmd/experiments/` | API demonstrations and measurements |
| `sos/`, `sosconfig/`, `internal/sosbuild/` | Language, policy/configuration and builds |
| `cmd/sysone/`, `cmd/sos/` | Language and agent CLI entry points |
| `studio/`, `internal/studio/`, `desktop/` | Browser UI, project services and native shell |
| `tests/e2e/` | Executable/protocol acceptance coverage |
| `docs-site/` | fastr-docs website, independent Go module |
