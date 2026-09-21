package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func examplesRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../examples/sos")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestExamplesAreOrganizedAsProjects(t *testing.T) {
	entries, err := os.ReadDir(examplesRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".sos" {
			t.Errorf("flat example %q: put runnable examples in their own folder", entry.Name())
		}
	}
}

func TestExternalStdioExampleCoversProjectWorkflow(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the stdio example")
	}
	t.Setenv("SOS_EXAMPLE_PLUGIN_TOKEN", "e2e-demo")
	dir := filepath.Join(examplesRoot(t), "external-stdio")

	for _, args := range [][]string{
		{"module", "check", "modules/profile/module.sos.toml"},
		{"module", "generate", "modules/profile/module.sos.toml"},
		{"module", "describe", "local/profile"},
		{"module", "doctor", "local/profile"},
	} {
		stdout, stderr, code := runCLI(t, dir, args...)
		if code != 0 || strings.TrimSpace(stdout) == "" {
			t.Fatalf("sos %v exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}

	stdout, stderr, code := runCLI(t, dir, "run", "main.sos")
	if code != 0 || !strings.Contains(stdout, "Ada") || !strings.Contains(stdout, "Lin") {
		t.Fatalf("stdio example exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runCLI(t, dir, "run", "failure.sos")
	if code != 0 || !strings.Contains(stdout, "Rejected:") || !strings.Contains(stdout, "handled") {
		t.Fatalf("typed failure example exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	_, stderr, code = runCLI(t, dir, "run", "timeout.sos")
	if code == 0 || !strings.Contains(stderr, "context deadline exceeded") {
		t.Fatalf("timeout example exit=%d stderr=%q", code, stderr)
	}
}

func TestExternalBundledExampleBuildsAndRuns(t *testing.T) {
	dir := filepath.Join(examplesRoot(t), "external-bundled")
	stdout, stderr, code := runCLI(t, dir, "run", "main.sos")
	if code != 0 || !strings.Contains(stdout, "hello from a checksummed bundle") {
		t.Fatalf("development run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	output := filepath.Join(t.TempDir(), "example-app")
	_, stderr, code = runCLI(t, dir, "build", "main.sos", "--output", output)
	if code != 0 {
		t.Fatalf("bundle build exit=%d stderr=%q", code, stderr)
	}
	result, err := exec.Command(filepath.Join(output, "example-app")).CombinedOutput()
	if err != nil || !strings.Contains(string(result), "hello from a checksummed bundle") {
		t.Fatalf("bundle output=%q err=%v", result, err)
	}
	manifest, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), `"distribution": "bundled"`) {
		t.Fatalf("manifest=%q err=%v", manifest, err)
	}
}
