package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/internal/studio"
)

// On-demand editor semantics over the public surfaces: the Studio HTTP API
// and the custom stdio LSP method. All cases run offline: the editor-off
// policy must refuse before any provider request, and canonical sources must
// analyze with zero admitted requests.

const analyzeURI = "file:///tmp/analyze.sos"

const analyzeSource = "command triage:\n  argument source as folder\n  make reports as empty list\n  show reports as table\n"

// setGlobalConfig points the global configuration layer at a fresh directory,
// optionally holding config.toml with the given body.
func setGlobalConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SOS_CONFIG_HOME", dir)
}

func postAnalyze(t *testing.T, host *httptest.Server, token, source string) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"source": source})
	req, _ := http.NewRequest("POST", host.URL+"/api/analyze", bytes.NewReader(body))
	req.Header.Set("X-Studio-Token", token)
	resp, err := host.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return resp, result
}

func analysisOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	analysis, ok := result["analysis"].(map[string]any)
	if !ok {
		t.Fatalf("response has no analysis object: %v", result)
	}
	return analysis
}

func usageOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	usage, ok := analysisOf(t, result)["usage"].(map[string]any)
	if !ok {
		t.Fatalf("analysis has no usage object: %v", result)
	}
	return usage
}

func errorOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	e, ok := result["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object: %v", result)
	}
	return e
}

func TestStudioAnalyzeEditorOffRefusesWithoutProvider(t *testing.T) {
	setGlobalConfig(t, "version = 1\n\n[editor]\nassistance = \"off\"\n")
	server, err := studio.New(studio.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	defer host.Close()

	resp, result := postAnalyze(t, host, server.Token(), analyzeSource)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("editor off: status %d %v", resp.StatusCode, result)
	}
	e := errorOf(t, result)
	msg, _ := e["message"].(string)
	if e["kind"] != "editor" || !strings.Contains(msg, "editor assistance is off") {
		t.Fatalf("editor off: %v", e)
	}
	if _, hasUsage := result["usage"]; hasUsage {
		t.Fatalf("editor off must not report usage: %v", result)
	}
}

func TestStudioAnalyzeCanonicalIsFreeAndReusable(t *testing.T) {
	setGlobalConfig(t, "")
	server, err := studio.New(studio.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	defer host.Close()

	resp, first := postAnalyze(t, host, server.Token(), analyzeSource)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical analyze: status %d %v", resp.StatusCode, first)
	}
	// The canonical policy is promoted to assisted for suggestions only.
	if first["promoted"] != true {
		t.Fatalf("canonical analyze should be labeled promoted: %v", first)
	}
	if usage := usageOf(t, first); usage["totalAdmitted"] != float64(0) {
		t.Fatalf("canonical analyze must admit zero requests: %v", usage)
	}

	// Re-analyzing the identical source reuses the cached analysis.
	resp, second := postAnalyze(t, host, server.Token(), analyzeSource)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repeat analyze: status %d %v", resp.StatusCode, second)
	}
	if second["reused"] != true {
		t.Fatalf("identical source should reuse the cached analysis: %v", second)
	}
	if usage := usageOf(t, second); usage["totalAdmitted"] != float64(0) {
		t.Fatalf("cache reuse must admit zero requests: %v", usage)
	}
}

func TestStudioAnalyzeInvalidFrontmatterIsConfigError(t *testing.T) {
	setGlobalConfig(t, "")
	// Frontmatter may not set editor preferences.
	source := "+++\nversion = 1\n\n[editor]\nassistance = \"off\"\n+++\nshow 5\n"
	server, err := studio.New(studio.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	defer host.Close()

	resp, result := postAnalyze(t, host, server.Token(), source)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("frontmatter: status %d %v", resp.StatusCode, result)
	}
	if e := errorOf(t, result); e["kind"] != "config" {
		t.Fatalf("frontmatter: %v", e)
	}
}

func TestStudioAnalyzeRequiresPOST(t *testing.T) {
	setGlobalConfig(t, "")
	server, err := studio.New(studio.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	defer host.Close()

	req, _ := http.NewRequest("GET", host.URL+"/api/analyze", nil)
	req.Header.Set("X-Studio-Token", server.Token())
	resp, err := host.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET analyze: status %d", resp.StatusCode)
	}
}

func TestLSPJevAnalyzeExplicitMethod(t *testing.T) {
	setGlobalConfig(t, "")
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{}))
	input.Write(notification("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": analyzeURI, "languageId": "sos", "version": 1, "text": analyzeSource},
	}))
	input.Write(request(2, "sos/analyze", map[string]any{"textDocument": map[string]any{"uri": analyzeURI}}))
	input.Write(request(3, "sos/analyze", map[string]any{}))
	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	var okResult, badParams map[string]any
	for _, m := range messages {
		switch m["id"] {
		case float64(2):
			okResult = m
		case float64(3):
			badParams = m
		}
	}
	if okResult == nil || badParams == nil {
		t.Fatalf("missing sos/analyze responses: %v", messages)
	}
	result := getMap(t, okResult, "result")
	analysis := getMap(t, result, "analysis")
	if result["promoted"] != true {
		t.Fatalf("canonical sos/analyze should be labeled promoted: %v", result)
	}
	usage, ok := analysis["usage"].(map[string]any)
	if !ok || usage["totalAdmitted"] != float64(0) {
		t.Fatalf("canonical sos/analyze must admit zero requests: %v", analysis)
	}
	if e := getMap(t, badParams, "error"); e["code"] != float64(-32600) {
		t.Fatalf("sos/analyze without text or document: %v", e)
	}
}

func TestLSPJevAnalyzeEditorOffRefuses(t *testing.T) {
	setGlobalConfig(t, "version = 1\n\n[editor]\nassistance = \"off\"\n")
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{}))
	input.Write(request(2, "sos/analyze", map[string]any{"text": analyzeSource}))
	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m["id"] != float64(2) {
			continue
		}
		e := getMap(t, m, "error")
		msg, _ := e["message"].(string)
		if e["code"] != float64(-32001) || !strings.Contains(msg, "editor") {
			t.Fatalf("editor off: %v", e)
		}
		return
	}
	t.Fatalf("missing sos/analyze response: %v", messages)
}
