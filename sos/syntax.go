package sos

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

var forms = []struct{ kind, pattern string }{
	{"command", `^command ([A-Za-z_]\w*):$`},
	{"parameter", `^(argument|option|switch) ([A-Za-z_]\w*)(?: as (text|number|integer|folder|file|duration|boolean))?(?: choices (.+?))?(?: default (.+))?$`},
	{"package", `^package ([A-Za-z_]\w*)$`},
	{"import", `^import "([^"\n]+)"(?: as ([A-Za-z_]\w*))?$`},
	{"export", `^export ([A-Za-z_]\w*)$`},
	{"word", `^word ([A-Za-z_]\w*) of ([A-Za-z_]\w*)$`},
	{"describe", `^describe (".*")$`},
	{"schema", `^expect ([\w-]+) with:$`},
	{"remember", `^remember (.+) called ([A-Za-z_]\w*)$`},
	{"find", `^find files under (.+) matching (.+) called ([A-Za-z_]\w*)$`},
	{"readEach", `^read each (\w+) in (.+) as lines of json into (\w+)$`},
	{"read", `^read (.+) as (json|text|lines of json) called (\w+)$`},
	{"require", `^require each (\w+) in (.+) matches ([\w-]+)$`},
	{"keep", `^keep (\w+) where (.+)$`},
	{"sort", `^sort (\w+) by (.+?)(?: (ascending|descending))?$`},
	{"group", `^group (\w+) by (.+) called (\w+)$`},
	{"folder", `^create folder (.+) if missing$`},
	{"make", `^(?:make|assign|set) (\w+) (.+)$`},
	{"map", `^map each (\w+) in (.+) with at most (.+) running called (\w+)( collecting failures)?:$`},
	{"for", `^for each (\w+) in (.+?)(?: numbered from (\d+))?:$`},
	{"while", `^while (.+):$`},
	{"repeat", `^repeat (.+) times:$`},
	{"when", `^when (.+):$`},
	{"otherwise", `^otherwise:$`},
	{"take", `^take (first|last) (.+) items from (.+) called (\w+)$`},
	{"classify", `^classify (.+) by (.+) called ([A-Za-z_]\w*):$`},
	{"evaluate", `^evaluate (.+) by jev using (.+) called ([A-Za-z_]\w*)$`},
	{"judge", `^judge (.+) by (.+) called ([A-Za-z_]\w*)$`},
	{"score", `^score (.+) by (.+) called ([A-Za-z_]\w*):$`},
	{"append", `^append (.+) to (\w+)$`},
	{"save", `^save (.+) as (json|text) (?:in (.+)|under (.+) named (.+))$`},
	{"show", `^(?:show|print|emit) (.+)$`},
	{"rethrow", `^rethrow$`},
	{"stop", `^stop(?: with (.+))?$`},
	{"to", `^to (\w+)(?: with (.+))?:$`},
	{"call", `^call ([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)(?: with (.+?))?(?: called (\w+))?$`},
	{"return", `^return (.+)$`},
	{"handler", `^on (failure|success|uncertain|existing)(?::| (.+))$`},
	{"ask", `^ask (.+)$`},
	{"using", `^using (.+)$`},
	{"model", `^model (.+)$`},
	{"accept", `^accept probability at least (.+)$`},
}
var patterns = map[string]*regexp.Regexp{}

func init() {
	for _, f := range forms {
		patterns[f.kind] = regexp.MustCompile(f.pattern)
	}
}
func match(kind, text string) []string {
	re := patterns[kind]
	if re == nil {
		return nil
	}
	masked := maskQuoted(text)
	indices := re.FindStringSubmatchIndex(string(masked))
	if indices == nil {
		return nil
	}
	out := make([]string, len(indices)/2)
	for i := range out {
		if indices[2*i] >= 0 {
			out[i] = text[indices[2*i]:indices[2*i+1]]
		}
	}
	return out
}

func Keywords() []string {
	return []string{"rethrow", "map", "evaluate", "describe", "choices", "read", "keep", "sort", "group", "save", "show", "make", "assign", "remember", "find", "for each", "when", "otherwise", "classify", "jev", "called", "where", "by", "as", "into", "on failure", "to", "call", "return", "while", "repeat", "judge", "score", "create folder", "take", "append", "require", "expect", "command", "option", "argument", "switch", "using", "ask", "accept", "model", "on uncertain", "on existing", "package", "import", "export"}
}
func classifyLine(text string) string {
	for _, f := range forms {
		if match(f.kind, text) != nil {
			return f.kind
		}
	}
	return ""
}
func stripComment(s string) string {
	quoted, esc := false, false
	for i, c := range s {
		if esc {
			esc = false
			continue
		}
		if c == '\\' && quoted {
			esc = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == '#' && !quoted {
			return strings.TrimRight(s[:i], " ")
		}
	}
	return s
}

// Parse builds an indented statement tree, collecting syntax errors without executing code.
func Parse(source string) (*Program, []Diagnostic) {
	p := &Program{Source: source}
	if len(source) > 1<<20 {
		return p, []Diagnostic{{1, 1, "source exceeds 1 MiB limit"}}
	}
	body, _, err := sosconfig.Extract(source)
	if err != nil {
		var positioned *sosconfig.Error
		if errors.As(err, &positioned) {
			return p, []Diagnostic{{positioned.Line, positioned.Column, positioned.Message}}
		}
		return p, []Diagnostic{{1, 1, err.Error()}}
	}
	var ds []Diagnostic
	type frame struct {
		indent int
		list   *[]*Statement
		parent *Statement
	}
	stack := []frame{{-1, &p.Statements, nil}}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		if strings.Contains(raw, "\t") {
			ds = append(ds, Diagnostic{i + 1, 1, "use spaces, not tabs, for indentation"})
			continue
		}
		text := strings.TrimSpace(stripComment(raw))
		if text == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent%2 != 0 {
			ds = append(ds, Diagnostic{i + 1, 1, "indentation must use multiples of two spaces"})
		}
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		fr := stack[len(stack)-1]
		expected := fr.indent + 2
		if fr.parent == nil {
			expected = 0
		}
		if indent != expected {
			ds = append(ds, Diagnostic{i + 1, 1, fmt.Sprintf("expected indentation of %d spaces", expected)})
		}
		kind := classifyLine(text)
		if kind == "" && fr.parent != nil {
			switch fr.parent.Kind {
			case "schema":
				if regexp.MustCompile(`^\w+ as (text|timestamp|number|integer|boolean|list)$`).MatchString(text) {
					kind = "field"
				}
			case "make":
				if strings.HasSuffix(fr.parent.Text, " with:") && regexp.MustCompile(`^\w+ from .+$`).MatchString(text) {
					kind = "field"
				}
			case "classify", "score":
				if regexp.MustCompile(`^(?:"[^"\n]+"|[0-9]+): ".*"$`).MatchString(text) {
					kind = "choice"
				}
			}
		}
		if kind == "" {
			ds = append(ds, Diagnostic{i + 1, indent + 1, "unknown construction: " + text})
			kind = "invalid"
		}
		st := &Statement{Kind: kind, Text: text, Line: i + 1}
		*fr.list = append(*fr.list, st)
		if kind == "otherwise" {
			siblings := *fr.list
			if len(siblings) < 2 || siblings[len(siblings)-2].Kind != "when" {
				ds = append(ds, Diagnostic{i + 1, indent + 1, "otherwise must follow when at the same indentation"})
			}
		}
		if kind == "handler" && (fr.parent == nil || fr.parent.Kind == "command" || fr.parent.Kind == "for") {
			ds = append(ds, Diagnostic{i + 1, indent + 1, "on handler must be indented under its operation"})
		}
		if kind == "command" && fr.parent != nil && fr.parent.Kind != "command" {
			ds = append(ds, Diagnostic{i + 1, 1, "command belongs at top level or directly inside a command"})
		}
		if kind == "package" || kind == "import" || kind == "export" || kind == "word" {
			if fr.parent != nil {
				ds = append(ds, Diagnostic{i + 1, 1, kind + " declarations belong at top level"})
			}
			if kind == "package" {
				for _, sibling := range *fr.list {
					if sibling != st && sibling.Kind == "package" {
						ds = append(ds, Diagnostic{i + 1, 1, "duplicate package declaration"})
					}
				}
			}
		}
		// Any operation may own on-failure handlers, even without a colon.
		stack = append(stack, frame{indent, &st.Body, st})
	}
	var validate func([]*Statement)
	validate = func(sts []*Statement) {
		for _, s := range sts {
			block := semanticCriterionDeclRe.MatchString(s.Text) || s.Kind == "map" || s.Kind == "for" || s.Kind == "while" || s.Kind == "repeat" || s.Kind == "when" || s.Kind == "otherwise" || s.Kind == "to" || s.Kind == "command" || s.Kind == "schema" || s.Kind == "classify" || s.Kind == "score" || strings.HasSuffix(s.Text, "with:") || strings.HasSuffix(s.Text, "jev:") || s.Kind == "handler" && strings.HasSuffix(s.Text, ":")
			if block && len(s.Body) == 0 {
				ds = append(ds, Diagnostic{s.Line, 1, "expected an indented body"})
			}
			for _, c := range s.Body {
				if !block && c.Kind != "handler" {
					ds = append(ds, Diagnostic{c.Line, 1, "only on handlers may follow this operation"})
				}
			}
			validate(s.Body)
		}
	}
	validate(p.Statements)
	ds = append(ds, buildCommands(p)...)
	return p, ds
}

func Check(source string) []Diagnostic {
	return checkSource(source, nil)
}
func Format(source string) (string, []Diagnostic) {
	p, ds := Parse(source)
	// Formatting is whitespace-only: unknown constructions (including
	// sentence calls whose vocabulary is resolved elsewhere) must not block
	// it. Every other diagnostic is real and refuses to format.
	for _, d := range ds {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			return "", ds
		}
	}
	_ = p
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	_, header, _ := sosconfig.Extract(source)
	inHeader := header != nil
	for i := range lines {
		if inHeader {
			if i > 0 && strings.TrimSpace(lines[i]) == "+++" {
				inHeader = false
			}
			continue
		}
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n", nil
}

func maskQuoted(text string) []byte {
	masked := []byte(text)
	quoted, escaped := false, false
	for i, c := range []byte(text) {
		if escaped {
			if quoted {
				masked[i] = '_'
			}
			escaped = false
			continue
		}
		if c == '\\' && quoted {
			masked[i] = '_'
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			masked[i] = '_'
		}
	}
	return masked
}
func splitOutside(text, separator string) (string, string, bool) {
	i := strings.Index(string(maskQuoted(text)), separator)
	if i < 0 {
		return text, "", false
	}
	return text[:i], text[i+len(separator):], true
}

// UsesJev reports whether a statement invokes the provider, matching the same
// token boundaries used by checking and execution (never quoted data).
func UsesJev(s *Statement) bool {
	if s == nil {
		return false
	}
	if s.Kind == "evaluate" {
		return true
	}
	if s.Kind == "judge" || s.Kind == "classify" || s.Kind == "score" {
		return true
	}
	if s.Kind == "keep" {
		m := match("keep", s.Text)
		return m != nil && jevPredicate(m[2])
	}
	return false
}
func jevPredicate(text string) bool { return text == "jev:" || strings.HasPrefix(text, "jev ") }
