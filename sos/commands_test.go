package sos

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const commandFixture = `command tickets:
  describe "Ticket manager"
  option source as file default "tickets.json"
  command list:
    option criterion as text choices "urgent", "all" default "urgent"
    argument target as text
    show target
  command count:
    switch verbose default off
    show source
`

func TestCommandsSelection(t *testing.T) {
	p, ds := Parse(commandFixture)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	sel, e := SelectCommand(p, []string{"--source", "input.json", "list", "here", "--criterion=all"})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(sel.Path, "/") != "list" || sel.Values["source"] != "input.json" || sel.Values["target"] != "here" || sel.Values["criterion"] != "all" {
		t.Fatalf("%+v", sel)
	}
	for _, args := range [][]string{{"list"}, {"list", "here", "--criterion=nope"}, {"--criterion=all", "list", "here"}, {"--source=a", "list", "here", "--source=b"}, {"missing"}, {"count", "--verbose=bad"}} {
		if _, e := SelectCommand(p, args); e == nil {
			t.Errorf("accepted %v", args)
		}
	}
	for _, args := range [][]string{nil, {"--help"}, {"list", "--help"}} {
		s, e := SelectCommand(p, args)
		if e != nil || !s.Help {
			t.Errorf("help %v: %+v %v", args, s, e)
		}
	}
	s, e := SelectCommand(p, []string{"list", "--", "--help"})
	if e != nil || s.Help || s.Values["target"] != "--help" {
		t.Fatalf("terminator: %+v %v", s, e)
	}
}
func TestCommandsStructure(t *testing.T) {
	for _, source := range []string{
		"command x:\n  show 1\nshow 2\n",
		"command x:\n  show 1\n  command y:\n    show 2\n",
		"command x:\n  argument a\n  command y:\n    show 2\n",
		"command x:\n  option a\n  command y:\n    option a\n    show a\n",
		"command x:\n  option help\n  show 1\n",
		"command x:\n  option a choices \"a\", \"b\" default \"c\"\n  show a\n",
	} {
		if _, ds := Parse(source); len(ds) == 0 {
			t.Errorf("accepted %s", source)
		}
	}
}
func TestCommandsRunSelectedScope(t *testing.T) {
	source := "to shared:\n  return \"done\"\ncommand app:\n  command good:\n    call shared called result\n    show result\n  command other:\n    show missing\n"
	p, ds := Parse(source)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	var output bytes.Buffer
	_, e := Run(context.Background(), p, Options{CommandPath: []string{"good"}, Stdout: &output})
	if e != nil {
		t.Fatal(e)
	}
	if strings.TrimSpace(output.String()) != "done" {
		t.Fatal(output.String())
	}
	if _, e = Run(context.Background(), p, Options{}); e == nil {
		t.Fatal("ran group")
	}
	if _, e = Run(context.Background(), p, Options{CommandPath: []string{"good"}, Args: map[string]any{"extra": "value"}}); e == nil {
		t.Fatal("accepted unknown input")
	}
}
func TestCommandCheckSiblingScope(t *testing.T) {
	source := "command app:\n  command first:\n    make value 1\n  command second:\n    show value\n"
	if ds := Check(source); len(ds) == 0 {
		t.Fatal("sibling variables leaked")
	}
}

func TestCommandResolutionCoverage(t *testing.T) {
	source := "command app:\n  command one:\n    show 1\n  command two:\n    show 2\n"
	p, _ := Parse(source)
	cfg := boundaryPolicy(t, "assisted", "explicit", 0)
	selected, e := SelectedSource(p, []string{"one"})
	if e != nil {
		t.Fatal(e)
	}
	saved, e := Analyze(context.Background(), selected, AnalyzeOptions{Config: cfg})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Run(context.Background(), p, Options{Config: &cfg, CommandPath: []string{"two"}, Resolution: saved, Locked: true}); e == nil {
		t.Fatal("accepted other command resolution")
	}
	whole, e := Analyze(context.Background(), source, AnalyzeOptions{Config: cfg})
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	result, e := Run(context.Background(), p, Options{Config: &cfg, CommandPath: []string{"two"}, Resolution: whole, Locked: true, Stdout: &out})
	if e != nil {
		t.Fatal(e)
	}
	if strings.TrimSpace(out.String()) != "2" || result.Usage.TotalAdmitted != 0 {
		t.Fatalf("whole replay: %s %+v", out.String(), result)
	}
}

func TestCommandUnselectedSemanticCostsNothing(t *testing.T) {
	source := "command app:\n  command cheap:\n    show 1\n  command expensive:\n    make tickets []\n    filter tickets where active is true\n"
	p, _ := Parse(source)
	cfg := boundaryPolicy(t, "assisted", "explicit", 0)
	result, e := Run(context.Background(), p, Options{Config: &cfg, CommandPath: []string{"cheap"}})
	if e != nil {
		t.Fatal(e)
	}
	if result.Usage.TotalAdmitted != 0 {
		t.Fatal("unselected command spent budget")
	}
	// An optional stale whole record cannot trigger fresh whole-source resolution.
	stale := &Analysis{SourceHash: semanticSourceHash(source), Version: 0}
	result, e = Run(context.Background(), p, Options{Config: &cfg, CommandPath: []string{"cheap"}, Resolution: stale})
	if e != nil {
		t.Fatal(e)
	}
	if result.Usage.TotalAdmitted != 0 {
		t.Fatal("stale whole record spent on unselected command")
	}
}

func TestCommandSharedCriterion(t *testing.T) {
	source := "criterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 0.8\n  on uncertain discard\ncommand app:\n  make tickets []\n  keep urgent tickets\n  show tickets\n"
	p, _ := Parse(source)
	cfg := boundaryPolicy(t, "semantic", "semantic", 0)
	if _, e := Run(context.Background(), p, Options{Config: &cfg}); e != nil {
		t.Fatal(e)
	}
}

func TestCommandUnusedSharedActionCostsNothing(t *testing.T) {
	source := "to unused:\n  make tickets []\n  filter tickets where active is true\nto indirect:\n  call used\nto used:\n  show 7\ncommand app:\n  call indirect\n"
	p, _ := Parse(source)
	cfg := boundaryPolicy(t, "assisted", "explicit", 0)
	var out bytes.Buffer
	result, e := Run(context.Background(), p, Options{Config: &cfg, Stdout: &out})
	if e != nil {
		t.Fatal(e)
	}
	if strings.TrimSpace(out.String()) != "7" || result.Usage.TotalAdmitted != 0 {
		t.Fatalf("%s %+v", out.String(), result)
	}
}

func TestSelectedSourceRetainsInlineHandlerActions(t *testing.T) {
	source := "to recover:\n  show \"recovered\"\ncommand app:\n  read \"missing.json\" as json called tickets\n    on failure call recover\n"
	p, _ := Parse(source)
	selected, e := SelectedSource(p, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(selected, "to recover:") {
		t.Fatal("pruned inline handler action")
	}
	cfg := boundaryPolicy(t, "canonical", "explicit", 0)
	var out bytes.Buffer
	if _, e := Run(context.Background(), p, Options{Config: &cfg, Dir: t.TempDir(), Stdout: &out}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "recovered") {
		t.Fatal(out.String())
	}
}

func TestHelpStillRejectsUnknownInputs(t *testing.T) {
	p, _ := Parse(commandFixture)
	for _, args := range [][]string{
		{"--help", "--bogus"}, {"--bogus", "--help"},
		{"--help", "bogus"}, {"bogus", "--help"},
		{"list", "--help", "here", "extra"}, {"list", "here", "extra", "--help"},
		{"list", "--help", "--criterion=urgent", "--criterion=all"},
	} {
		if _, e := SelectCommand(p, args); e == nil {
			t.Errorf("help hid invalid inputs %v", args)
		}
	}
	for _, args := range [][]string{
		{"--help", "list"}, {"list", "--help"},
		{"list", "--criterion=nope", "--help"}, {"list", "--help", "--criterion=nope"},
		{"list", "--criterion", "--help"}, {"list", "--help", "--criterion"},
		{"list", "--criterion", "-h"},
	} {
		s, e := SelectCommand(p, args)
		if e != nil || !s.Help {
			t.Errorf("help %v: %+v %v", args, s, e)
		}
	}
	plain, _ := Parse("show 1\n")
	if _, e := SelectCommand(plain, []string{"--help", "--bogus"}); e == nil {
		t.Fatal("plain help hid unknown input")
	}
	if _, e := SelectedSource(nil, nil); e == nil {
		t.Fatal("nil selected source")
	}
	if _, e := ValidateCommandInputs(nil, nil, nil); e == nil {
		t.Fatal("nil command validation")
	}
}

func TestRequiredArgumentsAndBooleanSwitchDeclarations(t *testing.T) {
	for _, decl := range []string{`argument target as text default "out"`, `switch enabled as integer`, `switch enabled as text default "yes"`} {
		if _, ds := Parse("command app:\n  " + decl + "\n  show 1\n"); len(ds) == 0 {
			t.Errorf("accepted invalid declaration %s", decl)
		}
	}
	for _, decl := range []string{`argument target as text`, `switch enabled as boolean default on`, `switch enabled default off`} {
		if _, ds := Parse("command app:\n  " + decl + "\n  show 1\n"); len(ds) > 0 {
			t.Errorf("rejected valid declaration %s: %v", decl, ds)
		}
	}
}
