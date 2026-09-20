package e2e

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewPredicateAndFrontmatterRegressions(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	source := "make jevscore true\nmake rows [1,2]\nkeep rows where jevscore\nkeep rows where \" where jev \" is \" where jev \"\nshow count of rows\n"
	script := writeScript(t, dir, "predicate.sos", source)
	out, err, code := runCLI(t, dir, "run", script)
	if code != 0 || out != "2\n" {
		t.Fatalf("predicate: %d %s %s", code, out, err)
	}
	_, err, code = runCLI(t, dir, "build", script, "--target", "wasm-browser", "--output", filepath.Join(dir, "browser", "predicate.wasm"))
	if code != 0 {
		t.Fatal(err)
	}
	bom := writeScript(t, dir, "bom.sos", "\ufeffshow 5\n")
	out, err, code = runCLI(t, dir, "run", bom)
	if code != 0 || out != "5\n" {
		t.Fatalf("BOM: %d %s %s", code, out, err)
	}
	invalid := writeScript(t, dir, "uncertain.sos", "make rows []\nkeep rows where jev:\n  ask \"Urgent\"\n  on uncertain:\n    show \"unsupported\"\n")
	_, err, code = runCLI(t, dir, "check", invalid)
	if code == 0 || !strings.Contains(err, "on uncertain expects inline") {
		t.Fatalf("uncertain block: %d %s", code, err)
	}
}

func TestBuildUsesConsistentDevelopmentSnapshot(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	// Retain the stale archive intentionally, then make live interpreter code
	// require a new symbol from live config code. Mixing live and archived
	// packages fails; sourcing a coherent checkout builds and executes correctly.
	for _, dir := range []string{"sos", "typesafe", "sosconfig", "internal/sosbuild", "internal/semcore", "internal/soslsp", "internal/sossyntax", "cmd/sos"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			dest := filepath.Join(clone, relative)
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			return os.WriteFile(dest, data, 0644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(clone, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for name, addition := range map[string]string{"sos/config.go": "\nvar _ = sosconfig.SnapshotSentinel\n", "sosconfig/config.go": "\nconst SnapshotSentinel = true\n"} {
		path := filepath.Join(clone, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, []byte(addition)...), 0644); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(clone, "cli")
	build := exec.Command("go", "build", "-o", cli, "./cmd/sos")
	build.Dir = clone
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, out)
	}
	script := writeScript(t, clone, "test.sos", "show 5\n")
	binary := filepath.Join(clone, "app")
	build = exec.Command(cli, "build", script, "--output", binary)
	build.Dir = clone
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("snapshot build: %v %s", err, out)
	}
	run := exec.Command(binary)
	run.Dir = t.TempDir()
	if out, err := run.CombinedOutput(); err != nil || string(out) != "5\n" {
		t.Fatalf("artifact: %v %s", err, out)
	}
}
