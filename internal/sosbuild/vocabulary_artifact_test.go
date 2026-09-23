package sosbuild

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestArtifactCarriesVocabulary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "proj")
	os.MkdirAll(filepath.Join(src, "media"), 0o755)
	writeT(t, filepath.Join(src, "sos.toml"), "version = 1\n[module]\npath = \"example.com/acme\"\n[[language.libraries]]\npath = \"std/text\"\n[[language.libraries]]\npath = \"example.com/acme/media\"\nas = \"media\"\n")
	writeT(t, filepath.Join(src, "media", "media.sos"), "package media\nimport \"std/text\"\nexport preview\n\nto preview with clip:\n  trim clip called clean\n  return clean\n")
	source := "trim \"  hello  \" called a\nmedia.preview \"  world  \" called b\nshow a\nshow b\n"
	writeT(t, filepath.Join(src, "main.sos"), source)

	// Build from inside the project so config resolution sees sos.toml.
	wd, _ := os.Getwd()
	t.Chdir(src)
	p, ds := sos.LoadProgram(filepath.Join(src, "main.sos"), source)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	out := filepath.Join(dir, "artifact")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	if err := Build(context.Background(), BuildOptions{Program: p, Output: out, Target: TargetNative, Name: "vocab-artifact"}); err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Chdir(wd)

	// Execute the artifact from a directory with no sources and no config.
	run := filepath.Join(dir, "run")
	os.MkdirAll(run, 0o755)
	cmd := exec.Command(out)
	cmd.Dir = run
	got, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, got)
	}
	if string(got) != "hello\nworld\n" {
		t.Errorf("artifact output = %q, want %q", got, "hello\nworld\n")
	}
	if err := os.Mkdir(filepath.Join(run, ".env"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(out)
	cmd.Dir = run
	got, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("artifact ignored .env load failure: %s", got)
	}
	if strings.Contains(string(got), "hello") || strings.Contains(string(got), "world") {
		t.Fatalf("artifact executed after .env load failure: %s", got)
	}
}

func writeT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
