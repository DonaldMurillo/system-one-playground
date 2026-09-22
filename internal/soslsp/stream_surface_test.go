package soslsp

import (
	"encoding/json"
	"strings"
	"testing"
)

const streamHandlingURI = "file:///stream-handling.sos"

// TestStreamHandlingHoverExplainsCanonicalAndTechnical verifies hover gives
// every canonical construction its technical name and explains a std/streams
// alias call with the exact canonical sentence it means.
func TestStreamHandlingHoverExplainsCanonicalAndTechnical(t *testing.T) {
	source := `to follow streaming text:
  finish

stream follow called changes
wait for changes to be quiet for 500 milliseconds called settled
call streams.debounce with changes, 500 milliseconds called settled2
`
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(streamHandlingURI, source),
		lspRequest(2, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": streamHandlingURI}, "position": map[string]int{"line": 4, "character": 5}}),
		lspRequest(3, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": streamHandlingURI}, "position": map[string]int{"line": 5, "character": 5}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	canonical := lspResult(t, messages, 2)["result"].(map[string]any)["contents"].(map[string]any)["value"].(string)
	if !strings.Contains(canonical, "quietStream") || !strings.Contains(canonical, "debounce") {
		t.Errorf("canonical construction hover missing kind or technical name: %s", canonical)
	}
	alias := lspResult(t, messages, 3)["result"].(map[string]any)["contents"].(map[string]any)["value"].(string)
	if !strings.Contains(alias, "streams.debounce") || !strings.Contains(alias, "wait for changes to be quiet for 500 milliseconds called settled2") {
		t.Errorf("alias hover missing canonical rewrite: %s", alias)
	}
}

// TestStreamAliasCodeActionRewritesCanonical verifies the editor offers the
// deterministic canonical rewrite for a technical alias call.
func TestStreamAliasCodeActionRewritesCanonical(t *testing.T) {
	source := `to follow streaming text:
  finish

stream follow called changes
call streams.debounce with changes, 500 milliseconds called settled
`
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(streamHandlingURI, source),
		lspRequest(2, "textDocument/codeAction", map[string]any{
			"textDocument": map[string]any{"uri": streamHandlingURI},
			"range":        map[string]any{"start": map[string]int{"line": 4, "character": 0}, "end": map[string]int{"line": 4, "character": 0}},
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(lspResult(t, messages, 2)["result"])
	var actions []map[string]any
	if err := json.Unmarshal(raw, &actions); err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if action["title"] != "Rewrite as canonical stream handling" {
			continue
		}
		changes := action["edit"].(map[string]any)["changes"].(map[string]any)[streamHandlingURI].([]any)
		text := changes[0].(map[string]any)["newText"].(string)
		if text != "wait for changes to be quiet for 500 milliseconds called settled" {
			t.Errorf("unexpected rewrite %q", text)
		}
		return
	}
	t.Errorf("no canonical rewrite offered: %s", string(raw))
}

// TestStreamHandlingCompletionOffersCanonicalForms verifies completion finds
// canonical constructions by both canonical and technical prefixes.
func TestStreamHandlingCompletionOffersCanonicalForms(t *testing.T) {
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(streamHandlingURI, "to demo:\n  wa"),
		lspRequest(2, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": streamHandlingURI}, "position": map[string]int{"line": 1, "character": 5}}),
		openDoc(streamHandlingURI, "to demo:\n  debounce"),
		lspRequest(3, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": streamHandlingURI}, "position": map[string]int{"line": 1, "character": 11}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	raw, _ := json.Marshal(lspResult(t, messages, 2)["result"])
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item["label"] == "wait for quiet" {
			found = true
		}
	}
	if !found {
		t.Errorf("canonical quiet-waiting completion missing from %d items", len(items))
	}
	items = nil
	raw, _ = json.Marshal(lspResult(t, messages, 3)["result"])
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, item := range items {
		if label, _ := item["label"].(string); strings.HasPrefix(label, "debounce") {
			found = true
		}
	}
	if !found {
		t.Errorf("searching the technical name debounce found no canonical completion")
	}
}
