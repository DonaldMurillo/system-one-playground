package sos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalFileFormsExecuteThroughSharedBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source", "a.txt"), []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}
	source := `make report "hello"
create folders through "out/nested"
create folder "empty" if missing
write report atomically to file "out/report.txt"
  only if it does not exist
append "!" to file "out/report.txt"
read file "out/report.txt" as text called contents
check whether file "out/report.txt" exists called present
inspect file "out/report.txt" called information
list files in folder "out" called immediate
walk through folder "out" at most 20 entries called walked
  including files and folders
  without following symbolic links
copy file "out/report.txt" to "out/copy.txt"
  only if the destination does not exist
copy folder "source" to "copied"
  only if the destination does not exist
move file "out/copy.txt" to "out/moved.txt"
  replacing an existing file
remove file "out/moved.txt"
remove folder "out/nested" including its contents
remove empty folder "empty"
`
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["contents"] != "hello!" || result.Variables["present"] != true {
		t.Fatalf("variables = %#v", result.Variables)
	}
	if _, err := os.Stat(filepath.Join(dir, "copied", "a.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalFileTraversalStreamExecutesAndContinues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustParse(t, `stream files under folder "." called files
  matching ["*.txt"]
  without following symbolic links
make seen false
for each file from files:
  assign seen true
  stop reading
make continued "yes"
`)
	var events []StreamEvent
	result, err := Run(context.Background(), p, Options{Dir: dir, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}, OnStreamEvent: func(event StreamEvent) {
		events = append(events, event)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Variables["seen"] != true || result.Variables["continued"] != "yes" {
		t.Fatalf("variables = %#v", result.Variables)
	}
	if len(events) == 0 {
		t.Fatal("expected traversal stream events")
	}
	for _, event := range events {
		if event.Binding == "files" {
			if event.Root == "" || event.Bound != 0 || event.Watching {
				t.Fatalf("filesystem stream scope = %#v", event)
			}
			return
		}
	}
	t.Fatalf("missing files stream event: %#v", events)
}
