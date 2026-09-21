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
go generate ./internal/sosbuild
mkdir -p vscode/bin
go build -o vscode/bin/sos ./cmd/sos
pnpm --dir vscode package
code --install-extension vscode/sysonescript-vscode-0.4.0.vsix
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

Use **SysOneScript: Set Jev Token** to store `TYPESAFE_API_KEY` in VS Code's
encrypted SecretStorage. The value is injected into the language server,
runner, and debugger process environments and is never returned through the
extension output or Variables view. Existing process and project `.env`
configuration remains valid. The project panel's **Set Jev token** row performs
the same setup directly and displays whether the credential comes from VS Code
or the environment. **Clear Jev Token** appears in the panel for a VS
Code-managed token and removes only the extension's stored secret. The view
title bar contains Refresh only; Run and Jev are not duplicated there.

Registered external modules appear in a separate project-control group. Each
module offers **Open definition**, an offline **Check definition**, and an
explicit **Diagnose runtime** action. The same actions are in the Command Palette.
Opening, editing, completion, and ordinary project refreshes never launch a
plugin. External calls appear as opaque debugger frames and write only redacted
module/action status to traces and the run Output channel.

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

### Marketplace publishing

The extension release workflow is [`.github/workflows/vscode-release.yml`](https://github.com/DonaldMurillo/system-one-playground/blob/main/.github/workflows/vscode-release.yml).
Create a VS Code Marketplace publisher whose identifier matches the extension
manifest (`donaldmurillo`). Automatic publishing uses an Azure DevOps Personal
Access Token with Marketplace **Manage** scope saved as `VSCE_PAT` on a
protected GitHub environment named `marketplace`; without it, CI produces the
same platform VSIX files for manual Marketplace upload. Then
bump `vscode/package.json` and `vscode/CHANGELOG.md` together. Pushing a tag
like `vscode-v0.4.0` runs the checks, builds the platform bundles, waits for
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

See [library vocabulary and the searchable dictionary](sysonescript-vocabulary.md)
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
