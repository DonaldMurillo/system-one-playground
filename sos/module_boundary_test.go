package sos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleImportsCannotEscapeProjectBoundary(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "sos.toml"), []byte("version = 1\n[module]\npath = \"example.com/project\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.sos"), []byte("package outside\nexport leak\nto leak:\n  show \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostics := LoadProgram(filepath.Join(project, "main.sos"), "import \"../outside.sos\" as outside\ncall outside.leak\n")
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "escapes the project boundary") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestNestedEntrypointUsesProjectConfigAsBoundaryWithoutModulePath(t *testing.T) {
	project := t.TempDir()
	nested := filepath.Join(project, "cmd", "demo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "sos.toml"), []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "shared.sos"), []byte("package shared\nexport greet\nto greet:\n  show \"hello\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostics := LoadProgram(filepath.Join(nested, "main.sos"), "import \"../../shared.sos\" as shared\ncall shared.greet\n")
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, "escapes the project boundary") {
			t.Fatalf("valid project import was rejected: %+v", diagnostics)
		}
	}
}

func TestSymlinkedEntrypointResolvesImportsFromRealProject(t *testing.T) {
	project := t.TempDir()
	launcher := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sos.toml"), []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "shared.sos"), []byte("package shared\nexport greet\nto greet:\n  show \"hello\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(project, "main.sos")
	if err := os.WriteFile(entry, []byte("placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(launcher, "main.sos")
	if err := os.Symlink(entry, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, diagnostics := LoadProgram(link, "import \"./shared.sos\" as shared\ncall shared.greet\n")
	if len(diagnostics) != 0 {
		t.Fatalf("symlinked entrypoint did not use real project: %+v", diagnostics)
	}
}
