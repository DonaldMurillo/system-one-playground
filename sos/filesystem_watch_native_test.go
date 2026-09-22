//go:build linux || darwin || windows

package sos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The tests in this file pin the native trigger path shared by the inotify,
// kqueue, and ReadDirectoryChangesW backends: delivery without elapsed
// polling time, recursive registration that follows the tree, and move
// inference from snapshot identity.

// TestWatchDeliversChangesWithoutTimerProgress proves the primary trigger is
// the OS notification backend, not elapsed polling time: with an interval no
// timer could satisfy inside a test, changes still arrive promptly, both for
// a new entry and for a content write to an existing file.
func TestWatchDeliversChangesWithoutTimerProgress(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := parseTraversalSpec(Options{Dir: root}, traversalOptions{root: root})
	if err != nil {
		t.Fatal(err)
	}
	source, err := newFileWatchSource(context.Background(), spec, time.Hour, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.cancel(context.Background()) })

	if err := os.WriteFile(filepath.Join(root, "arrival.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, source, "created:arrival.txt")

	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed with more bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, source, "modified:seed.txt")
}

// TestWatchRegistersNestedEntriesAsTheyAppear proves recursive registration
// follows the tree: folders created after startup are watched, and files
// inside them hold registrations of their own for later content writes.
func TestWatchRegistersNestedEntriesAsTheyAppear(t *testing.T) {
	root := t.TempDir()
	source := newTestWatcher(t, root, traversalOptions{})
	nested := filepath.Join(root, "in", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(nested, "leaf.txt")
	if err := os.WriteFile(leaf, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, source, "created:in/deep/leaf.txt")
	if err := os.WriteFile(leaf, []byte("one but longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, source, "modified:in/deep/leaf.txt")
}

// TestWatchInfersMovesFromSnapshotIdentity proves a rename inside one
// reconciliation window is reported as a single moved change carrying the
// previous relative path, and that folding it follows the file across names.
func TestWatchInfersMovesFromSnapshotIdentity(t *testing.T) {
	root := t.TempDir()
	source := newTestWatcher(t, root, traversalOptions{})
	origin := filepath.Join(root, "origin.txt")
	if err := os.WriteFile(origin, []byte("stable identity"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForChangeKind(t, source, "created:origin.txt")
	if err := os.Rename(origin, filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	moved := waitForChangeKind(t, source, "moved:renamed.txt")
	summary := changeSummaries(moved)
	if strings.Contains(summary, "removed:origin.txt") || strings.Contains(summary, "created:renamed.txt") {
		t.Fatalf("rename split into removal and creation: %s", summary)
	}
	var change map[string]any
	for _, item := range moved {
		if item["kind"] == changeMoved {
			change = item
		}
	}
	if change == nil || change["previous_path"] != "origin.txt" || change["entry_kind"] != fileKind {
		t.Fatalf("moved change = %#v", change)
	}
	scan := []any{
		map[string]any{"path": "/r/origin.txt", "relative_path": "origin.txt", "name": "origin.txt", "kind": "file", "depth": 1.0, "symbolic_link": false},
	}
	folded, err := reconcileFileChange(scan, change)
	if err != nil {
		t.Fatal(err)
	}
	if len(folded) != 1 || folded[0].(map[string]any)["relative_path"] != "renamed.txt" {
		t.Fatalf("moved fold = %#v", folded)
	}
}

// TestDiffWatchSnapshotsPairsOnlyIdenticalFiles pins the move-inference rule:
// only a removed/created pair of regular files with identical size and
// modification time collapses into one moved change; everything else stays a
// separate removal and creation, in lexical order.
func TestDiffWatchSnapshotsPairsOnlyIdenticalFiles(t *testing.T) {
	spec := traversalSpec{
		root:  "/r",
		kinds: map[string]bool{fileKind: true, folderKind: true, linkKind: true, otherKind: true},
	}
	stamp := time.Date(2026, 5, 4, 3, 2, 1, 0, time.UTC)
	other := stamp.Add(time.Second)
	previous := map[string]watchEntry{
		"gone.txt":  {kind: fileKind, size: 3, modified: stamp},
		"kept.txt":  {kind: fileKind, size: 5, modified: stamp},
		"folderold": {kind: folderKind, modified: stamp},
	}
	current := map[string]watchEntry{
		"moved.txt":   {kind: fileKind, size: 3, modified: stamp},
		"resized.txt": {kind: fileKind, size: 4, modified: stamp},
		"rewrote.txt": {kind: fileKind, size: 3, modified: other},
		"kept.txt":    {kind: fileKind, size: 5, modified: stamp},
		"foldernew":   {kind: folderKind, modified: stamp},
	}
	changes := diffWatchSnapshots(spec, previous, current, stamp)
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		parts = append(parts, change.kind+":"+change.rel)
	}
	got := strings.Join(parts, "|")
	want := "created:foldernew|removed:folderold|moved:moved.txt|created:resized.txt|created:rewrote.txt"
	if got != want {
		t.Fatalf("diff = %s, want %s", got, want)
	}
	for _, change := range changes {
		if change.kind == changeMoved && change.previous != "gone.txt" {
			t.Fatalf("moved change lost its previous path: %#v", change)
		}
	}
}
