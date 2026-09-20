package soslsp

import (
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

// Catalog semantics per the user-agreed import model and tooling-contract v2:
// open imports expose bare sentence forms (plus the default qualifier);
func TestVocabularyOpenImportExposesBareForms(t *testing.T) {
	r := Catalog("", VocabularyRequest{Source: "import \"std/text\"\ntrim \"  hello  \" called clean\nshow clean\n"})
	if r.Schema != "sos/vocabulary@1" {
		t.Fatalf("schema = %q", r.Schema)
	}
	cat := r.Catalog
	var trim *sos.VocabularyEntry
	for i := range cat.Entries {
		if cat.Entries[i].ID == "std/text.trim" {
			trim = &cat.Entries[i]
		}
	}
	if trim == nil {
		t.Fatal("std/text.trim missing from catalog")
	}
	if !trim.Enabled {
		t.Error("trim not enabled under its open import")
	}
	if !hasPattern(trim.Patterns, "trim VALUE") {
		t.Errorf("open import must expose the bare pattern; got %v", trim.Patterns)
	}
	if !hasPattern(trim.Patterns, "text.trim VALUE") {
		t.Errorf("open import must keep the qualified pattern; got %v", trim.Patterns)
	}
	for _, e := range cat.Entries {
		if strings.HasPrefix(e.Library, "std/json") && e.Enabled {
			t.Errorf("std/json enabled without an import: %+v", e)
		}
	}
}

func TestVocabularyAliasedImportRequiresQualifier(t *testing.T) {
	r := Catalog("", VocabularyRequest{Source: "import \"std/text\" as words\n"})
	var trim *sos.VocabularyEntry
	for i := range r.Catalog.Entries {
		if r.Catalog.Entries[i].ID == "std/text.trim" {
			trim = &r.Catalog.Entries[i]
		}
	}
	if trim == nil {
		t.Fatal("std/text.trim missing")
	}
	if !trim.Enabled || trim.Alias != "words" {
		t.Fatalf("aliased import must enable under its alias; got enabled=%v alias=%q", trim.Enabled, trim.Alias)
	}
	if hasPattern(trim.Patterns, "trim VALUE") {
		t.Errorf("aliased import must not expose bare patterns; got %v", trim.Patterns)
	}
	if !hasPattern(trim.Patterns, "words.trim VALUE") {
		t.Errorf("aliased import must expose the qualifier pattern; got %v", trim.Patterns)
	}
}

func TestVocabularyWordTargetsEnforceForms(t *testing.T) {
	s := &server{}
	open := s.wordTargets("", "", "import \"std/text\"\n")
	var trim *wordTarget
	for i := range open {
		if open[i].Name == "trim" {
			trim = &open[i]
		}
	}
	if trim == nil || !trim.Bare || trim.Qualifier == "" {
		t.Fatalf("open import: trim must be bare-callable with a qualifier; got %+v", trim)
	}
	if r := resolveSent("trim", open); r == nil {
		t.Error("bare head must resolve under an open import")
	}
	if r := resolveSent("text.trim", open); r == nil {
		t.Error("qualified head must resolve under an open import")
	}

	aliased := s.wordTargets("", "", "import \"std/text\" as words\n")
	if r := resolveSent("trim", aliased); r != nil {
		t.Error("bare head must not resolve under an aliased import — the prefix is mandatory")
	}
	if r := resolveSent("words.trim", aliased); r == nil {
		t.Error("qualified head must resolve under an aliased import")
	}
}

func TestVocabularyFiltersAndOrder(t *testing.T) {
	full := Catalog("", VocabularyRequest{Source: "import \"std/json\"\n"})
	if !entriesSorted(full.Catalog.Entries) {
		t.Error("entries must be sorted by (library, name)")
	}
	byQuery := Catalog("", VocabularyRequest{Source: "import \"std/text\"\n", Query: "trim"})
	for _, e := range byQuery.Catalog.Entries {
		if !strings.Contains(strings.ToLower(e.Name+e.Library+e.Description), "trim") {
			t.Errorf("query filter leaked entry %s", e.ID)
		}
	}
	if len(byQuery.Catalog.Entries) != 1 {
		t.Errorf("query trim should match exactly std/text.trim; got %d entries", len(byQuery.Catalog.Entries))
	}
	byLib := Catalog("", VocabularyRequest{Source: "import \"std/text\"\n", Library: "std/json"})
	for _, l := range byLib.Catalog.Libraries {
		if l.Path != "std/json" {
			t.Errorf("library filter leaked %s", l.Path)
		}
	}
	for _, e := range byLib.Catalog.Entries {
		if e.Library != "std/json" {
			t.Errorf("library filter leaked %s", e.ID)
		}
	}
}

func TestVocabularyMethodOverLSP(t *testing.T) {
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		lspNotify("initialized", map[string]any{}),
		lspNotify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{"uri": "file:///demo/buffer.sos", "version": 1,
				"text": "import \"std/text\"\ntrim \"  hello  \" called clean\n"},
		}),
		lspRequest(2, "sos/vocabulary", map[string]any{
			"textDocument": map[string]any{"uri": "file:///demo/buffer.sos"},
			"query":        "trim",
		}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	res := lspResult(t, messages, 2)
	raw, _ := res["result"].(map[string]any)
	if raw == nil {
		t.Fatalf("sos/vocabulary error response: %v", res)
	}
	if raw["schema"] != "sos/vocabulary@1" {
		t.Fatalf("schema = %v", raw["schema"])
	}
	cat, ok := raw["catalog"].(map[string]any)
	if !ok {
		t.Fatalf("catalog missing: %v", raw)
	}
	entries, _ := cat["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("query trim over the open-import buffer: want 1 entry, got %d", len(entries))
	}
	entry := entries[0].(map[string]any)
	if entry["id"] != "std/text.trim" || entry["enabled"] != true {
		t.Errorf("entry = %v", entry)
	}
	patterns, _ := entry["patterns"].([]any)
	if len(patterns) == 0 || !strings.Contains(strings.Join(stringify(patterns), " "), "trim VALUE") {
		t.Errorf("patterns = %v", patterns)
	}
}

func TestVocabularyMethodRejectsMalformedParams(t *testing.T) {
	messages, err := runLSP(t,
		lspRequest(1, "initialize", map[string]any{}),
		lspNotify("initialized", map[string]any{}),
		lspRequest(2, "sos/vocabulary", map[string]any{"query": 42}),
		lspRequest(3, "shutdown", nil),
		lspNotify("exit", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	res := lspResult(t, messages, 2)
	rpcErr, _ := res["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(codeInvalidParams) {
		t.Fatalf("want error code %d, got %v", codeInvalidParams, res)
	}
}

func hasPattern(patterns []string, want string) bool {
	for _, p := range patterns {
		if p == want {
			return true
		}
	}
	return false
}

func entriesSorted(entries []sos.VocabularyEntry) bool {
	for i := 1; i < len(entries); i++ {
		a, b := entries[i-1], entries[i]
		if a.Library > b.Library || (a.Library == b.Library && a.Name >= b.Name) {
			return false
		}
	}
	return true
}

func stringify(items []any) []string {
	out := make([]string, 0, len(items))
	for _, v := range items {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
