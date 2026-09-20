package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQuestionBatchCLI(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	var calls atomic.Int32
	var invalid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Questions map[string]any `json:"questions"`
			State     any            `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Questions) != 2 || body.Questions["urgent"] == nil || body.Questions["team"] == nil {
			t.Errorf("unexpected batch: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if invalid.Load() {
			w.Write([]byte(`{"answers":{"urgent":{"type":"noul"},"team":{"type":"choice","choice":"platform","confidence":0.9,"probabilities":{"platform":0.9,"other":0.1}}},"usage":{"input_tokens":5}}`))
			return
		}
		w.Write([]byte(`{"model":"test","answers":{"urgent":{"type":"noul","noul":0.95},"team":{"type":"choice","choice":"platform","confidence":0.9,"probabilities":{"platform":0.9,"other":0.1}}},"usage":{"input_tokens":25}}`))
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-only")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	source := `import "std/jev" as jev
call jev.noul with "Is this urgent?" called urgent
call jev.choice with "Who should investigate?", {"platform": "Service reliability", "other": "Another team"} called team
make questions with:
  urgent from urgent
  team from team
make state "Checkout is down"
evaluate state by jev using questions called answers
show answers.urgent.p_yes
show answers.team.value
`
	script := writeScript(t, dir, "batch.sos", source)
	if out, err, code := runCLI(t, dir, "check", script); code != 0 {
		t.Fatalf("check: %s %s", out, err)
	}
	record := filepath.Join(dir, "answers.json")
	out, stderr, code := runCLI(t, dir, "run", script, "--max-calls", "1", "--record", record)
	if code != 0 || !strings.Contains(out, "0.95") || !strings.Contains(out, "platform") || calls.Load() != 1 {
		t.Fatalf("batch: %d %s %s calls=%d", code, out, stderr, calls.Load())
	}
	out, stderr, code = runCLI(t, dir, "run", script, "--replay", record)
	if code != 0 || calls.Load() != 1 {
		t.Fatalf("replay: %d %s %s", code, out, stderr)
	}
	binary := filepath.Join(dir, "batch-cli")
	if out, stderr, code := runCLI(t, dir, "build", script, "--output", binary); code != 0 {
		t.Fatalf("build: %s %s", out, stderr)
	}
	if output, err := exec.Command(binary).CombinedOutput(); err != nil || !strings.Contains(string(output), "platform") {
		t.Fatalf("native batch: %s %v", output, err)
	}
	if calls.Load() != 2 {
		t.Fatal("native batch did not use one request")
	}
	invalid.Store(true)
	_, stderr, code = runCLI(t, dir, "run", script)
	if code == 0 || !strings.Contains(stderr, "invalid Noul probability") {
		t.Fatalf("malformed accepted: %d %s", code, stderr)
	}
	before := calls.Load()
	bad := writeScript(t, dir, "bad.sos", "make questions {\"bad\": {\"type\": \"choice\", \"instructions\": \"Pick\", \"criteria\": {\"only\": null}}}\nevaluate \"state\" by jev using questions called answers\n")
	_, _, code = runCLI(t, dir, "run", bad)
	if code == 0 || calls.Load() != before {
		t.Fatal("invalid questions dispatched")
	}
	zero := writeScript(t, dir, "zero.sos", frontmatter("version = 1\n[budget.run]\nrequests = 0\n")+source)
	_, _, code = runCLI(t, dir, "run", zero)
	if code == 0 || calls.Load() != before {
		t.Fatal("zero request budget bypassed")
	}
	_, _, code = runCLI(t, dir, "build", script, "--target", "wasm-browser", "--output", filepath.Join(dir, "batch.wasm"))
	if code == 0 || calls.Load() != before {
		t.Fatal("live batch incorrectly allowed on WASM")
	}
	denied := writeScript(t, dir, "denied.sos", frontmatter("version = 1\n[runtime]\njudgment = \"deny\"\n")+source)
	_, _, code = runCLI(t, dir, "run", denied)
	if code == 0 || calls.Load() != before {
		t.Fatal("runtime deny bypassed")
	}
	// Recorded nested answers retain the same validation as live responses.
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "0.95", "2.95"))
	if err = os.WriteFile(record, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, code = runCLI(t, dir, "run", script, "--replay", record)
	if code == 0 || calls.Load() != before {
		t.Fatal("invalid replay accepted")
	}
}

func TestPreparedSemlintSOS(t *testing.T) {
	isolateConfigHome(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "examples/sos/semlint")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Questions map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Questions) != 2 {
			t.Errorf("expected two questions, got %d", len(body.Questions))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"test","answers":{"error-swallowed":{"type":"noul","noul":0.9},"unvalidated-settings":{"type":"noul","noul":0.2}},"usage":{"input_tokens":20}}`))
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-only")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	out, stderr, code := runCLI(t, dir, "run", "main.sos", "--", "units.json")
	if code != 0 || calls.Load() != 1 || !strings.Contains(out, "error-swallowed") || strings.Contains(out, "unvalidated-settings") {
		t.Fatalf("SOS semlint: %d %s %s", code, out, stderr)
	}
}
