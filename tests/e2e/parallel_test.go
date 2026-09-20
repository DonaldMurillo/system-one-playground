package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelMapOrderedBudgetAndReplay(t *testing.T) {
	isolateConfigHome(t)
	var active, peak, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := active.Add(1)
		defer active.Add(-1)
		calls.Add(1)
		for {
			old := peak.Load()
			if a <= old || peak.CompareAndSwap(old, a) {
				break
			}
		}
		var req struct {
			State float64 `json:"state"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		select {
		case <-time.After(time.Duration(5-int(req.State)) * 30 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":"fixture","answers":{"answer":{"type":"noul","noul":%g}},"usage":{"input_tokens":7}}`, req.State/10)
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	dir := t.TempDir()
	src := `make items [1, 2, 3, 4]
make untouched [9]
map each item in items with at most 2 running called results:
  append item to untouched
  judge item by jev "Is the value relevant?" called answer
  show item
  return answer.p_yes
show results
show untouched
`
	script := writeScript(t, dir, "map.sos", src)
	recording := filepath.Join(dir, "record.json")
	out, stderr, code := runCLI(t, dir, "run", script, "--record", recording)
	if code != 0 {
		t.Fatalf("run %d %s %s", code, out, stderr)
	}
	if peak.Load() != 2 || calls.Load() != 4 {
		t.Fatalf("peak=%d calls=%d", peak.Load(), calls.Load())
	}
	if !strings.HasPrefix(out, "1\n2\n3\n4\n") || !strings.Contains(out, "[0.1,0.2,0.3,0.4]") || !strings.HasSuffix(out, "[9]\n") {
		t.Fatalf("unordered/leaked: %q", out)
	}
	replay, stderr, code := runCLI(t, dir, "run", script, "--replay", recording)
	if code != 0 || replay != out || calls.Load() != 4 {
		t.Fatalf("replay: %d %s %s", code, replay, stderr)
	}
	var records []struct {
		Path string `json:"path"`
	}
	data, err := os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[0].Path == records[1].Path {
		t.Fatalf("missing stable paths: %+v", records)
	}
	zero := writeScript(t, dir, "limited.sos", frontmatter("version = 1\n[budget.run]\nrequests = 1\n")+src)
	before := calls.Load()
	_, _, code = runCLI(t, dir, "run", zero)
	if code == 0 || calls.Load()-before > 1 {
		t.Fatal("parallel budget overspent")
	}
	// Exercise the in-process HTTP runtime too, so -race instruments workers.
	api := newWorkspaceAPI(t, dir)
	response := api.call("/api/run", map[string]any{"path": "map.sos", "source": src}, 200)
	if response["ok"] != true || response["output"] != out {
		t.Fatalf("HTTP parallel map: %#v", response)
	}
}

func TestParallelMapCollectFailuresAndJoin(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	src := `make values [1, 0, 2]
map each value in values with at most 3 running called results collecting failures:
  return 10 / value
show results
`
	script := writeScript(t, dir, "collect.sos", src)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("collect: %s %s", out, stderr)
	}
	var results []struct {
		OK    bool           `json:"ok"`
		Value any            `json:"value"`
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || !results[0].OK || results[1].OK || !results[2].OK || results[1].Error["kind"] != "runtime" {
		t.Fatalf("outcomes: %s", out)
	}
	for _, limit := range []string{"0", "129", "1.5"} {
		bad := writeScript(t, dir, "limit.sos", strings.Replace(src, "at most 3", "at most "+limit, 1))
		_, _, code := runCLI(t, dir, "run", bad)
		if code == 0 {
			t.Fatalf("accepted workers=%s", limit)
		}
	}
	var active atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	timeout := writeScript(t, dir, "timeout.sos", frontmatter("version = 1\n[budget.run]\ntimeout = \"100ms\"\n")+`make values [1, 2, 3]
map each value in values with at most 2 running called results collecting failures:
  judge value by jev "Is this relevant?" called answer
  return answer
show "unreachable"
`)
	out, _, code = runCLI(t, dir, "run", timeout)
	if code == 0 || strings.Contains(out, "unreachable") {
		t.Fatal("timeout swallowed")
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatal("workers remained live after CLI returned")
	}
}

func TestParallelRecordedFailuresAndIntegrity(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		w.Write([]byte(`{"detail":{"error_type":"unavailable","message":"provider temporarily unavailable"}}`))
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	source := `make values [1,2]
map each value in values with at most 2 running called outcomes collecting failures:
  judge value by jev "Is this relevant?" called answer
  return answer
show outcomes
`
	script := writeScript(t, dir, "failed.sos", source)
	record := filepath.Join(dir, "record.json")
	out, stderr, code := runCLI(t, dir, "run", script, "--record", record)
	if code != 0 || calls.Load() != 2 {
		t.Fatalf("record failures: %d %s %s", code, out, stderr)
	}
	replay, stderr, code := runCLI(t, dir, "run", script, "--replay", record)
	if code != 0 || out != replay || calls.Load() != 2 {
		t.Fatalf("replay failures: %d %s %s", code, replay, stderr)
	}
	if err := os.WriteFile(record, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = runCLI(t, dir, "run", script, "--replay", record)
	if code == 0 || !strings.Contains(stderr, "replay has no answer") || calls.Load() != 2 {
		t.Fatalf("missing replay swallowed: %d %s", code, stderr)
	}
}
