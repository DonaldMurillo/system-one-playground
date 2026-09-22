// Package e2e builds the sos CLI and exercises it end to end, including a
// standalone build executed from an unrelated working directory.
package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestExternalCommandModuleCheckGenerateAndRun(t *testing.T) {
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "modules", "echo")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(moduleDir, "module.sos.toml")
	definitionSource := `schema = 1
[module]
path = "local/echo"
version = "1.0.0"
description = "Echo through a command adapter"
[runtime]
kind = "command"
[capabilities]
network = false
filesystem = "none"
process = true
[[action]]
name = "say"
[[action.parameter]]
name = "message"
type = "text"
[action.result]
type = "text"
[action.command]
program = "printf"
arguments = ["%s", "${message}"]
stdout = "text"
stderr = "diagnostic"
exit_codes = [0]
`
	if err := os.WriteFile(definition, []byte(definitionSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte(`version = 1
[external]
process = true
[[module.external]]
path = "local/echo"
definition = "modules/echo/module.sos.toml"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t, dir, "module", "check", definition)
	if code != 0 {
		t.Fatalf("module check exit = %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runCLI(t, dir, "module", "generate", definition)
	if code != 0 || !strings.Contains(stdout, "to say with message as text returning text") {
		t.Fatalf("module generate exit = %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runCLI(t, dir, "module", "describe", "local/echo")
	if code != 0 || !strings.Contains(stdout, "to say with message as text returning text") {
		t.Fatalf("module describe exit = %d; stdout=%q stderr=%q", code, stdout, stderr)
	}

	script := writeScript(t, dir, "main.sos", "import \"local/echo\" as echo\ncall echo.say with \"hello external\" called answer\nshow answer\n")
	stdout, stderr, code = runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("external run exit = %d; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "hello external" {
		t.Fatalf("external run stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "model=external") {
		t.Fatalf("external trace missing from stderr: %q", stderr)
	}
	built := filepath.Join(dir, "dist", "external-app")
	_, stderr, code = runCLI(t, dir, "build", script, "--output", built)
	if code != 0 {
		t.Fatalf("external build exit=%d stderr=%q", code, stderr)
	}
	command := exec.Command(built)
	command.Dir = t.TempDir()
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "hello external") {
		t.Fatalf("standalone external output=%q err=%v", output, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte("version=1\n[[module.external]]\npath=\"local/echo\"\ndefinition=\"modules/echo/module.sos.toml\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runCLI(t, dir, "run", script)
	if code != 1 || !strings.Contains(stderr, "process capability, denied by project policy") {
		t.Fatalf("capability denial exit=%d stderr=%q", code, stderr)
	}
}

func TestExternalModuleCheckRejectsUnknownTypes(t *testing.T) {
	dir := t.TempDir()
	definition := writeScript(t, dir, "module.sos.toml", `schema=1
[module]
path="local/invalid"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="bad"
[[action.parameter]]
name="value"
type="not-a-type"
[action.command]
program="tool"
`)
	_, stderr, code := runCLI(t, dir, "module", "check", definition)
	if code == 0 || !strings.Contains(stderr, "unknown type") {
		t.Fatalf("module check exit=%d stderr=%q", code, stderr)
	}
}

func TestExternalStdioPluginReusesProcessForOneRun(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the protocol fixture")
	}
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "modules", "echo")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("fixtures/external/python_plugin.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "plugin.py"), fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	definition := `schema = 1
[module]
path = "local/stdio-echo"
version = "1.0.0"
[runtime]
kind = "stdio"
protocol = "sos-plugin/1"
command = ["python3", "plugin.py"]
working_directory = "${definition_dir}"
max_in_flight = 1
startup_timeout = "3s"
shutdown_timeout = "1s"
[capabilities]
network = false
filesystem = "none"
process = true
[[failure]]
name = "Rejected"
[[failure.field]]
name = "value"
type = "text"
[[action]]
name = "echo"
[[action.parameter]]
name = "value"
type = "text"
[action.result]
type = "number"
[[action]]
name = "reject"
failures = ["Rejected"]
[[action.parameter]]
name = "value"
type = "text"
[action.result]
type = "number"
`
	if err := os.WriteFile(filepath.Join(moduleDir, "module.sos.toml"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte("version=1\n[external]\nprocess=true\n[[module.external]]\npath=\"local/stdio-echo\"\ndefinition=\"modules/echo/module.sos.toml\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, dir, "main.sos", "import \"local/stdio-echo\" as ext\ncall ext.echo with \"one\" called pid_one\ncall ext.echo with \"two\" called pid_two\nshow pid_one\nshow pid_two\n")
	stdout, stderr, code := runCLI(t, dir, "module", "doctor", "local/stdio-echo")
	if code != 0 || !strings.Contains(stdout, "ready") {
		t.Fatalf("module doctor exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("stdio run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	lines := strings.Fields(stdout)
	if len(lines) != 2 || lines[0] != lines[1] {
		t.Fatalf("plugin process was not reused: %q", stdout)
	}
	failureScript := writeScript(t, dir, "failure.sos", "import \"local/stdio-echo\" as ext\ncall ext.echo with \"before\" called pid_before\nshow pid_before\ncall ext.reject with \"unsafe\" called result\n  on failure Rejected using value:\n    show value\n    recover with 0\nshow result\ncall ext.echo with \"after\" called pid_after\nshow pid_after\n")
	stdout, stderr, code = runCLI(t, dir, "run", failureScript)
	if code != 0 || !strings.Contains(stdout, "unsafe") || !strings.Contains(stdout, "0") {
		t.Fatalf("typed plugin failure exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	failureLines := strings.Fields(stdout)
	if len(failureLines) != 4 || failureLines[0] != failureLines[3] {
		t.Fatalf("declared failure restarted persistent plugin: %q", stdout)
	}
}

func TestExternalCommandJSONLinesStreamsThroughCLI(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the command stream fixture")
	}
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "modules", "events")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "producer.py"), []byte("import json\nfor n in range(3): print(json.dumps({'sequence': n}), flush=True)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := `schema=1
[module]
path="local/command-events"
version="1.0.0"
[runtime]
kind="command"
working_directory="${definition_dir}"
[capabilities]
process=true
[[type]]
name="Event"
[[type.field]]
name="sequence"
type="integer"
[[failure]]
name="StreamDecodeFailure"
[[action]]
name="follow"
[action.result]
type="stream of Event"
[action.command]
program="python3"
arguments=["producer.py"]
stdout="json-lines"
stderr="diagnostic"
[[action]]
name="broken"
failures=["StreamDecodeFailure"]
[action.result]
type="stream of Event"
[action.command]
program="python3"
arguments=["-c", "print('not-json')"]
stdout="json-lines"
`
	if err := os.WriteFile(filepath.Join(moduleDir, "module.sos.toml"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	config := "version=1\n[external]\nprocess=true\n[[module.external]]\npath=\"local/command-events\"\ndefinition=\"modules/events/module.sos.toml\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, dir, "main.sos", "import \"local/command-events\" as events\nstream events.follow called source\nfor each event from source:\n  show sequence of event\n")
	stdout, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 || stdout != "0\n1\n2\n" {
		t.Fatalf("command stream exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	failureScript := writeScript(t, dir, "failure.sos", "import \"local/command-events\" as events\nstream events.broken called source\nfor each event from source:\n  show sequence of event\n  on failure StreamDecodeFailure using message:\n    show message\n    recover\nshow \"caught\"\n")
	stdout, stderr, code = runCLI(t, dir, "run", failureScript)
	if code != 0 || !strings.Contains(stdout, "invalid JSON line") || !strings.Contains(stdout, "caught") {
		t.Fatalf("command stream typed decode failure exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestExternalNodePluginUsesSameProtocol(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required for the protocol fixture")
	}
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "modules", "upper")
	if err := os.MkdirAll(moduleDir, 0755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("fixtures/external/node_plugin.js")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "plugin.js"), fixture, 0644); err != nil {
		t.Fatal(err)
	}
	definition := `schema=1
[module]
path="local/node-upper"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["node","plugin.js"]
working_directory="${definition_dir}"
[capabilities]
process=true
[[action]]
name="upper"
[[action.parameter]]
name="value"
type="text"
[action.result]
type="text"
`
	if err := os.WriteFile(filepath.Join(moduleDir, "module.sos.toml"), []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	config := "version=1\n[external]\nprocess=true\n[[module.external]]\npath=\"local/node-upper\"\ndefinition=\"modules/upper/module.sos.toml\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, dir, "main.sos", "import \"local/node-upper\" as node\ncall node.upper with \"hello\" called result\nshow result\n")
	stdout, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 || strings.TrimSpace(stdout) != "HELLO" {
		t.Fatalf("node plugin exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestExternalBundledDistributionIsLockedAndRelocatable(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the bundled fixture")
	}
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "modules", "bundled")
	if err := os.MkdirAll(moduleDir, 0755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("fixtures/external/python_plugin.py")
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(moduleDir, "plugin")
	if err := os.WriteFile(artifact, fixture, 0755); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(fixture))
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	osName := runtime.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	target := osName + "-" + arch
	definition := fmt.Sprintf(`schema=1
[module]
path="local/bundled"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["./plugin"]
working_directory="${definition_dir}"
[capabilities]
process=true
[distribution]
mode="bundled"
[[distribution.artifact]]
target=%q
path="plugin"
sha256=%q
[[action]]
name="echo"
[[action.parameter]]
name="value"
type="text"
[action.result]
type="number"
`, target, digest)
	if err := os.WriteFile(filepath.Join(moduleDir, "module.sos.toml"), []byte(definition), 0644); err != nil {
		t.Fatal(err)
	}
	config := "version=1\n[external]\nprocess=true\n[[module.external]]\npath=\"local/bundled\"\ndefinition=\"modules/bundled/module.sos.toml\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, dir, "main.sos", "import \"local/bundled\" as ext\ncall ext.echo with \"ok\" called pid\nshow pid\n")
	for _, name := range []string{"bundle-a", "bundle-b"} {
		bundle := filepath.Join(dir, name, "app")
		_, stderr, code := runCLI(t, dir, "build", script, "--output", bundle)
		if code != 0 {
			t.Fatalf("bundle build %s exit=%d stderr=%q", name, code, stderr)
		}
		output, err := exec.Command(filepath.Join(bundle, "app")).CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) == "" {
			t.Fatalf("bundle run output=%q err=%v", output, err)
		}
	}
	first, err := os.ReadFile(filepath.Join(dir, "bundle-a", "app", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "bundle-b", "app", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("bundle manifests differ:\n%s\n%s", first, second)
	}
	firstExecutable, err := os.ReadFile(filepath.Join(dir, "bundle-a", "app", "app"))
	if err != nil {
		t.Fatal(err)
	}
	secondExecutable, err := os.ReadFile(filepath.Join(dir, "bundle-b", "app", "app"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstExecutable, secondExecutable) {
		t.Fatal("bundle executables are not reproducible")
	}
	if !strings.Contains(string(first), `"sha256": "sha256:`+digest+`"`) {
		t.Fatalf("manifest missing checksum: %s", first)
	}
	for _, required := range []string{`"schema": 1`, `"entrypoint": "app"`, `"pluginProtocol": "sos-plugin/1"`, `"distribution": "bundled"`, `"capabilities"`} {
		if !strings.Contains(string(first), required) {
			t.Fatalf("manifest missing %s: %s", required, first)
		}
	}
	manifestPath := filepath.Join(dir, "bundle-a", "app", "manifest.json")
	lockedChecksum := []byte(`"sha256": "sha256:` + digest + `"`)
	brokenChecksum := []byte(`"sha256": "sha256:` + strings.Repeat("0", len(digest)) + `"`)
	if err := os.WriteFile(manifestPath, bytes.Replace(first, lockedChecksum, brokenChecksum, 1), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(filepath.Join(dir, "bundle-a", "app", "app")).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "not locked by manifest") {
		t.Fatalf("modified manifest output=%q err=%v", output, err)
	}
	if err := os.WriteFile(manifestPath, first, 0o644); err != nil {
		t.Fatal(err)
	}
	moduleIdentity := sha256.Sum256([]byte("external:local/bundled"))
	tampered := filepath.Join(dir, "bundle-a", "app", "modules", fmt.Sprintf("local-bundled-%x", moduleIdentity[:16]), "plugin")
	if err := os.WriteFile(tampered, append(fixture, []byte("\n# tampered\n")...), 0755); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(filepath.Join(dir, "bundle-a", "app", "app")).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "bundled artifact checksum mismatch") {
		t.Fatalf("tampered bundle output=%q err=%v", output, err)
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
