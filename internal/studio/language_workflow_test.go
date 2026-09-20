package studio

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Exercise the public editor protocol: discover an import, apply the exact edit,
// then check and run the resulting script with no provider calls.
func TestEditorAutoImportThenRun(t *testing.T) {
	s, ts := newTestServer(t)
	source := "call text.trim with \"  hello  \" called clean\nshow clean\n"
	raw, _ := json.Marshal(map[string]any{"source": source, "method": "textDocument/codeAction", "range": map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 50}}, "context": map[string]any{"diagnostics": []any{}}})
	res, data := post(t, ts, s.Token(), "/api/lsp", string(raw))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("codeAction: %d %+v", res.StatusCode, data)
	}
	actions, _ := data["result"].([]any)
	if len(actions) == 0 {
		t.Fatalf("missing import fix: %+v", data)
	}
	action := actions[0].(map[string]any)
	changes := action["edit"].(map[string]any)["changes"].(map[string]any)
	var insertion string
	for _, value := range changes {
		for _, edit := range value.([]any) {
			e := edit.(map[string]any)
			position := e["range"].(map[string]any)["start"].(map[string]any)
			if position["line"] != float64(0) || position["character"] != float64(0) {
				t.Fatalf("unexpected edit: %+v", e)
			}
			insertion += e["newText"].(string)
		}
	}
	if !strings.Contains(insertion, `"std/text"`) {
		t.Fatalf("wrong import: %q", insertion)
	}
	source = insertion + source
	raw, _ = json.Marshal(map[string]any{"source": source})
	_, checked := post(t, ts, s.Token(), "/api/check", string(raw))
	if diagnostics, _ := checked["diagnostics"].([]any); len(diagnostics) > 0 {
		t.Fatalf("accepted import fails check: %+v", checked)
	}
	_, run := post(t, ts, s.Token(), "/api/run", string(raw))
	if run["ok"] != true || strings.TrimSpace(run["output"].(string)) != "hello" {
		t.Fatalf("accepted import fails run: %+v", run)
	}
	if traces, _ := run["traces"].([]any); len(traces) != 0 {
		t.Fatalf("offline standard operation used provider: %+v", run)
	}
}

func TestEditorFoldingIgnoresQuotedColons(t *testing.T) {
	s, ts := newTestServer(t)
	raw, _ := json.Marshal(map[string]any{"source": "command sample:\n  show \"not a block:\"\n  show 2\n", "method": "textDocument/foldingRange"})
	response, data := post(t, ts, s.Token(), "/api/lsp", string(raw))
	if response.StatusCode != 200 {
		t.Fatalf("folding: %+v", data)
	}
	ranges, _ := data["result"].([]any)
	if len(ranges) != 1 {
		t.Fatalf("folding: %+v", data)
	}
}
