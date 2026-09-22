package soslsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/internal/sossyntax"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestStreamSyntaxHasEditorSupport(t *testing.T) {
	source := `to follow with service as text streaming LogEvent
  may fail with ConnectionLost:

stream logs.follow with "payments" called events
for each event from events:
  stop reading
close stream events
collect at most 10 items from events called buffered
take first 2 items from events called sample`
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, source),
		lspRequest(2, "textDocument/semanticTokens/full", map[string]any{"textDocument": map[string]any{"uri": featureURI}}),
		lspRequest(3, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 3, "character": 3}}),
		lspRequest(4, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 3, "character": 10}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	var tokens []int
	result, _ := lspResult(t, messages, 2)["result"].(map[string]any)
	raw, _ := json.Marshal(result["data"])
	if err := json.Unmarshal(raw, &tokens); err != nil {
		t.Fatal(err)
	}
	decoded := decodeSemanticTokens(tokens)
	for _, want := range []string{"stream", "streaming", "from", "close", "stop", "collect"} {
		if !semanticTokenHasText(source, decoded, want, "keyword") {
			t.Errorf("%q is not highlighted as a keyword: %#v", want, decoded)
		}
	}
	for _, want := range []string{"events", "event", "buffered", "sample"} {
		if !semanticTokenHasText(source, decoded, want, "variable") {
			t.Errorf("%q is not highlighted as a variable: %#v", want, decoded)
		}
	}
	var items []map[string]any
	raw, _ = json.Marshal(lspResult(t, messages, 3)["result"])
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item["label"] == "stream" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("stream completion missing: %#v", items)
	}
	hover, _ := lspResult(t, messages, 4)["result"].(map[string]any)
	contents, _ := hover["contents"].(map[string]any)
	value, _ := contents["value"].(string)
	if !strings.Contains(value, "consumed once") || !strings.Contains(value, "terminal failures") {
		t.Fatalf("stream hover lacks lifecycle/ownership details: %q", value)
	}
}

func TestQualifiedExternalStreamHoverShowsCompleteSignature(t *testing.T) {
	root := t.TempDir()
	moduleDir := filepath.Join(root, "modules", "events")
	if err := os.MkdirAll(moduleDir, 0o700); err != nil {
		t.Fatal(err)
	}
	osName, arch := runtime.GOOS, runtime.GOARCH
	if osName == "windows" {
		osName = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	target := osName + "-" + arch
	definition := fmt.Sprintf(`schema = 1
[module]
path = "example/events"
version = "1.0.0"
[runtime]
kind = "stdio"
protocol = "sos-plugin/1"
command = ["unused"]
[capabilities]
process = true
[[type]]
name = "Event"
[[failure]]
name = "Disconnected"
[[action]]
name = "follow"
effects = ["process"]
targets = [%q]
failures = ["Disconnected"]
[action.result]
type = "stream of Event"
`, target)
	if err := os.WriteFile(filepath.Join(moduleDir, "module.sos.toml"), []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "version = 1\n[external]\nprocess = true\n[[module.external]]\npath = \"example/events\"\ndefinition = \"modules/events/module.sos.toml\"\n"
	if err := os.WriteFile(filepath.Join(root, "sos.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	source := "import \"example/events\" as events\nstream events.follow called incoming\nevents.fo\n"
	mainPath := filepath.Join(root, "main.sos")
	if err := os.WriteFile(mainPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + mainPath
	catalog, diagnostics := sos.Vocabulary(mainPath, source)
	if len(catalog.Entries) == 0 {
		t.Fatalf("external stream fixture did not resolve: diagnostics=%+v", diagnostics)
	}
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{"rootUri": "file://" + root}),
		openDoc(uri, source),
		lspRequest(2, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]int{"line": 1, "character": 14}}),
		lspRequest(3, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": map[string]int{"line": 2, "character": 9}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	hover := lspResult(t, messages, 2)["result"].(map[string]any)
	value := hover["contents"].(map[string]any)["value"].(string)
	for _, want := range []string{"Item type:** `Event`", "Disconnected", "Effects:** `process`", "Targets:** `" + target + "`"} {
		if !strings.Contains(value, want) {
			t.Fatalf("external stream hover missing %q: %s", want, value)
		}
	}
	items := lspResult(t, messages, 3)["result"].([]any)
	found := false
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["label"] == "events.follow" {
			t.Fatalf("streaming action offered as an ordinary call: %+v", item)
		}
		if item["label"] == "stream events.follow called items" {
			found = true
			edit := item["textEdit"].(map[string]any)
			if edit["newText"] != "stream events.follow called items" {
				t.Fatalf("stream completion edit = %+v", edit)
			}
		}
	}
	if !found {
		t.Fatalf("valid stream completion missing: %+v", items)
	}
}

func TestStreamOperationHoverUsesParserKinds(t *testing.T) {
	source := `to follow streaming text:
  finish
stream follow called events
close stream events
stop reading
collect at most 2 items from events called buffered
take first 2 items from events called sample`
	wants := map[int]string{
		3: "cancels an owned stream",
		4: "exits the nearest stream loop",
		5: "explicit overflow limit",
		6: "cancels after the limit",
	}
	requests := []func(*testing.T) []byte{lspRequest(1, "initialize", map[string]any{}), openDoc(featureURI, source)}
	for line := range wants {
		requests = append(requests, lspRequest(line+10, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": line, "character": 2}}))
	}
	messages, err := runLSP(t, requests...)
	if err != nil {
		t.Fatal(err)
	}
	for line, want := range wants {
		hover := lspResult(t, messages, float64(line+10))["result"].(map[string]any)
		value := hover["contents"].(map[string]any)["value"].(string)
		if !strings.Contains(value, want) {
			t.Errorf("line %d hover missing %q: %s", line+1, want, value)
		}
	}
}

func TestLocalStreamHoverAndTypeCompletionExposeDeclaredContract(t *testing.T) {
	source := `define LogEvent:
  message as text
define failure ConnectionLost:
  after as integer
to follow with service as text streaming LogEvent may fail with ConnectionLost:
  finish
stream follow with "payments" called events`
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, source),
		lspRequest(2, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 6, "character": 9}}),
		lspRequest(3, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 4, "character": 48}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	hover, _ := lspResult(t, messages, 2)["result"].(map[string]any)
	contents, _ := hover["contents"].(map[string]any)
	value, _ := contents["value"].(string)
	for _, want := range []string{"Opens stream", "LogEvent", "ConnectionLost", "consumed once"} {
		if !strings.Contains(value, want) {
			t.Fatalf("local stream hover missing %q: %s", want, value)
		}
	}
	raw, _ := json.Marshal(lspResult(t, messages, 3)["result"])
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item["label"] == "LogEvent" && strings.Contains(item["detail"].(string), "message as text") {
			found = true
		}
	}
	if !found {
		t.Fatalf("stream item type completion missing: %#v", items)
	}
}

func TestWrappedStreamContractAndPrimitiveItemRoles(t *testing.T) {
	source := `define failure Disconnected:
to follow
  streaming file
  may fail with Disconnected:
  finish
to folders streaming folder:
  finish
stream follow called files`
	item, failures, ok := localStreamMetadata(source, "follow")
	if !ok || item != "file" || len(failures) != 1 || failures[0] != "Disconnected" {
		t.Fatalf("wrapped metadata = item %q failures %v ok %v", item, failures, ok)
	}
	tokens := syntaxTokens(sossyntax.Parse(source), nil)
	for _, want := range []struct {
		line, kind int
		text       string
	}{{2, tokType, "file"}, {5, tokType, "folder"}, {7, tokFunction, "follow"}} {
		found := false
		for _, token := range tokens {
			if token.line == want.line && token.kind == want.kind && token.text == want.text {
				found = true
			}
		}
		if !found {
			t.Errorf("missing semantic role for %q on line %d: %#v", want.text, want.line, tokens)
		}
	}
}

func TestParameterizedStreamCompletionIncludesReadableArgumentSlots(t *testing.T) {
	target := wordTarget{
		Qualifier: "events",
		Name:      "follow",
		Result:    "stream of Event",
		Params:    []sos.VocabularyParam{{Name: "service", Type: "text"}},
	}
	form, offered := streamCompletionForm(target, completionForm{label: "events.follow", insert: "events.follow"}, "events.fo")
	if !offered || form.insert != "stream events.follow with service called items" {
		t.Fatalf("parameterized stream completion=%+v offered=%v", form, offered)
	}
}

func semanticTokenHasText(source string, tokens []decodedSemanticToken, text, kind string) bool {
	lines := strings.Split(source, "\n")
	for _, token := range tokens {
		if token.line < len(lines) && token.kind < len(tokenLegend) && tokenLegend[token.kind] == kind {
			line := []rune(lines[token.line])
			if token.start+token.length <= len(line) && string(line[token.start:token.start+token.length]) == text {
				return true
			}
		}
	}
	return false
}

type decodedSemanticToken struct{ line, start, length, kind int }

func decodeSemanticTokens(data []int) []decodedSemanticToken {
	line, start := 0, 0
	result := make([]decodedSemanticToken, 0, len(data)/5)
	for i := 0; i+4 < len(data); i += 5 {
		line += data[i]
		if data[i] == 0 {
			start += data[i+1]
		} else {
			start = data[i+1]
		}
		result = append(result, decodedSemanticToken{line: line, start: start, length: data[i+2], kind: data[i+3]})
	}
	return result
}
