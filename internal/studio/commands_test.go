package studio

import (
	"github.com/DonaldMurillo/system-one-playground/sos"
	"testing"
)

func TestCommandMetadataRetainsNestedDeclarations(t *testing.T) {
	p, diagnostics := sos.Parse("command tickets:\n  option source as file default \"tickets.json\"\n  command triage:\n    describe \"Find urgent tickets\"\n    option criterion as text choices \"urgent\", \"all\"\n    show criterion\n")
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	tree := commandsMeta(p)
	if tree == nil || len(tree.Commands) != 1 || len(tree.Inputs) != 1 {
		t.Fatalf("metadata: %#v", tree)
	}
	leaf := tree.Commands[0]
	if leaf.Description != "Find urgent tickets" || len(leaf.Inputs) != 1 || !leaf.Inputs[0].Required || len(leaf.Inputs[0].Choices) != 2 {
		t.Fatalf("leaf: %#v", leaf)
	}
}

func TestAnalysisCacheSelectionIdentity(t *testing.T) {
	source := "show 1"
	a := &sos.Analysis{}
	s := &Server{analysisCache: &analysisEntry{source: source, selection: selectionKey([]string{"triage"}, map[string]any{"criterion": "urgent"}), analysis: a}}
	if s.analysisSnapshot(source, []string{"triage"}, map[string]any{"criterion": "urgent"}) == nil {
		t.Fatal("matching selection not cached")
	}
	if s.analysisSnapshot(source, []string{"report"}, map[string]any{"criterion": "urgent"}) != nil {
		t.Fatal("cache reused across commands")
	}
	if s.analysisSnapshot(source, []string{"triage"}, map[string]any{"criterion": "all"}) != nil {
		t.Fatal("cache reused across input changes")
	}
}
