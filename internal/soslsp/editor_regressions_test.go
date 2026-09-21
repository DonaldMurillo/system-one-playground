package soslsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/internal/sossyntax"
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

func TestFailureCompletionAfterCommaIncludesImportedFailures(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "example.com/demo", map[string]string{
		"util/failures.sos": "package util\nexport fetch\nexport NotFound\ndefine failure NotFound:\n  path as text\nto fetch returning text may fail with NotFound:\n  fail NotFound with \"missing\":\n    path from \"x\"\n",
	})
	if err := os.WriteFile(filepath.Join(root, "sos.toml"), []byte("version = 1\n[module]\npath = \"example.com/demo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := "import \"example.com/demo/util\"\ndefine failure InvalidCity:\n  city as text\nto load returning text may fail with InvalidCity, NotFound:\n  finish with \"ok\"\n"
	mainPath := filepath.Join(root, "main.sos")
	if err := os.WriteFile(mainPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + mainPath
	catalog, diagnostics := sos.Vocabulary(mainPath, source)
	if len(catalog.Failures) < 2 {
		t.Fatalf("fixture did not resolve imported failures: failures=%+v diagnostics=%+v", catalog.Failures, diagnostics)
	}
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{"rootUri": "file://" + root}),
		openDoc(uri, source),
		lspRequest(2, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]int{"line": 3, "character": 51}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	items := lspResult(t, messages, 2)["result"].([]any)
	found := false
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["label"] == "NotFound" {
			found = true
			if !strings.Contains(item["detail"].(string), "path as text") {
				t.Fatalf("missing imported failure detail: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("imported failure completion missing after comma: %+v", items)
	}
}

func TestImportedRecordTypesCompleteAndHover(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "example.com/demo", map[string]string{
		"people/types.sos": "package people\nexport User\ndefine User:\n  name as text\n  age as optional number\n",
	})
	if err := os.WriteFile(filepath.Join(root, "sos.toml"), []byte("version = 1\n[module]\npath = \"example.com/demo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := "import \"example.com/demo/people\"\nmake user as Us\nmake other as User\n"
	mainPath := filepath.Join(root, "main.sos")
	if err := os.WriteFile(mainPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + mainPath
	catalog, diagnostics := sos.Vocabulary(mainPath, source)
	if len(catalog.Definitions) == 0 {
		t.Fatalf("fixture did not resolve imported records: definitions=%+v diagnostics=%+v", catalog.Definitions, diagnostics)
	}
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{"rootUri": "file://" + root}),
		openDoc(uri, source),
		lspRequest(2, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]int{"line": 1, "character": 15}}),
		lspRequest(3, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]int{"line": 2, "character": 16}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	items := lspResult(t, messages, 2)["result"].([]any)
	found := false
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["label"] == "User" {
			found = true
			if !strings.Contains(item["detail"].(string), "age as optional number") {
				t.Fatalf("imported record completion lacks fields: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("imported record completion missing: %+v", items)
	}
	hover := lspResult(t, messages, 3)["result"].(map[string]any)
	contents := hover["contents"].(map[string]any)["value"].(string)
	if !strings.Contains(contents, "Selected · type") || !strings.Contains(contents, "age as optional number") {
		t.Fatalf("imported record hover = %q", contents)
	}
}

func TestFailureSyntaxRolesAreTypes(t *testing.T) {
	toks := syntaxTokens(sossyntax.Parse("define failure InvalidCity:\n  city as text\nto load returning text may fail with InvalidCity, NotFound:"), nil)
	for _, want := range []struct {
		line, start int
		text        string
	}{
		{0, 15, "InvalidCity"}, {2, 37, "InvalidCity"}, {2, 50, "NotFound"},
	} {
		if !tokenIs(toks, want.line, want.start, tokType, want.text) {
			t.Fatalf("failure type token missing: %+v in %+v", want, toks)
		}
	}
}
