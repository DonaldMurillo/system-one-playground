# SysOneScript editor language services

Studio uses SOS's own Go syntax and analysis code. Tree-sitter is not a dependency.
The interpreter remains the authority for executable syntax; the editor additionally
retains comments, incomplete strings, source spans, and indented blocks.

## VS Code

The repository ships a standalone extension in [`vscode/`](https://github.com/DonaldMurillo/system-one-playground/tree/main/vscode) for `.sos`
files. It starts the same stdio language server as Studio, so diagnostics,
completion, hover, definition lookup, formatting, semantic tokens, inlay hints,
code actions, folding and CodeLens use the shared Go implementation. The
extension also owns project execution and debugging through the same native
runtime used by the CLI.

Build and install it from the repository root:

```sh
./scripts/sync-local-dev.sh
```

That command builds and smoke-tests the current checkout before updating the
standalone `sos` and `sysone` commands and the locally installed extension as
one unit. Reload VS Code windows that were already open afterward. To package
only the extension manually:

```sh
go generate ./internal/sosbuild
mkdir -p vscode/bin
go build -o vscode/bin/sos ./cmd/sos
pnpm --dir vscode package
code --install-extension vscode/sysonescript-vscode-0.5.1.vsix
```

Marketplace releases bundle a platform-matched `sos` language-server binary,
so users do not need a separate CLI installation. Development checkouts without
that bundle can use `sos` on PATH or configure an absolute executable path:

```json
{
  "sysonescript.server.command": "/absolute/path/to/bin/sos",
  "sysonescript.server.cwd": "${workspaceFolder}"
}
```

The extension also accepts `sysonescript.server.args` and
`sysonescript.server.env`; use `args: ["lsp"]` for `sysone` or `sos`, and pass
credentials through the environment only when explicitly invoking analysis or
other provider-backed behavior. Typing assistance itself is offline. The
`SysOneScript: Analyze Document` command is explicit and maps to the custom
`sos/analyze` request; the server never makes a model call just because a file
is open.

### Project panel, execution, and debugging

The SysOneScript Activity Bar panel is a project-control dashboard, not a
second file browser. It resolves the project entrypoint deterministically from
the workspace setting, the active `.sos` editor, `main.sos`/`index.sos`, or the first runnable script, and
exposes **Run project**, **Check project**, **Build project**, **Debug
project**, **Stop processes**, helper/generator actions, and Jev status. The
Run, Check, and Build commands execute immediately; the panel keeps concise
status while VS Code automatically opens the dedicated **SysOneScript Run**
Output channel with the command, complete output, errors, and exit result. The
panel's **Show run output** action reopens it. **Build project** writes to
`bin/ENTRY_NAME` by default. The
editor and Explorer context menus provide file-level **Run File**, **Explain
File**, and **Debug File** commands. When a script declares command inputs,
Debug File asks for the command and arguments; launch configurations can
provide `args` directly.
The extension contributes the `.sos` mark as a language-default icon, so
compatible file-icon packs can display it without replacing the user's active
pack. A pack's own `.sos` mapping wins, and packs that set
`showLanguageModeIcons` to `false` suppress language-default icons. The
standalone **SysOneScript Icons** theme remains available from VS Code's File
Icon Theme picker as a fallback.

VS Code presents a native **Get Started with SysOneScript** walkthrough after
installation. It covers the project panel, visible run output, debugging, Jev
setup, and the optional standalone CLI. Reopen it from **Welcome & setup** in
the project panel or **SysOneScript: Open Welcome** in the Command Palette.
Primary project actions remain available in both surfaces. The CLI download
action is explicit and never modifies the user's PATH automatically.
The panel also compares its bundled runtime version with `sysone` on `PATH`.
When they differ, VS Code reports both versions and offers to run the CLI's
explicit, checksum-verified updater in a visible terminal.

The native `sos debug` command speaks the Debug Adapter Protocol. It pauses at
executable source statements and supports source/conditional/hit-count
breakpoints, logpoints, continue/pause/stop/restart controls, stepping,
call-stack frames, locals, read-only expression evaluation, and runtime/trace
output. The runtime supplies source locations and snapshots; the extension is
not simulating a debugger from terminal text.
Stopping a debug session intentionally cancels its run and reports a clean
stopped session, without a spurious `context canceled` application error.

Use **SysOneScript: Set Jev Token** to store `TYPESAFE_API_KEY` in VS Code's
encrypted SecretStorage. The value is injected into the language server,
runner, and debugger process environments and is never returned through the
extension output or Variables view. Existing process and project `.env`
configuration remains valid. The project panel's **Set Jev token** row performs
the same setup directly and displays whether the credential comes from VS Code
or the environment. **Clear Jev Token** appears in the panel for a VS
Code-managed token and removes only the extension's stored secret. The Project
heading stays clear; **Refresh project** is a row below the active project
status, alongside the other actions.

Registered external modules appear in a separate project-control group. Each
module offers **Open definition**, an offline **Check definition**, and an
explicit **Diagnose runtime** action. The same actions are in the Command Palette.
Opening, editing, completion, and ordinary project refreshes never launch a
plugin. External calls appear as opaque debugger frames and write only redacted
module/action status to traces and the run Output channel.

Streaming actions receive completion, hover, diagnostics, and semantic
highlighting. The checker distinguishes `from` stream loops from `in`
collection loops and diagnoses copied, abandoned, or reused handles. Hover
shows the item type and declared opening/terminal failures.
Debugger Variables may show stream state, producer, received/buffered item
counts, and available credit, but expanding that value never requests the next
item. Studio's **Streams** tab and VS Code's **Streams** view update live from
the same non-consuming runtime snapshots. Each stream exposes **Stop stream**,
which gracefully ends that stream and continues the program below its loop.
The lifecycle view/output records open, reading, completion, stop, cancellation,
and failure events. In VS Code, finished runs leave the live **Streams** view
automatically; their lifecycle history remains in the **SysOneScript Streams**
Output channel. **Refresh streams** and **Show lifecycle log** are rows directly
below the Streams heading, with **Clear finished streams** available when a
running session contains terminal streams. Clearing only dismisses those rows
for that run; it does not stop live producers. Use **Stop processes** to cancel
the whole run and all of its producers.

Project helpers and generators are configured in `.vscode/sysonescript.json`:

```json
{
  "helpers": [
    { "name": "Build CLI", "command": "build", "args": ["main.sos", "--output", "bin/main"] },
    { "name": "Generate fixtures", "command": "my-sos-generator", "args": ["--project", "."] }
  ]
}
```

Built-in SysOneScript commands use the bundled runner; other helper commands
are launched from `PATH` with the configured project working directory.

### Control channel failure policy

VS Code observes live runs over an editor-owned loopback TCP control channel;
`sos run` attaches to it with `--stream-control` and `--stream-session` before
the file argument, and the `sos debug` adapter connects the same way. The
channel is inspection-only — lifecycle events, snapshots, and stream stop
requests — and program execution never depends on it. Its failure rules are
explicit:

- **Loopback only.** The editor binds the control server to `127.0.0.1`, and
  the runtime client refuses to connect to any non-loopback control address,
  so inspection traffic never leaves the machine.
- **Environment-only authentication.** The control token reaches the child
  runtime only through the `SOS_STREAM_TOKEN` environment variable. It is
  never accepted as a command-line flag and never travels in DAP launch
  arguments — both are logged by tooling, and both leakage paths are removed.
  `sos run` requires the variable whenever `--stream-control` is present.
- **Inspection degrades; the run survives.** In the debug adapter a failed or
  rejected control connection only disables inspection — the session reports
  "Stream inspector unavailable" and the program continues; an authentication
  failure never kills a run. A direct `sos run --stream-control` invocation
  that cannot authenticate reports the error on stderr and exits before the
  script starts.
- **Dropping beats blocking.** Lifecycle events flow through a bounded queue
  (2048 events). Under an extreme burst the runtime drops events instead of
  blocking execution, counts the drops, and reports the total in a final
  `bye` frame. The CLI prints the dropped count on stderr, and VS Code
  reports it in the SysOneScript output channel when a session ends; both
  note that snapshots remain authoritative. Editors refresh stream identity,
  counters, credit, and terminal state from runtime snapshots, so a dropped
  event never corrupts the view.
- **Bounded framing and connections.** Control frames are newline-delimited
  JSON capped at 8 MiB. An oversized frame is rejected observably: the socket
  is closed and the failure appears in the SysOneScript output channel. The
  editor's control server accepts at most 16 concurrent connections, counted
  at TCP accept whether or not they have authenticated; excess connections
  are refused immediately and surfaced the same way.
- **Stops name one run.** Studio's Stop stream request must carry the current
  `runId`. A request without one is rejected, and a request naming a previous
  run is reported as stale instead of stopping a live stream.

### Marketplace publishing

The extension release workflow is [`.github/workflows/vscode-release.yml`](https://github.com/DonaldMurillo/system-one-playground/blob/main/.github/workflows/vscode-release.yml).
Create a VS Code Marketplace publisher whose identifier matches the extension
manifest (`donaldmurillo`). Automatic publishing uses an Azure DevOps Personal
Access Token with Marketplace **Manage** scope saved as `VSCE_PAT` on a
protected GitHub environment named `marketplace`; without it, CI produces the
same platform VSIX files for manual Marketplace upload. Then
bump `vscode/package.json` and `vscode/CHANGELOG.md` together. Pushing a tag
like `vscode-v0.5.1` runs the checks, builds the platform bundles, waits for
approval, and publishes the matching version. The first publisher, token, and
GitHub environment setup are account-level actions; they cannot be completed
from the repository alone.

The Marketplace receives the `.vsix` extension packages, each with its matching
`sos` language server. The same approved release also attaches standalone
`sos`/`sysone` archives for macOS, Linux, and Windows to the GitHub Release.

## Colors, hints, and navigation

Immediate lexical colors remain available while a language-server request is in
flight. Semantic colors distinguish operations, bindings, parameters, types,
namespaces, operators (including `plus`, `times`, `+`, and `*`), strings,
numbers, and comments. Interpolated expressions receive their own variable,
number, and operator tokens instead of inheriting the surrounding string color.
The extension's operator token inherits keyword styling so themes do not render
language operators as ordinary variables.
Hover describes known constructions
and their effects. Inlay hints annotate values/types only when local analysis can
justify them, including confidently typed variables inside interpolations; they
do not predict provider answers. Colon-led blocks can be folded.

Named record definitions participate in the same offline editor model: type
names after `as` complete and navigate to `define`, hover shows the ordered
required/optional field shape, and definition/field tokens receive semantic
highlighting. Known fields in `field of value` and dotted access are checked by
the core; dynamic records continue to defer unknown-field errors to runtime.

LSP positions use UTF-16 columns, including for emoji. Comments and quoted strings
are scanned before keywords. The tolerant syntax engine (`internal/sossyntax`)
reuses tokenized unchanged prefix/suffix lines across document snapshots and rebuilds
indentation block spans. Stdio document sessions retain these snapshots; the HTTP
bridge currently starts a fresh snapshot for each request. This is incremental line
parsing, not incremental type checking.

## Imports

Type a library word at the start of a sentence, or a qualified action name after
`call`, and use completion (Ctrl+Space), or
invoke Quick Fix on an unresolved qualified call. An import suggestion carries
an additional text edit, so accepting it inserts both the action and its import.
Existing imports are reused. Local package suggestions are confined to the Studio
session's working folder; standard library suggestions also work without a project.

An import edit does not download a dependency or execute package code. External
package acquisition is not part of this release. Studio currently edits one buffer;
cross-file workspace editing/navigation is not yet a project explorer.

## Offline behavior

Completion, highlighting, hints, folding, and import fixes make no model calls.
Explicit Jev analysis retains its separate policy and budget controls. Studio
ignores language-service responses for an older buffer version, preventing stale
completion edits from changing new text.

The shared stdio LSP and authenticated HTTP bridge expose completion, hover,
definition, formatting, semantic tokens, inlay hints, code actions, and folding.
The HTTP bridge supplies workspace context from the server session; browser input
cannot choose an arbitrary filesystem root.

## Development checks

- `go test ./internal/sossyntax ./internal/soslsp ./internal/studio`
- `pnpm --dir studio test`
- `pnpm --dir studio lint`
- `pnpm --dir studio build`
- `pnpm --dir vscode check`
- `pnpm --dir vscode test`
- `pnpm --dir vscode package`
- `pnpm --dir vscode package:smoke`

Rebuild `bin/sos-studio` after bundling the frontend. Rebuild the Wails application
from `desktop/` with `go tool wails build` to update the installed build artifact.

See [library vocabulary and the searchable dictionary](/docs/vocabulary)
for open imports, parent aliases, and project-wide library configuration.

### Combined hover context

Hovering a token shows **Selected** (its role and available declaration or
library information), followed by **Sentence** (the containing operation and
its effects) in one panel. The highlighted range covers only the selected
token, using UTF-16 positions shared with Monaco. Whitespace retains sentence
context. Literal and comment text is never interpreted as a variable reference.
Declaration lookup considers preceding declarations in enclosing indentation
scopes; the panel does not invent a type when one is unavailable.

### Jev showcase and usage

Studio starts with `jev-workflow`: choose **Analyze** to inspect Jev's constrained
interpretation of `filter`, then **Run** to apply the declared `urgent` criterion
to the sample tickets. `jev-primitives` demonstrates explicit judgment,
classification, and scoring. These examples require the existing
`TYPESAFE_API_KEY` environment variable or a `.env` in the working directory.
Opening an example never invokes the provider.

Interpretation and Trace panels show per-operation request and reported input-token
usage. A **Jev 1.13 rate estimate** uses $0.042 per million input tokens (output
is free), verified at https://docs.typesafe.ai/models on 2026-09-19. This is a
reference-rate estimate, not billed cost: custom providers/models or future prices
may differ. Unreported requests remain unknown and are excluded from that estimate.
The documented API exposes tokens, not an account-wide billing endpoint. These
snapshots are not a persistent session/project spending ledger; Analyze and Run
have separate snapshots, and cached analysis does not represent new spending.

### Semantic diagnostics and decision evidence

The extension runs project checks with `sos check FILE --editor`. This remains
offline and preserves canonical errors, while reporting valid semantic phrases
as pending analysis instead of failing the project check. Plain `sos check FILE`
remains strict for CI and rejects source that has not been canonicalized or
supplied with an explicit resolution.

Run Project and Run File use the nearest `sos.toml` as the project boundary.
For command entrypoints, the extension prompts for script arguments and appends
them after `--`, preserving runner flags and the project's working directory.

The local editor checker marks registered semantic sentences as informational
"awaiting interpretation" notices when the effective policy allows them.
Unknown syntax and malformed criterion declarations remain errors. Analyze/Run
performs full resolution and validation; no provider calls happen while typing.
Successful analysis clears pending notices. Older in-flight checks cannot
replace the newer analysis/run diagnostics.

Successful runs summarize judgment counts in Output, with buttons opening Trace
and Interpretation. Predicate traces include the evaluated item, keep/discard/stop
decision, probability threshold and uncertainty-policy explanation. This is the
program's decision rule, not a model-generated rationale. Recorded traces also
contain these items; treat recordings as potentially sensitive script data.

### Language roles and CodeLens

Amber identifies language keywords, teal identifies criterion names, and purple
identifies registered semantic phrases. Hover text names the role explicitly.
`criterion urgent` declares a rule; `keep urgent tickets` has fixed syntax and
uses Jev at runtime. `filter` is a semantic phrase whose interpretation may
require Jev before execution. Other registered variants can resolve locally.

CodeLens labels above these sentences explain the distinction and invoke
Analyze when clicked. Pending semantic interpretation does not create a
squiggle. Actual malformed/unsupported syntax remains a diagnostic. CodeLens
is also exposed via standard `textDocument/codeLens`; clients can bind the
`sos.analyze` command to their own explicit analysis flow.

Changing examples or opening a file resets results and input values for the
previous document. Diagnostics and vocabulary refresh for the new source;
Output, Trace and Interpretation wait for a new Run or Analyze. Late results
from a previous document are discarded from the UI.

### Extension lifecycle and release boundary

VS Code serializes language-server restarts and rejects provider results from an
older server generation, even when the document text did not change. Extension
shutdown marks the state disposed before stopping the server, run processes, and
debug sessions so late analysis callbacks cannot repopulate editor state.

Debug stop requests also cover a launch that is still starting. A user-stopped
session is reported as stopped rather than successful. Secret-named container
evaluations are redacted and receive no expandable variables reference.

The packaged-extension smoke test selects the VSIX matching the current manifest
version and verifies the runtime plus every required JavaScript module. Release
verification regenerates the semantic lexicon, embedded compiler sources, and
public documentation before accepting a tag.

Release VSIX files are timestamp-normalized and packaged twice; byte differences
fail the release. The smoke check validates the target GOOS/GOARCH, Unix execute
permission, and a linker-injected runtime version marker. Symlinked entry files
use their target project's configuration consistently for modules, diagnostics,
and explicit semantic analysis.

Document synchronization also routes through the restart gate after a failed
language-server initialization instead of dereferencing a missing readiness
promise. Explain and canonicalize resolve symlink targets before loading project
policy. GitHub Release publication verifies the remote tag still identifies the
tested commit. Repository release immutability is enabled; CI creates a draft,
uploads every asset, rechecks the tag, publishes atomically, and verifies the
resulting release attestation. Marketplace retries skip only platform versions
that are already published.

An active, no-bypass repository ruleset prevents updates or deletion of
`vscode-v*` tags while still allowing new release tags to be created. CI verifies
that publicly readable protection and the exact tag commit before publication,
then polls boundedly for GitHub's asynchronously generated immutable-release
attestation. The workflow does not query the repository-admin-only immutable
release setting because GitHub does not expose that endpoint to `GITHUB_TOKEN`.
