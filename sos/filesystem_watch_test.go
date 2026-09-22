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

const watchTestInterval = 12 * time.Millisecond

// watchFor drains the watcher until keep returns true or the deadline passes.
func watchFor(t *testing.T, source *fileWatchSource, deadline time.Duration, keep func([]map[string]any) bool) ([]map[string]any, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	var seen []map[string]any
	for {
		item, more, err := source.next(ctx)
		if err != nil {
			return seen, err
		}
		if !more {
			return seen, nil
		}
		seen = append(seen, item.(map[string]any))
		if keep(seen) {
			return seen, nil
		}
	}
}

func changeSummaries(changes []map[string]any) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		parts = append(parts, change["kind"].(string)+":"+change["relative_path"].(string))
	}
	return strings.Join(parts, "|")
}

func TestWatchStartupFailureCleansUpPartialSetup(t *testing.T) {
	dir := t.TempDir()
	spec, err := parseTraversalSpec(Options{Dir: dir}, traversalOptions{root: filepath.Join(dir, "missing")})
	if err == nil {
		t.Fatal("spec for missing root parsed")
	}
	source, err := newFileWatchSource(context.Background(), spec, watchTestInterval, 8)
	if err == nil || source != nil {
		t.Fatalf("missing root opened a watcher: %v %v", source, err)
	}
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "FileNotFound" {
		t.Fatalf("startup failure = %#v", err)
	}

	plain := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileSpec, err := parseTraversalSpec(Options{Dir: dir}, traversalOptions{root: "file.txt"})
	if err == nil {
		if _, err := newFileWatchSource(context.Background(), fileSpec, watchTestInterval, 8); err == nil {
			t.Fatal("file root opened a watcher")
		}
	}
}

func newTestWatcher(t *testing.T, root string, o traversalOptions) *fileWatchSource {
	t.Helper()
	o.root = root
	spec, err := parseTraversalSpec(Options{Dir: root}, o)
	if err != nil {
		t.Fatal(err)
	}
	source, err := newFileWatchSource(context.Background(), spec, watchTestInterval, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.cancel(context.Background()) })
	return source
}

func waitForChangeKind(t *testing.T, source *fileWatchSource, kinds ...string) []map[string]any {
	t.Helper()
	want := strings.Join(kinds, "|")
	changes, err := watchFor(t, source, 4*time.Second, func(seen []map[string]any) bool {
		return strings.Contains(changeSummaries(seen), want)
	})
	if err != nil {
		t.Fatalf("watch error while waiting for %s: %v", want, err)
	}
	if !strings.Contains(changeSummaries(changes), want) {
		t.Fatalf("changes = %s, waiting for %s", changeSummaries(changes), want)
	}
	return changes
}

func TestWatchEmitsCreatedModifiedAndRemovedChanges(t *testing.T) {
	root := t.TempDir()
	source := newTestWatcher(t, root, traversalOptions{})

	target := filepath.Join(root, "note.txt")
	if err := os.WriteFile(target, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := waitForChangeKind(t, source, "created:note.txt")
	change := created[len(created)-1]
	if change["entry_kind"] != fileKind || change["path"] == "" {
		t.Fatalf("created change = %#v", change)
	}
	if observed, ok := change["observed_at"].(time.Time); !ok || observed.IsZero() {
		t.Fatalf("observed_at missing: %#v", change)
	}

	if err := os.WriteFile(target, []byte("two with more bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, source, "modified:note.txt")

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	removed := waitForChangeKind(t, source, "removed:note.txt")
	for _, change := range removed {
		if change["relative_path"] == "note.txt" && change["entry_kind"] == "" {
			t.Fatalf("removed change lost entry kind: %#v", change)
		}
	}
}

func TestWatchBatchesAreLexicallySorted(t *testing.T) {
	root := t.TempDir()
	source := newTestWatcher(t, root, traversalOptions{})
	for _, name := range []string{"c.txt", "a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := watchFor(t, source, 4*time.Second, func(seen []map[string]any) bool {
		return len(seen) >= 3
	})
	if err != nil {
		t.Fatal(err)
	}
	created := []string{}
	for _, change := range changes {
		if change["kind"] == "created" {
			created = append(created, change["relative_path"].(string))
		}
	}
	for i := 1; i < len(created); i++ {
		if created[i] < created[i-1] {
			t.Fatalf("batch not sorted: %v", created)
		}
	}
}

func TestWatchReportsRecursiveNestedFolders(t *testing.T) {
	root := t.TempDir()
	source := newTestWatcher(t, root, traversalOptions{})
	nested := filepath.Join(root, "in", "deep", "leaf.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes := waitForChangeKind(t, source, "created:in/deep/leaf.txt")
	summary := changeSummaries(changes)
	if !strings.Contains(summary, "created:in") || !strings.Contains(summary, "created:in/deep") {
		t.Fatalf("recursive batch = %s", summary)
	}
}

func TestWatchAppliesIncludeAndExcludeFilters(t *testing.T) {
	root := t.TempDir()
	source := newTestWatcher(t, root, traversalOptions{include: []string{"*.pdf"}, exclude: []string{"junk"}})
	if err := os.WriteFile(filepath.Join(root, "keep.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "junk", "hidden.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	drain, cancelDrain := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelDrain()
	delivered := 0
	for {
		item, more, err := source.next(drain)
		if err != nil || !more {
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			break
		}
		change := item.(map[string]any)
		if change["relative_path"] != "keep.pdf" || change["kind"] != "created" {
			t.Fatalf("filtered watcher delivered %#v", change)
		}
		delivered++
	}
	if delivered != 1 {
		t.Fatalf("filtered watcher delivered %d changes", delivered)
	}
}

func TestWatchOverflowSignalsRescanAndKeepsWatching(t *testing.T) {
	root := t.TempDir()
	// One batch slot: a slow consumer that never reads forces overflow.
	source, err := func() (*fileWatchSource, error) {
		spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root})
		if err != nil {
			return nil, err
		}
		return newFileWatchSource(context.Background(), spec, watchTestInterval, 1)
	}()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.cancel(context.Background()) })

	for i := range 3 {
		if err := os.WriteFile(filepath.Join(root, "early.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			waitForChangeKind(t, source, "created:early.txt")
		}
		time.Sleep(3 * watchTestInterval)
	}
	if err := os.Remove(filepath.Join(root, "early.txt")); err != nil {
		t.Fatal(err)
	}
	// The overflowed stream must report that it dropped observations...
	overflow := waitForChangeKind(t, source, "overflowed")
	if overflow[len(overflow)-1]["path"] == "" {
		t.Fatalf("overflow change missing root path: %#v", overflow[len(overflow)-1])
	}
	// ...and keep delivering future changes so the consumer can rescan.
	if err := os.WriteFile(filepath.Join(root, "after-overflow.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, source, "created:after-overflow.txt")
}

func TestWatchersAreIndependentlyOwned(t *testing.T) {
	root := t.TempDir()
	first := newTestWatcher(t, root, traversalOptions{})
	second := newTestWatcher(t, root, traversalOptions{})

	if err := first.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, more, err := first.next(context.Background()); err != nil || more {
		t.Fatalf("canceled watcher continued: %v %v", more, err)
	}
	if err := os.WriteFile(filepath.Join(root, "shared.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, second, "created:shared.txt")
}

func TestWatchCancelStopsThePollingGoroutine(t *testing.T) {
	root := t.TempDir()
	source := newTestWatcher(t, root, traversalOptions{})
	if err := source.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.stopped():
	case <-time.After(2 * time.Second):
		t.Fatal("polling goroutine did not stop after cancel")
	}
	if _, more, err := source.next(context.Background()); err != nil || more {
		t.Fatalf("canceled watcher next = %v %v", more, err)
	}
}

func TestWatchRootDeletionIsATerminalTypedFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vanishing"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec, err := parseTraversalSpec(Options{Dir: filepath.Dir(root)}, traversalOptions{root: filepath.Join(filepath.Base(root), "vanishing")})
	if err != nil {
		t.Fatal(err)
	}
	source, err := newFileWatchSource(context.Background(), spec, watchTestInterval, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.cancel(context.Background()) })
	if err := os.RemoveAll(filepath.Join(root, "vanishing")); err != nil {
		t.Fatal(err)
	}
	_, err = watchFor(t, source, 4*time.Second, func(seen []map[string]any) bool { return false })
	if err == nil {
		t.Fatal("deleted root did not fail the watcher")
	}
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "FileNotFound" {
		t.Fatalf("terminal failure = %#v", err)
	}
}

func TestReconcileFoldsChangesIntoAScan(t *testing.T) {
	scan := []any{
		map[string]any{"path": "/r/a.go", "relative_path": "a.go", "name": "a.go", "kind": "file", "depth": 1.0, "symbolic_link": false},
		map[string]any{"path": "/r/lib", "relative_path": "lib", "name": "lib", "kind": "folder", "depth": 1.0, "symbolic_link": false},
	}
	change := func(kind, rel, entryKind string) map[string]any {
		return map[string]any{"path": "/r/" + rel, "relative_path": rel, "kind": kind, "entry_kind": entryKind, "observed_at": time.Now()}
	}

	updated, err := reconcileFileChange(scan, change("created", "new.go", "file"))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 3 || updated[0].(map[string]any)["relative_path"] != "a.go" || updated[1].(map[string]any)["relative_path"] != "lib" || updated[2].(map[string]any)["relative_path"] != "new.go" {
		t.Fatalf("created fold = %#v", updated)
	}

	updated, err = reconcileFileChange(updated, change("modified", "a.go", "file"))
	if err != nil || len(updated) != 3 {
		t.Fatalf("modified fold = %#v %v", updated, err)
	}

	updated, err = reconcileFileChange(updated, change("removed", "lib", "folder"))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || updated[1].(map[string]any)["relative_path"] != "new.go" {
		t.Fatalf("removed fold = %#v", updated)
	}

	_, err = reconcileFileChange(updated, change("overflowed", "", ""))
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "FileWatchOverflow" || failure.value["root"] == nil {
		t.Fatalf("overflow fold = %#v", err)
	}
}

func TestStdFilesWatchActionStreamsFileChanges(t *testing.T) {
	root := t.TempDir()
	ops := stdRegistry["std/files"]
	source, err := ops["watch"].StreamFn(context.Background(), Options{Dir: root}, []any{root, []any{}, []any{}, []any{}, 0.0, false})
	if err != nil {
		t.Fatal(err)
	}
	watcher := source.(*fileWatchSource)
	t.Cleanup(func() { _ = watcher.cancel(context.Background()) })
	if err := os.WriteFile(filepath.Join(root, "event.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	item, more, err := watcher.next(ctx)
	if err != nil || !more {
		t.Fatalf("first change = %#v %v", item, err)
	}
	if change := item.(map[string]any); change["kind"] != "created" || change["relative_path"] != "event.txt" {
		t.Fatalf("change = %#v", change)
	}
}

func TestLanguageWatchStreamDeliversChanges(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "incoming")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	program, diagnostics := LoadProgram(filepath.Join(tree, "main.sos"), `watch folder "." recursively called changes
  without following symbolic links
make seen false
for each change from changes:
  assign seen true
  stop reading
`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(tree, "arrival.txt"), []byte("x"), 0o644)
	}()
	result, err := Run(watchLanguageContext(t), program, Options{Dir: tree, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["seen"] != true {
		t.Fatalf("seen = %#v", result.Variables)
	}
}

func watchLanguageContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	return ctx
}
