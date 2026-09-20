# SysOne command line and agent interface

`sysone` is the language entry point, like `go` or `python3`. It also exposes Studio's project services without requiring an open desktop window. It operates on the same files and settings as Studio; it does not remotely control an existing window's unsaved editor buffer.

Use `sysone update --check` to compare the installed CLI with the latest
published CLI release, or `sysone update` to download a checksum-verified
installer and replace the colocated `sysone` and `sos` commands. Updates are
always explicit; ordinary language commands never contact the release service.

Build and distribute these three executables together (or put them on `PATH`):

```sh
go build -o bin/sos ./cmd/sos
go build -o bin/sos-studio ./cmd/sos-studio
go build -o bin/sysone ./cmd/sysone
```

`sysone` delegates language commands to the sibling `sos` executable, then searches `PATH`. It preserves arguments, standard input/output/error and exit status. This keeps one implementation of compilation and language execution. Global `--project` goes **before** the command so script arguments remain untouched.

```sh
sysone --project ./my-project check main.sos
sysone --project ./my-project run main.sos -- --help
sysone --project ./my-project build main.sos --output ./my-cli
sysone --project ./my-project explain main.sos
sysone --project ./my-project vocabulary main.sos --json
sysone --project ./my-project open studio
```

Also supported: `fmt`, `config`, `lsp`, and `version`, with the existing `sos` arguments. `open` runs the browser Studio launcher in the foreground; Ctrl-C stops that server. `open examples` selects the examples experience, and `open studio` selects the project experience. The choice persists in the project's `.sysone-studio.json`.

## Files and pipelines

Workspace commands return JSON. Paths are relative to the project root. Hidden files and symlink paths are restricted by the same Studio project API used by the UI.

```sh
sysone --project ./my-project tree
sysone --project ./my-project mkdir src
printf 'show "hello"\n' | sysone --project ./my-project write src/main.sos
sysone --project ./my-project read src/main.sos
```

`read` returns `path`, `source`, and `revision` (SHA-256). To replace an existing file, pass the returned revision:

```sh
printf 'show "updated"\n' | sysone --project ./my-project write src/main.sos --revision REVISION_FROM_READ
```

An omitted or stale revision cannot overwrite an existing file. A new file uses an empty revision. Re-read after a conflict, reconcile the contents, then retry using the current revision.

`api check|run|analyze|build|lsp|project` takes a JSON object on stdin and returns the corresponding Studio API response. Use `source` for the exact buffer to act on and `path` for project-relative import resolution. This is the structured alternative to the ordinary language commands:

```sh
printf '{"path":"src/main.sos","source":"show 42\\n"}' | sysone --project ./my-project api run
sysone --project ./my-project analyze src/main.sos
```

`run` responses include output, trace and usage where available. `analyze` may call Jev; configured policies and budgets apply. Check diagnostics and runtime failures produce nonzero command exit status even when the HTTP response was successful. Input is limited to 2 MiB.

## Environment and experience settings

```sh
sysone --project ./my-project env list
sysone --project ./my-project settings studio
sysone --project ./my-project settings
```

`env set NAME` reads the value from stdin, avoiding secrets in command arguments. Pipe from your secret manager, or use a shell's hidden prompt to collect a value. No response includes stored values. A single trailing line ending is removed; embedded newlines are rejected by Studio. Environment values are stored in the project `.env`; its contents cannot be read through project file tools. Environment listing returns names, configuration status, and origin. This is a local plaintext configuration file with restrictive permissions, not an OS keychain.

## MCP

Configure an agent client with an absolute binary and project path:

```json
{
  "mcpServers": {
    "sysone": {
      "command": "/absolute/path/bin/sysone",
      "args": ["--project", "/absolute/path/my-project", "mcp"]
    }
  }
}
```

The server implements the MCP `2025-06-18` [stdio transport](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) and [tools interface](https://modelcontextprotocol.io/specification/2025-06-18/server/tools). Messages are newline-delimited JSON-RPC. Initialization and the initialized notification precede tool use. Standard output contains only protocol messages.

Tools:

| Tool | Arguments | Result |
| --- | --- | --- |
| `project_tree` | none | Project file listing |
| `project_read` | `path` | Source and revision |
| `project_write` | `path`, `source`, `revision` | Saved revision; stale writes fail |
| `project_mkdir` | `path` | Directory creation |
| `environment_list` | none | Names/status only |
| `environment_set` | `name`, `value` | Configuration status, never the value |
| `project_settings` | optional `mode` (`examples` or `studio`) | Persistent mode |
| `check` | `path`, `source` | Diagnostics and CLI command metadata |
| `analyze` | `path`, `source` | Interpretation report |
| `build` | `path`, `output` | Native executable compiled from saved files |
| `run` | `path`, `source`; optional `args`, `commandPath`, `timeoutMs` | Output, trace, usage and runtime errors |

Tool responses include both JSON text content and `structuredContent`. Operational failures set `isError`; malformed arguments use JSON-RPC errors. Unknown arguments are rejected. Discovery annotations identify read-only tools and warn that execution/analysis can interact with external systems. Running user code can write project files and consume Jev budget. Secret writes still pass through the client's tool invocation, so clients should avoid retaining sensitive argument logs.

The initial transport handles requests sequentially. It does not provide notifications, subscriptions, remote window control, or concurrent cancellation. Use `timeoutMs` and configured budgets to bound runs; terminate the stdio process if necessary. Native builds are available through the MCP `build` tool and `sysone api build`. Both use saved disk source, require an existing output directory, and refuse to overwrite an existing path (including a file created while compilation was running). Compilation is limited to two minutes and uses the selected project's configuration and credentials. Build results include interpretation analysis and usage where available. Use ordinary `sysone build` for target selection and its full compiler flags.

## Implementation and verification notes

The front door lives in `cmd/sysone`. Its in-process API adapter calls Studio's authenticated HTTP handler with a private per-session token, without binding a port. Keep validation, path confinement, environment redaction, and revision checks in the shared project handler instead of duplicating them in CLI/MCP wrappers.

Regression coverage is `tests/e2e/sysone_test.go`: it builds and launches the real executable, exercises native language commands and a compiled program, then opens an actual stdio MCP session to read/edit/run/check files and verify conflict handling, initialization, invalid arguments, and secret redaction. Build service code is in `internal/studio/build.go`; `sosbuild.BuildOptions.Dir` explicitly supplies the project configuration directory without changing the process working directory. Run `go test ./tests/e2e -run '^TestSysone' -count=1` after changes.
