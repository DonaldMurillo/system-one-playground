package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestReportInterpretationsIncludesConfidenceAndEstimatedCost(t *testing.T) {
	var out bytes.Buffer
	reportInterpretations(&out, &sos.Analysis{Decisions: []sos.Interpretation{
		{Line: 2, Method: "deterministic", Confidence: 1},
		{Line: 3, Method: "jev", Confidence: .91, UsageKnown: false},
		{Line: 4, Method: "jev", Confidence: .97, InputTokens: 721, UsageKnown: true, BatchSize: 8, UsageShared: true},
	}})
	for _, part := range []string{"line=4", "confidence=97%", "inputTokens=721", "estimatedUSD=0.00003028", "sharedAcross=8"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("report missing %q: %s", part, out.String())
		}
	}
	if !strings.Contains(out.String(), "line=2 method=deterministic confidence=100% usage=no-Jev-request") || !strings.Contains(out.String(), "line=3 method=jev confidence=91% usage=unavailable") {
		t.Fatalf("report omitted deterministic or unknown-usage attribution: %s", out.String())
	}
}

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

func TestRunCanPersistTheExactAnalysisForEditorReuse(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "main.sos")
	resolution := filepath.Join(dir, "analysis.json")
	if err := os.WriteFile(script, []byte("show \"ok\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"run", "--save-resolution", resolution, script}, &stdout, &stderr); code != 0 {
		t.Fatalf("run exit=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"canonical": "show \"ok\"\n"`) {
		t.Fatalf("saved analysis missing canonical source: %s", data)
	}
}

func TestSaveResolutionFlagIsReservedOnlyBeforeScriptPath(t *testing.T) {
	if isRunFlag("--save-resolution") {
		t.Fatal("--save-resolution after FILE must remain available to script arguments; runner uses it before FILE")
	}
}
