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
