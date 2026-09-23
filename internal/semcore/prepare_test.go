package semcore

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractPreparePreservesContextAndPolicies(t *testing.T) {
	specs := []RuleSpec{
		{ID: "semantic", Severity: "warning", Where: WhereSpec{Ext: []string{".go"}, Pattern: "SharedWidget"}, Ask: &AskSpec{Instructions: "Is this broken?", Yes: "broken", No: "correct", Threshold: .87}, Message: "broken"},
		{ID: "deterministic", Severity: "error", Where: WhereSpec{Ext: []string{".go"}, Pattern: "TODO"}, Check: &CheckSpec{Always: true}, Message: "unfinished"},
	}
	rs, err := Compile(specs)
	if err != nil {
		t.Fatal(err)
	}
	a := ExtractText("a.go", "// invariant\npackage sample\nfunc Example() {\n // TODO SharedWidget\n use(SharedWidget)\n}\n", rs)
	b := ExtractText("b.go", "package sample\nfunc Peer() {\n use(SharedWidget)\n}\n", rs)
	units := append(a, b...)
	prepared, err := Prepare(units, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) == 0 {
		t.Fatal("no units")
	}
	p := prepared[0]
	state := p["state"].(map[string]any)
	if !strings.Contains(state["related_lines_in_other_files"].(string), "b.go") {
		t.Fatalf("missing crossfile: %#v", state)
	}
	if !strings.Contains(state["file_header_comment"].(string), "invariant") {
		t.Fatal("missing header")
	}
	if len(p["deterministic_findings"].([]any)) != 1 {
		t.Fatalf("findings: %#v", p)
	}
	qs := p["questions"].([]any)
	if len(qs) != 1 {
		t.Fatalf("questions: %#v", qs)
	}
	q := qs[0].(map[string]any)["question"].(map[string]any)
	original, _ := json.Marshal(rs[0].Question)
	actual, _ := json.Marshal(q)
	var expected map[string]any
	json.Unmarshal(original, &expected)
	actualMap := map[string]any{}
	json.Unmarshal(actual, &actualMap)
	if expected["instructions"] != actualMap["instructions"] {
		t.Fatalf("question instructions changed: %s vs %s", original, actual)
	}
	if p["rules"].([]any)[0].(map[string]any)["threshold"] != .87 {
		t.Fatal("threshold changed")
	}
}

func TestPrepareRecordsDroppedContext(t *testing.T) {
	specs := []RuleSpec{{ID: "x", Where: WhereSpec{Ext: []string{".go"}, Pattern: "x"}, Ask: &AskSpec{Instructions: "broken?"}}}
	u := Unit{File: "x.go", StartLine: 1, EndLine: 1, Code: "x", Header: strings.Repeat("h", 80000), Sites: []Site{{RuleID: "x", Line: 1, Text: "x"}}}
	p, e := Prepare([]Unit{u}, specs)
	if e != nil {
		t.Fatal(e)
	}
	if !p[0]["trimmed"].(bool) || !p[0]["partial_context"].(bool) {
		t.Fatal("trim not recorded")
	}
	state := p[0]["state"].(map[string]any)
	if state["file_header_comment"] != "" || !strings.Contains(state["context_completeness"].(string), "header") {
		t.Fatal("missing context disclosure")
	}
}

func TestDiffNewSideRanges(t *testing.T) {
	c, e := ParseUnifiedDiff(strings.NewReader("--- a/a.go\n+++ b/a.go\n@@ -2,2 +2,3 @@\n old\n-removed\n+added\n+second\n"), "/repo")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(string(filepath.Separator), "repo", "a.go")
	if !c.Touches(path, 3, 4) || c.Touches(path, 1, 2) {
		t.Fatalf("wrong changes: %#v", c)
	}
}

func TestBuiltinRulesRemainValid(t *testing.T) {
	for _, name := range []string{"default", "browser-storage"} {
		specs, e := BuiltinSpecs(name)
		if e != nil {
			t.Fatal(e)
		}
		if len(specs) == 0 {
			t.Fatal("empty builtin")
		}
	}
}
