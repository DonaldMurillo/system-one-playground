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

func TestExpressionOperators(t *testing.T) {
	doc := Parse("make joined \"left\" + \"right\"\nmake also \"left\" plus \"right\"\nmake scaled 2 * 3 >= 6\n+++\n")

	if got := doc.Lines[0].Tokens[3]; got.Kind != "operator" || got.Text != "+" {
		t.Fatalf("symbolic concatenation token = %+v, want operator +", got)
	}
	if got := doc.Lines[1].Tokens[3]; got.Kind != "identifier" || got.Text != "plus" {
		t.Fatalf("word concatenation token = %+v, want identifier plus for semantic classification", got)
	}
	if got := doc.Lines[2].Tokens[2]; got.Kind != "number" || got.Text != "2" {
		t.Fatalf("number before symbolic operators = %+v", got)
	}
	if got := doc.Lines[2].Tokens[3]; got.Kind != "operator" || got.Text != "*" {
		t.Fatalf("multiplication token = %+v, want operator *", got)
	}
	if got := doc.Lines[2].Tokens[5]; got.Kind != "operator" || got.Text != ">=" {
		t.Fatalf("comparison token = %+v, want operator >=", got)
	}
	for _, token := range doc.Lines[3].Tokens {
		if token.Kind == "operator" {
			t.Fatalf("frontmatter delimiter tokenized as operator: %+v", token)
		}
	}
}
