package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Module paths are identities, and a directory is one package. Both files
// contribute declarations while each retains its own import aliases.
func TestDirectoryPackageFileLocalImportsAndStandaloneBuild(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[module]\npath = \"example.com/support-tools\"\n")
	pkgDir := filepath.Join(dir, "reporting")
	if err := os.Mkdir(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, pkgDir, "summaries.sos", `package reporting
import "std/text" as tools
export summarize

to summarize with value:
  call tools.trim with value called clean
  call stringify with clean called result
  return result
`)
	writeScript(t, pkgDir, "encoding.sos", `package reporting
import "std/json" as tools

to stringify with value:
  call tools.encode with value called result
  return result
`)
	entry := writeScript(t, dir, "main.sos", `import "example.com/support-tools/reporting" as reports
call reports.summarize with "  hello  " called summary
show summary
`)
	out, errOut, code := runCLI(t, dir, "run", entry)
	if code != 0 || strings.TrimSpace(out) != `"hello"` {
		t.Fatalf("directory package and file-local aliases: exit=%d out=%q err=%s", code, out, errOut)
	}
	bin := filepath.Join(t.TempDir(), "report")
	_, errOut, code = runCLI(t, dir, "build", entry, "-o", bin)
	if code != 0 {
		t.Fatalf("package build: %s", errOut)
	}
	if err := os.RemoveAll(pkgDir); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	data, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(data)) != `"hello"` {
		t.Fatalf("standalone graph lost package files: %v %s", err, data)
	}
}

func TestPackageEffectsAreRejectedBeforeEntryEffects(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[module]\npath = \"example.com/tools\"\n")
	pkg := filepath.Join(dir, "unsafe")
	if err := os.Mkdir(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, pkg, "main.sos", "package unsafe\nshow \"unexpected\"\nexport pass\nto pass with value:\n  return value\n")
	entry := writeScript(t, dir, "main.sos", "import \"example.com/tools/unsafe\"\nshow \"entry effect\"\n")
	out, _, code := runCLI(t, dir, "run", entry)
	if code == 0 || out != "" {
		t.Fatalf("library executable top level must be rejected before effects: code=%d out=%q", code, out)
	}
}
