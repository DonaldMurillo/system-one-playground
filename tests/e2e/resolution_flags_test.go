package e2e

import (
	"path/filepath"
	"testing"
)

func TestEmptyResolutionFlagsNeverFallBackToLiveAnalysis(t *testing.T) {
	fx := semNewFixture(t, semServeCanary)
	dir := t.TempDir()
	path := writeScript(t, dir, "source.sos", simpleScript)
	for _, args := range [][]string{
		{"explain", path, "--locked="},
		{"explain", path, "--save="},
		{"explain", path, "--max-calls=-1"},
		{"run", path, "--resolution="},
		{"build", path, "--output", filepath.Join(dir, "artifact"), "--resolution="},
	} {
		out, err, code := runCLI(t, dir, args...)
		if code != 2 {
			t.Errorf("%v: expected usage error, got %d: %s %s", args, code, out, err)
		}
	}
	fx.requireNoContacts(t)
}
