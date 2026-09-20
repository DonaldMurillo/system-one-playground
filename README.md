# System One Playground

Build readable scripts, semantic code checks and developer tools with the TypeSafe System One API. Use **SysOneScript** from your terminal or **Studio**, call the API directly from Go, or try **semlint** on your code.

**Start offline. Add Jev judgments when you need them.** Repository name: `system-one-playground`. MIT licensed · SOS 0.6 preview.

## Run your first script

You need **Go 1.25+**. Clone the repository, then build the CLI:

```sh
git clone https://github.com/DonaldMurillo/system-one-playground.git
cd system-one-playground
go build -o bin/sos ./cmd/sos
go build -o bin/sysone ./cmd/sysone
export PATH="$PWD/bin:$PATH"

printf 'make message "Hello from System One Playground"\nshow message\n' > hello.sos
sysone run hello.sos
```

You should see `Hello from System One Playground`. No API key or Node.js required.

Turn the same script into a standalone executable:

```sh
sysone build hello.sos --output bin/hello
./bin/hello
```

Recipients need neither Go nor SysOneScript. Keep `sysone` and `sos` together when using the language tools; `sysone` delegates language commands to `sos`.

## Open Studio

Studio gives you a project file tree, editor hints, vocabulary search, environment settings and separate views for interpretation decisions and runtime traces. Building its frontend requires **Node.js 18+ and pnpm**:

```sh
pnpm --dir studio install --frozen-lockfile
pnpm --dir studio build
go build -o bin/sos-studio ./cmd/sos-studio
sysone --project examples/sos/repo-assistant open studio
```

Keep `sos-studio` alongside the other two executables. This opens the browser workbench. For the optional native desktop app, see the [Studio guide](docs/studio-overview.md).

## Choose what to try next

| I want to… | Start here |
| --- | --- |
| Build a CLI in readable sentences | [Repository assistant walkthrough](examples/sos/repo-assistant/README.md): import issues, triage and generate reports |
| Inspect code without API calls | `go run ./cmd/semlint -sites-only cmd/semlint/fixtures` |
| Run the linter written in SOS | `sysone run examples/sos/semlint/scan.sos -- sites --root cmd/semlint/fixtures` |
| Use System One from Go | [Go client quickstart](docs/go-client.md): one request, typed answers and usage |
| Let an agent use the workspace | [CLI and MCP interface](docs/sysonescript-agent-interface.md) |
| Explore tool-call policy and API experiments | [Gate and repository tools](docs/repository-tools.md) |

The `sites` commands only inspect source and show the planned checks. Both the Go and SOS semlint implementations are available; [SOS semlint](docs/sysonescript-semlint.md) adds an example of bounded parallel judgments and failure handling in the language itself.

## Add live judgments

Set `TYPESAFE_API_KEY` in your environment or in Studio Settings. Then try the small Go API demonstration:

```sh
go run ./cmd/playground 01
```

This makes paid provider requests. [The client guide](docs/go-client.md) explains credentials, models, typed answers and usage. For scripts, see [Jev judgments and interpretation](docs/sysonescript-language.md) and [project environment settings](docs/sysonescript-project-environment.md).

Canonical scripts run offline. Jev can interpret supported semantic sentences and judge data at runtime. Parallel workers share request/time limits; those limits are not an account-wide monetary cap.

## Documentation

[Read the searchable documentation](https://donaldmurillo.github.io/system-one-playground/).

- [Language reference](docs/sysonescript-language.md) and [parallel maps](docs/sysonescript-parallel.md)
- [Go semlint guide](docs/semlint-guide.md) and [SOS semlint guide](docs/sysonescript-semlint.md)
- [All Markdown guides](docs/README.md)

Run the searchable documentation site locally with **Go 1.27+**:

```sh
cd docs-site
python3 scripts/sync_content.py
go run .
```

Open <http://localhost:3070>. The site is built with [fastr-docs](docs-site/README.md).

## Development and status

```sh
go test ./...
go vet ./...
pnpm --dir studio test
pnpm --dir studio run lint
(cd desktop && go test ./... && go vet ./...)
```

SOS is a preview: local packages and native builds work; remote package resolution and a plain JavaScript emitter are not implemented. Browser/WASI capabilities depend on the imported libraries; live Jev host adapters are not available. See the [language guide](docs/sysonescript-language.md) for limits.

The Go module is `github.com/DonaldMurillo/system-one-playground`. Install the client in your own module with `go get github.com/DonaldMurillo/system-one-playground/typesafe`. See [public release readiness](docs/public-release-readiness.md).

[MIT](LICENSE). Third-party dependencies retain their own licenses. This project uses the TypeSafe System One API; it is not the provider's official SDK or documentation.

## Jev and agents

[Add Jev to your application](docs/enable-jev.md) · [For agents](docs/for-agents.md)
