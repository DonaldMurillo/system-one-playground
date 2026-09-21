package sos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditorSemanticDiagnosticsStayLocalAndPreserveErrors(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "main.sos")
	source := "+++\nversion = 1\n[interpretation]\nmode = \"semantic\"\n+++\nfilter tickets where team is \"payments\"\nfrobnicate everything\n"
	got := EditorDiagnostics(filename, source, Check(source))
	pending, errors := 0, 0
	for _, d := range got {
		if d.Severity == "information" {
			pending++
		} else if strings.Contains(d.Message, "frobnicate") {
			errors++
		}
	}
	if pending != 1 || errors != 1 {
		t.Fatalf("diagnostics: %+v", got)
	}
	canonical := strings.Replace(source, "semantic", "canonical", 1)
	for _, d := range EditorDiagnostics(filename, canonical, Check(canonical)) {
		if d.Severity != "error" {
			t.Fatal(d)
		}
	}
	invalid := source + "criterion urgent:\n  ask \"Urgent?\"\n"
	for _, d := range EditorDiagnostics(filename, invalid, Check(invalid)) {
		if d.Line == 8 && d.Severity != "error" {
			t.Fatal(d)
		}
	}
}

func TestEditorDiagnosticsUsesRealProjectForSymlinkedFile(t *testing.T) {
	project := t.TempDir()
	launcher := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sos.toml"), []byte("version = 1\n[editor]\nassistance = \"off\"\n"), 0o600); err != nil {
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
	source := "only show \"adult\" once age has gone beyond 18\n"
	for _, diagnostic := range EditorDiagnostics(link, source, Check(source)) {
		if diagnostic.Severity == "information" {
			t.Fatalf("symlink ignored target project's disabled editor assistance: %+v", diagnostic)
		}
	}
}
