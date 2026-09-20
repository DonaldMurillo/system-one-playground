# System One Playground

Build readable scripts, semantic code checks and developer tools with the TypeSafe System One API. Use **SysOneScript** from your terminal or **Studio**, call the API directly from Go, or try **semlint** on your code.

**Start offline. Add Jev judgments when you need them.** Repository name: `system-one-playground`. MIT licensed · SOS 0.6 preview.

## Ready to install and use

You do **not** need to clone this repository or install Go to use the released
CLI or VS Code extension.

### Install the standalone CLI

Install the latest standalone CLI on macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/DonaldMurillo/system-one-playground/main/scripts/install.sh | sh
```

On Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/DonaldMurillo/system-one-playground/main/scripts/install.ps1 | iex
```

Both installers detect the platform, verify the release checksum, and install
`sos` plus `sysone`. The Unix installer defaults to `~/.local/bin` and tells you
if it is not on `PATH`. The Windows installer uses
`%LOCALAPPDATA%\Programs\SysOneScript\bin` and adds that directory to the user
`PATH`. Set `SYSONESCRIPT_VERSION` to pin a release or
`SYSONESCRIPT_INSTALL_DIR` to choose another destination.

Check for or install later CLI releases explicitly:

```sh
sysone update --check
sysone update
```

The CLI does not update silently. VS Code also compares its bundled runtime
with `sysone` on `PATH` and offers the same update command when they differ.

### Install the VS Code extension

Install [SysOneScript from the Visual Studio Marketplace](https://marketplace.visualstudio.com/items?itemName=donaldmurillo.sysonescript-vscode),
or search for **SysOneScript** in VS Code's Extensions view. The extension ships
with the matching runtime, language server, runner, builder, formatter, and
debugger for the user's platform. Go and a separate CLI installation are not
required.

The standalone CLI remains optional for people who also want `sysone` and `sos`
in an external terminal. See the [editor services guide](docs/sysonescript-editor.md)
for all extension features and settings.

### Run your first script

```sh
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

## Add live judgments

Set `TYPESAFE_API_KEY` in your environment or use **Set Jev token** in the VS
Code project panel. Canonical scripts run offline; only model-backed Jev
operations require the token and can make paid provider requests.

The [client guide](docs/go-client.md) explains credentials, models, typed
answers, and usage. For scripts, see [Jev judgments and interpretation](docs/sysonescript-language.md)
and [project environment settings](docs/sysonescript-project-environment.md).
Parallel workers share request/time limits; those limits are not an account-wide
monetary cap.

## Documentation

[Read the searchable documentation](https://donaldmurillo.github.io/system-one-playground/).

- [Language reference](docs/sysonescript-language.md) and [parallel maps](docs/sysonescript-parallel.md)
- [Go semlint guide](docs/semlint-guide.md) and [SOS semlint guide](docs/sysonescript-semlint.md)
- [All Markdown guides](docs/README.md)

## Develop this repository locally

Everything below is for contributors and source builds, not ordinary CLI or
VS Code users.

### Explore the repository examples

| I want to… | Start here |
| --- | --- |
| Build a CLI in readable sentences | [Repository assistant walkthrough](examples/sos/repo-assistant/README.md) |
| Inspect code without API calls | `go run ./cmd/semlint -sites-only cmd/semlint/fixtures` |
| Run the linter written in SOS | `sysone run examples/sos/semlint/scan.sos -- sites --root cmd/semlint/fixtures` |
| Try the Go API demonstration | `go run ./cmd/playground 01` (can make paid requests) |
| Let an agent use the workspace | [CLI and MCP interface](docs/sysonescript-agent-interface.md) |
| Explore tool-call policy | [Gate and repository tools](docs/repository-tools.md) |

The `sites` commands only inspect source and show planned checks. Both the Go
and SOS semlint implementations are available; [SOS semlint](docs/sysonescript-semlint.md)
demonstrates bounded parallel judgments and failure handling.

### Build the CLI from source

You need **Go 1.25+**:

```sh
git clone https://github.com/DonaldMurillo/system-one-playground.git
cd system-one-playground
mkdir -p bin
go build -o bin/sos ./cmd/sos
go build -o bin/sysone ./cmd/sysone
export PATH="$PWD/bin:$PATH"
```

### Run Studio from the repository

Studio gives you a project file tree, editor hints, vocabulary search,
environment settings, and separate interpretation and runtime traces. Its
frontend requires **Node.js 18+ and pnpm**:

```sh
pnpm --dir studio install --frozen-lockfile
pnpm --dir studio build
go build -o bin/sos-studio ./cmd/sos-studio
sysone --project examples/sos/repo-assistant open studio
```

Keep `sos-studio` alongside the other two executables. For the optional native
desktop app, see the [Studio guide](docs/studio-overview.md).

### Build the VS Code extension from the repository

```sh
go generate ./internal/sosbuild
mkdir -p vscode/bin
go build -o vscode/bin/sos ./cmd/sos
pnpm --dir vscode check
pnpm --dir vscode test
pnpm --dir vscode package
code --install-extension vscode/sysonescript-vscode-0.2.0.vsix
```

Development checkouts can set `sysonescript.server.command` to an absolute path
such as `/path/to/system-one-playground/bin/sos`; the `sysone` entry point is
also supported.

### Run the repository checks

```sh
go test ./...
go vet ./...
pnpm --dir studio test
pnpm --dir studio run lint
(cd desktop && go test ./... && go vet ./...)
```

### Run the documentation site locally

The docs site requires **Go 1.27+**:

```sh
cd docs-site
python3 scripts/sync_content.py
go run .
```

Open <http://localhost:3070>. The site is built with [fastr-docs](docs-site/README.md).

SOS is a preview: local packages and native builds work; remote package resolution and a plain JavaScript emitter are not implemented. Browser/WASI capabilities depend on the imported libraries; live Jev host adapters are not available. See the [language guide](docs/sysonescript-language.md) for limits.

The Go module is `github.com/DonaldMurillo/system-one-playground`. Install the client in your own module with `go get github.com/DonaldMurillo/system-one-playground/typesafe`. See [public release readiness](docs/public-release-readiness.md).

[MIT](LICENSE). Third-party dependencies retain their own licenses. This project uses the TypeSafe System One API; it is not the provider's official SDK or documentation.

## Jev and agents

[Add Jev to your application](docs/enable-jev.md) · [For agents](docs/for-agents.md)
