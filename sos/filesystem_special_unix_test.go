//go:build darwin || linux

package sos

import (
	"context"
	"syscall"
	"testing"
)

func TestInspectAndWalkClassifySpecialEntries(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(root+"/events.pipe", 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := inspectEntry(context.Background(), Options{Dir: root}, []any{"events.pipe"})
	if err != nil {
		t.Fatal(err)
	}
	if got := value.(map[string]any)["kind"]; got != otherKind {
		t.Fatalf("kind = %v", got)
	}
	spec := walkSpecForTest(t, Options{Dir: root}, root, traversalOptions{kinds: []string{otherKind}})
	entries := collectWalk(t, spec)
	if len(entries) != 1 || entries[0]["kind"] != otherKind {
		t.Fatalf("entries = %#v", entries)
	}
}
