package soslsp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The filesystem constructions get keyword tokens, effect-bearing hover, and
// canonical-phrase completion.
func TestFilespecSyntaxHasEditorSupport(t *testing.T) {
	source := `walk through folder "src" at most 100 entries called entries
  excluding [".git"]
read file "settings.toml" as text called settings
watch folder "incoming" recursively called changes
  matching ["*.pdf"]

for each change from changes:
  show path of change
remove file "temporary.txt"
copy file "a" to "b"
  only if the destination does not exist
wa`
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, source),
		lspRequest(2, "textDocument/semanticTokens/full", map[string]any{"textDocument": map[string]any{"uri": featureURI}}),
		lspRequest(3, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 11, "character": 2}}),
		lspRequest(4, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 3, "character": 2}}),
		lspRequest(5, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 0, "character": 2}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := lspResult(t, messages, 2)["result"].(map[string]any)
	raw, _ := json.Marshal(result["data"])
	var tokens []int
	if err := json.Unmarshal(raw, &tokens); err != nil {
		t.Fatal(err)
	}
	decoded := decodeSemanticTokens(tokens)
	for _, want := range []string{"walk", "watch", "read", "remove", "copy", "folder", "entries", "changes"} {
		if !semanticTokenHasText(source, decoded, want, "keyword") && !semanticTokenHasText(source, decoded, want, "variable") {
			t.Errorf("token %s missing from semantic tokens", want)
		}
	}
	var items []map[string]any
	raw, _ = json.Marshal(lspResult(t, messages, 3)["result"])
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item["label"] == "watch folder" {
			found = true
		}
	}
	if !found {
		t.Fatalf("watch folder completion missing: %#v", items)
	}
	hover, _ := lspResult(t, messages, 4)["result"].(map[string]any)
	value, _ := hover["contents"].(map[string]any)["value"].(string)
	if !strings.Contains(strings.ToLower(value), "ownership") || !strings.Contains(value, "FileChange") {
		t.Fatalf("watch hover lacks stream ownership detail: %q", value)
	}
	hover, _ = lspResult(t, messages, 5)["result"].(map[string]any)
	value, _ = hover["contents"].(map[string]any)["value"].(string)
	if !strings.Contains(value, "filesystem read") {
		t.Fatalf("walk hover lacks effect label: %q", value)
	}
}

// Rename and references resolve traversal and watcher handles through lexical
// ownership scopes exactly like opened module streams.
func TestFilespecWatcherRenameIsOwnershipAware(t *testing.T) {
	source := `to observe:
  watch folder "incoming" called changes
  for each change from changes:
    stop reading
to later:
  watch folder "incoming" called changes
  close stream changes
make changes "unrelated"
show changes
`
	locations, ok := ownedStreamOccurrences(source, 1, "changes")
	if !ok || len(locations) != 2 {
		t.Fatalf("watcher occurrences=%+v owned=%v", locations, ok)
	}
	if locations[0].Start.Line != 1 || locations[1].Start.Line != 2 {
		t.Fatalf("locations=%+v", locations)
	}
	if _, ok := ownedStreamOccurrences(source, 7, "changes"); ok {
		t.Fatal("an unrelated later value was resolved as the owned watcher handle")
	}

	traversal := `to scan:
  stream files under folder "src" called source_files
  for each file from source_files:
    stop reading
make source_files "unrelated"
`
	locations, ok = ownedStreamOccurrences(traversal, 1, "source_files")
	if !ok || len(locations) != 2 {
		t.Fatalf("traversal occurrences=%+v owned=%v", locations, ok)
	}
	if _, ok := ownedStreamOccurrences(traversal, 5, "source_files"); ok {
		t.Fatal("an unrelated later value was resolved as the traversal handle")
	}
}
