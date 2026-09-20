package sossyntax

import "testing"

func TestIncrementalSyntax(t *testing.T) {
	old := Parse("command demo:\n  make label \"hi\"\n  show label\n")
	next := Update(old, "command demo:\n  make label \"😀 #read\" # comment\n  show label\n")
	if &next.Lines[0].Tokens[0] != &old.Lines[0].Tokens[0] || &next.Lines[2].Tokens[0] != &old.Lines[2].Tokens[0] {
		t.Fatal("unchanged lines not reused")
	}
	tokens := next.Lines[1].Tokens
	if tokens[2].Kind != "string" || tokens[2].End-tokens[2].Start != 10 || tokens[3].Kind != "comment" {
		t.Fatalf("UTF16/string/comment: %+v", tokens)
	}
	if len(next.Blocks) != 1 || next.Blocks[0].Start != 0 || next.Blocks[0].End != 3 {
		t.Fatalf("blocks: %+v", next.Blocks)
	}
	incomplete := Update(next, "command demo:\n  make label \"unfinished\n  show label")
	if incomplete.Lines[1].Tokens[2].Kind != "incompleteString" || incomplete.Lines[2].Tokens[0].Text != "show" {
		t.Fatal("unfinished string consumed next line")
	}
}
func TestBlocksAndInsertions(t *testing.T) {
	old := Parse("when true:\n  # comment\n  when false:\n    show 1\nshow 2")
	if len(old.Blocks) != 2 {
		t.Fatalf("nested blocks: %+v", old.Blocks)
	}
	next := Update(old, "# new\nwhen true:\n  # comment\n  when false:\n    show 1\nshow 2")
	if &next.Lines[5].Tokens[0] != &old.Lines[4].Tokens[0] {
		t.Fatal("insertions should retain suffix tokens")
	}
	if len(Parse("show \"x:y\" # no block:").Blocks) != 0 {
		t.Fatal("colon in comment became block")
	}
}
