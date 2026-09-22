package sos

import (
	"fmt"
	"strings"
)

// semCandidate is one structurally valid meaning of a noncanonical
// sentence: a stable id, a human description, and the exact canonical
// lowering (unindented; the resolver applies source indentation).
type semCandidate struct {
	id      string
	meaning string
	lines   []string
	matches []SemanticMatch
	// requiresJev marks a structurally valid composition recovered from an
	// unfamiliar sentence shape. Jev must align the source to this bounded
	// candidate (or reject it), even when it is the only host-valid lowering.
	requiresJev bool
	// referent is the resolved collection name when the sentence operates
	// on one; result is a name the lowering binds.
	referent string
	result   string
}

// semanticExprProblem performs the pre-call structural check on expression
// slots: lexing and bracket balance. Unknown names remain the canonical
// checker's authority; this only refuses broken shapes before any request.
func semanticExprProblem(expr string) string {
	tokens, err := lex(expr)
	if err != nil {
		return err.Error()
	}
	if len(tokens) == 0 {
		return "expected expression"
	}
	var depth []string
	for _, t := range tokens {
		if t.quoted {
			continue
		}
		switch t.text {
		case "(", "[", "{":
			depth = append(depth, t.text)
		case ")", "]", "}":
			if len(depth) == 0 {
				return "unmatched " + t.text
			}
			open := depth[len(depth)-1]
			if (open == "(" && t.text != ")") || (open == "[" && t.text != "]") || (open == "{" && t.text != "}") {
				return "mismatched brackets"
			}
			depth = depth[:len(depth)-1]
		}
	}
	if len(depth) > 0 {
		return "unclosed bracket"
	}
	return ""
}

// candidatesFor builds the finite candidate set for one interpreted
// sentence from the current scope. Missing structural slots, invalid
// expressions, unknown criteria, and policy violations are returned as
// problems; they are diagnosed without provider requests.
func (a *semanticAnalysis) candidatesFor(n *semNode, scope *semScope) ([]semCandidate, []string) {
	var problems []string
	m := n.m
	verb := strings.Fields(n.text)[0]

	// A reference word that is itself the name of a visible collection is an
	// ordinary named binding, not a pronoun: it never triggers reference
	// resolution or a provider choice.
	referents := func(explicit, pron string) []string {
		if explicit == "" && pron != "" {
			if _, bound := scope.collections[pron]; bound {
				explicit = pron
			}
		}
		names, problem := scope.referentList(explicit, pron)
		if problem != "" {
			problems = append(problems, problem)
			return nil
		}
		return names
	}

	switch n.form {
	case "lexical":
		return lexicalCandidates(n.text, scope)

	case "keep-where", "keep-where-ref":
		expr := m[2]
		jevExpr := jevPredicate(expr)
		if expr != "jev:" {
			if p := semanticExprProblem(expr); p != "" {
				problems = append(problems, p)
				return nil, problems
			}
		}
		if jevExpr && (verb == "remove" || verb == "filter") {
			problems = append(problems, "a Jev predicate cannot be negated; use keep or retain")
			return nil, problems
		}
		dirs := []string{"keep-where"}
		if verb == "remove" {
			dirs = []string{"keep-where-not"}
		} else if verb == "filter" {
			dirs = []string{"keep-where", "keep-where-not"}
		}
		var names []string
		if n.form == "keep-where" {
			names = referents(m[1], "")
		} else {
			names = referents("", m[1])
		}
		if names == nil {
			return nil, problems
		}
		var cands []semCandidate
		for _, name := range names {
			for _, dir := range dirs {
				line := "keep " + name + " where " + expr
				meaning := fmt.Sprintf("keep only items of %s where %s", name, expr)
				if dir == "keep-where-not" {
					line = "keep " + name + " where not (" + expr + ")"
					meaning = fmt.Sprintf("remove items of %s where %s, keeping the rest", name, expr)
				}
				cands = append(cands, semCandidate{id: dir + ":" + name, meaning: meaning, lines: []string{line}, referent: name})
			}
		}
		return cands, problems

	case "order", "order-ref", "sort-ref", "sort-suffix":
		var field, dir string
		var names []string
		switch n.form {
		case "sort-suffix":
			field, dir = m[2], m[3]+" first"
			names = referents(m[1], "")
		case "order":
			field, dir = m[2], m[3]
			names = referents(m[1], "")
		default:
			field, dir = m[2], m[3]
			names = referents("", m[1])
		}
		if names == nil {
			return nil, problems
		}
		if p := semanticExprProblem(field); p != "" {
			problems = append(problems, p)
			return nil, problems
		}
		op := "sort-ascending"
		if dir == "descending" || dir == "highest first" {
			op = "sort-descending"
		}
		var cands []semCandidate
		for _, name := range names {
			line := "sort " + name + " by " + field
			meaning := fmt.Sprintf("order %s by %s, lowest or earliest values first", name, field)
			if op == "sort-descending" {
				line += " descending"
				meaning = fmt.Sprintf("order %s by %s, highest or latest values first", name, field)
			} else {
				line += " ascending"
			}
			cands = append(cands, semCandidate{id: op + ":" + name, meaning: meaning, lines: []string{line}, referent: name})
		}
		return cands, problems

	case "collect", "collect-ref", "group-ref":
		var names []string
		if n.form == "collect" {
			names = referents(m[1], "")
		} else {
			names = referents("", m[1])
		}
		if names == nil {
			return nil, problems
		}
		if p := semanticExprProblem(m[2]); p != "" {
			problems = append(problems, p)
			return nil, problems
		}
		var cands []semCandidate
		for _, name := range names {
			line := "group " + name + " by " + m[2] + " called " + m[3]
			cands = append(cands, semCandidate{
				id:       "group-by:" + name,
				meaning:  fmt.Sprintf("group %s into records of {key, items} keyed by %s, result named %s", name, m[2], m[3]),
				lines:    []string{line},
				referent: name,
				result:   m[3],
			})
		}
		return cands, problems

	case "read-load", "read-into":
		op := "read-json"
		representation := "JSON data"
		if m[2] == "text" {
			op = "read-text"
			representation = "plain text (not a collection)"
		} else if m[2] == "lines of json" {
			op = "read-lines"
			representation = "JSON lines"
		}
		line := "read " + m[1] + " as " + m[2] + " called " + m[3]
		return []semCandidate{{
			id:      op,
			meaning: fmt.Sprintf("read file %s as %s, result named %s (%s)", m[1], m[2], m[3], representation),
			lines:   []string{line},
			result:  m[3],
		}}, problems

	case "save-write", "save-store":
		if p := semanticExprProblem(m[1]); p != "" {
			problems = append(problems, p)
			return nil, problems
		}
		line := "save " + m[1] + " as " + m[2] + " in " + m[3]
		return []semCandidate{{
			id:      "save-in",
			meaning: fmt.Sprintf("write %s as %s to file %s, creating it exclusively", m[1], m[2], m[3]),
			lines:   []string{line},
		}}, problems

	case "output-table":
		return []semCandidate{{
			id:      "output-table",
			meaning: "display the requested table using the canonical show operation",
			lines:   []string{"show " + m[1]},
		}}, problems

	case "keep-criterion-name", "keep-criterion-ones":
		if a.policy.interpretation != "semantic" {
			problems = append(problems, fmt.Sprintf("semantic criteria require interpretation mode semantic; interpretation mode is %q", a.policy.interpretation))
			return nil, problems
		}
		if a.policy.runtime != "semantic" {
			problems = append(problems, fmt.Sprintf("applying criterion %q lowers to a runtime Jev judgment; runtime judgment mode is %q", m[1], a.policy.runtime))
			return nil, problems
		}
		decl, ok := scope.criteria[m[1]]
		if !ok {
			problems = append(problems, fmt.Sprintf("undeclared criterion %q; declare it with 'criterion %s:' before use", m[1], m[1]))
			return nil, problems
		}
		if !decl.valid() {
			return nil, problems
		}
		var names []string
		if n.form == "keep-criterion-name" {
			if _, visible := scope.collections[m[2]]; !visible {
				problems = append(problems, fmt.Sprintf("criterion application requires a visible collection; %q is not one", m[2]))
				return nil, problems
			}
			names = []string{m[2]}
		} else {
			names = referents("", "ones")
		}
		if names == nil {
			return nil, problems
		}
		var cands []semCandidate
		for _, name := range names {
			cands = append(cands, semCandidate{
				id:       "keep-criterion:" + name,
				meaning:  fmt.Sprintf("keep items of %s matching declared criterion %q (%s)", name, m[1], decl.ask),
				lines:    decl.lowering(name),
				referent: name,
			})
		}
		return cands, problems
	}
	problems = append(problems, "unsupported construction: "+n.text)
	return nil, problems
}

// applySemanticEffects updates collection visibility after an interpreted
// sentence resolves, mirroring its canonical lowering.
func applySemanticEffects(scope *semScope, n *semNode, cand semCandidate) {
	switch {
	case strings.HasPrefix(cand.id, "keep-where"):
		scope.collections[cand.referent] = "filtered by keep"
	case strings.HasPrefix(cand.id, "sort-"):
		scope.collections[cand.referent] = "sorted collection"
	case strings.HasPrefix(cand.id, "group-by"):
		scope.collections[cand.result] = "groups from " + cand.referent
	case cand.id == "read-json" || cand.id == "read-lines":
		scope.collections[cand.result] = "read from file"
	case cand.id == "read-text":
		delete(scope.collections, cand.result)
	case strings.HasPrefix(cand.id, "keep-criterion"):
		scope.collections[cand.referent] = "filtered by declared criterion"
	}
}

// explain states the operation, bindings, and why Jev was or was not
// consulted, generated from the selected construction rather than model
// prose.
func (a *semanticAnalysis) explain(n *semNode, cand semCandidate, conf float64, method string, scope *semScope) string {
	verb := strings.Fields(n.text)[0]
	var subject string
	if cand.referent != "" {
		if origin, ok := scope.collections[cand.referent]; ok && method == "deterministic" && isSemanticPronoun(firstReference(n)) {
			subject = fmt.Sprintf("reference %q binds to the visible collection %s (%s)", firstReference(n), cand.referent, origin)
		} else if origin, ok := scope.collections[cand.referent]; ok {
			subject = fmt.Sprintf("collection %s (%s)", cand.referent, origin)
		} else {
			subject = "collection " + cand.referent
		}
	}
	switch {
	case strings.HasPrefix(cand.id, "lexical-"):
		if method == "memoized" {
			return fmt.Sprintf("high-confidence Jev interpretation reused from the structural cache (confidence %g, cache minimum %g); no provider request was made", conf, semanticMemoMinConfidence)
		}
		if method == "jev" {
			return fmt.Sprintf("dictionary meanings retrieved known language definitions; type context pruned invalid meanings and Jev selected the remaining interpretation (confidence %g, policy minimum %g)", conf, semanticMinConfidence)
		}
		return "dictionary meanings matched one type-valid language definition; no Jev request was needed"
	case strings.HasPrefix(cand.id, "keep-criterion"):
		return fmt.Sprintf("adjective applies the declared criterion; %s; the lowering embeds the exact question, fields, threshold, and uncertainty policy", subject)
	case strings.HasPrefix(cand.id, "keep-where-not"):
		if method == "jev" {
			return fmt.Sprintf("verb %q leaves keep-or-remove open; Jev selected removal (%s; confidence %g, policy minimum %g)", verb, subject, conf, semanticMinConfidence)
		}
		return fmt.Sprintf("verb %q means removal; %s", verb, subject)
	case strings.HasPrefix(cand.id, "keep-where"):
		if method == "jev" && verb == "filter" {
			return fmt.Sprintf("verb %q leaves keep-or-remove open; Jev selected keeping (%s; confidence %g, policy minimum %g)", verb, subject, conf, semanticMinConfidence)
		}
		if method == "jev" {
			return fmt.Sprintf("competing references were resolved by Jev (%s; confidence %g, policy minimum %g)", subject, conf, semanticMinConfidence)
		}
		return fmt.Sprintf("verb %q lowers to canonical keep; %s", verb, subject)
	case strings.HasPrefix(cand.id, "sort-"):
		return fmt.Sprintf("verb %q lowers to canonical sort; %s", verb, subject)
	case strings.HasPrefix(cand.id, "group-by"):
		return fmt.Sprintf("verb %q lowers to canonical group; %s", verb, subject)
	case strings.HasPrefix(cand.id, "read-"):
		return fmt.Sprintf("verb %q lowers to canonical read; path, representation, and result name are explicit", verb)
	case cand.id == "save-in":
		return fmt.Sprintf("verb %q lowers to canonical save; format and destination are explicit; no overwrite policy is invented", verb)
	case cand.id == "output-table":
		return fmt.Sprintf("verb %q lowers to canonical show; the output expression and presentation are preserved", verb)
	}
	return "resolved to " + cand.id
}

// firstReference extracts the reference word of a sentence, if any.
func firstReference(n *semNode) string {
	fields := strings.Fields(n.text)
	for _, f := range fields {
		if isSemanticPronoun(f) {
			return f
		}
	}
	return ""
}
