# Project workspaces

Studio 0.5 has two project-local experiences, selectable in the toolbar:

- **Examples** loads bundled single-file demonstrations.
- **Project workspace** opens the current directory as a file tree, with an active
  editor buffer and the existing Output, Trace, Vocabulary and Interpretation panels.

The selection is saved in `.sysone-studio.json`. Open a directory using **Open
folder** (a native picker on desktop), or launch it with
`sysone --project PATH open studio`. Direct desktop launch accepts the directory
as its first argument. One window has one active project root.

The tree supports nested folders and text files, new files/folders, and refresh.
Unsaved buffers are retained when navigating within the project and indicated in
the explorer status. Save writes the selected file to disk. A SHA-256 revision
check refuses to overwrite a file changed by another editor or agent; preserve
unsaved text, reopen the project, reconcile and save again. Hidden files,
symlinks, and paths outside the root are excluded from the editor API. The tree
is bounded to 4,000 entries and text files to 1 MiB.

Run and Analyze use the selected `.sos` file's path for imports. Relative runtime
input/output paths use the project root. Imported files are loaded from disk;
save edited dependencies before running. **Build CLI** compiles the saved entry
point and its module graph to a native executable in the project. Save all edited
files first. Existing output files are not overwritten.

## Environment

Settings contains named environment values, including `TYPESAFE_API_KEY` and
`TYPESAFE_DEFAULT_MODEL`. Existing values are never returned to the UI or listed
by CLI/MCP. New values are saved in the project's `.env`, with owner-only
permissions, and used by the next Run/Analyze/Build. The form replaces the stored
value; an empty stored value disables process fallback for that name. No key is
included in the compiled source graph. Add `.env` to your own version-control
ignore rules. Updating settings rewrites supported NAME=value entries in stable
order, preserving values but not comments. Unsupported .env syntax is rejected.

These settings configure the provider environment; SOS does not currently expose
arbitrary OS environment reads as a language primitive. See
[environment precedence](sysonescript-project-environment.md).

## Agent and CLI workflows

[The sysone interface](sysonescript-agent-interface.md) exposes the same project
file/settings and execution services through a command line and stdio MCP.
Agents edit disk files; they do not control an existing window's unsaved buffers.
Refresh the tree to see agent-created files. Build and distribute `sysone`, `sos`,
and `sos-studio` together, or put all three on PATH.

Try the [repository assistant](../examples/sos/repo-assistant/README.md): a
multi-command, multi-file developer issue intake/triage/report pipeline. It works
on local JSON exports; it does not yet call git or GitHub. Offline triage and
native build acceptance tests run without credentials; blocking-issue judgments
use the configured real Jev provider.
