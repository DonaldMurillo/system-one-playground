# Files, paths, streams and subprocesses

These standard libraries expose host operations directly. Scripts are trusted
programs: the filesystem helpers are not a sandbox. Paths resolve against the
execution directory, which can be selected with `sysone --project`.

```text
import "std/files" as files
import "std/path" as path
import "std/io" as io
import "std/process" as process

files.discover ".", [".git", "node_modules", "*.min.js"] called sources
path.join "src", "main.go" called main
files.read main called source
io.error "Checking repository"
process.run "git", ["diff", "--no-ext-diff", "--unified=0"] called diff
show diff
```

`files.discover root, exclusions` returns regular files recursively in sorted
order, with slash-separated paths relative to `root`. Symlinks are skipped, and
the root must be a directory rather than a symlink. A pattern containing `/`
matches the relative path; other patterns match any component. Matching
directories are pruned. Patterns use Go's path glob rules (`*`, `?`, character
classes); `**` has no special recursive meaning. Permission and traversal errors
fail the operation. Discovery stops at cancellation or 100,000 files.

`files.read path` reads a regular file up to 16 MiB. `files.write path, text`
replaces a regular destination or creates a file, with existing parent
directories required and the same size limit. Both reject symlink and special
file destinations at validation time; they do not promise race-resistant
filesystem confinement.

`path.base`, `path.extension`, `path.directory`, and `path.clean` take one path.
`path.join base, child` joins two components; `path.relative base, target` returns
a relative path. These pure operations use the host platform's path conventions.

`io.read maxBytes` reads standard input, failing if input exceeds the explicit
limit (maximum 16 MiB). A host reader may block until it provides data; cancellation
is checked before and after reading. `io.error text` writes a newline to standard
error. `io.exit status` stops the script with an integer status from 0 through
255; embedding hosts receive an `ExitError` rather than having their process
terminated. No standard input supplied by the host is treated as an empty stream.

`process.run executable, arguments` executes a program directly, without shell
interpolation, in the execution directory. It returns `{stdout, stderr, status}`;
a nonzero process status is a returned value. A launch failure, cancellation or
30-second deadline is an operation error. Output is limited to 16 MiB per stream.
The subprocess inherits the host process environment. No stdin is passed. This
operation is native-only; it does not provide a browser or WASI process adapter.

## Text, list and record operations

`std/text` also provides `split text, separator`, `lines text`,
`slice text, start, end`, and `replace text, old, new`. Slice indices are zero-based
Unicode code-point positions, with an exclusive end; invalid bounds fail.
`lines` accepts LF and CRLF and preserves a trailing empty line.

`text.matches text, pattern` uses Go's RE2 regular expressions and returns at most
100,000 match records. Each contains `text`, zero-based `byteStart` and exclusive
`byteEnd`, one-based `line` and Unicode code-point `column`, and captured `groups`.
Unmatched optional capture groups contain null. Byte offsets are intentionally
separate from the character indices accepted by `text.slice`.

`std/list.at list, index` reads an element by zero-based index; invalid indices
fail. `list.flatten list` flattens exactly one level, requiring a list of lists.

`std/record.get record, key` reads a dynamically named field, failing if absent.
`record.set record, key, value` returns a new record and preserves the original.
`record.keys record` returns sorted field names. None of these operations mutate
input collections, so they can safely participate in isolated parallel loops.

`files.exists path` distinguishes missing paths (false) from unreadable paths
(errors), which lets a script handle optional sidecar files without suppressing
corrupt JSON or permission failures. It uses the existence of the path itself,
including a symlink, rather than following a link to its target.

`list.percentile numbers, percent` returns the sorted sample at index
`floor(percent / 100 * (count - 1))`. It requires a nonempty sample of finite
numbers and a percentile from 0 through 100. This is a discrete sample percentile,
not interpolation, and preserves the original list.
