# Add Jev to your application

Jev can judge your application's data, help resolve supported SOS sentences, or assist an editor analysis. These are separate switches. You can use runtime judgments while keeping the language completely canonical.

## First: credentials and a runnable script

Build `sysone`, `sos` and `sos-studio` as shown in [getting started](/docs/getting-started). Set `TYPESAFE_API_KEY` in the environment that launches the CLI, or open Studio Settings and save it for the project. Do not put keys in TOML or source code.

For a terminal session:

```sh
export TYPESAFE_API_KEY='your-api-key'
```

The language CLI also loads `.env` from its current working directory; nonempty process values take precedence there. Studio/project services use their per-project context and precedence described below.

Save this as `decide.sos`:

```text
+++
version = 1
[interpretation]
mode = "canonical"
[runtime]
judgment = "explicit"
[budget.run]
requests = 1
timeout = "15s"
+++
make message "Checkout is down for every customer."
judge message by jev "Does this describe an active service outage?" called outage
show outage
```

```sh
sysone config decide.sos
sysone check decide.sos
sysone run decide.sos
```

`config` shows effective settings and where they came from. `check` validates locally. `run` sends the message and question to Jev and prints an answer record containing `p_yes`. The answer is a probability, not a boolean or a guarantee. This example makes one paid request if no earlier policy denies it. Check the CLI usage summary or Studio Trace to confirm the request and response.

## Enable it at the right scope

| Scope | Where | Purpose |
| --- | --- | --- |
| Global | `sysonescript/config.toml` under Go's OS user config directory | Your default preferences across projects |
| Custom global location | `SOS_CONFIG_HOME` directory containing `config.toml` | Select a separate configuration profile |
| Project | Nearest `sos.toml`, found upward from the working directory | Team/application settings |
| File | `+++` TOML frontmatter | This script's interpretation, runtime and budget preferences |
| Invocation | `--model`, `--max-calls`, `--timeout` | Model choice and additional run constraints |

Every TOML document requires `version = 1`. Preferences resolve from defaults → global → project → file. Request and time ceilings take the minimum of the explicitly configured limits; a file cannot enlarge an enclosing allowance. Runtime `deny` cannot be overridden by a file. Frontmatter cannot set editor preferences.

For example, put this in `sos.toml` to enable supported semantic language and judgments throughout a project:

```toml
version = 1
[editor]
assistance = "on-demand"
[interpretation]
mode = "semantic"
[runtime]
judgment = "semantic"
[budget.run]
requests = 20
timeout = "30s"
```

Remove conflicting file preferences when switching a script to this recipe. Inspect `sysone --project /path/to/project config main.sos` rather than guessing which settings won.

## Three independent switches

| Setting | Values | What it enables |
| --- | --- | --- |
| `interpretation.mode` | `canonical`, `assisted`, `semantic` | Canonical grammar only, supported assisted sentence resolution, or semantic resolution including declared criteria |
| `runtime.judgment` | `deny`, `explicit`, `semantic` | No live calls, explicit Jev operations, or implicit runtime criteria as well |
| `editor.assistance` | `off`, `on-demand`, `automatic` | Permission for requested editor analysis; `automatic` currently behaves as on-demand |

Defaults are canonical interpretation, explicit judgments and on-demand assistance. Simply setting a mode does not call Jev. Canonical syntax, ordinary diagnostics, formatting and configuration inspection stay local. Analyze/explain may call Jev for competing supported meanings; Run calls it for live judgments. Unsupported arbitrary prose is not converted into invented code.

## Add decisions to ordinary code

The `judge`, `classify` and `score` statements return records you can inspect and branch on. For example, after the first script's judgment:

```text
when outage.p_yes >= 0.85:
  show "Escalate for review"
otherwise:
  show "No automatic escalation"
```

Use [the language reference](sysonescript-language.md) for classification labels, scoring rubrics and inline `keep ... where jev` filters. Inline filters support selected input fields, an explicit threshold and `keep`, `discard` or `stop` when uncertain. Choose uncertainty policy deliberately; a low-confidence answer is not the same as a negative answer.

For multiple questions about the same input, [question batches](sysonescript-question-batches.md) use one request. For independent inputs, [parallel maps](sysonescript-parallel.md) bound concurrency and share the run's budget. Worker count does not multiply the allowance.

## Enable semantic sentences and reusable criteria

With both interpretation and runtime set to `semantic`, run the included example:

```sh
sysone explain examples/sos/jev-workflow.sos
sysone run examples/sos/jev-workflow.sos
```

It declares `criterion urgent`, resolves `filter tickets where team is "payments"`, then applies `keep urgent tickets`. The declaration's question defines urgency; `accept probability at least 0.85` and `on uncertain discard` define the policy. Its frontmatter caps requests at eight.

**Interpretation** explains what operation a sentence became. **Trace** shows runtime questions, answers, decisions and reported usage. A successful execution alone does not establish whether the chosen interpretation or threshold matches your intent. See [inspecting and pinning interpretations](sysonescript-interpretation.md) to review and reuse those decisions.

## Connect an existing application

You do not have to replace the host application with SOS.

- **Subprocess:** invoke `sysone run` or a compiled native executable from any language. Pass CLI arguments or read stdin with `std/io`; use JSON output as your boundary. Native builds need credentials on the machine that runs them.
- **Structured project API:** pipe a JSON request to `sysone --project PATH api run`. Its response includes output, traces, usage and failures. This is useful when the host already has the source text.
- **Go embedding:** use `sos.Parse` and `sos.Run`; provide input/output streams and a cancellation context. `sos.WithEnvironment(ctx, values)` supplies per-invocation provider credentials without modifying global process environment. For semantic source and imports, use the resolution/module pipeline used by the project API rather than assuming raw `Parse` resolves them.
- **Agents:** use the project CLI or stdio MCP. Follow [For agents](for-agents.md); the public documentation website does not execute scripts.

Here is a complete structured invocation using an inherited API key:

```sh
printf '%s' '{"path":"decision.sos","source":"make message \"Checkout is down\"\njudge message by jev \"Is this an outage?\" called answer\nshow answer\n"}' | sysone --project . api run
```

The default explicit runtime mode permits this call unless project/global policy restricts it. Consume the response's `output`, `traces` and `usage` fields rather than parsing CLI status text. Check its error/ok status before using the result.

For a Go application, this complete example runs a canonical judgment with a one-request ceiling. It inherits the key from the environment and explicitly puts it in an invocation context:

```go
package main

import (
    "context"
    "fmt"
    "os"
    "time"

    "github.com/DonaldMurillo/system-one-playground/sos"
)

func main() {
    program, diagnostics := sos.Parse(`make message "Checkout is down"
judge message by jev "Is this an active outage?" called answer
show answer`)
    if len(diagnostics) != 0 { panic(fmt.Sprint(diagnostics)) }
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    ctx = sos.WithEnvironment(ctx, map[string]string{
        "TYPESAFE_API_KEY": os.Getenv("TYPESAFE_API_KEY"),
    })
    result, err := sos.Run(ctx, program, sos.Options{
        Dir: ".", Stdout: os.Stdout, Stderr: os.Stderr, MaxCalls: 1,
    })
    if err != nil { panic(err) }
    fmt.Fprintf(os.Stderr, "usage: %+v\n", result.Usage)
}
```

Native executables embed source and the interpreter; do not embed credentials. Live Jev host adapters for browser/WASM are not implemented. Pure operations and question construction can still run on supported WASM targets.

## Credentials, models and Studio

Studio Settings saves project provider values to a local `.env` with restrictive permissions. The project API supplies those values per invocation; names/status are visible, stored values are not returned. Shell-launched commands can inherit environment variables. Use the [environment guide](sysonescript-project-environment.md) for exact project/process precedence rather than assuming every embedding loads `.env` automatically.

`TYPESAFE_DEFAULT_MODEL` selects the default model; otherwise the provider default is used. CLI `--model` overrides the environment; a supported judgment block's explicit `model` is more specific. `TYPESAFE_BASE_URL` is available for a compatible endpoint. Never put provider secrets in committed `sos.toml`, frontmatter or an agent configuration file.

In Studio, use Analyze to inspect interpretation, then Run and inspect Trace. Editor `automatic` does not currently issue paid background calls as you type. Ordinary colors, hover information and diagnostics do not prove that a semantic decision has been resolved.

## Budgets, failures and replay

Configured ceilings constrain both interpretation and runtime requests. Failed attempts count; reported input tokens are recorded when available. There is no persistent daily allowance, account balance lookup or hard monetary cap. Studio also applies its run ceilings. Use [configuration and budgets](sysonescript-configuration.md) for exact behavior.

Record and replay a canonical judgment script:

```sh
sysone run decide.sos --record answers.json
sysone run decide.sos --replay answers.json
```

Replay makes no provider calls and validates compatible source, inputs and questions. It does not suppress ordinary filesystem/process effects. Recordings may contain the source data sent to Jev. Interpretation decisions have a separate saved-resolution mechanism.

If nothing happens, check the API key's scope, effective configuration, runtime `deny`, `requests = 0`, unsupported semantic syntax, timeouts and exhausted request ceilings. If it runs but the decision is unclear, inspect the returned probabilities and Trace. Handle provider failures explicitly; do not convert a failed request into a confident negative judgment.
