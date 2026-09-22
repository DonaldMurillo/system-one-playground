package sos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mustFileFailure asserts err is a typedFailure of the given kind and returns it.
func mustFileFailure(t *testing.T, err error, kind string) *typedFailure {
	t.Helper()
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != kind {
		t.Fatalf("error = %#v, want typed failure %s", err, kind)
	}
	return failure
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileFailuresAreReservedRuntimeKinds(t *testing.T) {
	for _, kind := range []string{
		"FileNotFound", "FileAlreadyExists", "FilePermissionDenied", "FileTooLarge",
		"InvalidFileType", "InvalidFilePath", "FileTraversalLimitExceeded",
		"FileWatchOverflow", "FileSystemUnavailable",
	} {
		if builtInFailures()[kind] == nil {
			t.Errorf("failure %s is not reserved by the runtime", kind)
		}
		diagnostics := Check("define failure " + kind + ":\n  path as text\n")
		if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "reserved by the SysOneScript runtime") {
			t.Errorf("redefining %s produced %v", kind, diagnostics)
		}
	}
}

func TestFilesModuleExposesCoreActionsAndTypedEntries(t *testing.T) {
	ops := stdRegistry["std/files"]
	for _, name := range []string{
		"exists", "read_text", "write_text", "write_text_atomically", "append_text",
		"inspect", "list", "copy_file", "copy_folder", "move", "create_folder",
		"create_folders", "remove_file", "remove_folder", "remove_folder_recursively",
		"read", "write", "discover",
	} {
		if ops[name].Name == "" {
			t.Errorf("std/files action %s is missing", name)
		}
	}
	if len(ops["read_text"].PossibleFailures) == 0 {
		t.Fatal("read_text must declare its possible typed failures")
	}
	module, ok := stdModule("std/files")
	if !ok {
		t.Fatal("std/files module missing")
	}
	entry, change := module.Definitions["FileEntry"], module.Definitions["FileChange"]
	if entry == nil || change == nil {
		t.Fatalf("module definitions = %#v", module.Definitions)
	}
	if !module.Exports["FileEntry"] || !module.Exports["FileChange"] {
		t.Fatal("FileEntry/FileChange are not exported")
	}
	record := map[string]any{
		"path": "a/b.txt", "relative_path": "b.txt", "name": "b.txt", "kind": "file",
		"size": float64(3), "modified_at": time.Now(), "depth": float64(1), "symbolic_link": false,
	}
	if !typeMatchesRef(record, TypeRef{Name: "FileEntry"}, map[string]*RecordDef{"FileEntry": entry}) {
		t.Fatal("entry value does not match the exported FileEntry shape")
	}
	if typeMatchesRef(map[string]any{"path": "x"}, TypeRef{Name: "FileEntry"}, map[string]*RecordDef{"FileEntry": entry}) {
		t.Fatal("FileEntry must stay a closed record")
	}
}

func TestReadTextBoundedAndTyped(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.txt"), "alpha")
	ctx := context.Background()
	got, err := stdRegistry["std/files"]["read_text"].ContextFn(ctx, Options{Dir: dir}, []any{"a.txt"})
	if err != nil || got != "alpha" {
		t.Fatalf("read = %#v %v", got, err)
	}

	_, err = stdRegistry["std/files"]["read_text"].ContextFn(ctx, Options{Dir: dir}, []any{"missing.txt"})
	failure := mustFileFailure(t, err, "FileNotFound")
	if failure.value["path"] != "missing.txt" {
		t.Fatalf("path field = %#v", failure.value["path"])
	}

	_, err = stdRegistry["std/files"]["read_text"].ContextFn(ctx, Options{Dir: dir}, []any{""})
	mustFileFailure(t, err, "InvalidFilePath")

	_, err = stdRegistry["std/files"]["read_text"].ContextFn(ctx, Options{Dir: dir}, []any{"."})
	failure = mustFileFailure(t, err, "InvalidFileType")
	if failure.value["expected"] != "file" || failure.value["actual"] != "folder" {
		t.Fatalf("folder read = %#v", failure.value)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(filepath.Join(dir, "a.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err = stdRegistry["std/files"]["read_text"].ContextFn(ctx, Options{Dir: dir}, []any{"link.txt"})
	failure = mustFileFailure(t, err, "InvalidFileType")
	if failure.value["actual"] != "link" {
		t.Fatalf("symlink read = %#v", failure.value)
	}

	binary := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(binary, []byte{0xff, 0xfe, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = stdRegistry["std/files"]["read_text"].ContextFn(ctx, Options{Dir: dir}, []any{"bin.dat"})
	failure = mustFileFailure(t, err, "InvalidFileType")
	if failure.value["expected"] != "UTF-8 text" {
		t.Fatalf("invalid utf-8 = %#v", failure.value)
	}

	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, make([]byte, maxIOBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = stdRegistry["std/files"]["read_text"].ContextFn(ctx, Options{Dir: dir}, []any{"big.txt"})
	failure = mustFileFailure(t, err, "FileTooLarge")
	if failure.value["limit"] != float64(maxIOBytes) || failure.value["observed"] != float64(maxIOBytes+1) {
		t.Fatalf("size failure = %#v", failure.value)
	}
}

func writeTextVia(t *testing.T, dir string, args ...any) (any, error) {
	t.Helper()
	return stdRegistry["std/files"]["write_text"].ContextFn(context.Background(), Options{Dir: dir}, args)
}

func TestWriteTextPolicyAndLimits(t *testing.T) {
	dir := t.TempDir()
	written, err := writeTextVia(t, dir, "a.txt", "one", "create")
	if err != nil || written != "a.txt" {
		t.Fatalf("create = %#v %v", written, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "one" {
		t.Fatalf("content = %q", data)
	}

	_, err = writeTextVia(t, dir, "a.txt", "two", "create")
	failure := mustFileFailure(t, err, "FileAlreadyExists")
	if failure.value["path"] != "a.txt" {
		t.Fatalf("path = %#v", failure.value["path"])
	}

	if _, err = writeTextVia(t, dir, "a.txt", "three", "replace"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "three" {
		t.Fatalf("replaced content = %q", data)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(filepath.Join(dir, "a.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err = writeTextVia(t, dir, "link.txt", "x", "replace")
	failure = mustFileFailure(t, err, "InvalidFileType")
	if failure.value["expected"] != "file" || failure.value["actual"] != "link" {
		t.Fatalf("symlink write = %#v", failure.value)
	}

	_, err = writeTextVia(t, dir, "missing/child.txt", "x", "create")
	mustFileFailure(t, err, "FileNotFound")

	_, err = writeTextVia(t, dir, "big.txt", strings.Repeat("a", maxIOBytes+1), "create")
	failure = mustFileFailure(t, err, "FileTooLarge")
	if failure.value["observed"] != float64(maxIOBytes+1) {
		t.Fatalf("write limit = %#v", failure.value)
	}

	if _, err = writeTextVia(t, dir, "p.txt", "x", "merge"); err == nil || strings.Contains(err.Error(), "FileNotFound") {
		t.Fatalf("policy validation = %v", err)
	}
}

func TestWriteTextAtomically(t *testing.T) {
	dir := t.TempDir()
	op := stdRegistry["std/files"]["write_text_atomically"]
	written, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"a.txt", "one", "create"})
	if err != nil || written != "a.txt" {
		t.Fatalf("create = %#v %v", written, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "one" {
		t.Fatalf("content = %q", data)
	}

	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"a.txt", "two", "replace"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "two" {
		t.Fatalf("replaced content = %q", data)
	}

	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"a.txt", "three", "create"})
	mustFileFailure(t, err, "FileAlreadyExists")
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "two" {
		t.Fatalf("existing file damaged by failed create: %q", data)
	}

	if os.Geteuid() != 0 {
		nested := filepath.Join(dir, "nested")
		if err := os.Mkdir(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, filepath.Join(nested, "keep.txt"), "kept")
		// chmod only blocks writes where the host honors directory permission
		// bits for the current user; verify the mode took effect first.
		if err := os.Chmod(nested, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(nested, 0o755) })
		if info, statErr := os.Stat(nested); statErr == nil && info.Mode().Perm() == 0o555 {
			_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"nested/keep.txt", "new", "replace"})
			mustFileFailure(t, err, "FilePermissionDenied")
			if data, _ := os.ReadFile(filepath.Join(nested, "keep.txt")); string(data) != "kept" {
				t.Fatalf("old file not preserved: %q", data)
			}
		}
	}

	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"missing/child.txt", "x", "create"})
	mustFileFailure(t, err, "FileNotFound")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".sos") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestAppendText(t *testing.T) {
	dir := t.TempDir()
	op := stdRegistry["std/files"]["append_text"]
	appended, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"log.txt", "first\n"})
	if err != nil || appended != "log.txt" {
		t.Fatalf("append create = %#v %v", appended, err)
	}
	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"log.txt", "second\n"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "log.txt")); string(data) != "first\nsecond\n" {
		t.Fatalf("log = %q", data)
	}

	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"log.txt", strings.Repeat("a", maxIOBytes)})
	failure := mustFileFailure(t, err, "FileTooLarge")
	if failure.value["observed"] != float64(maxIOBytes+13) {
		t.Fatalf("append limit = %#v", failure.value)
	}

	if err := os.Symlink(filepath.Join(dir, "log.txt"), filepath.Join(dir, "link.log")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"link.log", "x"})
	mustFileFailure(t, err, "InvalidFileType")
}

func TestInspectEntryShapes(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.txt"), "alpha")
	op := stdRegistry["std/files"]["inspect"]
	entry, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	file := entry.(map[string]any)
	if file["kind"] != "file" || file["name"] != "a.txt" || file["relative_path"] != "a.txt" || file["depth"] != float64(0) {
		t.Fatalf("file entry = %#v", file)
	}
	if file["size"] != float64(5) || file["symbolic_link"] != false {
		t.Fatalf("file entry = %#v", file)
	}
	if _, ok := file["modified_at"].(time.Time); !ok {
		t.Fatalf("modified_at = %#v", file["modified_at"])
	}

	folder, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"."})
	if err != nil {
		t.Fatal(err)
	}
	dirEntry := folder.(map[string]any)
	if dirEntry["kind"] != "folder" {
		t.Fatalf("folder entry = %#v", dirEntry)
	}
	if _, present := dirEntry["size"]; present {
		t.Fatalf("folders have no size: %#v", dirEntry)
	}

	if err := os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linked, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"link.txt"})
	if err != nil {
		t.Fatal(err)
	}
	linkEntry := linked.(map[string]any)
	if linkEntry["kind"] != "link" || linkEntry["symbolic_link"] != true {
		t.Fatalf("link entry = %#v", linkEntry)
	}

	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"nope.txt"})
	mustFileFailure(t, err, "FileNotFound")
}

func TestListEntriesSortedKindsAndBound(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "b.txt"), "b")
	mustWriteFile(t, filepath.Join(dir, "ünï.txt"), "u")
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "a", "c.txt"), "c")
	if err := os.Symlink(filepath.Join(dir, "b.txt"), filepath.Join(dir, "z.link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	op := stdRegistry["std/files"]["list"]
	result, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{".", "entries"})
	if err != nil {
		t.Fatal(err)
	}
	entries := result.([]any)
	var names []string
	for _, item := range entries {
		entry := item.(map[string]any)
		if entry["depth"] != float64(1) || entry["path"].(string) != filepath.ToSlash(filepath.Join(dir, entry["name"].(string))) {
			t.Fatalf("entry = %#v", entry)
		}
		names = append(names, entry["name"].(string)+"("+entry["kind"].(string)+")")
	}
	if strings.Join(names, ",") != "a(folder),b.txt(file),z.link(link),ünï.txt(file)" {
		t.Fatalf("entries = %v", names)
	}

	result, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{".", "files"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.([]any)) != 2 {
		t.Fatalf("files = %#v", result)
	}
	result, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{".", "folders"})
	if err != nil {
		t.Fatal(err)
	}
	if list := result.([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "a" {
		t.Fatalf("folders = %#v", result)
	}

	if _, err := listFolder(context.Background(), Options{Dir: dir}, ".", "entries", 2); err == nil {
		t.Fatal("bound must fail instead of truncating")
	} else {
		failure := mustFileFailure(t, err, "FileTraversalLimitExceeded")
		if failure.value["limit"] != float64(2) || failure.value["root"] != "." {
			t.Fatalf("limit failure = %#v", failure.value)
		}
	}

	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"missing", "entries"})
	mustFileFailure(t, err, "FileNotFound")
	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"b.txt", "entries"})
	failure := mustFileFailure(t, err, "InvalidFileType")
	if failure.value["expected"] != "folder" {
		t.Fatalf("root type = %#v", failure.value)
	}
	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{".", "everything"}); err == nil {
		t.Fatal("unknown kind must fail")
	}
}

func TestCopyFilePoliciesAndPermissions(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "report.txt")
	mustWriteFile(t, source, "alpha")
	if err := os.Chmod(source, 0o741); err != nil {
		t.Fatal(err)
	}
	op := stdRegistry["std/files"]["copy_file"]

	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"report.txt", "copy.txt", "create"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "copy.txt")); string(data) != "alpha" {
		t.Fatalf("copy = %q", data)
	}
	if source, err := os.Stat(source); err == nil && source.Mode().Perm() == 0o741 {
		// The host represents permission bits; the copy must preserve them.
		if info, err := os.Stat(filepath.Join(dir, "copy.txt")); err != nil || info.Mode().Perm() != 0o741 {
			t.Fatalf("copied permissions = %v %v", info.Mode(), err)
		}
	}

	_, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"report.txt", "copy.txt", "create"})
	mustFileFailure(t, err, "FileAlreadyExists")
	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"report.txt", "copy.txt", "replace"}); err != nil {
		t.Fatal(err)
	}

	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"missing.txt", "out.txt", "create"})
	mustFileFailure(t, err, "FileNotFound")

	if err := os.Symlink(source, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"link.txt", "out.txt", "create"})
	failure := mustFileFailure(t, err, "InvalidFileType")
	if failure.value["actual"] != "link" {
		t.Fatalf("symlink source = %#v", failure.value)
	}
	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"report.txt", "link.txt", "replace"}); err == nil {
		t.Fatal("symlink destination must be rejected")
	} else {
		mustFileFailure(t, err, "InvalidFileType")
	}

	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, maxIOBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"big.bin", "big-copy.bin", "create"})
	mustFileFailure(t, err, "FileTooLarge")
}

func TestCopyFolderRecursive(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "src", "a.txt"), "a")
	mustWriteFile(t, filepath.Join(dir, "src", "nested", "b.txt"), "b")
	mustWriteFile(t, filepath.Join(dir, "src", "skip", "c.txt"), "c")
	if err := os.Symlink(filepath.Join(dir, "src", "a.txt"), filepath.Join(dir, "src", "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	op := stdRegistry["std/files"]["copy_folder"]

	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"src", "dest", "create", []any{"skip"}}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "dest", "nested", "b.txt")); string(data) != "b" {
		t.Fatalf("nested copy missing: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "dest", "skip")); !os.IsNotExist(err) {
		t.Fatalf("excluded folder copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dest", "link.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink copied: %v", err)
	}

	_, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"src", "dest", "create", []any{}})
	mustFileFailure(t, err, "FileAlreadyExists")
	mustWriteFile(t, filepath.Join(dir, "blocker.txt"), "x")
	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"src", "blocker.txt", "replace", []any{}})
	mustFileFailure(t, err, "InvalidFileType")
	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"missing", "dest2", "create", []any{}})
	mustFileFailure(t, err, "FileNotFound")
	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"src", "dest3", "create", []any{"["}})
	if err == nil || !strings.Contains(err.Error(), "exclusion") {
		t.Fatalf("bad pattern = %v", err)
	}
}

func TestMoveEntry(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "draft.txt"), "body")
	op := stdRegistry["std/files"]["move"]

	if moved, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"draft.txt", "published.txt", "create"}); err != nil || moved != "published.txt" {
		t.Fatalf("move = %#v %v", moved, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "published.txt")); string(data) != "body" {
		t.Fatalf("moved content = %q", data)
	}
	if _, err := os.Lstat(filepath.Join(dir, "draft.txt")); !os.IsNotExist(err) {
		t.Fatal("source survived move")
	}

	mustWriteFile(t, filepath.Join(dir, "draft.txt"), "again")
	_, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"draft.txt", "published.txt", "create"})
	mustFileFailure(t, err, "FileAlreadyExists")
	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"draft.txt", "published.txt", "replace"}); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "folder", "keep.txt"), "keep")
	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"folder", "moved-folder", "create"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "moved-folder", "keep.txt")); string(data) != "keep" {
		t.Fatalf("folder move = %q", data)
	}

	_, err = op.ContextFn(context.Background(), Options{Dir: dir}, []any{"gone.txt", "x.txt", "create"})
	mustFileFailure(t, err, "FileNotFound")
	if _, err := op.ContextFn(context.Background(), Options{Dir: dir}, []any{"published.txt", "moved-folder", "replace"}); err == nil {
		t.Fatal("file onto folder must fail")
	} else {
		mustFileFailure(t, err, "InvalidFileType")
	}
}

func TestCreateAndRemoveEntries(t *testing.T) {
	dir := t.TempDir()
	files := stdRegistry["std/files"]
	ctx := context.Background()
	opts := Options{Dir: dir}

	if created, err := files["create_folder"].ContextFn(ctx, opts, []any{"out"}); err != nil || created != "out" {
		t.Fatalf("create = %#v %v", created, err)
	}
	if _, err := files["create_folder"].ContextFn(ctx, opts, []any{"out"}); err != nil {
		t.Fatalf("idempotent create = %v", err)
	}
	if _, err := files["create_folders"].ContextFn(ctx, opts, []any{"deep/nested/tree"}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "plain.txt"), "x")
	_, err := files["create_folder"].ContextFn(ctx, opts, []any{"plain.txt"})
	mustFileFailure(t, err, "InvalidFileType")

	mustWriteFile(t, filepath.Join(dir, "temp.txt"), "x")
	if removed, err := files["remove_file"].ContextFn(ctx, opts, []any{"temp.txt"}); err != nil || removed != "temp.txt" {
		t.Fatalf("remove = %#v %v", removed, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = files["remove_file"].ContextFn(ctx, opts, []any{"plain"})
	failure := mustFileFailure(t, err, "InvalidFileType")
	if failure.value["expected"] != "file" {
		t.Fatalf("remove file on folder = %#v", failure.value)
	}
	if err := os.Symlink(filepath.Join(dir, "plain.txt"), filepath.Join(dir, "gone.link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := files["remove_file"].ContextFn(ctx, opts, []any{"gone.link"}); err != nil {
		t.Fatalf("remove link = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plain.txt")); err != nil {
		t.Fatalf("link removal followed the link: %v", err)
	}

	if _, err := files["remove_folder"].ContextFn(ctx, opts, []any{"out"}); err != nil {
		t.Fatalf("remove empty folder = %v", err)
	}
	mustWriteFile(t, filepath.Join(dir, "tree", "a.txt"), "a")
	_, err = files["remove_folder"].ContextFn(ctx, opts, []any{"tree"})
	failure = mustFileFailure(t, err, "InvalidFileType")
	if failure.value["expected"] != "empty folder" {
		t.Fatalf("non-empty folder = %#v", failure.value)
	}
	if _, err := files["remove_folder_recursively"].ContextFn(ctx, opts, []any{"tree"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tree")); !os.IsNotExist(err) {
		t.Fatalf("tree survived: %v", err)
	}

	outside := filepath.Join(dir, "outside.txt")
	mustWriteFile(t, outside, "keep")
	mustWriteFile(t, filepath.Join(dir, "linked", "inner.txt"), "i")
	if err := os.Symlink(outside, filepath.Join(dir, "linked", "escape.link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := files["remove_folder_recursively"].ContextFn(ctx, opts, []any{"linked"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("recursive removal followed a link: %q %v", data, err)
	}
}

func runFileActionScript(t *testing.T, dir, source string) *Result {
	t.Helper()
	program, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	result, err := Run(context.Background(), program, Options{Dir: dir, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}})
	if err != nil {
		t.Fatalf("run = %v", err)
	}
	return result
}

func TestScriptTypedEntriesAndFailures(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.txt"), "alpha")
	result := runFileActionScript(t, dir, `import "std/files" as files
to describe with entry as FileEntry returning text:
  finish with name of entry + ":" + kind of entry
call files.inspect with "a.txt" called information
call describe with information called label
to load with name as text returning text may fail with FileNotFound, InvalidFileType, FileTooLarge, FilePermissionDenied, FileSystemUnavailable, InvalidFilePath:
  call files.read_text with name called contents
    on failure FileNotFound using path:
      pass failure on
  finish with contents
call load with "missing.txt" called text
  on failure FileNotFound using path:
    recover with "fallback:" + path
call files.list with ".", "files" called found
make total as length of found
`)
	if result.Variables["label"] != "a.txt:file" {
		t.Fatalf("label = %#v", result.Variables["label"])
	}
	if result.Variables["text"] != "fallback:missing.txt" {
		t.Fatalf("text = %#v", result.Variables["text"])
	}
	if result.Variables["total"] != 1.0 {
		t.Fatalf("total = %#v", result.Variables["total"])
	}
}

func TestCompatibilityAliasesShareImplementation(t *testing.T) {
	dir := t.TempDir()
	result := runFileActionScript(t, dir, `import "std/files" as files
call files.write with "a.txt", "one" called written
call files.read with "a.txt" called first
call files.write with "a.txt", "two" called rewritten
call files.read with "a.txt" called second
call files.exists with "a.txt" called present
call files.exists with "gone.txt" called absent
`)
	if result.Variables["written"] != "a.txt" || result.Variables["first"] != "one" || result.Variables["second"] != "two" {
		t.Fatalf("alias results = %#v", result.Variables)
	}
	if result.Variables["present"] != true || result.Variables["absent"] != false {
		t.Fatalf("exists results = %#v", result.Variables)
	}
}
