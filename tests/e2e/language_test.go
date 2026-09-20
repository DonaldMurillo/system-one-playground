package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSentenceWorkflow(t *testing.T) {
	dir := t.TempDir()
	source := `make entries [{"service": "api", "level": "error"}, {"service": "web", "level": "info"}, {"service": "api", "level": "error"}]
keep entries where level is "error"
group entries by service called services
make reports as empty list
for each service in services numbered from 1:
  make report with:
    name from key of service
    errors from count of items of service
  append report to reports
save reports as json in "report.json"
read "report.json" as json called loaded
show loaded as table with name, errors
`
	path := writeScript(t, dir, "workflow.sos", source)
	out, err, code := runCLI(t, dir, "run", path)
	if code != 0 || !strings.Contains(strings.Join(strings.Fields(out), " "), "api 2") {
		t.Fatalf("code %d out %q error %s", code, out, err)
	}
	data, e := os.ReadFile(filepath.Join(dir, "report.json"))
	if e != nil || !strings.Contains(string(data), `"errors": 2`) {
		t.Fatalf("saved report %s %v", data, e)
	}
	_, err, code = runCLI(t, dir, "run", path)
	if code == 0 || !strings.Contains(err, "file exists") {
		t.Fatalf("must not silently overwrite: code=%d %s", code, err)
	}
}
func TestActionsBranchesAndLoops(t *testing.T) {
	dir := t.TempDir()
	p := writeScript(t, dir, "flow.sos", `to double with value:
  return value * 2
make total 0
while total < 3:
  assign total total + 1
call double with total called answer
when answer is 6:
  when false:
    show "wrong"
  otherwise:
    show "nested"
otherwise:
  show "wrong outer"
show answer
`)
	out, err, code := runCLI(t, dir, "run", p)
	if code != 0 || out != "nested\n6\n" {
		t.Fatalf("code %d out %q err %s", code, out, err)
	}
}
func TestDiagnosticsBeforeSideEffects(t *testing.T) {
	dir := t.TempDir()
	p := writeScript(t, dir, "bad.sos", `save "oops" as text in "should-not-exist"
show missing_name
`)
	_, err, code := runCLI(t, dir, "run", p)
	if code == 0 || !strings.Contains(err, "unknown name") {
		t.Fatalf("%d %s", code, err)
	}
	if _, e := os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(e) {
		t.Fatal("side effect occurred before static error")
	}
}
func TestHandledReadError(t *testing.T) {
	dir := t.TempDir()
	p := writeScript(t, dir, "fallback.sos", `read "missing.json" as json called rows
  on failure make rows as empty list
show count of rows
`)
	out, err, code := runCLI(t, dir, "run", p)
	if code != 0 || out != "0\n" {
		t.Fatalf("%d %q %s", code, out, err)
	}
}
func TestNoulWrapperChecksOffline(t *testing.T) {
	dir := t.TempDir()
	p := writeScript(t, dir, "semantic.sos", `make entries []
keep entries where jev:
  ask "The customer needs immediate help"
  using message
  accept probability at least 0.85
  on uncertain discard
show entries
`)
	out, err, code := runCLI(t, dir, "run", p)
	if code != 0 || out != "[]\n" {
		t.Fatalf("empty collection should need no credentials: %d %q %s", code, out, err)
	}
}

func TestLiveJevAndReplay(t *testing.T) {
	if os.Getenv("SOS_LIVE_TEST") != "1" {
		t.Skip("opt in to real TypeSafe API calls with SOS_LIVE_TEST=1")
	}
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	recording := filepath.Join(t.TempDir(), "answers.json")
	script := filepath.Join(root, "examples/sos/urgent-filter.sos")
	out, err, code := runCLI(t, root, "run", script, "--record", recording, "--max-calls", "2", "--timeout", "30s")
	if code != 0 || !strings.Contains(out, "checkout") || strings.Contains(out, "purple") {
		t.Fatalf("live classification: %d %q %s", code, out, err)
	}
	t.Setenv("TYPESAFE_BASE_URL", "http://127.0.0.1:1")
	replayOut, replayErr, code := runCLI(t, root, "run", script, "--replay", recording, "--max-calls", "2")
	if code != 0 || replayOut != out || !strings.Contains(replayErr, "replay=true") {
		t.Fatalf("offline replay mismatch: %d %q %s", code, replayOut, replayErr)
	}
}

func TestTriageCLI(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if e := os.Mkdir(logs, 0755); e != nil {
		t.Fatal(e)
	}
	source, e := os.ReadFile("../../examples/sos/triage.sos")
	if e != nil {
		t.Fatal(e)
	}
	data := `{"time":"2026-09-19T00:00:00Z","service":"api","level":"error","message":"database offline"}
{"time":"2026-09-19T00:00:00Z","service":"web","level":"info","message":"healthy"}
`
	if e = os.WriteFile(filepath.Join(logs, "app.jsonl"), []byte(data), 0644); e != nil {
		t.Fatal(e)
	}
	script := writeScript(t, dir, "triage.sos", string(source))
	out, stderr, code := runCLI(t, dir, "run", script, "--", logs, "--since", "876000h", "--output", filepath.Join(dir, "reports"))
	if code != 0 || !strings.Contains(out, "1 errors across 1 services") {
		t.Fatalf("triage code=%d output=%s err=%s", code, out, stderr)
	}
	b, e := os.ReadFile(filepath.Join(dir, "reports", "1.json"))
	if e != nil || !strings.Contains(string(b), `"category": "unclassified"`) {
		t.Fatalf("report %s %v", b, e)
	}
}

func TestLiveJevWorkflowExample(t *testing.T) {
	if os.Getenv("SOS_LIVE_TEST") != "1" {
		t.Skip("opt in with SOS_LIVE_TEST=1")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "examples/sos/jev-workflow.sos")
	saved := filepath.Join(t.TempDir(), "resolution.json")
	out, stderr, code := runCLI(t, root, "explain", script, "--save", saved)
	if code != 0 {
		t.Fatalf("analysis: %s %s", out, stderr)
	}
	if !strings.Contains(out, `"method": "jev"`) {
		t.Fatalf("expected provider interpretation: %s", out)
	}
	recording := filepath.Join(t.TempDir(), "decisions.json")
	out, stderr, code = runCLI(t, root, "run", script, "--resolution", saved, "--record", recording)
	if code != 0 || !strings.Contains(out, "Checkout") || !strings.Contains(out, "twice") || strings.Contains(out, "purple") {
		t.Fatalf("workflow: %d %s %s", code, out, stderr)
	}
	if !strings.Contains(stderr, "inputTokens=") || !strings.Contains(stderr, "unresolved=0") {
		t.Fatalf("missing measured usage: %s", stderr)
	}
	data, err := os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"decision": "keep"`, `"item":`, `"reason":`, "acceptance threshold"} {
		if !strings.Contains(string(data), fragment) {
			t.Fatalf("missing decision evidence %s: %s", fragment, data)
		}
	}
	t.Log(stderr)
}
