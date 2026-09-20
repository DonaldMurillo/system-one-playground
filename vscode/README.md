# SysOneScript for VS Code

This extension adds `.sos` language support to VS Code and connects it to the
repository's stdio SysOneScript language server. It has no runtime npm
dependencies: the extension uses VS Code's API and Node's built-in process and
stream modules.

## Build and install

From the repository root, build the language executable and package the
extension:

```sh
go generate ./internal/sosbuild
mkdir -p vscode/bin
go build -o vscode/bin/sos ./cmd/sos
pnpm --dir vscode check
pnpm --dir vscode test
pnpm --dir vscode package
code --install-extension vscode/sysonescript-vscode-0.1.0.vsix
```

Marketplace releases include a platform-matched `sos` language-server binary,
so a normal installation does not need a separate SysOneScript CLI install.
Development checkouts without a bundled binary can use `sos` on PATH or set an
absolute executable path in VS Code settings:

```json
{
  "sysonescript.server.command": "/absolute/path/to/system-one-playground/bin/sos",
  "sysonescript.server.cwd": "${workspaceFolder}"
}
```

Using the `sysone` entry point directly is also supported:

```json
{
  "sysonescript.server.command": "/absolute/path/to/system-one-playground/bin/sysone",
  "sysonescript.server.args": ["lsp"]
}
```

## Features

- diagnostics and full-document synchronization
- completion with import edits and quick fixes
- hover, local definition lookup and formatting
- semantic highlighting, folding and inlay hints
- CodeLens for explicit semantic analysis
- `SysOneScript: Analyze Document` and `SysOneScript: Restart Language Server`

The extension forwards only explicit `sos/analyze` requests to semantic
analysis. Ordinary typing assistance is local and offline. Use
`sysonescript.trace.server` when diagnosing executable or protocol issues; the
output is available in the **SysOneScript** channel.

## Publish to the Marketplace

The repository has a release workflow at
`.github/workflows/vscode-release.yml`. Before the first release:

1. Create a publisher in the [VS Code Marketplace publisher management page](https://marketplace.visualstudio.com/manage/publishers/).
2. Make sure its identifier matches the `publisher` field in `package.json`.
   This extension uses `donaldmurillo`, producing the Marketplace ID
   `donaldmurillo.sysonescript-vscode`.
3. Configure a trusted publishing policy for this GitHub repository and
   workflow, allowing OIDC publishing for that publisher.
4. Create a GitHub environment named `marketplace` and add required reviewers
   if you want a human approval before the publish job runs.

For each release, update `version` and `CHANGELOG.md`, commit the changes, and
push a tag such as `vscode-v0.1.0`:

```sh
git tag vscode-v0.1.0
git push origin vscode-v0.1.0
```

The workflow checks and tests the extension, builds platform-matched `sos`
servers for macOS, Linux and Windows, verifies that the tag matches the
manifest version, then pauses at the protected `marketplace` environment.
After approval, it publishes each VSIX target to the Marketplace with
`vsce --oidc` and attaches standalone CLI archives to the matching GitHub
Release. Each CLI archive contains both `sos` and `sysone`.

The Marketplace is for the installable VS Code extension. GitHub Releases are
the download location for the standalone CLI archives; package managers such
as Homebrew or Scoop can be added later as separate distribution layers.
