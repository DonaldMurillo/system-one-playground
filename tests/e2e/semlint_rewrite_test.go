package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func semlintRewriteFixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"sample.js", "rules.json"} {
		data, err := os.ReadFile(filepath.Join(root, "examples/sos/semlint/fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		writeScript(t, dir, name, string(data))
	}
	return dir, filepath.Join(root, "examples/sos/semlint/scan.sos")
}

func TestSemlintSOSSourceWorkflow(t *testing.T) {
	fx := semCanary(t)
	dir, script := semlintRewriteFixture(t)
	run := func(command string, args ...string) (string, string, int) {
		return runCLI(t, dir, append([]string{"run", script, "--", command, "--root", ".", "--sets", "none", "--rules_file", "rules.json"}, args...)...)
	}
	if out, err, code := runCLI(t, dir, "check", script); code != 0 {
		t.Fatalf("check: %s %s", out, err)
	}
	out, stderr, code := run("sites")
	var units []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &units) != nil || len(units) != 2 {
		t.Fatalf("sites: %d %s %s", code, out, stderr)
	}
	out, stderr, code = run("check")
	var report struct {
		Findings []struct {
			File string
			Line int
			Rule string
		}
		Skipped []any
		Units   int
	}
	if code != 1 || json.Unmarshal([]byte(out), &report) != nil || len(report.Findings) != 2 || report.Units != 2 || len(report.Skipped) != 0 {
		t.Fatalf("check findings: %d %s %s", code, out, stderr)
	}
	if report.Findings[0].Line != 2 || report.Findings[1].Line != 6 {
		t.Fatalf("locations: %+v", report.Findings)
	}
	out, stderr, code = run("check", "--severity", "error")
	if code != 0 || !strings.Contains(out, `"findings":[]`) {
		t.Fatalf("severity: %d %s %s", code, out, stderr)
	}
	out, stderr, code = run("check", "--max_units", "1")
	if code != 2 || !strings.Contains(stderr, "exceed") || out != "" {
		t.Fatalf("guard: %d %s %s", code, out, stderr)
	}
	writeScript(t, dir, "change.patch", "diff --git a/sample.js b/sample.js\n--- a/sample.js\n+++ b/sample.js\n@@ -6 +6 @@\n-  console.log(\"before\");\n+  console.log(\"second\");\n")
	out, stderr, code = run("check", "--diff_file", "change.patch", "--no_fail")
	if code != 0 || json.Unmarshal([]byte(out), &report) != nil || len(report.Findings) != 1 || report.Findings[0].Line != 6 {
		t.Fatalf("diff: %d %s %s", code, out, stderr)
	}
	fx.requireNoContacts(t)
}

func TestSemlintSOSParallelJudgmentsAndFailures(t *testing.T) {
	isolateConfigHome(t)
	dir, script := semlintRewriteFixture(t)
	writeScript(t, dir, "rules.json", `{"rules":[{"id":"needs-review","severity":"warning","where":{"ext":[".js"],"pattern":"console\\.log"},"ask":{"instructions":"Does this log need review?","threshold":0.75},"message":"Review log","why":"Example rule"}]}`)
	var active, peak, calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		var body struct {
			State     map[string]any `json:"state"`
			Questions map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Questions) != 1 {
			t.Errorf("question batch: %+v", body.Questions)
		}
		first := strings.Contains(fmt.Sprint(body.State["code"]), "first")
		if first {
			time.Sleep(90 * time.Millisecond)
		} else {
			time.Sleep(20 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() && first {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"type":"service_unavailable","message":"fixture unavailable"}}`)
			return
		}
		fmt.Fprint(w, `{"model":"test","answers":{"needs-review":{"type":"noul","noul":0.9}},"usage":{"input_tokens":20}}`)
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-only")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	run := func(extra ...string) (string, string, int) {
		return runCLI(t, dir, append([]string{"run", script, "--", "check", "--root", ".", "--sets", "none", "--rules_file", "rules.json", "--workers", "2"}, extra...)...)
	}
	out, stderr, code := run()
	var report struct {
		Findings []struct{ Line int }
		Skipped  []any
	}
	if code != 1 || json.Unmarshal([]byte(out), &report) != nil || len(report.Findings) != 2 || peak.Load() != 2 || calls.Load() != 2 {
		t.Fatalf("parallel: %d %s %s peak=%d calls=%d", code, out, stderr, peak.Load(), calls.Load())
	}
	if report.Findings[0].Line != 2 || report.Findings[1].Line != 6 {
		t.Fatalf("completion changed order: %+v", report.Findings)
	}
	writeScript(t, dir, "sample.expected.json", `{"file":"sample.js","expect":[{"rule":"needs-review","line":2}]}`)
	out, stderr, code = runCLI(t, dir, "run", script, "--", "separation", "--root", ".", "--sets", "none", "--rules_file", "rules.json", "--workers", "2", "--runs", "2")
	var measurement struct {
		Measurements []struct {
			Verdict string
			True    []float64 `json:"true_readings"`
			False   []float64 `json:"false_readings"`
		}
	}
	if code != 0 || json.Unmarshal([]byte(out), &measurement) != nil || len(measurement.Measurements) != 1 || measurement.Measurements[0].Verdict != "UNUSABLE" || len(measurement.Measurements[0].True) != 2 || len(measurement.Measurements[0].False) != 2 {
		t.Fatalf("separation: %d %s %s", code, out, stderr)
	}

	fail.Store(true)
	out, stderr, code = run()
	if code != 2 || json.Unmarshal([]byte(out), &report) != nil || len(report.Findings) != 1 || len(report.Skipped) != 1 || active.Load() != 0 || !strings.Contains(out, `"kind":"provider"`) {
		t.Fatalf("partial: %d %s %s active=%d", code, out, stderr, active.Load())
	}
}

func TestSemlintSOSGitScope(t *testing.T) {
	fx := semCanary(t)
	dir, script := semlintRewriteFixture(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("init", "-q")
	git("add", "sample.js")
	git("-c", "user.name=SOS Test", "-c", "user.email=sos@example.invalid", "commit", "-qm", "fixture")
	writeScript(t, dir, "sample.js", "function first() {\n  console.log(\"first\");\n}\n\nfunction second() {\n  console.log(\"updated\");\n}\n")
	writeScript(t, dir, "new.js", "function added() {\n  console.log(\"new\");\n}\n")
	out, stderr, code := runCLI(t, dir, "run", script, "--", "check", "--root", ".", "--sets", "none", "--rules_file", "rules.json", "--since", "HEAD", "--changed_lines_only", "--no_fail")
	var report struct {
		Findings []struct {
			File string
			Line int
		}
		Units int
	}
	if code != 0 || json.Unmarshal([]byte(out), &report) != nil || len(report.Findings) != 2 || report.Units != 2 || report.Findings[0].File != "new.js" || report.Findings[1].Line != 6 {
		t.Fatalf("git scope: %d %s %s", code, out, stderr)
	}
	fx.requireNoContacts(t)
}

func TestSemlintSOSOversizedRetry(t *testing.T) {
	isolateConfigHome(t)
	dir, script := semlintRewriteFixture(t)
	writeScript(t, dir, "sample.js", "function first() {\n  console.log(\"first\");\n}\n")
	writeScript(t, dir, "rules.json", `{"rules":[{"id":"needs-review","severity":"warning","where":{"ext":[".js"],"pattern":"console\\.log"},"ask":{"instructions":"Review?","threshold":0.75},"message":"Review log"}]}`)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body struct {
			State map[string]any `json:"state"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"detail":{"error_type":"max_tokens_exceeded","message":"fixture oversized"}}`)
			return
		}
		if !strings.Contains(fmt.Sprint(body.State["context_completeness"]), "INCOMPLETE") {
			t.Errorf("retry omitted disclosure: %+v", body.State)
		}
		fmt.Fprint(w, `{"model":"test","answers":{"needs-review":{"type":"noul","noul":0.9}},"usage":{"input_tokens":20}}`)
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-only")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	out, stderr, code := runCLI(t, dir, "run", script, "--max-calls", "2", "--", "check", "--root", ".", "--sets", "none", "--rules_file", "rules.json")
	if code != 1 || calls.Load() != 2 || !strings.Contains(out, "needs-review") {
		t.Fatalf("retry: %d %s %s calls=%d", code, out, stderr, calls.Load())
	}
}

func TestSemlintSOSNativeWorkflow(t *testing.T) {
	fx := semCanary(t)
	dir, script := semlintRewriteFixture(t)
	contents, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	temporary := writeScript(t, t.TempDir(), "scan.sos", string(contents))
	binary := hostExecutablePath(filepath.Join(t.TempDir(), "semlint-sos"))
	if out, stderr, code := runCLI(t, dir, "build", temporary, "--output", binary); code != 0 {
		t.Fatalf("native build: %d %s %s", code, out, stderr)
	}
	if err := os.Remove(temporary); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runAppArtifact(t, dir, binary, "check", "--root", ".", "--sets", "none", "--rules_file", "rules.json")
	if code != 1 || !strings.Contains(out, "console-output") || !strings.Contains(out, `"units":2`) {
		t.Fatalf("native source-free: %d %s %s", code, out, stderr)
	}
	fx.requireNoContacts(t)
}
