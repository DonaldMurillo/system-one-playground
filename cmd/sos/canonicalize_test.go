package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestCanonicalizeOneLinePreservesCRLF(t *testing.T) {
	source := "make age 21\r\nif age bigger 18 show \"adult\"\r\n"
	a := &sos.Analysis{Decisions: []sos.Interpretation{{Line: 2, Source: "if age bigger 18 show \"adult\"", Canonical: "when age > 18:\n  show \"adult\"", Method: "jev"}}}
	got, err := canonicalizeOneLine(source, a, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := "make age 21\r\nwhen age > 18:\r\n  show \"adult\"\r\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCanonicalDiffIsReviewable(t *testing.T) {
	got := canonicalDiff("demo.sos", "show \"old\"", "show \"new\"\n")
	for _, part := range []string{"--- demo.sos", "+++ demo.sos", "-show \"old\"", "+show \"new\"", "\\ No newline at end of file"} {
		if !strings.Contains(got, part) {
			t.Fatalf("diff missing %q: %s", part, got)
		}
	}
}

func TestCanonicalizeProjectDirUsesNearestManifest(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sos.toml"), []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalizeProjectDir(filepath.Join(nested, "main.sos"))
	if err != nil || got != root {
		t.Fatalf("project dir = %q, %v; want %q", got, err, root)
	}
}
