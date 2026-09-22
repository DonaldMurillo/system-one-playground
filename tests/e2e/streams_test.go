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
		{"main.sos", "Watching checkout deployment...\n[queued] release accepted\n[building] container image built\n[testing] smoke tests passed\n[deploying] traffic shifting to new release\n[failed] health check failed\nDeployment failed; stopping live feed\nMonitor stopped without waiting for rollback updates\n"},
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

func TestCLINodeStreamExample(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for the Node stream fixture")
	}
	dir := filepath.Join(examplesRoot(t), "streams")
	stdout, stderr, code := runCLI(t, dir, "run", "node.sos")
	if code != 0 || stdout != "node event 0\nnode event 1\nnode event 2\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCLITimedMarketStreamCompletesAndCancels(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for the timed market stream example")
	}
	dir := filepath.Join(examplesRoot(t), "streams")
	completion := writeScript(t, dir, "market-fast.sos", `import "example/streams-node" as market
stream market.watch_market with "SYS", 3, 5 called ticks
for each tick from ticks:
  show second of tick
show "complete"
`)
	t.Cleanup(func() { _ = os.Remove(completion) })
	stdout, stderr, code := runCLI(t, dir, "run", completion)
	if code != 0 || stdout != "1\n2\n3\ncomplete\n" {
		t.Fatalf("completion exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	cancellation := writeScript(t, dir, "market-cancel.sos", `import "example/streams-node" as market
stream market.watch_market with "SYS", 50, 5 called ticks
for each tick from ticks:
  show second of tick
  when second of tick is 2:
    stop reading
show "canceled"
`)
	t.Cleanup(func() { _ = os.Remove(cancellation) })
	stdout, stderr, code = runCLI(t, dir, "run", cancellation)
	if code != 0 || stdout != "1\n2\ncanceled\n" {
		t.Fatalf("cancellation exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCLILocalStreamExample(t *testing.T) {
	dir := filepath.Join(examplesRoot(t), "streams")
	stdout, stderr, code := runCLI(t, dir, "run", "local.sos")
	want := "Watching a live deployment...\n[queued] release accepted\n[building] container image built\n[testing] smoke tests passed\n[deployed] traffic is live\nDeployment succeeded; canceling the remaining feed\nCaller continued after the stream stopped\n"
	if code != 0 || stdout != want {
		t.Fatalf("exit=%d stdout=%q, want %q stderr=%q", code, stdout, want, stderr)
	}
}

func TestLocalStreamInterpreterAndNativeBuildParity(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", `to count streaming integer:
  make current 0
  while current < 4:
    assign current current + 1
    send current
  finish
stream count called numbers
take first 3 items from numbers called sample
for each number in sample:
  show number
show "continued"
`)
	runnerOut, runnerErr, runnerCode := runCLI(t, dir, "run", script)
	if runnerCode != 0 {
		t.Fatalf("runner exit=%d stderr=%s", runnerCode, runnerErr)
	}
	artifact := filepath.Join(dir, "stream-app")
	_, buildErr, buildCode := runCLI(t, dir, "build", script, "--output", artifact)
	if buildCode != 0 {
		t.Fatalf("build exit=%d stderr=%s", buildCode, buildErr)
	}
	artifactOut, artifactErr, artifactCode := runAppArtifact(t, dir, artifact)
	if artifactCode != runnerCode || artifactOut != runnerOut || artifactErr != runnerErr {
		t.Fatalf("runner=(%d,%q,%q) artifact=(%d,%q,%q)", runnerCode, runnerOut, runnerErr, artifactCode, artifactOut, artifactErr)
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
