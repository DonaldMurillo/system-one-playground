# Filesystem standard library

Status: approved design; implementation pending

This specification defines the complete filesystem surface for useful
SysOneScript command-line programs and long-running automations. It extends the
existing `std/files` and `std/path` modules without introducing a second
filesystem model.

The public language has two layers:

1. canonical English constructions used by documentation, completion,
   generators, canonicalization, and examples; and
2. typed module actions used as the precise fallback and adapter contract.

Both forms resolve to the same checked runtime operations. Jev may help an
author rewrite unfamiliar wording into a canonical construction, but execution
of canonical filesystem operations is deterministic and makes no Jev request.

## Goals

- Cover practical CLI file manipulation and background directory automation.
- Distinguish immediate listing, recursive traversal, and future change
  watching.
- Process very large trees incrementally with bounded memory.
- Make overwrite, deletion, link-following, and error policies explicit.
- Provide stable typed entries, events, and failures across the CLI, native
  builds, Studio, VS Code, and the debugger.
- Preserve cancellation, stream ownership, and capability enforcement.
- Read like ordinary English without hiding expensive or destructive behavior.

## Non-goals

This library does not provide a filesystem sandbox, transactionally roll back a
series of writes, silently infer encodings, grant capabilities from paths, or
make remote object stores pretend to be local files. Archive formats, temporary
workspace management, file locking, and platform access-control editing require
separate specifications.

## Canonical vocabulary

The following verbs have distinct meanings and are not synonyms:

| Wording | Meaning |
|---|---|
| `list` | Return the immediate children that exist now. |
| `walk through` | Recursively inspect the tree that exists now and return a bounded list. |
| `stream ... under` | Recursively emit the current tree incrementally, then finish. |
| `watch` | Remain active and emit future filesystem changes. |
| `find` | Select current entries using explicit matching criteria. |
| `read` | Obtain the complete bounded contents of one regular file. |
| `write` | Create or explicitly replace one regular file. |
| `append` | Add bytes to the end of one regular file. |
| `copy` | Duplicate an entry while preserving the source. |
| `move` | Relocate an entry. |
| `remove` | Delete an exact typed target. |
| `create folder` | Ensure a directory exists. |

`walk` never waits for future changes. `watch` never emits an implicit snapshot;
code that needs both performs a traversal and then opens a watcher using the
documented reconciliation strategy.

## Types

### FileEntry

```sos
define FileEntry:
  path as text
  relative_path as text
  name as text
  kind as text
  size as optional integer
  modified_at as optional timestamp
  depth as integer
  symbolic_link as boolean
```

`kind` is one of `file`, `folder`, `link`, or `other`. Size is available for
regular files. Paths returned from traversal use `/` separators so scripts and
recorded output are portable; operations accept host paths and normalize them
at the filesystem boundary.

### FileChange

```sos
define FileChange:
  path as text
  relative_path as text
  kind as text
  entry_kind as text
  previous_path as optional text
  observed_at as timestamp
```

Change `kind` is one of `created`, `modified`, `removed`, `moved`, or
`overflowed`. Native watcher APIs may coalesce repeated modifications. An
`overflowed` event means the consumer must rescan; the runtime never claims it
observed changes that the operating system dropped.

## Reading and writing

Canonical forms:

```sos
read file "settings.toml" as text called settings

write report to file "report.txt"
  only if it does not exist

write report to file "report.txt"
  replacing an existing file

write report atomically to file "report.txt"
  replacing an existing file

append line to file "activity.log"
```

Fallback module actions:

```sos
call files.read_text with path called contents
call files.write_text with path, contents, "create" called written
call files.write_text_atomically with path, contents, "replace" called written
call files.append_text with path, contents called written
```

Write policy is `create` or `replace`; there is no implicit overwrite. Atomic
write creates a temporary regular file in the destination folder, flushes and
closes it, then atomically replaces the destination where the host filesystem
supports that operation. A failure before replacement preserves the old file.
Durability against power loss is not promised unless a future operation
explicitly requests directory synchronization.

Text operations accept UTF-8. Invalid UTF-8 fails rather than being replaced.
Complete reads and writes default to a 16 MiB limit. Larger data requires a
bounded byte-stream API specified separately.

## Metadata and existence

```sos
check whether file "settings.toml" exists called configured
inspect entry "settings.toml" called information
```

`exists` returns false only when the named entry is absent. Permission,
malformed-path, and I/O errors fail. `inspect` examines the path itself without
following a final symbolic link unless explicitly requested.

## Immediate listing

```sos
list entries in folder "src" called entries
list files in folder "src" called files
list folders in folder "packages" called packages
```

Listing returns immediate children only, sorted by portable relative path. It
does not recurse. Results are bounded by an explicit runtime maximum and fail
instead of silently truncating.

## Recursive traversal

Materialized traversal is explicit about its bound:

```sos
walk through folder "src" at most 10000 entries called entries
  including files and folders
  matching ["*.go", "*.sos"]
  excluding [".git", "vendor", "generated"]
  at most 8 folders deep
  without following symbolic links
```

Incremental traversal is preferred for large trees:

```sos
stream files under folder "src" called source_files
  matching ["*.go", "*.sos"]
  excluding [".git", "vendor"]
  at most 8 folders deep
  without following symbolic links

for each file from source_files:
  show relative_path of file
```

The stream emits entries in stable depth-first lexical order. Backpressure
prevents traversal from outrunning the consumer. `stop reading`, `close stream`,
run cancellation, or scope cleanup stops the filesystem walk promptly.

Defaults are:

- include files and folders;
- no include patterns;
- no exclusions;
- do not follow symbolic links;
- stop on inaccessible entries;
- no depth restriction for the streaming form; and
- require an explicit entry bound for materialization.

Patterns containing `/` match slash-separated relative paths. Other patterns
match individual path components. Glob syntax is documented and deterministic;
`**` is not accepted unless recursive-glob semantics are implemented explicitly.

If symbolic links are followed, traversal tracks resolved directory identity to
detect cycles. A target outside the starting tree is rejected by default. Link
following never expands filesystem capability.

## Watching future changes

```sos
watch folder "incoming" recursively called changes
  matching ["*.pdf"]
  without following symbolic links

for each change from changes:
  when kind of change is "created":
    call invoices.process with path of change
```

`watch` creates a new independently owned stream for every invocation. Closing
one watcher does not close another watcher created from the same action. A
module may share an operating-system watcher internally, but subscription
identity, credit, counters, cancellation, and terminal failures remain separate.

Watching is native-only unless a host supplies an equivalent adapter. Recursive
watch setup must close every partial native watch if startup fails. Changes
during recursive startup may race with the initial registration; applications
that need a complete initial state use the scan/watch/reconcile helper defined
by the implementation notes rather than assuming impossible atomicity.

## Copying and moving

```sos
copy file "report.txt" to "archive/report.txt"
  only if the destination does not exist

move file "draft.txt" to "published/report.txt"
  replacing an existing file
```

File copy preserves contents and ordinary permission bits where supported. It
does not follow a symbolic-link source by default. Folder copy is recursive and
must say `copy folder`; it observes the same link, exclusion, cancellation, and
entry-limit rules as traversal. Cross-device move may fall back to copy followed
by removal only when explicitly permitted, because that fallback is not atomic.

## Creating and removing

```sos
create folder "output" if missing
create folders through "output/reports/2026"

remove file "temporary.txt"
remove empty folder "cache"
remove folder "generated" including its contents
```

`remove entry` is not canonical because it conceals whether recursive deletion
is possible. Removing a nonempty folder requires the exact phrase `including
its contents`. Recursive removal does not follow symbolic links and applies an
entry limit before deletion begins where the host can enumerate safely. There
is no force option that converts permission or I/O failures into success.

## Failure contract

The runtime provides reserved typed failures:

| Failure | Required fields |
|---|---|
| `FileNotFound` | `path as text` |
| `FileAlreadyExists` | `path as text` |
| `FilePermissionDenied` | `path as text`, `operation as text` |
| `FileTooLarge` | `path as text`, `limit as integer`, `observed as optional integer` |
| `InvalidFileType` | `path as text`, `expected as text`, `actual as text` |
| `InvalidFilePath` | `path as text`, `reason as text` |
| `FileTraversalLimitExceeded` | `root as text`, `limit as integer` |
| `FileWatchOverflow` | `root as text` |
| `FileSystemUnavailable` | `path as text`, `operation as text` |

Host error messages and raw platform codes may appear in safe diagnostic
metadata but do not replace these portable kinds. Cancellation and global run
deadlines remain fatal run termination rather than catchable file failures.

## Capabilities and targets

Every operation declares `filesystem-read`, `filesystem-write`, or both.
Project policy must authorize the required capability before execution. Path
validation does not grant access, and the standard library is not a security
sandbox for an otherwise trusted native process.

Pure path manipulation remains in `std/path` and works in browser, WASI, and
native targets. Native filesystem operations target native and capable WASI
hosts; browser builds reject them unless an embedding host supplies a declared
filesystem adapter.

## Tooling requirements

Studio, VS Code, CLI help, vocabulary, and generated documentation must expose:

- canonical English forms and precise fallback module calls;
- parameter names instead of positional boolean hints;
- read/write/destructive effect labels;
- possible typed failures;
- target availability;
- traversal bounds and active watcher state; and
- ownership-aware references and rename for traversal/watcher handles.

The debugger may inspect buffered metadata and counters but must never consume a
traversal or watcher item. Stop Stream targets one traversal or watcher without
stopping the complete run.

## Compatibility

Existing `files.read`, `files.write`, and `files.discover` remain accepted.
Documentation and canonicalization prefer `read_text`, explicit write policy,
and the separate list/walk/stream constructions. Compatibility aliases must use
the same implementation rather than duplicating behavior.

## Required verification

Implementation is complete only when tests cover:

- files, folders, links, special entries, Unicode names, and invalid UTF-8;
- create-versus-replace policy and atomic-write preservation;
- lexical ordering and slash-normalized relative paths;
- include/exclude patterns, depth, entry limits, and cancellation;
- symlink cycles and escape attempts;
- permission and disappearance races;
- independent concurrent traversals and watchers;
- watcher overflow and recursive-startup cleanup;
- early `stop reading` with continuation below the loop;
- interpreter/native-build parity;
- CLI, Studio, debugger, and VS Code observability; and
- Linux, macOS, and Windows behavior in CI.

## Delivery sequence

1. Define built-in file types and failures.
2. Consolidate current primitives behind one internal filesystem boundary.
3. Implement text I/O, metadata, explicit write policy, and atomic writing.
4. Implement list and bounded materialized traversal.
5. Implement backpressured streaming traversal.
6. Implement copy, move, folder creation, and explicit removal.
7. Implement native watcher streams and reconciliation helpers.
8. Add canonical English grammar, aliases, canonicalization, and editor support.
9. Add complete CLI and background-service examples.
10. Run cross-platform, race, native-build, and independent OMP reviews.
