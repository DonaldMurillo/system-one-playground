package soslsp

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestCompletionQualifiedImportEditsStayExecutable(t *testing.T) {
	for _, tc := range []struct {
		name, header, want string
		extra              bool
	}{
		{"frontmatter", "+++\nversion = 1\n+++\n", "text.trim", true},
		{"crlf-frontmatter", "+++\r\nversion = 1\r\n+++\r\n", "text.trim", true},
		{"reuse-alias", "import \"std/text\" as words\n", "words.trim", false},
		{"avoid-alias-collision", "import \"std/json\" as text\n", "text2.trim", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.header + "call text.tr with \" hi \" called clean\nshow clean\n"
			line := strings.Count(tc.header, "\n")
			messages, err := runLSP(t, lspRequest(1, "initialize", map[string]any{}), openDoc(featureURI, source), lspRequest(2, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": line, "character": 12}}))
			if err != nil {
				t.Fatal(err)
			}
			items := lspResult(t, messages, 2)["result"].([]any)
			var item map[string]any
			for _, raw := range items {
				v := raw.(map[string]any)
				if v["label"] == tc.want {
					item = v
					break
				}
			}
			if item == nil {
				t.Fatalf("missing %s: %+v", tc.want, items)
			}
			type edit struct {
				Range   lspRange `json:"range"`
				NewText string   `json:"newText"`
			}
			var primary edit
			b, _ := json.Marshal(item["textEdit"])
			json.Unmarshal(b, &primary)
			if primary.Range.Start.Character != 5 {
				t.Fatalf("qualified replacement starts at %d, want 5", primary.Range.Start.Character)
			}
			edits := []edit{primary}
			var additional []edit
			b, _ = json.Marshal(item["additionalTextEdits"])
			json.Unmarshal(b, &additional)
			if (len(additional) > 0) != tc.extra {
				t.Fatalf("wrong import edits: %+v", item)
			}
			edits = append(edits, additional...)
			sort.Slice(edits, func(i, j int) bool {
				a, b := edits[i].Range.Start, edits[j].Range.Start
				return a.Line > b.Line || a.Line == b.Line && a.Character > b.Character
			})
			for _, e := range edits {
				source = applyRange(source, e.Range, e.NewText)
			}
			if ds := sos.CheckFile("", source); len(ds) > 0 {
				t.Fatalf("completion produced invalid source: %+v\n%s", ds, source)
			}
		})
	}
}

func TestHintsUseBindingOffsetsAndTypes(t *testing.T) {
	source := "command demo:\n  make a 3\n  read \"file\" as text called text\n  make ready true\n"
	messages, err := runLSP(t, lspRequest(1, "initialize", map[string]any{}), openDoc(featureURI, source), lspRequest(2, "textDocument/inlayHint", map[string]any{"textDocument": map[string]any{"uri": featureURI}}))
	if err != nil {
		t.Fatal(err)
	}
	hints := lspResult(t, messages, 2)["result"].([]any)
	want := map[int]struct {
		column int
		label  string
	}{1: {8, ": number"}, 2: {33, ": text"}, 3: {12, ": boolean"}}
	for _, raw := range hints {
		hint := raw.(map[string]any)
		p := hint["position"].(map[string]any)
		line := int(p["line"].(float64))
		w, ok := want[line]
		if !ok || p["character"] != float64(w.column) || hint["label"] != w.label {
			t.Fatalf("hint misplaced: %+v want %+v", hint, w)
		}
		delete(want, line)
	}
	if len(want) > 0 {
		t.Fatalf("missing hints: %+v", want)
	}
}
