package studio

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// newTestServerFor serves an existing session (used when the session dir
// must contain a prepared module workspace).
func newTestServerFor(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

func TestLSPBridgeSemanticTokens(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/lsp",
		`{"source":"make x 7","method":"textDocument/semanticTokens/full"}`)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	result, _ := data["result"].(map[string]any)
	got, _ := result["data"].([]any)
	want := []float64{0, 0, 4, 0, 0, 0, 5, 1, 1, 0, 0, 2, 1, 7, 0}
	if len(got) != len(want) {
		t.Fatalf("data = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("data[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestLSPBridgeExposesReferencesAndRename(t *testing.T) {
	s, ts := newTestServer(t)
	source := "stream source called events\nclose stream events\n"
	res, refs := post(t, ts, s.Token(), "/api/lsp", `{"source":`+strconv.Quote(source)+`,"method":"textDocument/references","position":{"line":1,"character":14}}`)
	if res.StatusCode != 200 || len(refs["result"].([]any)) != 2 {
		t.Fatalf("references: %d %+v", res.StatusCode, refs)
	}
	res, renamed := post(t, ts, s.Token(), "/api/lsp", `{"source":`+strconv.Quote(source)+`,"method":"textDocument/rename","position":{"line":1,"character":14},"newName":"incoming"}`)
	if res.StatusCode != 200 {
		t.Fatalf("rename: %d %+v", res.StatusCode, renamed)
	}
	changes := renamed["result"].(map[string]any)["changes"].(map[string]any)
	for _, edits := range changes {
		if len(edits.([]any)) != 2 {
			t.Fatalf("rename edits=%+v", edits)
		}
	}
}

func TestLSPBridgeInlayHint(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/lsp",
		`{"source":"command tidy:\n  make count 3\n","method":"textDocument/inlayHint"}`)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	hints, _ := data["result"].([]any)
	if len(hints) != 1 {
		t.Fatalf("hints = %+v, want the literal hint", hints)
	}
	h := hints[0].(map[string]any)
	if h["label"] != ": number" {
		t.Fatalf("label = %v", h["label"])
	}
	pos := h["position"].(map[string]any)
	if pos["line"] != float64(1) || pos["character"] != float64(12) {
		t.Fatalf("position = %+v", pos)
	}
}

func TestLSPBridgeCodeActionAutoImport(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/lsp",
		`{"source":"command tidy:\n  call text.trim with name called cleaned\n","method":"textDocument/codeAction",`+
			`"range":{"start":{"line":0,"character":0},"end":{"line":1,"character":0}},"context":{"diagnostics":[]}}`)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	actions, _ := data["result"].([]any)
	if len(actions) != 1 {
		t.Fatalf("actions = %+v", actions)
	}
	a := actions[0].(map[string]any)
	if a["kind"] != "quickfix" || a["title"] != "Import \"std/text\"" {
		t.Fatalf("action = %+v", a)
	}
	changes := a["edit"].(map[string]any)["changes"].(map[string]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestLSPBridgeWorkspaceBackedImports(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte("version = 1\n[module]\npath = \"example.com/demo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "util"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "util", "helper.sos"), []byte("package util\nexport greet\nto greet with name:\n  return name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ts := newTestServerFor(t, s)

	// Completion offers the indexed local action, with the import edit. The
	// client-supplied workspacePath field must not redirect indexing.
	res, data := post(t, ts, s.Token(), "/api/lsp",
		`{"source":"command tidy:\n  call ut","method":"textDocument/completion","position":{"line":1,"character":9},`+
			`"workspacePath":"/etc"}`)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	items, _ := data["result"].([]any)
	var greet map[string]any
	for _, it := range items {
		if item, ok := it.(map[string]any); ok && item["label"] == "util.greet" {
			greet = item
		}
	}
	if greet == nil {
		t.Fatalf("util.greet missing from %+v", items)
	}
	extra, _ := greet["additionalTextEdits"].([]any)
	if len(extra) != 1 || extra[0].(map[string]any)["newText"] != "import \"example.com/demo/util\" as util\n" {
		t.Fatalf("additionalTextEdits = %+v", extra)
	}

	// codeAction fixes a used local package call.
	res, data = post(t, ts, s.Token(), "/api/lsp",
		`{"source":"command tidy:\n  call util.greet with name called hi\n","method":"textDocument/codeAction",`+
			`"range":{"start":{"line":0,"character":0},"end":{"line":1,"character":0}},"context":{"diagnostics":[]}}`)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	actions, _ := data["result"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["title"] != "Import \"example.com/demo/util\" as util" {
		t.Fatalf("actions = %+v", actions)
	}
}

func TestLSPBridgeRejectsUnknownMethod(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/lsp", `{"source":"show 1","method":"workspace/symbol"}`)
	if res.StatusCode != 400 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if errKind(t, data) != "method" {
		t.Fatalf("data = %+v", data)
	}
}

func TestLSPBridgeVocabulary(t *testing.T) {
	s, ts := newTestServer(t)
	res, data := post(t, ts, s.Token(), "/api/lsp",
		`{"source":"import \"std/text\" as text\nmake title \" x \"\ncall text.trim with title called clean\n",`+
			`"method":"sos/vocabulary","query":"trim","library":"std/text"}`)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	result, _ := data["result"].(map[string]any)
	if result["schema"] != "sos/vocabulary@1" {
		t.Fatalf("schema = %+v", result["schema"])
	}
	catalog, _ := result["catalog"].(map[string]any)
	if catalog == nil {
		t.Fatalf("catalog missing from %+v", result)
	}
	entries, _ := catalog["entries"].([]any)
	var trim map[string]any
	for _, e := range entries {
		if entry, ok := e.(map[string]any); ok && entry["library"] == "std/text" && entry["name"] == "trim" {
			trim = entry
		}
	}
	if trim == nil {
		t.Fatalf("std/text trim missing from %+v", entries)
	}
	// An aliased import is the parent-required guarantee: qualified pattern
	// present, no bare pattern.
	patterns, _ := trim["patterns"].([]any)
	var qualified, bare bool
	for _, p := range patterns {
		switch p {
		case "text.trim VALUE":
			qualified = true
		case "trim VALUE":
			bare = true
		}
	}
	if !qualified || bare {
		t.Fatalf("patterns = %+v (qualified=%v bare=%v)", patterns, qualified, bare)
	}
}

func TestLSPBridgeVocabularyValidatesFilters(t *testing.T) {
	s, ts := newTestServer(t)
	// Filters are plain strings; anything else is a malformed body.
	res, data := post(t, ts, s.Token(), "/api/lsp",
		`{"source":"show 1","method":"sos/vocabulary","query":{"nested":"object"}}`)
	if res.StatusCode != 400 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if errKind(t, data) != "request" {
		t.Fatalf("data = %+v", data)
	}
}
