package soslsp

import (
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

// Semantic token legend, by index. The order is part of the wire contract in
// lsp-contract.md: keyword, variable, parameter, function, type, namespace,
// string, number, comment, macro, enumMember, operator. New token types are
// appended so existing semantic-token indices remain stable.
var tokenLegend = []string{
	"keyword",
	"variable",
	"parameter",
	"function",
	"type",
	"namespace",
	"string",
	"number",
	"comment",
	"macro",
	"enumMember",
	"operator",
}

const (
	tokKeyword = iota
	tokVariable
	tokParameter
	tokFunction
	tokType
	tokNamespace
	tokString
	tokNumber
	tokComment
	tokSemanticPhrase
	tokCriterion
	tokOperator
)

type lexToken struct {
	line  int // zero based
	start int // UTF-16 code unit offset within the line
	len   int // UTF-16 code unit length
	kind  int
	text  string
}

// keywordWords are the single-word grammar words highlighted as keywords.
// Multi-word canonical phrases are split; sentence words that only ever occur
// inside data values are deliberately excluded.
var keywordWords = func() map[string]bool {
	phrases := append([]string{}, sos.Keywords()...)
	phrases = append(phrases,
		"package", "export", "import", "set", "print", "emit", "for", "each",
		"in", "from", "under", "matching", "times", "of", "lines", "if",
		"missing", "first", "last", "items", "numbered", "ascending",
		"descending", "with", "empty", "list",
	)
	set := make(map[string]bool, len(phrases))
	for _, p := range phrases {
		for _, w := range strings.Fields(p) {
			if w != "" {
				set[w] = true
			}
		}
	}
	return set
}()

// roleSpan is a byte range within a statement line classified as a named
// binding or type phrase. Offsets are relative to the trimmed line text the
// role regexes matched.
type roleSpan struct {
	start, end int
	kind       int
}

var (
	reCallQualified = regexp.MustCompile(`^\s*call\s+([A-Za-z_]\w*)\.([A-Za-z_]\w*)`)
	reAsType        = regexp.MustCompile(`\bas\s+((?:text|number|integer|folder|file|duration|boolean|json|table|lines\s+of\s+json|empty\s+list))\b`)
)

// nameRoleRegexes map a captured identifier group to its semantic role. The
// capture group of interest is always group 1. Anchors tolerate statement
// indentation.
var nameRoleRegexes = []struct {
	kind int
	re   *regexp.Regexp
}{
	{tokFunction, regexp.MustCompile(`^\s*command\s+([A-Za-z_]\w*)`)},
	{tokParameter, regexp.MustCompile(`^\s*(?:argument|option|switch)\s+([A-Za-z_]\w*)`)},
	{tokVariable, regexp.MustCompile(`^\s*repeat\s+([A-Za-z_]\w*)\s+times\b`)},
	{tokVariable, regexp.MustCompile(`^\s*(?:make|assign|set)\s+([A-Za-z_]\w*)`)},
	{tokVariable, regexp.MustCompile(`\bcalled\s+([A-Za-z_]\w*)`)},
	{tokVariable, regexp.MustCompile(`^\s*for\s+each\s+([A-Za-z_]\w*)`)},
	{tokVariable, regexp.MustCompile(`^\s*read\s+each\s+([A-Za-z_]\w*)`)},
	{tokVariable, regexp.MustCompile(`\binto\s+([A-Za-z_]\w*)`)},
	{tokNamespace, regexp.MustCompile(`^\s*package\s+([A-Za-z_]\w*)`)},
	{tokFunction, regexp.MustCompile(`^\s*export\s+([A-Za-z_]\w*)`)},
	{tokNamespace, regexp.MustCompile(`^\s*import\s+"[^"]*"\s+as\s+([A-Za-z_]\w*)`)},
}

// lineRoles classifies the identifier roles of one statement line. Only the
// comment-free prefix is matched so quoted data never becomes a role.
func lineRoles(line string) []roleSpan {
	code := stripLineComment(line)
	var spans []roleSpan
	add := func(kind, start, end int) {
		if start >= 0 && end > start && end <= len(code) {
			spans = append(spans, roleSpan{start: start, end: end, kind: kind})
		}
	}
	// Qualified call: call alias.action — alias is a namespace, action a function.
	if m := reCallQualified.FindStringSubmatchIndex(code); m != nil {
		add(tokNamespace, m[2], m[3])
		add(tokFunction, m[4], m[5])
	}
	for _, r := range nameRoleRegexes {
		for _, m := range r.re.FindAllStringSubmatchIndex(code, -1) {
			add(r.kind, m[2], m[3])
		}
	}
	// Type words after `as`, including the phrases "lines of json" and
	// "empty list": every word inside the captured phrase is a type token.
	for _, m := range reAsType.FindAllStringSubmatchIndex(code, -1) {
		spans = append(spans, roleSpan{start: m[2], end: m[3], kind: tokType})
	}
	return spans
}

// stripLineComment removes a trailing `#` comment, respecting quotes. Byte
// offsets of the remaining prefix match the original line.
func stripLineComment(line string) string {
	quoted, esc := false, false
	for i, c := range line {
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
			return line[:i]
		}
	}
	return line
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		w := utf16.RuneLen(r)
		if w < 0 {
			w = 1
		}
		n += w
	}
	return n
}
