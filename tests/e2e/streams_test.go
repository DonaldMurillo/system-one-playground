package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIStreamExamples(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the stream plugin example")
	}
	dir := filepath.Join(examplesRoot(t), "streams")
	cases := []struct {
		file string
		want string
	}{
		{"main.sos", "Stream opened\n0: event 0\n1: event 1\n2: event 2\nStream finished\n"},
		{"early-stop.sos", "event 0\nevent 1\nevent 2\nStopped intentionally\n"},
		{"close.sos", "Producer started\nProducer closed\n"},
		{"collect.sos", "event 0\nevent 1\nevent 2\n"},
		{"sample.sos", "event 0\nevent 1\nevent 2\n"},
		{"failure.sos", "event 0\nevent 1\nConnection lost after 2 items\nKept the items already processed\n"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, dir, "run", tc.file)
			if code != 0 || stdout != tc.want {
				t.Fatalf("exit=%d stdout=%q, want %q stderr=%q", code, stdout, tc.want, stderr)
			}
		})
	}
}

func TestCLIStreamRuntimeActionCleanupAndTerminalFailure(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for stream fixture")
	}
	dir := filepath.Join(examplesRoot(t), "streams")
	path := writeScript(t, dir, "action-cleanup.sos", `import "example/streams" as events
define failure StopNow:
to abandon may fail with StopNow:
  stream events.infinite called incoming
  fail StopNow with "done"
  close stream incoming
call abandon
  on failure StopNow:
    recover
stream events.failing with 1 called failing
for each event from failing:
  show message of event
  on failure ConnectionLost:
    recover
show "clean"
`)
	t.Cleanup(func() { _ = os.Remove(path) })
	out, stderr, code := runCLI(t, dir, "run", path, "--timeout", "5s")
	if code != 0 || !strings.Contains(out, "event 0") || !strings.Contains(out, "clean") {
		t.Fatalf("code=%d out=%q stderr=%s", code, out, stderr)
	}
}

func TestCalleeCleanupPreservesCallerOwnedStreamAlias(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for stream fixture")
	}
	dir := filepath.Join(examplesRoot(t), "streams")
	path := writeScript(t, dir, "caller-owned.sos", `import "example/streams" as events
to inspect with value:
  finish
stream events.infinite called incoming
call inspect with incoming
take first 1 items from incoming called sample
for each event in sample:
  show message of event
`)
	t.Cleanup(func() { _ = os.Remove(path) })
	out, stderr, code := runCLI(t, dir, "run", path, "--timeout", "5s")
	if code != 0 || !strings.Contains(out, "event 0") {
		t.Fatalf("code=%d out=%q stderr=%s", code, out, stderr)
	}
}

func TestCLIStreamOwnershipAcceptance(t *testing.T) {
	dir := filepath.Join(examplesRoot(t), "streams")
	valid := writeScript(t, dir, "valid-stream.sos", `import "example/streams" as events
stream events.finite with 1 called incoming
for each event from incoming:
  show event
`)
	t.Cleanup(func() { _ = os.Remove(valid) })
	if out, stderr, code := runCLI(t, dir, "check", valid); code != 0 {
		t.Fatalf("valid stream check: code=%d stdout=%q stderr=%s", code, out, stderr)
	}

	invalidDir := t.TempDir()
	invalid := writeScript(t, invalidDir, "invalid-stream.sos", `to follow streaming text:
  finish
stream follow called events
make copied events
`)
	_, stderr, code := runCLI(t, invalidDir, "check", invalid)
	if code == 0 || !strings.Contains(stderr, "cannot copy stream events with make") || !strings.Contains(stderr, "stream events remains active") {
		t.Fatalf("ownership diagnostics: code=%d stderr=%s", code, stderr)
	}
}

func TestCLIRejectsStopReadingOutsideStreamLoop(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "stop-reading.sos", "stop reading\n")
	_, stderr, code := runCLI(t, dir, "check", path)
	if code == 0 || !strings.Contains(stderr, "stop reading requires an active stream loop") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestCLIRejectsActionAndBranchStreamLeaks(t *testing.T) {
	for name, body := range map[string]string{
		"action":     "to work:\n  stream follow called events\n  finish\n",
		"branch":     "when true:\n  stream follow called events\n",
		"one-branch": "stream follow called events\nwhen true:\n  close stream events\notherwise:\n  show \"open\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeScript(t, dir, "scope.sos", "to follow streaming text:\n  finish\n"+body)
			_, stderr, code := runCLI(t, dir, "check", path)
			if code == 0 || !strings.Contains(stderr, "remains active") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
		})
	}
}

func TestCLIRejectsQualifiedLegacyStreamCalls(t *testing.T) {
	dir := filepath.Join(examplesRoot(t), "streams")
	for name, operation := range map[string]string{
		"call":    "call events.finite with 1 called result\n",
		"capture": "capture events.finite with 1 called outcome\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeScript(t, dir, "legacy-"+name+".sos", "import \"example/streams\" as events\n"+operation)
			t.Cleanup(func() { _ = os.Remove(path) })
			_, stderr, code := runCLI(t, dir, "check", path)
			if code == 0 || !(strings.Contains(stderr, "must be opened with stream") || strings.Contains(stderr, "cannot capture streaming action")) {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
		})
	}
}
