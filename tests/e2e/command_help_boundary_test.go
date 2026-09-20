package e2e

import (
	"strings"
	"testing"
)

func TestCommandHelpStillValidatesDeclarationStructure(t *testing.T) {
	semCanary(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "bad.sos", `+++
version = 1
[interpretation]
mode = "assisted"
+++
command bad:
  option duplicated as text default "first"
  option duplicated as text default "second"
  show "must not execute"
`)
	out, stderr, code := runCLI(t, dir, "run", script, "--", "--help")
	if code != 1 || out != "" || !strings.Contains(stderr, "duplicated") {
		t.Fatalf("exit=%d out=%q err=%s", code, out, stderr)
	}
}

func TestCommandUnknownArgumentsAfterHelpAreUsageErrors(t *testing.T) {
	semCanary(t)
	execCommandCases(t, []cmdCase{
		{name: "unknown flag after help", args: []string{"list", "--help", "--bogus"}, code: 2, stdoutEmpty: true, stderrHas: []string{"bogus"}},
		{name: "unknown child after help", args: []string{"--help", "bogus"}, code: 2, stdoutEmpty: true, stderrHas: []string{"bogus"}},
	})
}
