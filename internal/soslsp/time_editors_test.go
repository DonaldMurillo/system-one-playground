package soslsp

import (
	"encoding/json"
	"strings"
	"testing"
)

func timeCompletionItems(t *testing.T, messages []map[string]any, id int) []map[string]any {
	t.Helper()
	var items []map[string]any
	raw, _ := json.Marshal(lspResult(t, messages, float64(id))["result"])
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestTimeSentenceCompletionsOfferCanonicalForms(t *testing.T) {
	source := "wa\n"
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, source),
		lspRequest(2, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 0, "character": 2}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	items := timeCompletionItems(t, messages, 2)
	var waitForm map[string]any
	for _, item := range items {
		if item["label"] == "wait for 5 seconds" {
			waitForm = item
		}
	}
	if waitForm == nil {
		t.Fatalf("wait completion missing: %#v", items)
	}
	if waitForm["insertTextFormat"] != float64(2) {
		t.Fatalf("wait completion is not a snippet: %#v", waitForm)
	}
	docs, _ := waitForm["documentation"].(map[string]any)
	value, _ := docs["value"].(string)
	if !strings.Contains(value, "monotonic") {
		t.Fatalf("wait hover documentation lacks clock contract: %q", value)
	}
}

func TestTimerCompletionsOnlyOnStreamLines(t *testing.T) {
	onStreamLine := func(id int, source string, line, char int) []map[string]any {
		t.Helper()
		messages, err := runLSP(t,
			lspRequest(1, "initialize", map[string]any{}),
			openDoc(featureURI, source),
			lspRequest(id, "textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": line, "character": char}}),
		)
		if err != nil {
			t.Fatal(err)
		}
		return timeCompletionItems(t, messages, id)
	}
	items := onStreamLine(2, "stream a\n", 0, 8)
	found := false
	for _, item := range items {
		if item["label"] == "a tick every 10 seconds called ticks" {
			found = true
		}
	}
	if !found {
		t.Fatalf("repeating timer completion missing on stream line: %#v", items)
	}
	// Away from a stream sentence the tick form must not be offered, because
	// inserting it there would produce invalid source.
	items = onStreamLine(3, "print \"a\"\na\n", 1, 1)
	for _, item := range items {
		if item["label"] == "a tick every 10 seconds called ticks" {
			t.Fatalf("tick form offered outside a stream sentence: %#v", item)
		}
	}
}

func TestTimeSentenceHoverDocumentsContracts(t *testing.T) {
	source := "stream a tick every 10 seconds called ticks\nallow at most 30 seconds for:\n  call reports.generate called report\n"
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		openDoc(featureURI, source),
		lspRequest(2, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 0, "character": 12}}),
		lspRequest(3, "textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": featureURI}, "position": map[string]int{"line": 1, "character": 5}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	timerHover, _ := lspResult(t, messages, 2)["result"].(map[string]any)
	contents, _ := timerHover["contents"].(map[string]any)
	value, _ := contents["value"].(string)
	if !strings.Contains(value, "never drifts") && !strings.Contains(value, "drift") {
		t.Fatalf("timer hover lacks anchoring guarantee: %q", value)
	}
	if !strings.Contains(value, "missed") {
		t.Fatalf("timer hover lacks missed-tick policy: %q", value)
	}
	deadlineHover, _ := lspResult(t, messages, 3)["result"].(map[string]any)
	contents, _ = deadlineHover["contents"].(map[string]any)
	value, _ = contents["value"].(string)
	if !strings.Contains(value, "DeadlineExceeded") || !strings.Contains(value, "earliest effective deadline") {
		t.Fatalf("deadline hover lacks failure and nesting contract: %q", value)
	}
}
