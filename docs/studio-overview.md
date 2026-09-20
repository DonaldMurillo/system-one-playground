# SysOneScript Studio

Studio is the repository's editor and project workbench. It combines Monaco with the shared Go language and project services, available as a browser server or Wails desktop application.

## Launch

```sh
go build -o bin/sos ./cmd/sos
go build -o bin/sysone ./cmd/sysone
go build -o bin/sos-studio ./cmd/sos-studio
bin/sysone --project examples/sos/repo-assistant open studio
```

The browser launcher stays in the foreground. Use `open examples` to select the examples experience instead. For a native shell, `make sos-desktop` uses Wails and its platform build prerequisites.

## Open a project

Switching to Project workspace shows a clean welcome screen. Select **Open a
project…** to choose a folder. Desktop opens the native folder dialog; the
browser workbench provides a folder-browsing dialog for the machine running
Studio. Cancel keeps the welcome screen unchanged. The editor and result panels
appear only after a folder is selected. Empty folders are supported.

Returning to Project workspace from Examples asks for a folder again rather
than opening the launch directory or an example automatically. Unsaved project
buffers are retained across mode switches; choosing another project asks before
discarding them. Run or Analyze must finish or stop before switching.

## Choose your workflow

- [Projects and files](/docs/projects): explorer, unsaved buffers, conflict-aware saves and native builds.
- [Environment settings](/docs/project-environment): configure the Jev key without displaying stored values.
- [Editor services](/docs/editor): element and sentence hints, semantic colors, completion and auto-import.
- [Vocabulary](/docs/vocabulary): search what a library enables and what is active in the project.
- [Interpretation](/docs/interpretation): inspect a sentence's resolved operation separately from runtime judgments.
- [CLI and MCP](/docs/agent-interface): use shared project services from an agent or shell.

Settings stores project environment values in a local `.env` with restrictive permissions. This is plaintext configuration, not an OS keychain. File APIs restrict paths, but running scripts is trusted code execution rather than a sandbox.

## Understand the panels

Output shows emitted program text and run failures. Diagnostics reports checker findings. Interpretation explains sentence resolution. Trace reports runtime judgment results and available usage. Vocabulary explains enabled words and imports.

A successful run and a recognized sentence answer different questions. Inspect the judgment result and selected records to understand what happened. CLI/MCP services operate on disk or explicitly supplied source; they do not remotely control another window's unsaved buffers.

## Desktop icon and local build

Studio uses the System One terminal icon in its app bundle and macOS About
window. Its editable SVG and macOS regeneration command live in
`desktop/assets/README.md`; the checked-in PNG lets other platforms build
without Swift. After `make sos-desktop`, open the generated application under
`desktop/build/bin/`. On macOS, pass a project folder explicitly:

```sh
open -n desktop/build/bin/sos-studio.app --args "$PWD"
```

`-n` opens a new instance, preserving any existing window's unsaved work.
