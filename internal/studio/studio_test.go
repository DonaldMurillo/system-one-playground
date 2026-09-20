package studio

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

func post(t *testing.T, ts *httptest.Server, token, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Studio-Token", token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var data map[string]any
	json.Unmarshal(raw, &data) //nolint:errcheck
	return res, data
}

func get(t *testing.T, ts *httptest.Server, token, path string) (*http.Response, map[string]any, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Studio-Token", token)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var data map[string]any
	json.Unmarshal(raw, &data) //nolint:errcheck
	return res, data, string(raw)
}

func errKind(t *testing.T, data map[string]any) string {
	t.Helper()
	e, _ := data["error"].(map[string]any)
	k, _ := e["kind"].(string)
	return k
}

func TestTokenRequired(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, "", "/api/check", `{"source":"show \"hi\""}`)
	if res.StatusCode != http.StatusUnauthorized || errKind(t, data) != "auth" {
		t.Fatalf("missing token: got %d %v", res.StatusCode, data)
	}
	res, data = post(t, ts, s.Token()+"x", "/api/check", `{"source":""}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", res.StatusCode)
	}
	res, _ = post(t, ts, s.Token(), "/api/check", `{"source":""}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("valid token: got %d", res.StatusCode)
	}
}

func TestOriginValidation(t *testing.T) {
	s, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/check", strings.NewReader(`{"source":""}`))
	req.Header.Set("X-Studio-Token", s.Token())
	req.Header.Set("Origin", "http://evil.example")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: got %d", res.StatusCode)
	}

	// Same-origin and the Wails webview origin must pass; missing Origin
	// (curl, tests) is allowed because the token is still required.
	for _, origin := range []string{"", ts.URL, "http://wails.localhost"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/check", strings.NewReader(`{"source":""}`))
		req.Header.Set("X-Studio-Token", s.Token())
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusForbidden {
			t.Fatalf("origin %q rejected", origin)
		}
	}
}

func TestExamplesListAndOpen(t *testing.T) {
	s, ts := newTestServer(t)
	res, data, _ := get(t, ts, s.Token(), "/api/examples")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("examples: %d", res.StatusCode)
	}
	examples, _ := data["examples"].([]any)
	if len(examples) < 2 {
		t.Fatalf("expected bundled examples, got %v", examples)
	}
	first, _ := examples[0].(map[string]any)
	name, _ := first["name"].(string)
	res, data = post(t, ts, s.Token(), "/api/open", `{"name":"`+name+`"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("open %q: %d", name, res.StatusCode)
	}
	if src, _ := data["source"].(string); strings.TrimSpace(src) == "" {
		t.Fatal("example source is empty")
	}
	res, _ = post(t, ts, s.Token(), "/api/open", `{"name":"../../go.mod"}`)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("traversal name: got %d", res.StatusCode)
	}
}

func TestSemanticDictionaryDemoAnalyzesAndRunsOffline(t *testing.T) {
	s, ts := newTestServer(t)
	res, opened := post(t, ts, s.Token(), "/api/open", `{"name":"semantic-dictionary"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("open semantic dictionary demo: %d %+v", res.StatusCode, opened)
	}
	source, _ := opened["source"].(string)
	if !strings.Contains(source, "if age bigger 18 show") {
		t.Fatalf("semantic dictionary example is missing its defining sentence: %q", source)
	}
	raw, _ := json.Marshal(map[string]any{"source": source})
	res, analyzed := post(t, ts, s.Token(), "/api/analyze", string(raw))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("analyze semantic dictionary demo: %d %+v", res.StatusCode, analyzed)
	}
	analysis, _ := analyzed["analysis"].(map[string]any)
	decisions, _ := analysis["decisions"].([]any)
	if len(decisions) != 2 {
		t.Fatalf("want two local dictionary decisions, got %+v", analysis)
	}
	for _, value := range decisions {
		decision := value.(map[string]any)
		if decision["method"] != "deterministic" {
			t.Fatalf("demo unexpectedly requires Jev: %+v", decision)
		}
		if matches, _ := decision["matches"].([]any); len(matches) != 3 {
			t.Fatalf("demo does not expose dictionary matches: %+v", decision)
		}
	}
	usage, _ := analysis["usage"].(map[string]any)
	if usage["totalAdmitted"] != float64(0) {
		t.Fatalf("demo spent a provider request: %+v", usage)
	}

	res, ran := post(t, ts, s.Token(), "/api/run", string(raw))
	if res.StatusCode != http.StatusOK || ran["ok"] != true {
		t.Fatalf("run semantic dictionary demo: %d %+v", res.StatusCode, ran)
	}
	if got, _ := ran["output"].(string); strings.TrimSpace(got) != "adult\nqualified" {
		t.Fatalf("unexpected demo output %q", got)
	}
}

func TestCheckAndFormatEnvelopes(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/check", `{"source":"show \"hi\""}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("check: %d %v", res.StatusCode, data)
	}
	if _, ok := data["diagnostics"].([]any); !ok {
		t.Fatalf("check diagnostics missing: %v", data)
	}
	res, data = post(t, ts, s.Token(), "/api/format", `{"source":"show \"hi\""}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("format: %d", res.StatusCode)
	}
	if _, ok := data["source"].(string); !ok {
		t.Fatalf("format source missing: %v", data)
	}
	res, data = post(t, ts, s.Token(), "/api/check", `{invalid`)
	if res.StatusCode != http.StatusBadRequest || errKind(t, data) != "request" {
		t.Fatalf("invalid body: got %d %v", res.StatusCode, data)
	}
}

func TestRunEnvelope(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/run", `{"source":"show \"hello\"","timeoutMs":5000}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("run: %d %v", res.StatusCode, data)
	}
	if _, ok := data["ok"].(bool); !ok {
		t.Fatalf("run ok flag missing: %v", data)
	}
	if _, ok := data["output"].(string); !ok {
		t.Fatalf("run output missing: %v", data)
	}
	if _, ok := data["traces"].([]any); !ok {
		t.Fatalf("run traces missing: %v", data)
	}
	if _, ok := data["steps"].(float64); !ok {
		t.Fatalf("run steps missing: %v", data)
	}
	if ms, ok := data["durationMs"].(float64); !ok || ms < 0 {
		t.Fatalf("run durationMs missing: %v", data)
	}
}

func TestCancelWithoutRunIsConflict(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/cancel", `{}`)
	if res.StatusCode != http.StatusConflict || errKind(t, data) != "idle" {
		t.Fatalf("idle cancel: got %d %v", res.StatusCode, data)
	}
}

func TestSaveDownload(t *testing.T) {
	s, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/save",
		strings.NewReader(`{"name":"../../etc/passwd","source":"show \"x\""}`))
	req.Header.Set("X-Studio-Token", s.Token())
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save: %d", res.StatusCode)
	}
	disp := res.Header.Get("Content-Disposition")
	if !strings.Contains(disp, "attachment") || strings.ContainsAny(disp, "/\\") || strings.Contains(disp, "..") {
		t.Fatalf("unsafe disposition: %q", disp)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != `show "x"` {
		t.Fatalf("save body: %q", body)
	}
}

func TestIndexServedWithInjectedToken(t *testing.T) {
	s, ts := newTestServer(t)
	res, _, raw := get(t, ts, "", "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("index: %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("index content type: %q", ct)
	}
	if !strings.Contains(raw, `window.__SOS_STUDIO_TOKEN__="`+s.token+`"`) {
		t.Fatal("token not injected into document")
	}
	if res.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("CSP missing on document")
	}
	res, _, _ = get(t, ts, "", "/definitely-not-an-asset.js")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown asset: got %d", res.StatusCode)
	}
}

func TestSessionMetadata(t *testing.T) {
	s, ts := newTestServer(t)
	res, data, _ := get(t, ts, s.Token(), "/api/session")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session: %d", res.StatusCode)
	}
	if dir, _ := data["dir"].(string); dir != s.Dir() {
		t.Fatalf("session dir: %v", data)
	}
	if kw, _ := data["keywords"].([]any); len(kw) == 0 {
		t.Fatalf("session keywords empty: %v", data)
	}
}
