# SysOneScript for VS Code

This extension adds `.sos` language support to VS Code and connects it to the
repository's stdio SysOneScript language server and native runtime. It has no
runtime npm dependencies: the extension uses VS Code's API and Node's built-in
process and stream modules.

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
code --install-extension vscode/sysonescript-vscode-0.5.1.vsix
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
- a SysOneScript Activity Bar project-control dashboard (not a second Explorer)
- folder-level Run, Check, Build, Debug, and Stop actions
- editor and Explorer actions for running, explaining, and debugging `.sos` files
- a native Debug Adapter Protocol debugger with breakpoints, stepping, locals,
  stack frames, expression evaluation, conditional breakpoints, logpoints, and
  Jev trace output
- secure Jev token setup through VS Code SecretStorage (`TYPESAFE_API_KEY`)
- external-module project controls for opening, offline checking, and explicit diagnostic handshakes
- stream syntax, ownership diagnostics, producer/action hover, and non-consuming
  debugger inspection of state, item counts, buffering, and credit
- additive `.sos` language icon, fallback file-icon theme, and extension icon
- a matching monochrome S/1 Activity Bar glyph designed for VS Code chrome
- terminal CLI version detection, out-of-sync warnings, and an explicit update action
- project-defined helper actions in `.vscode/sysonescript.json`
- a native Getting Started walkthrough, reopenable from the project panel or
  **SysOneScript: Open Welcome** in the Command Palette
- `SysOneScript: Analyze Document` and `SysOneScript: Restart Language Server`
- `SysOneScript: Make File Canonical`, which resolves semantic syntax through
  the bundled CLI and applies the deterministic result as one undoable edit

The panel is a project-control dashboard: it shows the active project and the
automatically resolved entrypoint (configured entry, active `.sos` file, then
project discovery), then exposes folder-level Run, Check, Build,
Debug, Stop, helper, generator, and Jev controls. Run, Check, and Build execute
directly, keep concise status in the sidebar, and automatically open the
dedicated **SysOneScript Run** Output channel with complete output and errors;
they do not open an entrypoint picker or terminal. When the entrypoint declares
a command, Run asks for that command's arguments and passes them across the
script `--` boundary. In workspaces containing multiple nested projects, the
panel follows the active `.sos` file to its nearest `sos.toml`. It does not
mirror the file system. File Run
actions operate on the selected `.sos` file while retaining the containing
project's `sos.toml`, working directory, and `.env`.
Long-running runs remain cancellable from **Stop processes**, which also closes
their owned streams. Inspecting a stream in Variables never advances its
producer. The **Streams** view shows live runtime snapshots and offers an
individual **Stop stream** action; **SysOneScript Streams** records lifecycle
events without logging program-visible stream items. The view places **Refresh
streams** and **Show lifecycle log** below its title. Finished run sessions
disappear from the live view automatically; while a run continues, **Clear
finished streams** dismisses terminal rows without stopping live producers.
The `.sos` mark is contributed as a language-default icon, allowing compatible
file-icon packs to display it without replacing the active pack. A pack's own
`.sos` mapping takes precedence, and packs can disable language-mode icons. The
standalone **SysOneScript Icons** theme remains available from VS Code's File
Icon Theme picker as a fallback.

### Jev credentials

Use **SysOneScript: Set Jev Token** or the panel's key button. The token is
stored in VS Code's encrypted SecretStorage and injected into the language
server, runner, and debugger only when they start. It is never written to
workspace settings or shown in output. The runtime-facing environment name is
`TYPESAFE_API_KEY`; existing process and project `.env` configuration remains
supported. The panel row shows the credential source and performs setup
directly; **Clear Jev Token** appears there when VS Code owns the stored secret.
**Refresh project** is a row below the active project status, leaving the
Project heading unobstructed.
The panel also links to the optional standalone CLI downloads; the extension
does not silently modify the user's PATH.
It compares the bundled runtime with `sysone` on `PATH`, shows both versions in
the panel, and warns once per session when they differ. **Update standalone
CLI** opens a visible terminal running `sysone update`; extension upgrades
continue to use VS Code's Marketplace updater.

### Helpers and generators

Add project-specific actions to `.vscode/sysonescript.json`:

```json
{
  "helpers": [
    {
      "name": "Build CLI",
      "command": "build",
      "args": ["main.sos", "--output", "bin/main"]
    },
    {
      "name": "Generate fixtures",
      "command": "my-sos-generator",
      "args": ["--project", "."]
    }
  ]
}
```

Known SysOneScript commands (`run`, `check`, `build`, `fmt`, `explain`,
`config`, and `vocabulary`) use the bundled runner. Other commands are resolved
as executables on `PATH` or through the configured runner environment.

### Debugging

The extension's `sysonescript` debugger launches `sos debug`, a native DAP
server backed by runtime statement hooks. Use the normal Run and Debug view or
the **Debug File** command. A launch configuration can provide script arguments:

```json
{
  "type": "sysonescript",
  "request": "launch",
  "name": "Debug SysOneScript",
  "program": "${file}",
  "cwd": "${workspaceFolder}",
  "args": []
}
```

The debugger exposes read-only language expressions. Credential-shaped names
are redacted from the Variables view, and provider traces are written to the
Debug Console without token values.

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
3. For automatic Marketplace publishing, create an Azure DevOps Personal
   Access Token with Marketplace **Manage** scope and add it as the `VSCE_PAT`
   secret on the protected GitHub `marketplace` environment. Without that
   secret, CI still produces platform VSIX artifacts for manual upload.
4. Create the GitHub environment named `marketplace` and add required
   reviewers. The secret is not available until that approval is granted.

For each release, update `version` and `CHANGELOG.md`, commit the changes, and
regenerate the standalone compiler bundle with
`go generate ./sos ./internal/sosbuild`. Commit any generated changes before
tagging. Check that regeneration leaves no diff and that
`node vscode/test/release-contract.js` passes, then push a tag such as
`vscode-v0.5.1`:

```sh
git tag vscode-v0.5.1
git push origin vscode-v0.5.1
```

The workflow checks and tests the extension, builds platform-matched `sos`
servers for macOS, Linux and Windows, verifies that the tag matches the
manifest version, then pauses at the protected `marketplace` environment.
After approval, it creates the GitHub Release with standalone CLI archives and
checksums. When `VSCE_PAT` exists it also publishes every VSIX target; otherwise
the same VSIX files remain available as workflow artifacts for manual
Marketplace upload. Each CLI archive contains both `sos` and `sysone`.

The Marketplace is for the installable VS Code extension. GitHub Releases are
the download location for the standalone CLI archives; package managers such
as Homebrew or Scoop can be added later as separate distribution layers.
