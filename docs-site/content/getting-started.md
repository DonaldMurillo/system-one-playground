# Get started

SysOneScript source files end in `.sos`. Start from a source checkout; release downloads and a public installation URL have not been established yet.

## Build the language tools

Use Go 1.25 or newer from the repository root:

```sh
go build -o bin/sos ./cmd/sos
go build -o bin/sysone ./cmd/sysone
go build -o bin/sos-studio ./cmd/sos-studio
export PATH="$PWD/bin:$PATH"
sysone version
```

Keep these three executables together. `sysone` delegates language commands to `sos`; the browser workspace uses `sos-studio`. The documentation site is a separate Go 1.27 module and is not required to run the language.

## Run an offline script

Create `hello.sos`:

```text
make message "hello from SysOneScript"
show message
```

```sh
sysone check hello.sos
sysone run hello.sos
sysone build hello.sos --output hello
./hello
```

The output is `hello from SysOneScript`. No API key is required. Building requires Go; running the resulting native executable does not.

## Open a project

```sh
sysone --project examples/sos/repo-assistant open studio
```

Select `main.sos` in the file tree. The repository example includes commands, a local multi-file package, fixtures and a project configuration. Follow [Build a repository CLI](/docs/repository-cli) for the offline import, triage and report workflow.

Use `open examples` for the smaller example experience. The selection persists per project. The browser launcher stays in the foreground; Ctrl-C stops it.

## Add Jev when needed

Use Studio's Settings to set `TYPESAFE_API_KEY` for your project. Values are saved in a local `.env` with restrictive permissions; keep it out of version control. CLI scripts can also use the launching environment. See [environment precedence](/docs/project-environment) for the differences.

Start with the [interpretation guide](/docs/interpretation) and [configuration and budgets](/docs/configuration). Inspect Interpretation for sentence resolution, and Trace for runtime judgments and reported usage. A successful run alone does not explain which records were selected.

## Next steps

- [Language reference](/docs/language)
- [Commands and actions](/docs/commands)
- [Packages and vocabulary](/docs/packages)
- [CLI and MCP](/docs/agent-interface)
