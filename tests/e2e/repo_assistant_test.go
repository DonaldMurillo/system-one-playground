package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the checked-in project through the real runner and a source-free
// native executable, including package imports and the offline triage path.
func TestRepoAssistantProjectWorkflow(t *testing.T) {
	fx := semCanary(t)
	sourceDir := t.TempDir()
	files := []string{"main.sos", "sos.toml", "operations/normalize.sos", "operations/report.sos", "fixtures/issues.json"}
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join("..", "..", "examples", "sos", "repo-assistant", name))
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(sourceDir, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(sourceDir, "main.sos")
	if _, stderr, code := runCLI(t, sourceDir, "check", source); code != 0 {
		t.Fatalf("check: %s", stderr)
	}
	binary := filepath.Join(t.TempDir(), "repo")
	if _, stderr, code := runCLI(t, sourceDir, "build", source, "--output", binary); code != 0 {
		t.Fatalf("build: %s", stderr)
	}
	fixture, err := os.ReadFile(filepath.Join(sourceDir, "fixtures", "issues.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, native := range []bool{false, true} {
		name := "runner"
		if native {
			name = "native"
			if err := os.RemoveAll(sourceDir); err != nil {
				t.Fatal(err)
			}
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeScript(t, dir, "input.json", string(fixture))
			run := func(args ...string) (string, string, int) {
				if native {
					return runAppArtifact(t, dir, binary, args...)
				}
				return runCLI(t, dir, append([]string{"run", source, "--"}, args...)...)
			}
			mustRun := func(args ...string) string {
				t.Helper()
				out, stderr, code := run(args...)
				if code != 0 {
					t.Fatalf("%v: %s", args, stderr)
				}
				return out
			}
			help := mustRun("--help")
			for _, command := range []string{"import", "triage", "report"} {
				if !strings.Contains(help, command) {
					t.Fatalf("missing %s in help: %s", command, help)
				}
			}
			mustRun("import", "input.json", "--output", "issues.json")
			var normalized []struct {
				ID                    int
				Area, Status, Message string
			}
			data, err := os.ReadFile(filepath.Join(dir, "issues.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &normalized); err != nil {
				t.Fatal(err)
			}
			if len(normalized) != 5 || normalized[0].Area != "build" || normalized[0].Status != "open" || normalized[0].Message != "Release build fails after upgrading the compiler." {
				t.Fatalf("normalization: %s", data)
			}
			mustRun("triage", "issues.json", "--criterion", "all", "--output", "selected.json")
			data, err = os.ReadFile(filepath.Join(dir, "selected.json"))
			if err != nil {
				t.Fatal(err)
			}
			var selected []struct{ ID int }
			if err := json.Unmarshal(data, &selected); err != nil {
				t.Fatal(err)
			}
			if len(selected) != 4 {
				t.Fatalf("selected: %s", data)
			}
			for _, issue := range selected {
				if issue.ID == 104 {
					t.Fatal("closed issue survived triage")
				}
			}
			report := mustRun("report", "selected.json", "--output", "reports")
			if !strings.Contains(report, "runtime") {
				t.Fatalf("report: %s", report)
			}
			for i, want := range []struct {
				Area  string
				Count int
			}{{"build", 1}, {"docs", 1}, {"runtime", 2}} {
				data, err := os.ReadFile(filepath.Join(dir, "reports", string(rune('1'+i))+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					Area  string
					Count int
				}
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("report %d: %s", i, data)
				}
			}
			before, err := os.ReadFile(filepath.Join(dir, "issues.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, code := run("import", "input.json", "--output", "issues.json"); code == 0 {
				t.Fatal("overwrite unexpectedly succeeded")
			}
			after, err := os.ReadFile(filepath.Join(dir, "issues.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("overwrite changed existing file")
			}
			if _, _, code := run("triage", "issues.json", "--criterion", "typo", "--output", "bad.json"); code == 0 {
				t.Fatal("invalid choice accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "bad.json")); !os.IsNotExist(err) {
				t.Fatal("invalid option wrote output")
			}
		})
	}
	fx.requireNoContacts(t)
}

func TestCommandImportsStillResolveAndValidate(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "main.sos", "import \"std/missing\" as missing\ncommand app:\n  show \"must not run\"\n")
	out, stderr, code := runCLI(t, dir, "run", "main.sos")
	if code == 0 || out != "" || !strings.Contains(stderr, "std/missing") {
		t.Fatalf("invalid import: %d %q %q", code, out, stderr)
	}
}
