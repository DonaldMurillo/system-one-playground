package sos

import (
	"strings"
	"testing"
)

func TestExpressionFailures(t *testing.T) {
	for _, src := range []string{`1 / 0`, `1 % 0`, `"unfinished`, `[1,2`, `true + 1`, `unknown`, `not 1`} {
		if _, e := evaluate(src, nil, nil); e == nil {
			t.Errorf("%s should fail", src)
		}
	}
}
func TestCheckWithoutExecution(t *testing.T) {
	for _, src := range []string{"show missing\n", "when 3:\n  show 1\n", "make a [1,2\n", "keep rows where jev:\n  using message\n"} {
		if d := Check(src); len(d) == 0 {
			t.Errorf("expected diagnostic for %q", src)
		}
	}
}
func TestExpressionComposition(t *testing.T) {
	v, e := evaluate(`count of words of "one two three" + 1`, nil, nil)
	if e != nil || v != float64(4) {
		t.Fatalf("%v %v", v, e)
	}
	v, e = evaluate(`"got {count of rows}"`, map[string]any{"rows": []any{1, 2}}, nil)
	if e != nil || v != "got 2" {
		t.Fatalf("%v %v", v, e)
	}
}
func TestParserRejectsMalformedStructure(t *testing.T) {
	for _, src := range []string{"show 1\n  make bad 2\n", "otherwise:\n  show 1\n", "when true:\nshow 1\n", "make x \"a\"\n   show x\n"} {
		if _, d := Parse(src); len(d) == 0 {
			t.Errorf("expected structure error for %s", src)
		}
	}
}
func TestFormatPreservesComments(t *testing.T) {
	src := "# words matter\nmake a 2  \n\nshow a\n"
	got, d := Format(src)
	if len(d) > 0 || !strings.Contains(got, "# words matter") || strings.Contains(got, "2  ") {
		t.Fatalf("%q %v", got, d)
	}
}
func TestBooleanShortCircuit(t *testing.T) {
	for _, src := range []string{`false and row.missing is 3`, `true or row.missing is 3`, `true or (1 / 0 is 2)`} {
		if _, e := evaluate(src, map[string]any{"row": map[string]any{}}, nil); e != nil {
			t.Errorf("%s: %v", src, e)
		}
	}
}
func TestQuotedConnectorsAreData(t *testing.T) {
	src := `make sample "written by somebody"
classify sample by jev "Primary cause suggested by these errors" called assessment:
  "yes": "Supported by evidence"
  "no": "Not supported"
`
	if d := Check(src); len(d) > 0 {
		t.Fatalf("%v", d)
	}
	m := match("classify", strings.Split(src, "\n")[1])
	if m[1] != "sample" || m[2] != `jev "Primary cause suggested by these errors"` {
		t.Fatalf("%q", m)
	}
}
func TestTableConnectorInsideString(t *testing.T) {
	src := `show "please show this as table"`
	p, d := Parse(src)
	if len(d) > 0 {
		t.Fatal(d)
	}
	a, _, ok := splitOutside(match("show", p.Statements[0].Text)[1], " as table")
	if ok || a != `"please show this as table"` {
		t.Fatalf("%q %v", a, ok)
	}
}
