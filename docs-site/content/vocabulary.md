# Library vocabulary and the language dictionary

Libraries contribute readable operations to SOS. An import without `as` opens
its vocabulary; an import with `as` requires the chosen parent name:

```sos
import "std/text"
import "std/json" as json

trim "  hello  " called clean
uppercase clean called heading
json.encode heading called payload
show payload
```

The `called` clause binds an operation's result. An alias changes the parent,
not the words supplied by the library. There is no `with parent` modifier.
Explicit `call alias.action with ... called result` remains useful for action
calls and interoperating with existing programs.

## Libraries enabled by configuration

A project can declare its imports once in `sos.toml`:

```toml
version = 1

[[language.libraries]]
path = "std/text"

[[language.libraries]]
path = "std/json"
as = "json"
```

Scripts in that project can use the same sentences without source import lines.
The optional `as` has exactly the same meaning in TOML and SOS source.
An explicit source import replaces the configured binding for that library: add
`as words` in one file to require a parent there even if project defaults open it.
Language-library settings belong in project/global TOML, not frontmatter.
Global configuration can supply defaults. A project's explicit library list
replaces those defaults; use the following to clear inherited libraries:

```toml
version = 1
[language]
libraries = []
```

Keep the project's required vocabulary in its checked-in configuration so others
can reproduce the script's behavior. Importing only loads declarations: it does
not execute library code or request Jev judgments. Local package operations still
use their definition-site imports. Third-party package paths currently refer to
local packages; this feature does not add remote downloading.

## Conflicts

Conflicting open definitions must be resolved before execution. Check and build
report the conflicting operations; Studio presents the same diagnostics. Add
`as` to require a parent for one library and distinguish its operations.
Import order never silently chooses a winning definition. Core sentence syntax
cannot be replaced by an imported library.

## Dictionary

The dictionary has two purposes: inspect what a library enables and inspect what
is enabled in the current project and source file. It is backed by the same
operation metadata used for language tooling, rather than handwritten frontend
lists. Search can match words, descriptions, library names, and sentence patterns.
Aliased entries display their qualified form. Entries include provenance so an
implicit project import can be traced back to configuration.

Dictionary inspection, ordinary completion, and conflict checking are offline;
they do not spend the Jev request budget. Running an operation may have the effects
shown by that operation. Inspecting a package never executes its actions.

See [package authoring](/docs/packages),
[configuration](/docs/configuration), and
[editor services](/docs/editor).

## Try the examples

```sh
bin/sos run examples/sos/vocabulary.sos
bin/sos run examples/sos/vocabulary-project/main.sos
```

The first demonstrates explicit imports. The second reads its library bindings
from the adjacent `sos.toml`. In Studio, choose that project folder to inspect
its configured vocabulary in the current buffer.

## Giving a local library another word

Exported action names become vocabulary words. A package can declare an
additional word for an exported action:

```sos
package cleaning
import "std/text" as text
export tidy
word polish of tidy

to tidy with value:
  call text.trim with value called clean
  return clean
```

An open import enables both `tidy value called result` and
`polish value called result`. With `import "./cleaning" as cleaning`, use
`cleaning.tidy` or `cleaning.polish`. Synonyms name one action and inherit its
argument shape; they do not install arbitrary parser code. Multiple arguments
use `with first, second`, matching the existing action-call convention.

## CLI and editor API

```sh
bin/sos vocabulary examples/sos/vocabulary.sos
bin/sos vocabulary examples/sos/vocabulary.sos --query whitespace
bin/sos vocabulary --library std/json --json
```

Without a filename, the command inspects the current working folder. JSON mode
returns a `sos/vocabulary@1` envelope with `catalog.entries`,
`catalog.libraries`, `catalog.definitions`, `catalog.failures`, and
`diagnostics`. Definitions and failures include the local declarations and
exported types visible through the file's resolved imports, sorted by name. A dictionary query can return useful
metadata alongside diagnostics; use `sos check FILE` to enforce validity.

Go tools can call `sos.Vocabulary(filename, source)` directly. Language-server
clients can send `sos/vocabulary` with an open `textDocument`, or an inline
`source`, plus optional `query` and `library` filters. The catalog lists enabled
operations and available library previews; inspect each entry's `enabled` and
`patterns` instead of guessing availability from its name.

In Studio, open the **Vocabulary** tab. **Enabled here** shows the current
file's imports and configured libraries; **Library explorer** previews available
libraries. Search matches descriptions as well as operation names and synonyms.
Right-click an import and select **Open Vocabulary for Library at Cursor** to
inspect that library. Import hovers point to this action.

The formatter trims whitespace and preserves incomplete sentence text. It rejects
structural errors such as invalid indentation; use Check for semantic validity.
