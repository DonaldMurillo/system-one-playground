# SysOneScript editor language services

Studio uses SOS's own Go syntax and analysis code. Tree-sitter is not a dependency.
The interpreter remains the authority for executable syntax; the editor additionally
retains comments, incomplete strings, source spans, and indented blocks.

## Colors, hints, and navigation

Immediate lexical colors remain available while a language-server request is in
flight. Semantic colors distinguish operations, bindings, parameters, types,
namespaces, strings, numbers, and comments. Hover describes known constructions
and their effects. Inlay hints annotate values/types only when local analysis can
justify them; they do not predict provider answers. Colon-led blocks can be folded.

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
