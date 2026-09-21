package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// These are HTTP accounting fixtures, not a semantic provider implementation.
// The product continues to use the real client; live semantic coverage is in
// TestLiveJevAndReplay. Here exact response shapes let us test billing failures.
func TestBudgetHTTPAccounting(t *testing.T) {
	cases := []struct {
		name, body, usage string
		status, exit      int
	}{
		{"reported", `{"model":"fixture","answers":{"answer":{"type":"noul","noul":0.9}},"usage":{"input_tokens":7}}`, "inputTokens=7 unresolved=0", 200, 0},
		{"missing usage", `{"model":"fixture","answers":{"answer":{"type":"noul","noul":0.9}}}`, "inputTokens=0 unresolved=1", 200, 0},
		{"invalid answer reported usage", `{"answers":{"answer":{"type":"noul","noul":9}},"usage":{"input_tokens":7}}`, "inputTokens=7 unresolved=0", 200, 1},
		{"service failure", `{"message":"unavailable"}`, "inputTokens=0 unresolved=1", 503, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOS_CONFIG_HOME", t.TempDir())
			t.Setenv("TYPESAFE_API_KEY", "test-transport-only")
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			t.Setenv("TYPESAFE_BASE_URL", server.URL)
			dir := t.TempDir()
			script := writeScript(t, dir, "accounting.sos", "judge \"text\" by jev \"Urgent\" called result\nshow result\n")
			_, stderr, code := runCLI(t, dir, "run", script, "--max-calls", "1")
			if code != tc.exit || calls.Load() != 1 || !strings.Contains(stderr, tc.usage) {
				t.Fatalf("exit=%d calls=%d stderr=%s", code, calls.Load(), stderr)
			}
		})
	}
}

func TestBudgetReplayMakesNoHTTPRequest(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "test-transport-only")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"answers":{"answer":{"type":"noul","noul":0.9}},"usage":{"input_tokens":7}}`)
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	dir := t.TempDir()
	script := writeScript(t, dir, "replay.sos", "judge \"text\" by jev \"Urgent\" called result\nshow result\n")
	record := filepath.Join(dir, "answers.json")
	first, stderr, code := runCLI(t, dir, "run", script, "--record", record)
	if code != 0 {
		t.Fatal(stderr)
	}
	writeScript(t, dir, "sos.toml", "version=1\n[runtime]\njudgment='deny'\n[budget.run]\nrequests=0\n")
	second, stderr, code := runCLI(t, dir, "run", script, "--replay", record)
	if code != 0 || first != second || calls.Load() != 1 || strings.Contains(stderr, "sos: usage") {
		t.Fatalf("replay exit=%d calls=%d out=%s err=%s", code, calls.Load(), second, stderr)
	}
}

func TestBudgetExhaustionStopsAdditionalHTTPAttempts(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "test-transport-only")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
		fmt.Fprint(w, `{"message":"unavailable"}`)
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	dir := t.TempDir()
	// Handling a provider failure must not reset the consumed allowance, and
	// budget exhaustion is fatal rather than catchable by the legacy handler.
	source := "+++\nversion=1\n[budget.run]\nrequests=1\n+++\nrepeat 3 times:\n  judge \"text\" by jev \"Urgent\" called answer\n    on failure show \"handled\"\n"
	script := writeScript(t, dir, "limits.sos", source)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 1 || calls.Load() != 1 || strings.Count(out, "handled") != 1 || !strings.Contains(stderr, "requests=1/1") {
		t.Fatalf("exit=%d calls=%d out=%s err=%s", code, calls.Load(), out, stderr)
	}
}
