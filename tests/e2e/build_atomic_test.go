package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedCLIBuildPreservesExistingExecutable(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", "show \"hello\"\n")
	output := filepath.Join(dir, "app")
	before := []byte("previous-valid-build")
	if err := os.WriteFile(output, before, 0o755); err != nil {
		t.Fatal(err)
	}
	// The already-built test CLI is invoked by absolute path; hiding the Go
	// toolchain forces failure after analysis and staging have completed.
	t.Setenv("PATH", t.TempDir())
	_, stderr, code := runCLI(t, dir, "build", script, "--output", output)
	if code == 0 || !strings.Contains(stderr, "go toolchain required") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	after, err := os.ReadFile(output)
	if err != nil || string(after) != string(before) {
		t.Fatalf("output after failed build=%q err=%v", after, err)
	}
	staged, err := filepath.Glob(filepath.Join(dir, ".app-stage-*"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("staging residue=%v err=%v", staged, err)
	}
}
