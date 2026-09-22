package sos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// This file is the internal filesystem boundary for std/files: the reserved
// typed failure definitions, the stable FileEntry record shape, and the core
// actions for bounded UTF-8 text I/O, metadata, immediate listing, and
// copy/move/create/remove. Compatibility aliases (read, write) delegate to
// the same implementations as their canonical action names. Recursive
// traversal, streaming, and watching live in filesystem_traversal.go and
// share fileFailure, classifyMode, and fileEntryValue from here.

// maxListEntries bounds one immediate listing and the entry-count safety
// checks for folder copy and recursive removal; operations fail instead of
// truncating.
const maxListEntries = 100000

// fileFailures are the reserved portable failure kinds for filesystem
// operations. Host error text is preserved in the failure message as
// diagnostic metadata but never replaces the kind.
func fileFailures() map[string]*FailureDef {
	return map[string]*FailureDef{
		"FileNotFound": {
			Name:   "FileNotFound",
			Fields: []RecordField{{Name: "path", Type: TypeRef{Name: "text"}}},
		},
		"FileAlreadyExists": {
			Name:   "FileAlreadyExists",
			Fields: []RecordField{{Name: "path", Type: TypeRef{Name: "text"}}},
		},
		"FilePermissionDenied": {
			Name: "FilePermissionDenied",
			Fields: []RecordField{
				{Name: "path", Type: TypeRef{Name: "text"}},
				{Name: "operation", Type: TypeRef{Name: "text"}},
			},
		},
		"FileTooLarge": {
			Name: "FileTooLarge",
			Fields: []RecordField{
				{Name: "path", Type: TypeRef{Name: "text"}},
				{Name: "limit", Type: TypeRef{Name: "integer"}},
				{Name: "observed", Type: TypeRef{Name: "integer", Optional: true}},
			},
		},
		"InvalidFileType": {
			Name: "InvalidFileType",
			Fields: []RecordField{
				{Name: "path", Type: TypeRef{Name: "text"}},
				{Name: "expected", Type: TypeRef{Name: "text"}},
				{Name: "actual", Type: TypeRef{Name: "text"}},
			},
		},
		"InvalidFilePath": {
			Name: "InvalidFilePath",
			Fields: []RecordField{
				{Name: "path", Type: TypeRef{Name: "text"}},
				{Name: "reason", Type: TypeRef{Name: "text"}},
			},
		},
		"FileTraversalLimitExceeded": {
			Name: "FileTraversalLimitExceeded",
			Fields: []RecordField{
				{Name: "root", Type: TypeRef{Name: "text"}},
				{Name: "limit", Type: TypeRef{Name: "integer"}},
			},
		},
		"FileWatchOverflow": {
			Name:   "FileWatchOverflow",
			Fields: []RecordField{{Name: "root", Type: TypeRef{Name: "text"}}},
		},
		"FileSystemUnavailable": {
			Name: "FileSystemUnavailable",
			Fields: []RecordField{
				{Name: "path", Type: TypeRef{Name: "text"}},
				{Name: "operation", Type: TypeRef{Name: "text"}},
			},
		},
	}
}

// stdModuleDefinitions owns the reserved typed entry shapes. They are
// exported by std/files, visible as types in every file without an import via
// builtInFileDefinitions, and shared by listing, traversal, and watching. The
// definitions are singletons: scope comparisons rely on pointer identity, so
// every caller must treat the map as immutable.
var (
	fileRecordDefinitionsOnce sync.Once
	fileRecordDefinitions     map[string]*RecordDef
)

func stdModuleDefinitions(key string) map[string]*RecordDef {
	if key != "std/files" {
		return nil
	}
	fileRecordDefinitionsOnce.Do(func() {
		fileRecordDefinitions = map[string]*RecordDef{
			"FileEntry": {
				Name: "FileEntry",
				Fields: []RecordField{
					{Name: "path", Type: TypeRef{Name: "text"}},
					{Name: "relative_path", Type: TypeRef{Name: "text"}},
					{Name: "name", Type: TypeRef{Name: "text"}},
					{Name: "kind", Type: TypeRef{Name: "text"}},
					{Name: "size", Type: TypeRef{Name: "integer", Optional: true}},
					{Name: "modified_at", Type: TypeRef{Name: "timestamp", Optional: true}},
					{Name: "depth", Type: TypeRef{Name: "integer"}},
					{Name: "symbolic_link", Type: TypeRef{Name: "boolean"}},
				},
			},
			"FileChange": {
				Name: "FileChange",
				Fields: []RecordField{
					{Name: "path", Type: TypeRef{Name: "text"}},
					{Name: "relative_path", Type: TypeRef{Name: "text"}},
					{Name: "kind", Type: TypeRef{Name: "text"}},
					{Name: "entry_kind", Type: TypeRef{Name: "text"}},
					{Name: "previous_path", Type: TypeRef{Name: "text", Optional: true}},
					{Name: "observed_at", Type: TypeRef{Name: "timestamp"}},
				},
			},
		}
	})
	return fileRecordDefinitions
}

// fileFailure builds one reserved filesystem failure value with the common
// metadata and the kind's required fields.
func fileFailure(kind, message string, fields map[string]any) error {
	value := map[string]any{"kind": kind, "message": message, "retryable": false}
	for key, field := range fields {
		value[key] = field
	}
	return &typedFailure{kind: kind, value: value}
}

// classifyMode reports the portable kind of one FileMode: file, folder, link,
// or other (sockets, devices, FIFOs).
func classifyMode(mode fs.FileMode) string {
	switch {
	case mode.IsRegular():
		return fileKind
	case mode.IsDir():
		return folderKind
	case mode&os.ModeSymlink != 0:
		return linkKind
	default:
		return otherKind
	}
}

// fileEntryValue shapes one walked entry as the portable FileEntry record
// shared by listing, inspection, traversal, and watching. Paths use "/"
// separators; size is present only for regular files.
func fileEntryValue(item walkItem) map[string]any {
	entry := map[string]any{
		"path":          filepath.ToSlash(item.hostPath),
		"relative_path": item.rel,
		"name":          item.name,
		"kind":          item.kind,
		"depth":         float64(item.depth),
		"symbolic_link": item.symLink,
	}
	if item.hasSize {
		entry["size"] = float64(item.size)
	}
	if !item.modified.IsZero() {
		entry["modified_at"] = item.modified
	}
	return entry
}

// statFileEntry shapes one lstat result into the shared FileEntry record so
// listing, inspection, traversal, and watching cannot drift apart.
func statFileEntry(resolved, relative, name string, info fs.FileInfo, depth int) map[string]any {
	item := walkItem{
		rel:      relative,
		hostPath: resolved,
		name:     name,
		kind:     classifyMode(info.Mode()),
		modified: info.ModTime(),
		depth:    depth,
		symLink:  info.Mode()&os.ModeSymlink != 0,
	}
	if info.Mode().IsRegular() {
		item.size, item.hasSize = info.Size(), true
	}
	return fileEntryValue(item)
}

// fileOpError maps one host error to the portable failure kinds for a named
// operation, keeping the host message as diagnostic context.
func fileOpError(err error, displayPath, operation string) error {
	if err == nil {
		return nil
	}
	wrap := func(kind, message string, fields map[string]any) error {
		fields["path"] = displayPath
		return fileFailure(kind, fmt.Sprintf("%s: %v", message, err), fields)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return wrap("FileNotFound", "path does not exist", map[string]any{})
	case errors.Is(err, fs.ErrExist):
		return wrap("FileAlreadyExists", "path already exists", map[string]any{})
	case errors.Is(err, fs.ErrPermission):
		return wrap("FilePermissionDenied", "permission denied", map[string]any{"operation": operation})
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return wrap("FileSystemUnavailable", "filesystem operation failed", map[string]any{"operation": operation})
	}
}

// filePathResolve validates a script-supplied path and resolves it against
// the execution directory.
func filePathResolve(opts Options, display string) (string, error) {
	if strings.TrimSpace(display) == "" {
		return "", fileFailure("InvalidFilePath", "path is empty",
			map[string]any{"path": display, "reason": "path is empty"})
	}
	return ioPath(opts, display), nil
}

// fileRelative reports the slash-separated path of resolved relative to the
// execution directory, falling back to the cleaned portable form when the
// entry lives outside it.
func fileRelative(opts Options, resolved, display string) string {
	if rel, err := filepath.Rel(opts.Dir, resolved); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	cleaned := filepath.Clean(display)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return "."
	}
	return filepath.ToSlash(cleaned)
}

func writePolicyValue(value any) (string, error) {
	policy, ok := value.(string)
	if !ok || (policy != "create" && policy != "replace") {
		return "", fmt.Errorf("write policy must be \"create\" or \"replace\"")
	}
	return policy, nil
}

func checkUTF8(data []byte, display string) error {
	if !utf8.Valid(data) {
		return fileFailure("InvalidFileType", "file contents are not valid UTF-8",
			map[string]any{"path": display, "expected": "UTF-8 text", "actual": "invalid UTF-8 bytes"})
	}
	return nil
}

func checkUTF8String(text, display string) error {
	if !utf8.ValidString(text) {
		return fileFailure("InvalidFileType", "text is not valid UTF-8",
			map[string]any{"path": display, "expected": "UTF-8 text", "actual": "invalid UTF-8 bytes"})
	}
	return nil
}

func fileTooLarge(display string, observed int64, known bool) error {
	fields := map[string]any{"path": display, "limit": float64(maxIOBytes)}
	if known {
		fields["observed"] = float64(observed)
	}
	return fileFailure("FileTooLarge", fmt.Sprintf("content exceeds the %d byte limit", maxIOBytes), fields)
}

// readTextFile returns the complete bounded UTF-8 contents of one regular
// file. Symlinks and special files are rejected before the file is opened,
// matching the documented compatibility behavior.
func readTextFile(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	initial, err := os.Lstat(resolved)
	if err != nil {
		return nil, fileOpError(err, display, "read")
	}
	if !initial.Mode().IsRegular() {
		return nil, fileFailure("InvalidFileType", "read requires a regular file",
			map[string]any{"path": display, "expected": fileKind, "actual": classifyMode(initial.Mode())})
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fileOpError(err, display, "read")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fileOpError(err, display, "read")
	}
	if !info.Mode().IsRegular() {
		return nil, fileFailure("InvalidFileType", "read requires a regular file",
			map[string]any{"path": display, "expected": fileKind, "actual": classifyMode(info.Mode())})
	}
	if !os.SameFile(initial, info) {
		return nil, fileFailure("InvalidFileType", "file changed while opening; refusing to follow a replacement", map[string]any{"path": display, "expected": fileKind, "actual": "replaced entry"})
	}
	if info.Size() > maxIOBytes {
		return nil, fileTooLarge(display, info.Size(), true)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxIOBytes+1))
	if err != nil {
		return nil, fileOpError(err, display, "read")
	}
	if len(data) > maxIOBytes {
		return nil, fileTooLarge(display, int64(len(data)), true)
	}
	if err := checkUTF8(data, display); err != nil {
		return nil, err
	}
	return string(data), ctx.Err()
}

// validateWriteDestination rejects destinations that exist but are not
// regular files (symlinks included), before any bytes are written.
func validateWriteDestination(resolved, display string) error {
	info, err := os.Lstat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fileOpError(err, display, "write")
	}
	if !info.Mode().IsRegular() {
		return fileFailure("InvalidFileType", "write requires a regular file destination",
			map[string]any{"path": display, "expected": fileKind, "actual": classifyMode(info.Mode())})
	}
	return nil
}

// writeTextPlain writes contents with an explicit create-or-replace policy.
// Parent directories must already exist.
func writeTextPlain(resolved, display, contents, policy string) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if policy == "create" {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(resolved, flags, 0o644)
	if err != nil {
		return fileOpError(err, display, "write")
	}
	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		return fileOpError(err, display, "write")
	}
	return fileOpError(file.Close(), display, "write")
}

// writeTextAtomic writes contents to a temporary regular file in the
// destination folder, flushes and closes it, then installs it with a
// no-clobber link (create) or an atomic rename (replace). A failure before
// installation preserves the previous file.
func writeTextAtomic(resolved, display, contents, policy string) error {
	if policy == "create" {
		if _, err := os.Lstat(resolved); err == nil {
			return fileFailure("FileAlreadyExists", "path already exists", map[string]any{"path": display})
		} else if !os.IsNotExist(err) {
			return fileOpError(err, display, "write")
		}
	}
	perm := fs.FileMode(0o644)
	if info, err := os.Lstat(resolved); err == nil && info.Mode().IsRegular() {
		perm = info.Mode().Perm()
	}
	temp, err := os.CreateTemp(filepath.Dir(resolved), ".sos-write-*")
	if err != nil {
		return fileOpError(err, display, "write")
	}
	tempName := temp.Name()
	if err := func() error {
		if _, err := temp.WriteString(contents); err != nil {
			return err
		}
		if err := temp.Sync(); err != nil {
			return err
		}
		if err := temp.Chmod(perm); err != nil {
			return err
		}
		return temp.Close()
	}(); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return fileOpError(err, display, "write")
	}
	if policy == "create" {
		if err := os.Link(tempName, resolved); err != nil {
			_ = os.Remove(tempName)
			if os.IsExist(err) {
				return fileFailure("FileAlreadyExists", "path already exists", map[string]any{"path": display})
			}
			return fileFailure("FileSystemUnavailable", "atomic create requires same-filesystem hard-link installation on this host", map[string]any{"path": display, "operation": "write"})
		}
		// Destination installation already succeeded; temporary cleanup failure
		// is diagnostic-only and must not report the completed write as failed.
		_ = os.Remove(tempName)
		return nil
	}
	if err := os.Rename(tempName, resolved); err != nil {
		_ = os.Remove(tempName)
		return fileOpError(err, display, "write")
	}
	return nil
}

// writeText is the shared implementation of write_text, write_text_atomically,
// and the compatibility alias files.write (which fixes the policy to replace).
func writeText(ctx context.Context, opts Options, args []any, atomic bool) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	contents := args[1].(string)
	policy, err := writePolicyValue(args[2])
	if err != nil {
		return nil, err
	}
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maxIOBytes {
		return nil, fileTooLarge(display, int64(len(contents)), true)
	}
	if err := checkUTF8String(contents, display); err != nil {
		return nil, err
	}
	if err := validateWriteDestination(resolved, display); err != nil {
		return nil, err
	}
	if atomic {
		err = writeTextAtomic(resolved, display, contents, policy)
	} else {
		err = writeTextPlain(resolved, display, contents, policy)
	}
	if err != nil {
		return nil, err
	}
	return display, ctx.Err()
}

// appendText adds bytes to the end of one regular file, creating it when
// absent, and keeps the resulting file inside the complete-write bound.
func appendText(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	contents := args[1].(string)
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	if err := checkUTF8String(contents, display); err != nil {
		return nil, err
	}
	var existing int64
	if info, err := os.Lstat(resolved); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fileFailure("InvalidFileType", "append requires a regular file",
				map[string]any{"path": display, "expected": fileKind, "actual": classifyMode(info.Mode())})
		}
		existing = info.Size()
	} else if !os.IsNotExist(err) {
		return nil, fileOpError(err, display, "append")
	}
	if observed := existing + int64(len(contents)); observed > maxIOBytes {
		return nil, fileTooLarge(display, observed, true)
	}
	file, err := os.OpenFile(resolved, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fileOpError(err, display, "append")
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, fileOpError(statErr, display, "append")
	}
	if !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, fileFailure("InvalidFileType", "append requires a regular file", map[string]any{"path": display, "expected": fileKind, "actual": classifyMode(opened.Mode())})
	}
	existing = opened.Size()
	if observed := existing + int64(len(contents)); observed > maxIOBytes {
		_ = file.Close()
		return nil, fileTooLarge(display, observed, true)
	}
	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		return nil, fileOpError(err, display, "append")
	}
	if err := file.Close(); err != nil {
		return nil, fileOpError(err, display, "append")
	}
	return display, ctx.Err()
}

// entryExists reports whether the path itself exists; permission and I/O
// errors fail instead of reporting false.
func entryExists(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(resolved); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return nil, fileOpError(err, display, "exists")
	}
	return true, nil
}

// inspectEntry describes the path itself without following a final symbolic
// link.
func inspectEntry(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return nil, fileOpError(err, display, "inspect")
	}
	name := filepath.Base(filepath.Clean(display))
	if name == "." || name == string(filepath.Separator) {
		name = filepath.Base(resolved)
	}
	return statFileEntry(resolved, fileRelative(opts, resolved, display), name, info, 0), ctx.Err()
}

// listFolder returns the immediate children of one folder, sorted by portable
// relative path, filtered by kind, and bounded by limit. kindFilter is
// "entries", "files", or "folders".
func listFolder(ctx context.Context, opts Options, folder, kindFilter string, limit int) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if kindFilter != "entries" && kindFilter != "files" && kindFilter != "folders" {
		return nil, fmt.Errorf("list kind must be \"entries\", \"files\", or \"folders\"")
	}
	resolved, err := filePathResolve(opts, folder)
	if err != nil {
		return nil, err
	}
	root, err := os.Lstat(resolved)
	if err != nil {
		return nil, fileOpError(err, folder, "list")
	}
	if !root.IsDir() {
		return nil, fileFailure("InvalidFileType", "listing requires a folder",
			map[string]any{"path": folder, "expected": folderKind, "actual": classifyMode(root.Mode())})
	}
	dirents, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fileOpError(err, folder, "list")
	}
	out := []any{}
	for _, dirent := range dirents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := dirent.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fileOpError(err, folder, "list")
		}
		kind := classifyMode(info.Mode())
		if kindFilter == "files" && kind != fileKind {
			continue
		}
		if kindFilter == "folders" && kind != folderKind {
			continue
		}
		out = append(out, statFileEntry(filepath.Join(resolved, dirent.Name()), dirent.Name(), dirent.Name(), info, 1))
		if len(out) > limit {
			return nil, fileFailure("FileTraversalLimitExceeded",
				fmt.Sprintf("listing exceeds %d entries", limit),
				map[string]any{"root": folder, "limit": float64(limit)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].(map[string]any)["relative_path"].(string) < out[j].(map[string]any)["relative_path"].(string)
	})
	return out, nil
}

func listFolderAction(ctx context.Context, opts Options, args []any) (any, error) {
	folder, _ := args[0].(string)
	kindFilter, _ := args[1].(string)
	return listFolder(ctx, opts, folder, kindFilter, maxListEntries)
}

// copyRegularFile duplicates one regular file without following a
// symbolic-link source, preserving contents and ordinary permission bits.
func copyRegularFile(sourceResolved, sourceDisplay, destResolved, destDisplay, policy string) error {
	info, err := os.Lstat(sourceResolved)
	if err != nil {
		return fileOpError(err, sourceDisplay, "copy")
	}
	if !info.Mode().IsRegular() {
		return fileFailure("InvalidFileType", "copy source must be a regular file",
			map[string]any{"path": sourceDisplay, "expected": fileKind, "actual": classifyMode(info.Mode())})
	}
	if info.Size() > maxIOBytes {
		return fileTooLarge(sourceDisplay, info.Size(), true)
	}
	if err := validateWriteDestination(destResolved, destDisplay); err != nil {
		return err
	}
	if _, err := os.Lstat(destResolved); err == nil && policy == "create" {
		return fileFailure("FileAlreadyExists", "path already exists", map[string]any{"path": destDisplay})
	}
	in, err := os.Open(sourceResolved)
	if err != nil {
		return fileOpError(err, sourceDisplay, "copy")
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destResolved), ".sos-copy-*")
	if err != nil {
		return fileOpError(err, destDisplay, "copy")
	}
	tempName := out.Name()
	cleanup := func() { _ = out.Close(); _ = os.Remove(tempName) }
	written, err := io.Copy(out, io.LimitReader(in, maxIOBytes+1))
	if err == nil && written > maxIOBytes {
		cleanup()
		return fileTooLarge(sourceDisplay, written, true)
	}
	if err != nil {
		cleanup()
		return fileOpError(err, destDisplay, "copy")
	}
	if err = out.Sync(); err == nil {
		err = out.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = out.Close()
	}
	if err != nil {
		cleanup()
		return fileOpError(err, destDisplay, "copy")
	}
	if policy == "create" {
		if err = os.Link(tempName, destResolved); err != nil {
			_ = os.Remove(tempName)
			if os.IsExist(err) {
				return fileFailure("FileAlreadyExists", "path already exists", map[string]any{"path": destDisplay})
			}
			return fileOpError(err, destDisplay, "copy")
		}
		_ = os.Remove(tempName)
		return nil
	}
	if err = os.Rename(tempName, destResolved); err != nil {
		_ = os.Remove(tempName)
		return fileOpError(err, destDisplay, "copy")
	}
	return nil
}

func copyFileAction(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceDisplay := args[0].(string)
	destDisplay := args[1].(string)
	policy, err := writePolicyValue(args[2])
	if err != nil {
		return nil, err
	}
	sourceResolved, err := filePathResolve(opts, sourceDisplay)
	if err != nil {
		return nil, err
	}
	destResolved, err := filePathResolve(opts, destDisplay)
	if err != nil {
		return nil, err
	}
	if err := copyRegularFile(sourceResolved, sourceDisplay, destResolved, destDisplay, policy); err != nil {
		return nil, err
	}
	return destDisplay, ctx.Err()
}

func copyFolderAction(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceDisplay := args[0].(string)
	destDisplay := args[1].(string)
	policy, err := writePolicyValue(args[2])
	if err != nil {
		return nil, err
	}
	exclusions, err := stringListArg(args[3], "exclusions")
	if err != nil {
		return nil, err
	}
	patterns, err := parseGlobList(exclusions, "exclusions")
	if err != nil {
		return nil, err
	}
	sourceResolved, err := filePathResolve(opts, sourceDisplay)
	if err != nil {
		return nil, err
	}
	destResolved, err := filePathResolve(opts, destDisplay)
	if err != nil {
		return nil, err
	}
	source, err := os.Lstat(sourceResolved)
	if err != nil {
		return nil, fileOpError(err, sourceDisplay, "copy")
	}
	if !source.IsDir() {
		return nil, fileFailure("InvalidFileType", "folder copy requires a folder source",
			map[string]any{"path": sourceDisplay, "expected": folderKind, "actual": classifyMode(source.Mode())})
	}
	if rel, relErr := filepath.Rel(sourceResolved, destResolved); relErr == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return nil, fileFailure("InvalidFilePath", "folder destination must be outside its source", map[string]any{"path": destDisplay, "reason": "destination is inside source"})
	}
	if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(destResolved)); parentErr == nil {
		if rel, relErr := filepath.Rel(sourceResolved, filepath.Join(parent, filepath.Base(destResolved))); relErr == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			return nil, fileFailure("InvalidFilePath", "folder destination must be outside its source", map[string]any{"path": destDisplay, "reason": "destination resolves inside source"})
		}
	}
	if existing, err := os.Lstat(destResolved); err == nil {
		if !existing.IsDir() {
			return nil, fileFailure("InvalidFileType", "folder copy requires a folder destination",
				map[string]any{"path": destDisplay, "expected": folderKind, "actual": classifyMode(existing.Mode())})
		}
		if policy == "create" {
			return nil, fileFailure("FileAlreadyExists", "path already exists", map[string]any{"path": destDisplay})
		}
	} else if !os.IsNotExist(err) {
		return nil, fileOpError(err, destDisplay, "copy")
	}
	if err := os.MkdirAll(destResolved, source.Mode().Perm()); err != nil {
		return nil, fileOpError(err, destDisplay, "copy")
	}
	seen := 0
	err = walkNoFollow(sourceResolved, func(relative string, info fs.FileInfo) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		seen++
		if seen > maxListEntries {
			return false, fileFailure("FileTraversalLimitExceeded",
				fmt.Sprintf("folder copy exceeds %d entries", maxListEntries),
				map[string]any{"root": sourceDisplay, "limit": float64(maxListEntries)})
		}
		for _, pattern := range patterns {
			if globMatches(pattern, relative) {
				return true, nil
			}
		}
		target := filepath.Join(destResolved, filepath.FromSlash(relative))
		if info.IsDir() {
			return false, fileOpError(os.MkdirAll(target, info.Mode().Perm()), destDisplay, "copy")
		}
		if !info.Mode().IsRegular() {
			// Symlinks and special entries inside the tree are not followed
			// and not duplicated.
			return false, nil
		}
		return false, copyRegularFile(filepath.Join(sourceResolved, filepath.FromSlash(relative)),
			path.Join(filepath.ToSlash(sourceDisplay), relative), target,
			path.Join(filepath.ToSlash(destDisplay), relative), "replace")
	})
	if err != nil {
		var typed *typedFailure
		if errors.As(err, &typed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fileOpError(err, sourceDisplay, "copy")
	}
	return destDisplay, ctx.Err()
}

// walkNoFollow visits every entry under root in lexical depth-first order
// without following symbolic links; visit receives slash-separated paths
// relative to root and returns true to prune a directory's children.
func walkNoFollow(root string, visit func(relative string, info fs.FileInfo) (bool, error)) error {
	var walk func(dir, relative string) error
	walk = func(dir, relative string) error {
		dirents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, dirent := range dirents {
			info, err := dirent.Info()
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			childRelative := relative
			if childRelative != "" {
				childRelative += "/"
			}
			childRelative += dirent.Name()
			prune, err := visit(childRelative, info)
			if err != nil {
				return err
			}
			if prune || !info.IsDir() {
				continue
			}
			if err := walk(filepath.Join(dir, dirent.Name()), childRelative); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, "")
}

func moveEntryAction(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceDisplay := args[0].(string)
	destDisplay := args[1].(string)
	policy, err := writePolicyValue(args[2])
	if err != nil {
		return nil, err
	}
	sourceResolved, err := filePathResolve(opts, sourceDisplay)
	if err != nil {
		return nil, err
	}
	destResolved, err := filePathResolve(opts, destDisplay)
	if err != nil {
		return nil, err
	}
	source, err := os.Lstat(sourceResolved)
	if err != nil {
		return nil, fileOpError(err, sourceDisplay, "move")
	}
	if existing, err := os.Lstat(destResolved); err == nil {
		if policy == "create" {
			return nil, fileFailure("FileAlreadyExists", "path already exists", map[string]any{"path": destDisplay})
		}
		if existing.IsDir() && !source.IsDir() {
			return nil, fileFailure("InvalidFileType", "cannot move a file onto a folder",
				map[string]any{"path": destDisplay, "expected": fileKind, "actual": folderKind})
		}
		if !existing.IsDir() && source.IsDir() {
			return nil, fileFailure("InvalidFileType", "cannot move a folder onto a file",
				map[string]any{"path": destDisplay, "expected": folderKind, "actual": classifyMode(existing.Mode())})
		}
	} else if !os.IsNotExist(err) {
		return nil, fileOpError(err, destDisplay, "move")
	}
	if err := os.Rename(sourceResolved, destResolved); err != nil {
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
			return nil, fileFailure("FileSystemUnavailable",
				"cross-device move is not atomic; copy and remove explicitly",
				map[string]any{"path": destDisplay, "operation": "move"})
		}
		return nil, fileOpError(err, sourceDisplay, "move")
	}
	return destDisplay, ctx.Err()
}

func createFolderAction(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(resolved); err == nil {
		if !info.IsDir() {
			return nil, fileFailure("InvalidFileType", "path exists and is not a folder",
				map[string]any{"path": display, "expected": folderKind, "actual": classifyMode(info.Mode())})
		}
		return display, nil
	} else if !os.IsNotExist(err) {
		return nil, fileOpError(err, display, "create")
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return nil, fileOpError(err, display, "create")
	}
	return display, ctx.Err()
}

func removeFileAction(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return nil, fileOpError(err, display, "remove")
	}
	kind := classifyMode(info.Mode())
	if kind != fileKind && kind != linkKind {
		return nil, fileFailure("InvalidFileType", "remove file requires a regular file",
			map[string]any{"path": display, "expected": fileKind, "actual": kind})
	}
	if err := os.Remove(resolved); err != nil {
		return nil, fileOpError(err, display, "remove")
	}
	return display, ctx.Err()
}

func removeFolderAction(ctx context.Context, opts Options, args []any, recursive bool) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	display := args[0].(string)
	resolved, err := filePathResolve(opts, display)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return nil, fileOpError(err, display, "remove")
	}
	if !info.IsDir() {
		return nil, fileFailure("InvalidFileType", "remove folder requires a folder",
			map[string]any{"path": display, "expected": folderKind, "actual": classifyMode(info.Mode())})
	}
	if !recursive {
		children, err := os.ReadDir(resolved)
		if err != nil {
			return nil, fileOpError(err, display, "remove")
		}
		if len(children) > 0 {
			return nil, fileFailure("InvalidFileType", "folder is not empty; remove it recursively instead",
				map[string]any{"path": display, "expected": "empty folder", "actual": "folder with contents"})
		}
		if err := os.Remove(resolved); err != nil {
			return nil, fileOpError(err, display, "remove")
		}
		return display, ctx.Err()
	}
	// Apply the entry limit before deletion begins where enumeration is safe.
	count := 0
	if err := walkNoFollow(resolved, func(string, fs.FileInfo) (bool, error) {
		count++
		if count > maxListEntries {
			return false, fileFailure("FileTraversalLimitExceeded",
				fmt.Sprintf("recursive removal exceeds %d entries", maxListEntries),
				map[string]any{"root": display, "limit": float64(maxListEntries)})
		}
		return false, ctx.Err()
	}); err != nil {
		var typed *typedFailure
		if errors.As(err, &typed) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fileOpError(err, display, "remove")
	}
	if err := os.RemoveAll(resolved); err != nil {
		return nil, fileOpError(err, display, "remove")
	}
	return display, ctx.Err()
}

func registerFilesAction(name string, params []NativeParam, result, description string, failures []string, fn func(context.Context, Options, []any) (any, error)) {
	registerIO("std/files", name, params, result, description, "filesystem", fn)
	op := stdRegistry["std/files"][name]
	op.PossibleFailures = failures
	if spec, ok := fileActionContract[name]; ok {
		op.Effects = append([]string(nil), spec.effects...)
		op.Targets = append([]string(nil), spec.targets...)
	}
	stdRegistry["std/files"][name] = op
}

func init() {
	ioFailures := []string{"FilePermissionDenied", "FileSystemUnavailable", "InvalidFilePath"}
	readFailures := append([]string{"FileNotFound", "InvalidFileType", "FileTooLarge"}, ioFailures...)
	writeFailures := append([]string{"FileAlreadyExists", "FileNotFound", "InvalidFileType", "FileTooLarge"}, ioFailures...)

	registerFilesAction("read_text", []NativeParam{{"path", "text"}}, "text",
		"Reads the complete UTF-8 contents of one regular file (up to 16 MiB), relative to the execution directory. Symlinks and special files are rejected.",
		readFailures, readTextFile)
	registerFilesAction("write_text", []NativeParam{{"path", "text"}, {"contents", "text"}, {"policy", "text"}}, "text",
		"Writes UTF-8 text to one regular file with an explicit create-or-replace policy (up to 16 MiB). Parent directories must exist.",
		writeFailures, func(ctx context.Context, opts Options, args []any) (any, error) {
			return writeText(ctx, opts, args, false)
		})
	registerFilesAction("write_text_atomically", []NativeParam{{"path", "text"}, {"contents", "text"}, {"policy", "text"}}, "text",
		"Writes UTF-8 text through a temporary file in the destination folder, then installs it atomically with an explicit create-or-replace policy. A failure before installation preserves the old file.",
		writeFailures, func(ctx context.Context, opts Options, args []any) (any, error) {
			return writeText(ctx, opts, args, true)
		})
	registerFilesAction("append_text", []NativeParam{{"path", "text"}, {"contents", "text"}}, "text",
		"Appends UTF-8 text to one regular file, creating it when absent; the resulting file stays within the 16 MiB complete-write limit.",
		writeFailures, appendText)
	registerFilesAction("exists", []NativeParam{{"path", "text"}}, "boolean",
		"Reports whether a path itself exists, including a symlink; permission and other filesystem errors fail rather than returning false.",
		ioFailures, entryExists)
	registerFilesAction("inspect", []NativeParam{{"path", "text"}}, "record",
		"Describes one entry as a FileEntry without following a final symbolic link: path, relative_path, name, kind, size, modified_at, depth, and symbolic_link.",
		append([]string{"FileNotFound"}, ioFailures...), inspectEntry)
	registerFilesAction("list", []NativeParam{{"folder", "text"}, {"kind", "text"}}, "list",
		"Returns the immediate children of a folder as FileEntry records, sorted by portable relative path. kind is \"entries\", \"files\", or \"folders\"; results are bounded instead of truncated.",
		append([]string{"FileNotFound", "InvalidFileType", "FileTraversalLimitExceeded"}, ioFailures...), listFolderAction)
	registerFilesAction("copy_file", []NativeParam{{"source", "text"}, {"destination", "text"}, {"policy", "text"}}, "text",
		"Copies one regular file (up to 16 MiB), preserving contents and ordinary permission bits, without following a symbolic-link source. Policy is \"create\" or \"replace\".",
		append([]string{"FileNotFound", "FileAlreadyExists", "InvalidFileType", "FileTooLarge"}, ioFailures...), copyFileAction)
	registerFilesAction("copy_folder", []NativeParam{{"source", "text"}, {"destination", "text"}, {"policy", "text"}, {"exclusions", "list"}}, "text",
		"Copies a folder tree recursively without following symbolic links. Exclusions match slash-separated relative paths or any component. Policy is \"create\" or \"replace\".",
		append([]string{"FileNotFound", "FileAlreadyExists", "InvalidFileType", "FileTraversalLimitExceeded", "FileTooLarge"}, ioFailures...), copyFolderAction)
	registerFilesAction("move", []NativeParam{{"source", "text"}, {"destination", "text"}, {"policy", "text"}}, "text",
		"Relocates one entry by renaming it, with an explicit create-or-replace policy for the destination. Cross-device moves fail instead of degrading into a non-atomic copy.",
		append([]string{"FileNotFound", "FileAlreadyExists", "InvalidFileType"}, ioFailures...), moveEntryAction)
	registerFilesAction("create_folder", []NativeParam{{"path", "text"}}, "text",
		"Ensures a folder exists, creating missing parents; an existing non-folder path fails.",
		append([]string{"InvalidFileType"}, ioFailures...), createFolderAction)
	registerFilesAction("create_folders", []NativeParam{{"path", "text"}}, "text",
		"Creates a folder including every missing parent component; an existing non-folder path fails.",
		append([]string{"InvalidFileType"}, ioFailures...), createFolderAction)
	registerFilesAction("remove_file", []NativeParam{{"path", "text"}}, "text",
		"Removes one regular file or symbolic link (the link itself, never its target).",
		append([]string{"FileNotFound", "InvalidFileType"}, ioFailures...), removeFileAction)
	registerFilesAction("remove_folder", []NativeParam{{"path", "text"}}, "text",
		"Removes one empty folder; removing a folder with contents requires remove_folder_recursively.",
		append([]string{"FileNotFound", "InvalidFileType"}, ioFailures...), func(ctx context.Context, opts Options, args []any) (any, error) {
			return removeFolderAction(ctx, opts, args, false)
		})
	registerFilesAction("remove_folder_recursively", []NativeParam{{"path", "text"}}, "text",
		"Removes a folder and its contents without following symbolic links, after applying an entry-limit safety bound.",
		append([]string{"FileNotFound", "InvalidFileType", "FileTraversalLimitExceeded"}, ioFailures...), func(ctx context.Context, opts Options, args []any) (any, error) {
			return removeFolderAction(ctx, opts, args, true)
		})

	// Compatibility aliases: same implementations, fixed legacy surface.
	registerFilesAction("read", []NativeParam{{"path", "text"}}, "text",
		"Compatibility alias of read_text: reads a regular UTF-8 text file (up to 16 MiB), relative to the execution directory.",
		readFailures, readTextFile)
	registerFilesAction("write", []NativeParam{{"path", "text"}, {"text", "text"}}, "text",
		"Compatibility alias of write_text with the legacy replacing policy: writes text to a regular file. Parent directories must exist.",
		writeFailures, func(ctx context.Context, opts Options, args []any) (any, error) {
			return writeText(ctx, opts, []any{args[0], args[1], "replace"}, false)
		})
}
