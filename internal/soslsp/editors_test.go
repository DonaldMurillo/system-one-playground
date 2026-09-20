package soslsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/internal/sossyntax"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// Framed stdio helpers: tests exercise the real Content-Length boundary.

func lspFrameBody(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func lspFrame(t *testing.T, v any) []byte {
	t.Helper()
	b := lspFrameBody(t, v)
	return append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(b))), b...)
}

func lspRequest(id any, method string, params any) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		return lspFrame(t, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}
}

func lspNotify(method string, params any) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		return lspFrame(t, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
}

func runLSP(t *testing.T, frames ...func(t *testing.T) []byte) ([]map[string]any, error) {
	t.Helper()
	var in, out bytes.Buffer
	for _, f := range frames {
		in.Write(f(t))
	}
	if err := Serve(&in, &out); err != nil {
		return nil, err
	}
	var messages []map[string]any
	br := bufio.NewReader(&out)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")), "%d", &n); err != nil {
			return messages, nil
		}
		if _, err := br.ReadString('\n'); err != nil {
			break
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(br, body); err != nil {
			break
		}
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			messages = append(messages, m)
		}
	}
	return messages, nil
}

func lspResult(t *testing.T, messages []map[string]any, id float64) map[string]any {
	t.Helper()
	for _, m := range messages {
		if m["id"] == id {
			return m
		}
	}
	t.Fatalf("no response with id %v in %d messages", id, len(messages))
	return nil
}

const featureURI = "file:///feature.sos"

func openDoc(uri, text string) func(t *testing.T) []byte {
	return lspNotify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "sos", "version": 1, "text": text},
	})
}

func semanticData(t *testing.T, source string, init map[string]any) []float64 {
	t.Helper()
	frames := []func(t *testing.T) []byte{
		lspRequest(1, "initialize", init),
		openDoc(featureURI, source),
		lspRequest(2, "textDocument/semanticTokens/full", map[string]any{"textDocument": map[string]any{"uri": featureURI}}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	}
	messages, err := runLSP(t, frames...)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	res := lspResult(t, messages, 2)
	data, _ := res["result"].(map[string]any)["data"].([]any)
	if data == nil {
		t.Fatalf("semanticTokens result has no data: %+v", res)
	}
	out := make([]float64, len(data))
	for i, v := range data {
		out[i], _ = v.(float64)
	}
	return out
}

func TestSemanticTokensWireEncoding(t *testing.T) {
	// make x 7 -> keyword(0,4) variable(5,1) number(7,1)
	got := semanticData(t, "make x 7", map[string]any{})
	want := []float64{0, 0, 4, 0, 0, 0, 5, 1, 1, 0, 0, 2, 1, 7, 0}
	if len(got) != len(want) {
		t.Fatalf("data = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("data[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSemanticTokensUTF16String(t *testing.T) {
	// "🎁x" occupies 1 + 2 + 1 + 1 = 5 UTF-16 units.
	got := semanticData(t, "make s \"🎁x\"", map[string]any{})
	want := []float64{0, 0, 4, 0, 0, 0, 5, 1, 1, 0, 0, 2, 5, 6, 0}
	if len(got) != len(want) {
		t.Fatalf("data = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("data[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSemanticTokensOperators(t *testing.T) {
	got := semanticData(t, "make value \"a\" + \"b\"\nmake other \"a\" plus \"b\"\nmake scaled 2 * 3 times 4", map[string]any{})
	want := []float64{
		0, 0, 4, 0, 0, 0, 5, 5, 1, 0, 0, 6, 3, 6, 0, 0, 4, 1, 11, 0, 0, 2, 3, 6, 0,
		1, 0, 4, 0, 0, 0, 5, 5, 1, 0, 0, 6, 3, 6, 0, 0, 4, 4, 11, 0, 0, 5, 3, 6, 0,
		1, 0, 4, 0, 0, 0, 5, 6, 1, 0, 0, 7, 1, 7, 0, 0, 2, 1, 11, 0, 0, 2, 1, 7, 0, 0, 2, 5, 11, 0, 0, 6, 1, 7, 0,
	}
	if len(got) != len(want) {
		t.Fatalf("data = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("data[%d] = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSemanticTokensInsideInterpolation(t *testing.T) {
	toks := syntaxTokens(sossyntax.Parse("show \"Value {total plus 1 times 2}\""), nil)
	for _, want := range []struct {
		start, kind int
		text        string
	}{
		{13, tokVariable, "total"},
		{19, tokOperator, "plus"},
		{24, tokNumber, "1"},
		{26, tokOperator, "times"},
		{32, tokNumber, "2"},
	} {
		if !tokenIs(toks, 0, want.start, want.kind, want.text) {
			t.Fatalf("interpolation token %q kind %d missing at %d: %+v", want.text, want.kind, want.start, toks)
		}
	}
}

func TestOperatorWordsRespectBindingRoles(t *testing.T) {
	toks := syntaxTokens(sossyntax.Parse("command count:\n  option times as integer default 5\n  repeat times times:"), nil)
	if !tokenIs(toks, 1, 9, tokParameter, "times") {
		t.Fatalf("option binding named times must remain a parameter: %+v", toks)
	}
	if !tokenIs(toks, 2, 9, tokVariable, "times") || !tokenIs(toks, 2, 15, tokOperator, "times") {
		t.Fatalf("repeat operand and connector must have distinct roles: %+v", toks)
	}
}

func TestInitializeAdvertisesLegendAndProviders(t *testing.T) {
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		lspRequest(2, "shutdown", nil),
		lspNotify("exit", nil),
	)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	res := lspResult(t, messages, 1)["result"].(map[string]any)
	caps := res["capabilities"].(map[string]any)
	legend := caps["semanticTokensProvider"].(map[string]any)["legend"].(map[string]any)
	types := legend["tokenTypes"].([]any)
	if len(types) != len(tokenLegend) {
		t.Fatalf("legend = %v, want %v", types, tokenLegend)
	}
	for i, name := range tokenLegend {
		if types[i] != name {
			t.Fatalf("legend[%d] = %v, want %s", i, types[i], name)
		}
	}
	if caps["inlayHintProvider"] != true {
		t.Fatalf("inlayHintProvider missing: %+v", caps)
	}
	if _, ok := caps["codeActionProvider"]; !ok {
		t.Fatalf("codeActionProvider missing: %+v", caps)
	}
}

// tokenIs reports whether (line, start) carries the given kind and text.
func tokenIs(toks []lexToken, line, start, kind int, text string) bool {
	for _, tok := range toks {
		if tok.line == line && tok.start == start {
			return tok.kind == kind && tok.text == text
		}
	}
	return false
}

func TestTokenizeClassification(t *testing.T) {
	src := strings.Join([]string{
		"package demo",
		"import \"std/text\" as text",
		"export clean",
		"command tidy:",
		"  argument source as folder",
		"  make count 3 # make count",
		"  read \"in.json\" as json called doc",
		"  call text.trim with doc called cleaned",
		"  show \"make count\"",
	}, "\n")
	toks := syntaxTokens(sossyntax.Parse(src), nil)
	cases := []struct {
		line, start, kind int
		text              string
	}{
		{0, 8, tokNamespace, "demo"},
		{1, 0, tokKeyword, "import"},
		{1, 7, tokString, "\"std/text\""},
		{1, 21, tokNamespace, "text"},
		{2, 7, tokFunction, "clean"},
		{3, 0, tokKeyword, "command"},
		{3, 8, tokFunction, "tidy"},
		{4, 2, tokKeyword, "argument"},
		{4, 11, tokParameter, "source"},
		{4, 21, tokType, "folder"},
		{5, 2, tokKeyword, "make"},
		{5, 7, tokVariable, "count"},
		{5, 13, tokNumber, "3"},
		{5, 15, tokComment, "# make count"},
		{6, 2, tokKeyword, "read"},
		{6, 7, tokString, "\"in.json\""},
		{6, 20, tokType, "json"},
		{6, 32, tokVariable, "doc"},
		{7, 2, tokKeyword, "call"},
		{7, 7, tokNamespace, "text"},
		{7, 12, tokFunction, "trim"},
		{7, 33, tokVariable, "cleaned"},
		{8, 2, tokKeyword, "show"},
	}
	for _, c := range cases {
		if !tokenIs(toks, c.line, c.start, c.kind, c.text) {
			t.Fatalf("no %q token (kind %d) at line %d char %d", c.text, c.kind, c.line, c.start)
		}
	}
	// Keywords inside strings and comments are never classified.
	for _, tok := range toks {
		if tok.kind != tokKeyword {
			continue
		}
		// Line 5 comment starts at char 15; line 8 string starts at 7.
		if (tok.line == 5 && tok.start >= 15) || (tok.line == 8 && tok.start >= 7) {
			t.Fatalf("keyword inside string/comment classified: %+v", tok)
		}
	}
}

func TestInlayHintsConfidentValues(t *testing.T) {
	src := strings.Join([]string{
		"command tidy:",
		"  make count 3",
		"  make label \"hi\"",
		"  make reports as empty list",
		"  read \"in.json\" as json called doc",
		"  read each row in \"rows.json\" as lines of json into rows",
		"  take first 2 items from rows called top",
		"  show label # make count 3",
		"  show \"Count {count plus 1}\"",
	}, "\n")
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, src),
		lspRequest(2, "textDocument/inlayHint", map[string]any{"textDocument": map[string]any{"uri": featureURI}}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	hints, _ := lspResult(t, messages, 2)["result"].([]any)
	want := map[int]string{1: ": number", 2: ": text", 3: ": list", 4: ": json", 5: ": list", 6: ": list", 8: ": number"}
	seen := map[int]string{}
	for _, h := range hints {
		item := h.(map[string]any)
		pos := item["position"].(map[string]any)
		seen[int(pos["line"].(float64))] = item["label"].(string)
	}
	if len(seen) != len(want) {
		t.Fatalf("hints = %v, want labels %v", seen, want)
	}
	for line, label := range want {
		if seen[line] != label {
			t.Fatalf("hint on line %d = %q, want %q (all: %v)", line, seen[line], label, seen)
		}
	}
	// Line 7 has a comment mentioning literals but no hint: not a binding.
	if _, ok := seen[7]; ok {
		t.Fatalf("comment-only line must not produce a hint: %v", seen)
	}
}

func codeActionFrames(source string, rangeLines [2]int, init map[string]any) []func(t *testing.T) []byte {
	return []func(t *testing.T) []byte{
		lspRequest(1, "initialize", init),
		openDoc(featureURI, source),
		lspRequest(2, "textDocument/codeAction", map[string]any{
			"textDocument": map[string]any{"uri": featureURI},
			"range":        map[string]any{"start": map[string]any{"line": rangeLines[0], "character": 0}, "end": map[string]any{"line": rangeLines[1], "character": 0}},
			"context":      map[string]any{"diagnostics": []any{}},
		}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	}
}

func TestCodeActionStdAutoImport(t *testing.T) {
	src := "command tidy:\n  call text.trim with name called cleaned\n"
	messages, err := runLSP(t, codeActionFrames(src, [2]int{0, 1}, map[string]any{})...)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	actions, _ := lspResult(t, messages, 2)["result"].([]any)
	if len(actions) != 1 {
		t.Fatalf("actions = %+v, want one quickfix", actions)
	}
	a := actions[0].(map[string]any)
	if a["kind"] != "quickfix" || a["title"] != "Import \"std/text\"" {
		t.Fatalf("action = %+v", a)
	}
	edit := a["edit"].(map[string]any)["changes"].(map[string]any)[featureURI].([]any)[0].(map[string]any)
	if edit["newText"] != "import \"std/text\"\n" {
		t.Fatalf("edit = %+v", edit)
	}
	rng := edit["range"].(map[string]any)
	start := rng["start"].(map[string]any)
	if start["line"] != float64(0) || start["character"] != float64(0) {
		t.Fatalf("import inserted at %+v, want line 0", start)
	}
}

func TestCodeActionNoFixWhenImportedOrUnknown(t *testing.T) {
	src := strings.Join([]string{
		"import \"std/text\" as text",
		"call text.trim with name called cleaned",
		"call text.frobnicate with name called nope",
		"call mystery.action with name called x",
	}, "\n")
	messages, err := runLSP(t, codeActionFrames(src, [2]int{0, 3}, map[string]any{})...)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	actions, _ := lspResult(t, messages, 2)["result"].([]any)
	if len(actions) != 0 {
		t.Fatalf("imported alias and unknown actions must yield no fixes: %+v", actions)
	}
}

func TestCodeActionLocalPackage(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "example.com/demo", map[string]string{
		"util/helper.sos": "package util\nAdd a greeting helper.\nexport greet\n# export ignored\n",
	})
	src := "command tidy:\n  call util.greet with name called hi\n"
	init := map[string]any{"rootUri": "file://" + root}
	messages, err := runLSP(t, codeActionFrames(src, [2]int{0, 1}, init)...)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	actions, _ := lspResult(t, messages, 2)["result"].([]any)
	if len(actions) != 1 {
		t.Fatalf("actions = %+v, want the local package import", actions)
	}
	a := actions[0].(map[string]any)
	if a["title"] != "Import \"example.com/demo/util\" as util" {
		t.Fatalf("title = %v", a["title"])
	}
	edit := a["edit"].(map[string]any)["changes"].(map[string]any)[featureURI].([]any)[0].(map[string]any)
	if edit["newText"] != "import \"example.com/demo/util\" as util\n" {
		t.Fatalf("edit = %+v", edit)
	}
}

func TestCodeActionRangeBoundsResults(t *testing.T) {
	src := "command tidy:\n  call text.trim with name called cleaned\n"
	messages, err := runLSP(t, codeActionFrames(src, [2]int{5, 6}, map[string]any{})...)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	actions, _ := lspResult(t, messages, 2)["result"].([]any)
	if len(actions) != 0 {
		t.Fatalf("calls outside the requested range must yield nothing: %+v", actions)
	}
}

func TestCompletionAutoImportFields(t *testing.T) {
	src := "command tidy:\n  call tr\n"
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, src),
		lspRequest(2, "textDocument/completion", map[string]any{
			"textDocument": map[string]any{"uri": featureURI},
			"position":     map[string]any{"line": 1, "character": 9},
		}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	items, _ := lspResult(t, messages, 2)["result"].([]any)
	var trim map[string]any
	for _, it := range items {
		item := it.(map[string]any)
		if item["label"] == "text.trim" {
			trim = item
		}
	}
	if trim == nil {
		t.Fatalf("text.trim missing from %+v", items)
	}
	if trim["kind"] != float64(3) || !strings.HasPrefix(trim["detail"].(string), "auto-import · std/text") {
		t.Fatalf("item = %+v", trim)
	}
	doc := trim["documentation"].(map[string]any)
	if doc["kind"] != "markdown" || !strings.Contains(doc["value"].(string), "whitespace") {
		t.Fatalf("documentation = %+v", doc)
	}
	te := trim["textEdit"].(map[string]any)
	rng := te["range"].(map[string]any)
	start, end := rng["start"].(map[string]any), rng["end"].(map[string]any)
	if start["line"] != float64(1) || start["character"] != float64(7) || end["character"] != float64(9) {
		t.Fatalf("textEdit range = %v-%v, want word \"tr\" on line 1", start, end)
	}
	if te["newText"] != "text.trim" {
		t.Fatalf("newText = %v", te["newText"])
	}
	extra, _ := trim["additionalTextEdits"].([]any)
	if len(extra) != 1 {
		t.Fatalf("additionalTextEdits = %+v, want the import", extra)
	}
	if extra[0].(map[string]any)["newText"] != "import \"std/text\"\n" {
		t.Fatalf("additionalTextEdit = %+v", extra[0])
	}
}

func TestCompletionEmptyPrefixKeywordsOnly(t *testing.T) {
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, "command tidy:\n"),
		lspRequest(2, "textDocument/completion", map[string]any{
			"textDocument": map[string]any{"uri": featureURI},
			"position":     map[string]any{"line": 0, "character": 0},
		}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	items, _ := lspResult(t, messages, 2)["result"].([]any)
	if len(items) != len(sos.Keywords()) {
		t.Fatalf("empty prefix must return exactly the canonical keywords, got %d", len(items))
	}
}

func TestHoverRichStatement(t *testing.T) {
	src := "command tidy:\n  read \"in.json\" as json called doc\n  remember jev classify doc called c\n"
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, src),
		lspRequest(2, "textDocument/hover", map[string]any{
			"textDocument": map[string]any{"uri": featureURI},
			"position":     map[string]any{"line": 1, "character": 2},
		}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	value := lspResult(t, messages, 2)["result"].(map[string]any)["contents"].(map[string]any)["value"].(string)
	for _, want := range []string{"```sos", "read \"in.json\" as json called doc", "**Effect:** reads a file into memory", "**Binds:** the parsed file content"} {
		if !strings.Contains(value, want) {
			t.Fatalf("hover %q missing %q", value, want)
		}
	}
	if strings.Contains(value, "Provider") {
		t.Fatalf("read must not claim a provider call: %q", value)
	}
}

func TestHoverNewSyntaxAndHonesty(t *testing.T) {
	src := strings.Join([]string{
		"import \"std/text\" as text",
		"call text.trim with name called cleaned",
		"call text.frobnicate with name called nope",
		"call ghost.action with name called x",
	}, "\n")
	hoverAt := func(line int) string {
		t.Helper()
		messages, err := runLSP(t,
			lspRequest(1, "initialize", map[string]any{}),
			openDoc(featureURI, src),
			lspRequest(2, "textDocument/hover", map[string]any{
				"textDocument": map[string]any{"uri": featureURI},
				"position":     map[string]any{"line": line, "character": 2},
			}),
			lspRequest(3, "shutdown", nil),
			lspNotify("exit", nil),
		)
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
		return lspResult(t, messages, 2)["result"].(map[string]any)["contents"].(map[string]any)["value"].(string)
	}
	if v := hoverAt(0); !strings.Contains(v, "Standard library package") || !strings.Contains(v, "`trim`") ||
		!strings.Contains(v, "every call requires the `text.` prefix") {
		t.Fatalf("import hover = %q", v)
	}
	if v := hoverAt(1); !strings.Contains(v, "whitespace") || !strings.Contains(v, "import \"std/text\" as text") {
		t.Fatalf("std call hover = %q", v)
	}
	if v := hoverAt(2); !strings.Contains(v, "Unknown action") {
		t.Fatalf("unknown std action must be reported honestly: %q", v)
	}
	if v := hoverAt(3); !strings.Contains(v, "Unknown action") {
		t.Fatalf("unknown alias must be reported honestly: %q", v)
	}
}

func writeModule(t *testing.T, root, identity string, files map[string]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "sos.toml"), []byte("[module]\npath = \""+identity+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndexWorkspaceLocalPackages(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "example.com/demo", map[string]string{
		"util/helper.sos":  "package util\nA grab bag of helpers.\nexport greet\nexport farewell\n# export shadowed\n",
		"nested/extra.sos": "package extra\nexport ping\n",
		"notpkg.txt":       "package nope\nexport nope\n",
	})
	idx := indexWorkspace(root)
	if !idx.moduleOK {
		t.Fatal("module must be recognized")
	}
	pkg := idx.packages["util"]
	if pkg == nil || len(pkg.Actions) != 2 {
		t.Fatalf("util package = %+v, want greet+farewell", pkg)
	}
	if pkg.ImportPath != "example.com/demo/util" {
		t.Fatalf("import path = %q", pkg.ImportPath)
	}
	if pkg.Actions[0].Doc != "A grab bag of helpers." {
		t.Fatalf("export doc = %q", pkg.Actions[0].Doc)
	}
	if idx.packages["extra"] == nil || idx.packages["extra"].ImportPath != "example.com/demo/nested" {
		t.Fatalf("nested package = %+v", idx.packages["extra"])
	}
	if idx.packages["nope"] != nil {
		t.Fatal("non-sos file must not be indexed")
	}
}

func TestIndexWorkspaceSafetyAndFallbacks(t *testing.T) {
	if idx := indexWorkspace(""); idx.moduleOK || len(idx.packages) != 0 {
		t.Fatal("empty root must yield an empty index")
	}
	if idx := indexWorkspace(filepath.Join(t.TempDir(), "missing")); len(idx.packages) != 0 {
		t.Fatal("missing root must yield an empty index")
	}
	// No sos.toml: empty index, std catalog still served by callers.
	if idx := indexWorkspace(t.TempDir()); idx.moduleOK || len(idx.packages) != 0 {
		t.Fatal("root without sos.toml must not be a module")
	}
	// The [module] path is a logical identity and is never joined to a
	// filesystem path; an identity that looks like a traversal is inert.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sos.toml"), []byte("[module]\npath = \"../escape\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx := indexWorkspace(root)
	if !idx.moduleOK || len(idx.packages) != 0 || idx.identity != "../escape" {
		t.Fatalf("logical identity must be recorded without filesystem use: %+v", idx)
	}
	// Std catalog helpers: the fallback path when no workspace exists.
	if _, ok := stdImportForAlias("text"); !ok {
		t.Fatal("std catalog must resolve text")
	}
	if stdHasAction("text", "trim") != true || stdHasAction("text", "frobnicate") {
		t.Fatal("std catalog must only know real actions")
	}
	// Imports without `as` bind the default alias.
	if !hasImport("import \"std/text\"\ncall text.trim with x called y\n", "text") {
		t.Fatal("default alias import must bind text")
	}
	if hasImport("import \"std/json\" as j\n", "text") {
		t.Fatal("unrelated import must not bind text")
	}
	if !hasImportPath("import \"std/json\" as j\n", "std/json") || hasImportPath("import \"std/json\" as j\n", "std/text") {
		t.Fatal("path collision detection must match the actual import path")
	}
}
