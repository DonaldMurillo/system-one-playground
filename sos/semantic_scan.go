package sos

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// semanticPronouns are the bounded reference words that may stand for a
// visible collection. They never name files, destinations, or formats.
var semanticPronouns = map[string]bool{"them": true, "it": true, "those": true, "these": true, "ones": true}

var semanticCriterionDeclRe = regexp.MustCompile(`^criterion ([A-Za-z_]\w*):$`)

// semanticForms is the finite registry of noncanonical sentence patterns.
// Each pattern captures explicit slots only; anything a pattern does not
// capture (destination, format, result name) is a diagnostic, never an
// inferred default. Patterns run before canonical classification, so a
// pattern must never match a canonical sentence.
var semanticForms = []struct {
	id      string
	pattern string
}{
	{"keep-where-ref", `^(?:keep|retain|remove|filter) (them|it|those|these|ones) where (.+)$`},
	{"keep-where", `^(?:retain|remove|filter) (\w+) where (.+)$`},
	{"keep-criterion-ones", `^keep (?:the )?([A-Za-z_]\w*) ones$`},
	{"keep-criterion-name", `^keep (?:the )?([A-Za-z_]\w*) ([A-Za-z_]\w*)$`},
	{"order-ref", `^order (them|it|those|these|ones) by (.+?)(?: (ascending|descending|highest first|lowest first))?$`},
	{"order", `^order (\w+) by (.+?)(?: (ascending|descending|highest first|lowest first))?$`},
	{"sort-ref", `^sort (them|it|those|these|ones) by (.+?)(?: (ascending|descending|highest first|lowest first))?$`},
	{"sort-suffix", `^sort (\w+) by (.+?) (highest|lowest) first$`},
	{"collect-ref", `^collect (them|it|those|these|ones) by (.+?) called ([A-Za-z_]\w*)$`},
	{"collect", `^collect (\w+) by (.+?) called ([A-Za-z_]\w*)$`},
	{"group-ref", `^group (them|it|those|these|ones) by (.+?) called ([A-Za-z_]\w*)$`},
	{"read-load", `^load ("(?:[^"\\]|\\.)*") as (json|text|lines of json) (?:called|into) ([A-Za-z_]\w*)$`},
	{"read-into", `^read ("(?:[^"\\]|\\.)*") as (json|text|lines of json) into ([A-Za-z_]\w*)$`},
	{"save-write", `^write (.+) as (json|text) to ("(?:[^"\\]|\\.)*")$`},
	{"save-store", `^store (.+) as (json|text) (?:in|to) ("(?:[^"\\]|\\.)*")$`},
}

var semanticPatterns = map[string]*regexp.Regexp{}

func init() {
	for _, f := range semanticForms {
		semanticPatterns[f.id] = regexp.MustCompile(f.pattern)
	}
}

// matchSemantic matches a registry pattern against comment-free text using
// the same quoted-region masking as the canonical matcher.
func matchSemantic(id, text string) []string {
	re := semanticPatterns[id]
	if re == nil {
		return nil
	}
	return matchRe(re, text)
}

func matchRe(re *regexp.Regexp, text string) []string {
	indices := re.FindStringSubmatchIndex(string(maskQuoted(text)))
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

type semLine struct {
	num    int
	indent int
}

type semNode struct {
	line     *semLine
	text     string
	role     string // canonical | semantic | criterion | unknown
	form     string // canonical kind or registry form id
	m        []string
	children []*semNode
}

type semanticScan struct {
	lines       []string
	trailingNL  bool
	nodes       []*semNode
	diagnostics []Diagnostic
}

// scanSemantic builds a statement tree over the original source, preserving
// line numbers and indentation. Header, blank, and comment lines are skipped
// for classification but remain in lines for canonical assembly.
func scanSemantic(source string) *semanticScan {
	// Lines keep their original bytes (CR endings, BOM) so canonical assembly
	// can reproduce the source exactly; classification works on trimmed text.
	s := &semanticScan{lines: strings.Split(source, "\n")}
	if strings.HasSuffix(source, "\n") && len(s.lines) > 1 {
		s.lines = s.lines[:len(s.lines)-1]
		s.trailingNL = true
	}
	start := 0
	if len(s.lines) > 0 && strings.TrimSuffix(strings.TrimPrefix(s.lines[0], "\ufeff"), "\r") == "+++" {
		// The header stays verbatim in lines for canonical assembly; the
		// scanner skips it so body statements keep original line numbers.
		for i := 1; i < len(s.lines); i++ {
			if strings.TrimSuffix(s.lines[i], "\r") == "+++" {
				start = i + 1
				break
			}
		}
	}
	type frame struct {
		indent   int
		node     *semNode
		children *[]*semNode
	}
	stack := []frame{{-1, nil, &s.nodes}}
	for i := start; i < len(s.lines); i++ {
		work := strings.TrimSuffix(s.lines[i], "\r")
		text := strings.TrimSpace(stripComment(work))
		if text == "" {
			continue
		}
		if strings.Contains(work, "\t") {
			s.diagnostics = append(s.diagnostics, Diagnostic{i + 1, 1, "use spaces, not tabs, for indentation"})
			continue
		}
		indent := len(work) - len(strings.TrimLeft(work, " "))
		if indent%2 != 0 {
			s.diagnostics = append(s.diagnostics, Diagnostic{i + 1, 1, "indentation must use multiples of two spaces"})
		}
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		if i == 0 {
			text = strings.TrimPrefix(text, "\ufeff")
		}
		node := &semNode{line: &semLine{num: i + 1, indent: indent}, text: text}
		node.role, node.form, node.m = classifySemantic(text)
		if node.role == "unknown" {
			node.role, node.form = classifyUnderParent(stack[len(stack)-1].node, text)
		}
		if node.role == "unknown" {
			s.diagnostics = append(s.diagnostics, Diagnostic{i + 1, indent + 1, "unknown construction: " + text})
		}
		fr := stack[len(stack)-1]
		*fr.children = append(*fr.children, node)
		stack = append(stack, frame{indent, node, &node.children})
	}
	return s
}

var (
	semanticSchemaFieldRe = regexp.MustCompile(`^\w+ as (?:optional )?(?:(?:list of )*(?:text|timestamp|number|integer|boolean|duration|[A-Z][A-Za-z0-9_]*))$`)
	semanticMakeFieldRe   = regexp.MustCompile(`^\w+ from .+$`)
	semanticChoiceRe      = regexp.MustCompile(`^(?:"[^"\n]+"|[0-9]+): ".*"$`)
)

// classifyUnderParent recognizes context-dependent member lines the way the
// canonical parser does: schema fields, make-with fields, and choice rows.
func classifyUnderParent(parent *semNode, text string) (string, string) {
	if parent == nil || parent.role != "canonical" {
		return "unknown", ""
	}
	switch parent.form {
	case "schema", "define", "failure":
		if semanticSchemaFieldRe.MatchString(text) {
			return "canonical", "field"
		}
	case "fail":
		if semanticMakeFieldRe.MatchString(text) {
			return "canonical", "field"
		}
	case "make":
		if strings.HasSuffix(parent.text, " with:") && semanticMakeFieldRe.MatchString(text) {
			return "canonical", "field"
		}
	case "classify", "score":
		if semanticChoiceRe.MatchString(text) {
			return "canonical", "choice"
		}
	}
	return "unknown", ""
}

// applyVocabulary promotes resolvable sentence calls from unknown to
// canonical, dropping their unknown-construction diagnostics so vocabulary
// never reaches the paid semantic engine. Nodes with non-handler children
// stay unknown.
func (s *semanticScan) applyVocabulary(v *fileVocab) {
	fixed := map[int]string{}
	var walk func([]*semNode)
	walk = func(nodes []*semNode) {
		for _, n := range nodes {
			childrenOK := true
			for _, c := range n.children {
				if c.role != "canonical" || c.form != "handler" {
					childrenOK = false
					break
				}
			}
			if n.role == "unknown" && childrenOK {
				if m := matchSent(n.text); m != nil {
					if mod, action, ok := v.resolveName(m[1]); ok {
						if want, known := sentArity(mod, action); known {
							if args, e := sentArguments(m); e == nil && len(args) == want {
								n.role, n.form, n.m = "canonical", "sent", m
								fixed[n.line.num] = n.text
							}
						}
					}
				}
			}
			walk(n.children)
		}
	}
	walk(s.nodes)
	s.diagnostics = dropReclassifiedDiagnostics(s.diagnostics, fixed)
}

// onlyCanonical reports whether every significant line is a canonical
// construction, enabling the byte-preserving fast path.
func (s *semanticScan) onlyCanonical() bool {
	var walk func(nodes []*semNode) bool
	walk = func(nodes []*semNode) bool {
		for _, n := range nodes {
			if n.role != "canonical" {
				return false
			}
			if !walk(n.children) {
				return false
			}
		}
		return true
	}
	return walk(s.nodes)
}

func classifySemantic(text string) (role, form string, m []string) {
	if m = matchRe(semanticCriterionDeclRe, text); m != nil {
		return "criterion", "criterion-decl", m
	}
	for _, f := range semanticForms {
		if m = matchSemantic(f.id, text); m != nil {
			return "semantic", f.id, m
		}
	}
	kind := classifyLine(text)
	if kind == "" {
		if semanticCouldInterpret(text) {
			return "semantic", "lexical", []string{text}
		}
		return "unknown", "", nil
	}
	return "canonical", kind, match(kind, text)
}

func isSemanticPronoun(word string) bool { return semanticPronouns[word] }

// commentTail returns a raw line's trailing comment (including '#') that
// stripComment would remove, so lowerings can preserve it.
func commentTail(s string) string {
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
			return s[i:]
		}
	}
	return ""
}

// semScope tracks collection bindings and declared criteria with block-local
// lifetime. Bindings made inside actions, loops, and branches never leak out.
type semScope struct {
	collections map[string]string
	criteria    map[string]*criterionDecl
	bindings    map[string]string
}

func newSemScope() *semScope {
	return &semScope{collections: map[string]string{}, criteria: map[string]*criterionDecl{}, bindings: map[string]string{}}
}

func (s *semScope) clone() *semScope {
	n := &semScope{collections: map[string]string{}, criteria: map[string]*criterionDecl{}, bindings: map[string]string{}}
	for k, v := range s.collections {
		n.collections[k] = v
	}
	for k, v := range s.criteria {
		n.criteria[k] = v
	}
	for k, v := range s.bindings {
		n.bindings[k] = v
	}
	return n
}

func (s *semScope) collectionNames() []string {
	names := make([]string, 0, len(s.collections))
	for name := range s.collections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// referentList resolves an explicit collection name or a bounded pronoun to
// the candidate referents. Explicit names pass through so the canonical
// checker remains the authority on unknown names; pronouns require visible
// collections and return all of them when several compete equally.
func (s *semScope) referentList(explicit, pron string) (names []string, problem string) {
	if explicit != "" {
		return []string{explicit}, ""
	}
	names = s.collectionNames()
	if len(names) == 0 {
		return nil, fmt.Sprintf("no visible collection for reference %q", pron)
	}
	return names, ""
}

// scopeForChildren decides whether a block introduces a local scope.
func scopeForChildren(kind string, scope *semScope) *semScope {
	switch kind {
	case "to", "for", "when", "otherwise", "while", "repeat", "command", "handler":
		return scope.clone()
	}
	return scope
}

// applyCanonicalEffects updates collection visibility for one canonical
// statement, mirroring the interpreter's binding behavior conservatively.
func applyCanonicalEffects(scope *semScope, n *semNode) {
	switch n.form {
	case "read":
		if len(n.m) >= 4 {
			if n.m[2] == "text" {
				delete(scope.collections, n.m[3])
			} else {
				scope.collections[n.m[3]] = "read " + n.m[1] + " as " + n.m[2]
			}
		}
	case "readEach":
		if len(n.m) >= 4 {
			scope.collections[n.m[3]] = "records read from each " + n.m[2]
		}
	case "find":
		if len(n.m) >= 4 {
			scope.collections[n.m[3]] = "files found under " + n.m[1]
		}
	case "make":
		if len(n.m) >= 3 {
			name := n.m[1]
			if typ := inferSemanticExprType(strings.TrimPrefix(n.m[2], "as ")); typ != "" {
				scope.bindings[name] = typ
			} else {
				delete(scope.bindings, name)
			}
			if isCollectionExpr(strings.TrimPrefix(n.m[2], "as "), scope) {
				scope.collections[name] = "value of " + n.m[2]
			} else {
				delete(scope.collections, name)
			}
		}
	case "keep":
		if len(n.m) >= 2 {
			scope.collections[n.m[1]] = "filtered by keep"
		}
	case "sort":
		if len(n.m) >= 2 {
			scope.collections[n.m[1]] = "sorted by " + n.m[2]
		}
	case "group":
		if len(n.m) >= 4 {
			scope.collections[n.m[3]] = "groups from " + n.m[1]
		}
	case "take":
		if len(n.m) >= 5 {
			scope.collections[n.m[4]] = "items taken from " + n.m[3]
		}
	case "call":
		if len(n.m) >= 4 && n.m[3] != "" {
			delete(scope.collections, n.m[3])
		}
	case "sent":
		if len(n.m) >= 5 && n.m[4] != "" {
			delete(scope.collections, n.m[4])
		}
	case "remember", "classify", "judge", "score", "evaluate":
		if len(n.m) >= 4 {
			delete(scope.collections, n.m[3])
		}
	}
}

// isCollectionExpr recognizes value shapes that are statically lists.
func isCollectionExpr(expr string, scope *semScope) bool {
	if strings.HasPrefix(expr, "[") {
		return true
	}
	if _, ok := scope.collections[expr]; ok {
		return true
	}
	for _, form := range []string{"first ", "last "} {
		if strings.HasPrefix(expr, form) && strings.Contains(expr, " items from ") {
			tail := strings.SplitN(expr, " items from ", 2)[1]
			if _, ok := scope.collections[strings.TrimSpace(tail)]; ok {
				return true
			}
		}
	}
	return false
}
