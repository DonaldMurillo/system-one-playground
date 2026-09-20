package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/internal/studio"
)

func TestStudioFrontmatterBudget(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	server, err := studio.New(studio.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	defer host.Close()
	call := func(path, source string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"source": source})
		req, _ := http.NewRequest("POST", host.URL+path, bytes.NewReader(body))
		req.Header.Set("X-Studio-Token", server.Token())
		resp, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, result)
		}
		return result
	}
	header := "+++\nversion = 1\n[budget.run]\nrequests = 0\n+++\n"
	result := call("/api/run", header+"show 5\n")
	if result["ok"] != true || result["output"] != "5\n" {
		t.Fatalf("ordinary code: %v", result)
	}
	usage, ok := result["usage"].(map[string]any)
	if !ok || usage["totalAdmitted"] != float64(0) || usage["totalLimit"] != float64(0) {
		t.Fatalf("usage: %v", result)
	}
	result = call("/api/run", header+"judge \"hello\" by jev \"Urgent\" called answer\n")
	failure, _ := result["error"].(map[string]any)
	message, _ := failure["message"].(string)
	if result["ok"] != false || failure["kind"] != "budget" || !strings.Contains(message, "budget exhausted") {
		t.Fatalf("live denial: %v", result)
	}
	result = call("/api/check", header+"show unknown_name\n")
	ds, _ := result["diagnostics"].([]any)
	if len(ds) != 1 {
		t.Fatalf("diagnostics: %v", result)
	}
	diag := ds[0].(map[string]any)
	if diag["line"] != float64(6) {
		t.Fatalf("wrong source position: %v", diag)
	}
}
