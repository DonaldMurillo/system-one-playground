package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidReplayInputsFailBeforeInterpretation(t *testing.T) {
	fx := semNewFixture(t, semServeCanary)
	dir := t.TempDir()
	source := writeScript(t, dir, "source.sos", frontmatter("version = 1\n[interpretation]\nmode = \"assisted\"\n")+ambiguousEditorSource)
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{{"--record", "record.json", "--replay", bad}, {"--replay", bad}, {"--replay", filepath.Join(dir, "missing.json")}} {
		args := append([]string{"run", source}, flags...)
		_, stderr, code := runCLI(t, dir, args...)
		if code != 1 || strings.Contains(stderr, "interpretation") {
			t.Errorf("%v: %d %s", flags, code, stderr)
		}
	}
	fx.requireNoContacts(t)
}

func TestKnownWasmIncompatibilityFailsBeforeInterpretation(t *testing.T) {
	fx := semNewFixture(t, semServeCanary)
	dir := t.TempDir()
	source := writeScript(t, dir, "source.sos", frontmatter("version = 1\n[interpretation]\nmode = \"assisted\"\n")+"read \"tickets.json\" as json called input\n"+ambiguousEditorSource)
	_, stderr, code := runCLI(t, dir, "build", source, "--target", "wasm-browser", "--output", filepath.Join(dir, "program.wasm"))
	if code != 1 || !strings.Contains(stderr, "filesystem") {
		t.Fatalf("%d %s", code, stderr)
	}
	fx.requireNoContacts(t)
}

func TestBuildReportsInterpretationUsage(t *testing.T) {
	fx := semNewFixture(t, semServeCooperative, semChoiceRule{when: []string{"group"}, pick: []string{"tickets"}})
	dir := t.TempDir()
	source := writeScript(t, dir, "source.sos", frontmatter("version = 1\n[interpretation]\nmode = \"assisted\"\n")+ambiguousEditorSource)
	_, stderr, code := runCLI(t, dir, "build", source, "--output", filepath.Join(dir, "program"))
	if code != 0 || !strings.Contains(stderr, "usage requests=1/") || !strings.Contains(stderr, "inputTokens=140") {
		t.Fatalf("build usage: %d %s", code, stderr)
	}
	fx.requireContacts(t, 1)
}
