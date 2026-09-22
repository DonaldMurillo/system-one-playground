# Files and folders

SysOneScript filesystem operations read like ordinary English while keeping
expensive and destructive behavior explicit. Every operation resolves to one
checked runtime action: listing, traversal, watching, reading, writing,
copying, moving, and removing each say what they do, and overwrite or
recursive deletion is never implied.

The vocabulary distinguishes four ways to observe a tree:

- `list` returns the immediate children that exist now.
- `walk through` recursively inspects the tree that exists now and returns a
  bounded list.
- `stream ... under` recursively emits the current tree incrementally, then
  finishes.
- `watch` stays active and emits future filesystem changes.

`walk` never waits for future changes. `watch` never emits an implicit
snapshot; code that needs both performs a traversal and then opens a watcher.

## Reading and writing

Canonical constructions:

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

Typed module actions serve as the precise fallback and adapter contract:

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

Text operations accept UTF-8; invalid UTF-8 fails rather than being replaced.
Complete reads and writes default to a 16 MiB limit — larger data requires the
bounded byte-stream API specified separately.

## Metadata and existence

```sos
check whether file "settings.toml" exists called configured
inspect entry "settings.toml" called information
```

`exists` returns false only when the named entry is absent; permission,
malformed-path, and I/O errors fail instead. `inspect` examines the path
itself without following a final symbolic link unless explicitly requested.

Typed entries use one stable shape across the CLI, native builds, Studio,
VS Code, and the debugger:

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
regular files. Traversal results use `/` separators so scripts and recorded
output stay portable; operations accept host paths and normalize them at the
filesystem boundary.

## Listing and traversal

Immediate listing returns children only, sorted by portable relative path,
and fails rather than silently truncating:

```sos
list entries in folder "src" called entries
list files in folder "src" called files
list folders in folder "packages" called packages
```

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

The stream emits entries in stable depth-first lexical order, and backpressure
prevents traversal from outrunning the consumer. `stop reading`, `close
stream`, run cancellation, or scope cleanup stops the walk promptly.

Defaults are: include files and folders; no include patterns; no exclusions;
do not follow symbolic links; stop on inaccessible entries; no depth
restriction for the streaming form; and an explicit entry bound required for
materialization. Patterns containing `/` match slash-separated relative
paths; other patterns match individual path components. Glob syntax is
documented and deterministic; `**` is not accepted unless recursive-glob
semantics are implemented explicitly. When symbolic links are followed,
traversal tracks resolved directory identity to detect cycles, and a target
outside the starting tree is rejected by default. Link following never
expands filesystem capability.

## Watching future changes

```sos
watch folder "incoming" recursively called changes
  matching ["*.pdf"]
  without following symbolic links

for each change from changes:
  when kind of change is "created":
    call invoices.process with path of change
```

Changes use one typed shape:

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

`watch` creates a new independently owned stream for every invocation. Closing
one watcher does not close another watcher created from the same action.
Watching is native-only unless a host supplies an equivalent adapter.
Recursive watch setup closes every partial native watch if startup fails, and
changes during recursive startup may race with initial registration —
applications needing a complete initial state use the scan/watch/reconcile
helper rather than assuming impossible atomicity.

## Copying, moving, creating, and removing

```sos
copy file "report.txt" to "archive/report.txt"
  only if the destination does not exist

move file "draft.txt" to "published/report.txt"
  replacing an existing file

create folder "output" if missing
create folders through "output/reports/2026"

remove file "temporary.txt"
remove empty folder "cache"
remove folder "generated" including its contents
```

File copy preserves contents and ordinary permission bits where supported and
does not follow a symbolic-link source by default. Folder copy is recursive,
must say `copy folder`, and observes the same link, exclusion, cancellation,
and entry-limit rules as traversal. Cross-device move may fall back to copy
followed by removal only when explicitly permitted, because that fallback is
not atomic.

`remove entry` is not canonical because it conceals whether recursive deletion
is possible. Removing a nonempty folder requires the exact phrase `including
its contents`. Recursive removal does not follow symbolic links and applies an
entry limit before deletion begins where the host can enumerate safely. There
is no force option that converts permission or I/O failures into success.

## Typed failures

The runtime reserves these portable failure kinds; host error messages and
raw platform codes may appear in diagnostic metadata but never replace them:

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

Cancellation and global run deadlines remain fatal run termination rather
than catchable file failures.

## Capabilities and targets

Every operation declares `filesystem-read`, `filesystem-write`, or both.
Project policy must authorize the required capability before execution; path
validation does not grant access, and the standard library is not a security
sandbox for an otherwise trusted native process.

Pure path manipulation stays in `std/path` and works in browser, WASI, and
native targets. Native filesystem operations target native and capable WASI
hosts; browser builds reject them unless an embedding host supplies a
declared filesystem adapter.

## Editor and debugger support

Completion, hover, generated documentation, and canonicalization expose both
the canonical English constructions and the precise fallback module calls,
with parameter names instead of positional boolean hints, read/write/destructive
effect labels, possible typed failures, and target availability. Traversal and
watcher handles are owned streams: references, rename, and diagnostics follow
lexical ownership exactly like any other stream handle.

The debugger may inspect buffered metadata and counters but never consumes a
traversal or watcher item. Studio's Streams panel and VS Code's Streams view
report traversal bounds and active watcher state from the same non-consuming
snapshots, and **Stop stream** ends one traversal or watcher without stopping
the complete run.

## Compatibility

Existing `files.read`, `files.write`, and `files.discover` remain accepted.
Documentation and canonicalization prefer `read_text`, explicit write policy,
and the separate list/walk/stream constructions. Compatibility aliases use the
same implementation rather than duplicating behavior.

See the runnable [filesystem examples](https://github.com/DonaldMurillo/system-one-playground/tree/main/examples/sos/files)
project for a complete CLI pipeline and a background watcher service.
