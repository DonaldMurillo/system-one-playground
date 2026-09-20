package e2e

import (
	"bytes"
	"strings"
	"testing"
)

func TestHoverCombinesSelectedElementAndSentence(t *testing.T) {
	source := "import \"std/text\" as words\nmake title \"  hello  \"\nwords.trim title called clean\nshow \"😀\" # note\n"
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{"capabilities": map[string]any{}}))
	input.Write(notification("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": testURI, "languageId": "sos", "version": 1, "text": source}}))
	cases := []struct {
		line, col, start, end int
		want                  string
	}{
		{2, 1, 0, 5, "library parent"},
		{2, 7, 6, 10, "action"},
		{2, 12, 11, 16, "Declared on line 2"},
		{2, 18, 17, 23, "result binding"},
		{2, 26, 24, 29, "binding"},
		{3, 7, 5, 9, "literal text"},
		{3, 12, 10, 16, "not executed"},
	}
	for i, c := range cases {
		input.Write(request(i+2, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": testURI}, "position": map[string]int{"line": c.line, "character": c.col}}))
	}
	input.Write(request(100, "shutdown", nil))
	input.Write(notification("exit", nil))
	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, m := range messages {
		id, ok := m["id"].(float64)
		if !ok || id < 2 || id >= float64(len(cases)+2) {
			continue
		}
		c := cases[int(id)-2]
		seen++
		result := getMap(t, m, "result")
		value := getMap(t, result, "contents")["value"].(string)
		if !strings.Contains(value, c.want) || !strings.Contains(value, "**Sentence**") {
			t.Fatalf("hover %v: %s", c, value)
		}
		if c.line == 2 && !strings.Contains(value, "words.trim title called clean") {
			t.Fatal(value)
		}
		r := getMap(t, result, "range")
		if getMap(t, r, "start")["character"] != float64(c.start) || getMap(t, r, "end")["character"] != float64(c.end) {
			t.Fatalf("wrong range: %v", r)
		}
	}
	if seen != len(cases) {
		t.Fatalf("received %d hovers", seen)
	}
}

func TestSemanticRolesAndCodeLens(t *testing.T) {
	source := "+++\nversion = 1\n[interpretation]\nmode = \"semantic\"\n[runtime]\njudgment = \"semantic\"\n+++\ncriterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 0.85\n  on uncertain discard\nmake tickets []\nfilter tickets where id is 1\nkeep urgent tickets\n"
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{"capabilities": map[string]any{}}))
	input.Write(notification("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": testURI, "languageId": "sos", "version": 1, "text": source}}))
	cases := []struct {
		line, col int
		want      string
	}{{7, 2, "Language keyword"}, {12, 2, "Semantic phrase"}, {13, 7, "Defined criterion"}}
	for i, c := range cases {
		input.Write(request(i+2, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": testURI}, "position": map[string]int{"line": c.line, "character": c.col}}))
	}
	input.Write(request(10, "textDocument/codeLens", map[string]any{"textDocument": map[string]any{"uri": testURI}}))
	input.Write(request(100, "shutdown", nil))
	input.Write(notification("exit", nil))
	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, m := range messages {
		if m["method"] == "textDocument/publishDiagnostics" {
			ds := getMap(t, m, "params")["diagnostics"].([]any)
			if len(ds) != 0 {
				t.Fatalf("valid semantic source has squiggles: %v", ds)
			}
		}
		id, ok := m["id"].(float64)
		if !ok {
			continue
		}
		if id >= 2 && id <= 4 {
			value := getMap(t, getMap(t, m, "result"), "contents")["value"].(string)
			if !strings.Contains(value, cases[int(id)-2].want) || strings.Contains(value, "Kind: `invalid`") {
				t.Fatal(value)
			}
			seen++
		}
		if id == 10 {
			lenses := m["result"].([]any)
			if len(lenses) != 3 {
				t.Fatalf("lenses: %v", lenses)
			}
			seen++
		}
	}
	if seen != 4 {
		t.Fatalf("missing replies: %d", seen)
	}
}
