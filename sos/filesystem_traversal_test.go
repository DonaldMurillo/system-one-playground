package sos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"
)

func makeWalkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeWalkTree(t, root)
	return root
}

func writeWalkTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"b.txt":                 "b",
		"a.txt":                 "a",
		"a/b.go":                "package a",
		"a/c/y.go":              "package c",
		"a/c/z.go":              "package c",
		".git/x":                "git",
		"vendor/lib/lib.go":     "vendored",
		"notes.tmp":             "scratch",
		"a/c/old.tmp":           "scratch",
		"deep/ünïcode-náme.txt": "unicode",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func walkSpecForTest(t *testing.T, opts Options, root string, o traversalOptions) traversalSpec {
	t.Helper()
	o.root = root
	spec, err := parseTraversalSpec(opts, o)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func collectWalk(t *testing.T, spec traversalSpec) []map[string]any {
	t.Helper()
	walker, err := newTreeWalker(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var out []map[string]any
	for {
		entry, more, err := walker.nextEntry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return out
		}
		out = append(out, entry)
	}
}

func relPaths(entries []map[string]any) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry["relative_path"].(string))
	}
	return out
}

func TestFileWalkIsStableDepthFirstLexicalOrder(t *testing.T) {
	root := makeWalkTree(t)
	entries := collectWalk(t, walkSpecForTest(t, Options{Dir: root}, root, traversalOptions{exclude: []string{".git", "vendor"}}))
	want := []string{"a", "a/b.go", "a/c", "a/c/old.tmp", "a/c/y.go", "a/c/z.go", "a.txt", "b.txt", "deep", "deep/ünïcode-náme.txt", "notes.tmp"}
	if got := relPaths(entries); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v", got)
	}
	first := entries[0]
	if first["kind"] != "folder" || first["name"] != "a" || first["depth"] != 1.0 || first["symbolic_link"] != false {
		t.Fatalf("folder entry = %#v", first)
	}
	if path := first["path"].(string); !filepath.IsAbs(path) || !strings.HasSuffix(path, "/a") || strings.Contains(path, "\\") {
		t.Fatalf("path %q is not a portable absolute path", path)
	}
	file := entries[1]
	if file["kind"] != "file" || file["size"] != 9.0 || file["depth"] != 2.0 {
		t.Fatalf("file entry = %#v", file)
	}
	if _, ok := file["modified_at"].(time.Time); !ok {
		t.Fatalf("modified_at missing: %#v", file)
	}
	if _, present := file["missing"]; present {
		t.Fatal("unexpected field")
	}
}

func TestFileWalkKindsFilterDefaultsToFilesAndFolders(t *testing.T) {
	root := makeWalkTree(t)
	link := filepath.Join(root, "a", "link.txt")
	if err := os.Symlink(filepath.Join(root, "a.txt"), link); err != nil {
		t.Fatal(err)
	}
	spec := walkSpecForTest(t, Options{Dir: root}, root, traversalOptions{exclude: []string{".git", "vendor", "*.tmp", "deep"}})
	for _, entry := range collectWalk(t, spec) {
		if entry["kind"] == "link" {
			t.Fatalf("default kinds reported a link: %#v", entry)
		}
	}
	spec.kinds = map[string]bool{fileKind: true, folderKind: true, linkKind: true}
	seen := map[string]bool{}
	for _, entry := range collectWalk(t, spec) {
		seen[entry["kind"].(string)] = true
	}
	if !seen["link"] {
		t.Fatal("explicit link kind missing from results")
	}
}

func TestFileWalkIncludeAndExcludePatterns(t *testing.T) {
	root := makeWalkTree(t)
	base := traversalOptions{exclude: []string{".git", "vendor", "*.tmp", "deep"}}

	spec := walkSpecForTest(t, Options{Dir: root}, root, base)
	spec.include = []string{"*.go"}
	if got := relPaths(collectWalk(t, spec)); strings.Join(got, "|") != "a/b.go|a/c/y.go|a/c/z.go" {
		t.Fatalf("include *.go = %v", got)
	}

	spec = walkSpecForTest(t, Options{Dir: root}, root, base)
	spec.include = []string{"a/*.go"}
	if got := relPaths(collectWalk(t, spec)); strings.Join(got, "|") != "a/b.go" {
		t.Fatalf("include a/*.go = %v", got)
	}

	spec = walkSpecForTest(t, Options{Dir: root}, root, base)
	spec.exclude = append(spec.exclude, "a/c")
	if got := relPaths(collectWalk(t, spec)); strings.Join(got, "|") != "a|a/b.go|a.txt|b.txt" {
		t.Fatalf("exclude a/c = %v", got)
	}

	spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root, exclude: []string{"a/**"}})
	if err == nil || !strings.Contains(err.Error(), "**") {
		t.Fatalf("recursive glob error = %v", err)
	}
}

func TestFileWalkDepthLimit(t *testing.T) {
	root := makeWalkTree(t)
	spec := walkSpecForTest(t, Options{Dir: root}, root, traversalOptions{exclude: []string{".git", "vendor", "*.tmp", "deep"}, depth: 2})
	if got := relPaths(collectWalk(t, spec)); strings.Join(got, "|") != "a|a/b.go|a/c|a.txt|b.txt" {
		t.Fatalf("depth 2 = %v", got)
	}
}

func TestWalkRootMustBeAnExistingFolder(t *testing.T) {
	dir := t.TempDir()
	if _, err := parseTraversalSpec(Options{Dir: dir}, traversalOptions{root: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := parseTraversalSpec(Options{Dir: dir}, traversalOptions{root: "missing"}); err == nil {
		t.Fatal("relative missing root accepted")
	}
	plain := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var failure *typedFailure
	_, err := parseTraversalSpec(Options{Dir: dir}, traversalOptions{root: "file.txt"})
	if !errors.As(err, &failure) || failure.kind != "InvalidFileType" || failure.value["expected"] != "folder" {
		t.Fatalf("file root error = %#v", err)
	}
}

func TestMaterializedWalkBoundFailsInsteadOfTruncating(t *testing.T) {
	root := makeWalkTree(t)
	spec := walkSpecForTest(t, Options{Dir: root}, root, traversalOptions{exclude: []string{".git", "vendor", "*.tmp", "deep"}})
	entries, err := walkFileTree(context.Background(), spec, 7)
	if err != nil || len(entries) != 7 {
		t.Fatalf("exact bound: %d %v", len(entries), err)
	}
	_, err = walkFileTree(context.Background(), spec, 3)
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "FileTraversalLimitExceeded" || failure.value["limit"] != 3.0 || failure.value["root"] == "" {
		t.Fatalf("limit failure = %#v", err)
	}
	if _, err := walkFileTree(context.Background(), spec, 0); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("zero bound error = %v", err)
	}
}

func TestFileStreamIsPullBasedWithIndependentOwnership(t *testing.T) {
	root := makeWalkTree(t)
	spec := walkSpecForTest(t, Options{Dir: root}, root, traversalOptions{exclude: []string{".git", "vendor", "*.tmp", "deep"}})
	first := newFileStreamSource(spec)
	second := newFileStreamSource(spec)
	item, more, err := first.next(context.Background())
	if err != nil || !more || item.(map[string]any)["name"] != "a" {
		t.Fatalf("first item = %#v %v", item, err)
	}
	if err := first.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, more, err := first.next(context.Background()); err != nil || more {
		t.Fatalf("canceled source continued: %v %v", more, err)
	}
	var got []string
	for {
		item, more, err := second.next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		got = append(got, item.(map[string]any)["relative_path"].(string))
	}
	if len(got) != 7 {
		t.Fatalf("second source saw %d entries: %v", len(got), got)
	}
}

func TestFileStreamCancellationStopsTheWalkPromptly(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 500; i++ {
		if err := os.WriteFile(filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('0'+i/26))+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root})
	if err != nil {
		t.Fatal(err)
	}
	source := newFileStreamSource(spec)
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := source.next(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	started := time.Now()
	if _, _, err := source.next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("cancellation took %v", time.Since(started))
	}
}

func TestWalkSkipsEntriesThatDisappearMidTraversal(t *testing.T) {
	root := t.TempDir()
	var names []string
	for i := 0; i < 4; i++ {
		name := "keep" + string(rune('1'+i)) + ".txt"
		names = append(names, name)
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root})
	if err != nil {
		t.Fatal(err)
	}
	walker, err := newTreeWalker(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, more, err := walker.nextEntry(ctx)
	if err != nil || !more {
		t.Fatalf("first entry: %v", err)
	}
	got := []string{first["name"].(string)}
	// keep1 was returned; remove keep2..keep4 after the root listing was read.
	for _, name := range names[1:] {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for {
		entry, more, err := walker.nextEntry(ctx)
		if err != nil {
			t.Fatalf("disappearance treated as failure: %v", err)
		}
		if !more {
			break
		}
		got = append(got, entry["name"].(string))
	}
	if strings.Join(got, ",") != "keep1.txt" {
		t.Fatalf("survivors = %v", got)
	}
}

func TestWalkPermissionDeniedIsATypedFailure(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("chmod 0000 does not block directory reads on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	root := makeWalkTree(t)
	if err := os.Chmod(filepath.Join(root, "a", "c"), 0o0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "a", "c"), 0o755) })
	spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root})
	if err != nil {
		t.Fatal(err)
	}
	walker, err := newTreeWalker(spec)
	if err != nil {
		t.Fatal(err)
	}
	var failure *typedFailure
	for {
		_, _, err := walker.nextEntry(context.Background())
		if err != nil {
			if !errors.As(err, &failure) || failure.kind != "FilePermissionDenied" || failure.value["operation"] != "traverse" {
				t.Fatalf("permission failure = %#v", err)
			}
			break
		}
	}
}

func TestWalkFollowsLinksWithoutCycles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a/b/loop -> a, a/self -> a: both cycles must be detected and skipped.
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "a", "b", "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "a", "self")); err != nil {
		t.Fatal(err)
	}
	spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root, follow: true})
	if err != nil {
		t.Fatal(err)
	}
	walker, err := newTreeWalker(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []string
	for {
		entry, more, err := walker.nextEntry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		got = append(got, entry["relative_path"].(string))
	}
	if strings.Join(got, "|") != "a|a/b|a/b/leaf.txt|a/b/loop|a/self" {
		t.Fatalf("cycle-following order = %v", got)
	}
}

func TestWalkRejectsLinkEscapeByDefault(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root, follow: true})
	if err != nil {
		t.Fatal(err)
	}
	walker, err := newTreeWalker(spec)
	if err != nil {
		t.Fatal(err)
	}
	var failure *typedFailure
	for {
		_, _, err := walker.nextEntry(context.Background())
		if err != nil {
			if !errors.As(err, &failure) || failure.kind != "InvalidFilePath" || !strings.Contains(failure.value["reason"].(string), "escapes") {
				t.Fatalf("escape failure = %#v", err)
			}
			if failure.value["path"] == "" {
				t.Fatal("escape failure missing path")
			}
			break
		}
	}

	spec.allowEscape = true
	entries := collectWalk(t, spec)
	found := false
	for _, entry := range entries {
		if entry["relative_path"] == "escape/secret.txt" && entry["kind"] == "file" && entry["symbolic_link"] == false {
			found = true
		}
	}
	if !found {
		t.Fatalf("permitted escape missing: %v", relPaths(entries))
	}
}

func TestStdFilesModuleExposesWalkAndStreams(t *testing.T) {
	ops := stdRegistry["std/files"]
	if ops["walk"].ContextFn == nil || ops["walk"].Result != "list" {
		t.Fatalf("walk op = %#v", ops["walk"])
	}
	if !strings.HasPrefix(ops["stream"].Result, "stream of ") || ops["stream"].StreamFn == nil {
		t.Fatalf("stream op = %#v", ops["stream"])
	}
	if !strings.HasPrefix(ops["watch"].Result, "stream of ") || ops["watch"].StreamFn == nil {
		t.Fatalf("watch op = %#v", ops["watch"])
	}
	if len(ops["watch"].Targets) != 1 || ops["watch"].Targets[0] != "native" {
		t.Fatalf("watch targets = %v", ops["watch"].Targets)
	}
	root := makeWalkTree(t)
	result, err := ops["walk"].ContextFn(context.Background(), Options{Dir: root}, []any{
		root, 100.0, []any{}, []any{".git", "vendor", "*.tmp", "deep"}, []any{}, 0.0, false,
	})
	if err != nil {
		t.Fatal(err)
	}
	list := result.([]any)
	if len(list) != 7 {
		t.Fatalf("walk via module = %d entries", len(list))
	}
	if entry := list[0].(map[string]any); entry["kind"] != "folder" || entry["relative_path"] != "a" {
		t.Fatalf("first entry = %#v", entry)
	}
}

func runFileScript(t *testing.T, dir, source string) (*Result, error) {
	t.Helper()
	program, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	return Run(context.Background(), program, Options{Dir: dir, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}})
}

func TestLanguageStreamsFilesAndStopsReadingWithContinuation(t *testing.T) {
	tree := t.TempDir()
	writeWalkTree(t, tree)
	source := `import "std/files" as files
stream files.stream with ".", [], [".git", "vendor", "*.tmp", "deep"], [], 0, false called source
make stopped false
for each entry from source:
  assign stopped true
  stop reading
make after "continued"
`
	result, err := runFileScript(t, tree, source)
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["stopped"] != true || result.Variables["after"] != "continued" {
		t.Fatalf("variables = %#v", result.Variables)
	}
}

func TestLanguageWalkCollectsBoundedEntries(t *testing.T) {
	tree := t.TempDir()
	writeWalkTree(t, tree)
	source := `import "std/files" as files
call files.walk with ".", 100, [], [".git", "vendor", "*.tmp", "deep"], [], 0, false called entries
make count length of entries
`
	result, err := runFileScript(t, tree, source)
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["count"] != 7.0 {
		t.Fatalf("count = %#v", result.Variables["count"])
	}
}
