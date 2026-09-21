// Package e2e builds the sos CLI and exercises it end to end, including a
// standalone build executed from an unrelated working directory.
package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var sosBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "sos-e2e-*")
	if err != nil {
		os.Stderr.WriteString("e2e: " + err.Error() + "\n")
		os.Exit(1)
	}
	sosBin = filepath.Join(tmp, "sos")
	if out, err := exec.Command("go", "build", "-o", sosBin, "github.com/DonaldMurillo/system-one-playground/cmd/sos").CombinedOutput(); err != nil {
		os.Stderr.WriteString("e2e: building CLI:\n" + string(out))
		os.RemoveAll(tmp)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

const simpleScript = "make total 2\nassign total total + 3\nshow total\n"

func writeScript(t *testing.T, dir, name, source string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(sosBin, args...)
	cmd.Dir = dir
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running %v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	return out.String(), errBuf.String(), code
}

func TestVersion(t *testing.T) {
	stdout, _, code := runCLI(t, t.TempDir(), "version")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.HasPrefix(stdout, "sos ") {
		t.Errorf("stdout = %q, want a version line", stdout)
	}
}

func TestNoCommandIsUsageError(t *testing.T) {
	_, stderr, code := runCLI(t, t.TempDir())
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "usage: sos") {
		t.Errorf("stderr = %q, want usage", stderr)
	}
}

func TestRunSimpleScript(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)
	stdout, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "5") {
		t.Errorf("stdout = %q, want it to contain 5", stdout)
	}
}

func TestCheckCleanScript(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)
	_, _, code := runCLI(t, dir, "check", script)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestCheckJSONIncludesTypedFailureMetadata(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "failure.sos", `define failure Missing:
to fetch may fail with Missing:
  fail Missing with "missing"
`)
	stdout, stderr, code := runCLI(t, dir, "check", script, "--json")
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	var payload struct {
		Diagnostics []any            `json:"diagnostics"`
		Actions     []map[string]any `json:"actions"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("check JSON = %q: %v", stdout, err)
	}
	if len(payload.Diagnostics) != 0 || len(payload.Actions) != 1 {
		t.Fatalf("check payload = %#v", payload)
	}
	if got := payload.Actions[0]["possibleFailures"]; got == nil {
		t.Fatalf("check payload action = %#v, want possibleFailures", payload.Actions[0])
	}
}

func TestCheckMalformedScriptReportsLine(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "bad.sos", "frobnicate the wibble\n")
	_, stderr, code := runCLI(t, dir, "check", script)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "bad.sos") || !strings.Contains(stderr, ":1:") {
		t.Errorf("stderr = %q, want a line-1 diagnostic naming bad.sos", stderr)
	}
}

func TestRunMalformedScriptFails(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "bad.sos", "make total 2\nassign total\n")
	_, stderr, code := runCLI(t, dir, "run", script)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "bad.sos") {
		t.Errorf("stderr = %q, want diagnostics naming bad.sos", stderr)
	}
}

func TestFmtScript(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)
	stdout, stderr, code := runCLI(t, dir, "fmt", script)
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "make") {
		t.Errorf("stdout = %q, want formatted source", stdout)
	}
}

// A minimal executable leaf exercises script arguments and their defaults.
const commandScript = "command triage:\n  argument source as folder\n  option since as duration default 24h\n  switch judge default off\n  show source\n"

func TestRunScriptHelp(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "triage.sos", commandScript)
	stdout, stderr, code := runCLI(t, dir, "run", script, "--help")
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"usage: triage SOURCE", "--since", "--judge"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestRunScriptArgsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "triage.sos", commandScript)
	_, stderr, code := runCLI(t, dir, "run", script, "--", "./logs")
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
}

func TestRunScriptMissingRequiredArg(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "triage.sos", commandScript)
	_, stderr, code := runCLI(t, dir, "run", script)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, `required argument "source"`) {
		t.Errorf("stderr = %q, want required-argument error", stderr)
	}
}

func TestRunScriptUnknownOption(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "triage.sos", commandScript)
	_, stderr, code := runCLI(t, dir, "run", script, "--", "./logs", "--bogus")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unknown option --bogus") {
		t.Errorf("stderr = %q, want unknown-option error", stderr)
	}
}

func TestBuildNativeStandalone(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)
	output := filepath.Join(dir, "build", "total")
	_, stderr, code := runCLI(t, dir, "build", script, "--output", output)
	if code != 0 {
		t.Fatalf("build exit = %d; stderr:\n%s", code, stderr)
	}
	assertArtifact(t, output)
	// The artifact runs from an unrelated working directory.
	elsewhere := t.TempDir()
	var out, errBuf strings.Builder
	cmd := exec.Command(output)
	cmd.Dir = elsewhere
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("standalone run: %v\nstderr:\n%s", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "5") {
		t.Errorf("standalone stdout = %q, want it to contain 5", out.String())
	}
}

func TestBuildNativeStandaloneTypedFailurePayload(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "failure.sos", `define failure InvalidCity:
  city as text
to fetch with city as text returning text may fail with InvalidCity:
  fail InvalidCity with "A city is required":
    city from city
call fetch with "" called value
`)
	output := filepath.Join(dir, "build", "failure")
	_, stderr, code := runCLI(t, dir, "build", script, "--output", output)
	if code != 0 {
		t.Fatalf("build exit = %d; stderr:\n%s", code, stderr)
	}
	var out, errBuf strings.Builder
	cmd := exec.Command(output)
	cmd.Dir = t.TempDir()
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err == nil {
		t.Fatal("typed failure artifact succeeded")
	}
	if !strings.Contains(errBuf.String(), `"kind":"InvalidCity"`) || !strings.Contains(errBuf.String(), `"city":""`) {
		t.Fatalf("artifact stderr = %q, want structured typed failure", errBuf.String())
	}
}

func TestBuildWasmWasiArtifact(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a WASM module; skipped in -short")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)
	output := filepath.Join(dir, "dist", "total.wasm")
	_, stderr, code := runCLI(t, dir, "build", script, "--output", output, "--target", "wasm-wasi")
	if code != 0 {
		t.Fatalf("build exit = %d; stderr:\n%s", code, stderr)
	}
	assertArtifact(t, output)
}

func TestBuildWasmBrowserArtifacts(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a WASM module; skipped in -short")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)
	output := filepath.Join(dir, "dist", "total.wasm")
	_, stderr, code := runCLI(t, dir, "build", script, "--output", output, "--target", "wasm-browser")
	if code != 0 {
		t.Fatalf("build exit = %d; stderr:\n%s", code, stderr)
	}
	for _, name := range []string{"total.wasm", "wasm_exec.js", "index.html"} {
		assertArtifact(t, filepath.Join(dir, "dist", name))
	}
	html, err := os.ReadFile(filepath.Join(dir, "dist", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "total.wasm") || !strings.Contains(string(html), "wasm_exec.js") {
		t.Errorf("index.html does not reference the module:\n%s", html)
	}
}

func assertArtifact(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("artifact %s: %v", path, err)
	}
	if info.IsDir() || info.Size() == 0 {
		t.Fatalf("artifact %s is empty or a directory", path)
	}
}
