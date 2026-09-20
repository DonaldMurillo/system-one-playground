# Local packages and the standard library

SOS supports local packages and standard-library operations. A module
is identified by a logical path in project `sos.toml`; packages are directories
of `.sos` source files. There is no package download service or remote dependency
resolver.

```toml
version = 1

[module]
path = "example.com/support-tools"
```

For a `reporting/` directory, import `example.com/support-tools/reporting`.
A source file can name its package, declare imports, and export actions:

```sos
package reporting
import "std/text" as text
export summarize

to summarize with title:
  call text.trim with title called clean
  return clean
```

The entry script can call it:

```sos
import "example.com/support-tools/reporting" as reports
call reports.summarize with "  Support queue  " called title
show title
```

Package files share action declarations; imports belong to their defining file.
Private helper actions stay private to the package. Importing is declaration-only:
package-level executable statements are rejected. Package actions receive explicit
arguments and cannot capture their caller's variables. Their operations use the
calling run's configuration and budget; library frontmatter cannot raise limits.

The loader bounds graph depth and source size and rejects cycles. Native and WASM
artifacts carry the resolved source graph so local source files are not needed on
the destination machine. Existing single-file scripts remain supported.

## Import vocabulary

In SOS 0.4, imports without `as` expose bare sentence words and preserve the
default qualified form. An explicit `as` requires the alias prefix. Exported
actions contribute their names; packages may add synonyms with `word alias of action`.
See [vocabulary](sysonescript-vocabulary.md) for configured imports and discovery.

## Standard library

| Import | Action | Input | Result |
|---|---|---|---|
| `std/text` | `trim` | text | text without surrounding Unicode whitespace |
| `std/text` | `upper` | text | uppercase text |
| `std/text` | `lower` | text | lowercase text |
| `std/json` | `encode` | any supported value | JSON text |
| `std/json` | `decode` | JSON text | decoded value; invalid JSON is an error |

```sos
import "std/text"
call text.upper with "hello" called greeting
show greeting
```

These operations are pure: no filesystem, network, or model requests. Argument
counts and types are checked. They work with native, browser WASM, and WASI builds.
Broader filesystem, HTTP, process, time, and collection package APIs remain future
work; existing canonical sentences retain their behavior.

## Go API

`LoadProgram(filename, source)` resolves a program and its local imports.
`CheckFile(filename, source)` checks with the same filesystem context.
`LoadProgramFromGraph` restores the embedded graph for standalone execution.
`Program.ExportedOperations()` describes imported exports.
`StandardOperations()` returns a detached, sorted catalog of executable standard
operations, including parameter types, result types, effects, and target support.
It reads the runtime registry, performs no I/O, and powers editor documentation
and completion.

See [editor services](sysonescript-editor.md) for offline completion and auto-import.

Run the checked-in multi-file example with:

```sh
bin/sos run examples/sos/packages/main.sos
```

In Studio, choose the **library** example to try colors, type hints, and standard
operations. Choose the project's working folder before using local package suggestions.

See [library vocabulary and the searchable dictionary](sysonescript-vocabulary.md)
for open imports, parent aliases, and project-wide library configuration.

## Jev question values

`std/jev` supplies pure question constructors, named batch assembly and answer lookup. See [question batches](sysonescript-question-batches.md). Construction does not make provider requests; the canonical `evaluate` sentence does.

## Host and collection libraries

See [Files, streams and values](sysonescript-io.md) for `std/files`, `std/path`,
`std/io`, `std/process`, `std/list`, `std/record` and expanded `std/text` operations.
Use [parallel maps](sysonescript-parallel.md) to process independent records with
bounded workers and ordered results. Host operations publish target restrictions
in the vocabulary catalog; subprocesses require a native target.
