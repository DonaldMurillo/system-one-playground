package soslsp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSemanticProjectDirUsesSymlinkTargetProject(t *testing.T) {
	project := t.TempDir()
	launcher := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sos.toml"), []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(project, "main.sos")
	if err := os.WriteFile(realFile, []byte("placeholder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(launcher, "main.sos")
	if err := os.Symlink(realFile, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if got := semanticProjectDir(link); got != want {
		t.Fatalf("semanticProjectDir(%q) = %q, want %q", link, got, want)
	}
}
