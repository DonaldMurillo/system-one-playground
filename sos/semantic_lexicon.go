package sos

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:generate go run ../cmd/semanticgen -input ../semantic/lexicon.json -output semantic_lexicon_generated.go

type semanticConcept struct {
	ID          string
	Phrases     []string
	Definitions []string
}

// SemanticMatch explains one lexical span used to retrieve an executable
// language definition. It is persisted with analysis decisions so editors can
// show what the dictionary contributed without exposing model chain-of-thought.
type SemanticMatch struct {
	Phrase     string `json:"phrase"`
	Concept    string `json:"concept"`
	Definition string `json:"definition"`
}

type semanticPhrase struct {
	phrase     string
	concept    string
	definition string
}

type semanticLanguageDefinition struct {
	id        string
	canonical string
	inputs    []string
}

var semanticDefinitions = map[string]semanticLanguageDefinition{
	"language.when":             {id: "language.when", canonical: "when", inputs: []string{"boolean", "statement"}},
	"language.show":             {id: "language.show", canonical: "show", inputs: []string{"any"}},
	"operator.greater_than":     {id: "operator.greater_than", canonical: ">", inputs: []string{"ordered", "ordered"}},
	"operator.greater_or_equal": {id: "operator.greater_or_equal", canonical: ">=", inputs: []string{"ordered", "ordered"}},
	"operator.less_than":        {id: "operator.less_than", canonical: "<", inputs: []string{"ordered", "ordered"}},
	"operator.less_or_equal":    {id: "operator.less_or_equal", canonical: "<=", inputs: []string{"ordered", "ordered"}},
}

var (
	semanticLexiconOnce sync.Once
	semanticByConcept   map[string][]semanticPhrase
)

func semanticLexiconIndex() map[string][]semanticPhrase {
	semanticLexiconOnce.Do(func() {
		semanticByConcept = make(map[string][]semanticPhrase, len(generatedSemanticConcepts))
		for _, concept := range generatedSemanticConcepts {
			for _, phrase := range concept.Phrases {
				phrase = strings.ToLower(strings.TrimSpace(phrase))
				for _, definition := range concept.Definitions {
					semanticByConcept[concept.ID] = append(semanticByConcept[concept.ID], semanticPhrase{
						phrase: phrase, concept: concept.ID, definition: definition,
					})
				}
			}
			sort.Slice(semanticByConcept[concept.ID], func(i, j int) bool {
				return len(semanticByConcept[concept.ID][i].phrase) > len(semanticByConcept[concept.ID][j].phrase)
			})
		}
	})
	return semanticByConcept
}

func semanticCouldInterpret(text string) bool {
	_, _, _, ok := splitInlineConditional(text)
	return ok
}

// lexicalCandidates aligns dictionary concepts to executable language
// definitions, then composes only host-known structures. Type pruning happens
// before a candidate can reach Jev.
func lexicalCandidates(text string, scope *semScope) ([]semCandidate, []string) {
	condition, action, matches, ok := splitInlineConditional(text)
	if !ok {
		return nil, []string{"no semantic dictionary construction matches this sentence"}
	}
	left, right, comparisons := splitSemanticComparison(condition)
	if len(comparisons) == 0 {
		return nil, []string{"the conditional does not contain a supported comparison meaning"}
	}
	if p := semanticExprProblem(left); p != "" {
		return nil, []string{p}
	}
	if p := semanticExprProblem(right); p != "" {
		return nil, []string{p}
	}
	if p := semanticExprProblem(action); p != "" {
		return nil, []string{p}
	}
	if typ := scope.bindings[strings.TrimSpace(left)]; typ != "" && !semanticOrderedType(typ) {
		return nil, []string{"comparison meaning requires an ordered value; " + strings.TrimSpace(left) + " is " + typ}
	}
	if typ := inferSemanticExprType(right); typ != "" && !semanticOrderedType(typ) {
		return nil, []string{"comparison meaning requires an ordered right value; received " + typ}
	}

	var candidates []semCandidate
	for _, comparison := range comparisons {
		definition, exists := semanticDefinitions[comparison.definition]
		if !exists || len(definition.inputs) != 2 {
			continue
		}
		candidateMatches := append([]SemanticMatch(nil), matches...)
		candidateMatches = append(candidateMatches, SemanticMatch{
			Phrase: comparison.phrase, Concept: comparison.concept, Definition: comparison.definition,
		})
		candidates = append(candidates, semCandidate{
			id:      "lexical-inline-conditional:" + strings.TrimPrefix(comparison.definition, "operator."),
			meaning: "execute the output action when " + strings.TrimSpace(left) + " is " + comparison.concept + " " + strings.TrimSpace(right),
			lines: []string{
				"when " + strings.TrimSpace(left) + " " + definition.canonical + " " + strings.TrimSpace(right) + ":",
				"show " + strings.TrimSpace(action),
			},
			matches: candidateMatches,
		})
	}
	if len(candidates) == 0 {
		return nil, []string{"dictionary meanings do not map to an available language definition"}
	}
	return candidates, nil
}

func splitInlineConditional(text string) (condition, action string, matches []SemanticMatch, ok bool) {
	index := semanticLexiconIndex()
	remaining := strings.TrimSpace(text)
	opener, found := matchSemanticPrefix(remaining, index["control.conditional"])
	if !found {
		return "", "", nil, false
	}
	remaining = strings.TrimSpace(remaining[len(opener.phrase):])
	position, output, found := findSemanticPhraseOutsideQuotes(remaining, index["output.show"])
	if !found {
		return "", "", nil, false
	}
	condition = strings.TrimSpace(remaining[:position])
	action = strings.TrimSpace(remaining[position+len(output.phrase):])
	if condition == "" || action == "" {
		return "", "", nil, false
	}
	matches = []SemanticMatch{
		{Phrase: opener.phrase, Concept: opener.concept, Definition: opener.definition},
		{Phrase: output.phrase, Concept: output.concept, Definition: output.definition},
	}
	return condition, action, matches, true
}

func splitSemanticComparison(condition string) (left, right string, matches []semanticPhrase) {
	concepts := []string{"comparison.greater_or_equal", "comparison.less_or_equal", "comparison.greater_than", "comparison.less_than"}
	index := semanticLexiconIndex()
	bestAt, bestLen := -1, -1
	for _, concept := range concepts {
		position, match, ok := findSemanticPhraseOutsideQuotes(condition, index[concept])
		if !ok {
			continue
		}
		if bestAt == -1 || position < bestAt || position == bestAt && len(match.phrase) > bestLen {
			bestAt, bestLen = position, len(match.phrase)
			left = strings.TrimSpace(condition[:position])
			right = strings.TrimSpace(condition[position+len(match.phrase):])
			matches = []semanticPhrase{match}
		} else if position == bestAt && len(match.phrase) == bestLen {
			matches = append(matches, match)
		}
	}
	if left == "" || right == "" {
		return "", "", nil
	}
	return left, right, matches
}

func matchSemanticPrefix(text string, phrases []semanticPhrase) (semanticPhrase, bool) {
	lower := strings.ToLower(text)
	for _, phrase := range phrases {
		if strings.HasPrefix(lower, phrase.phrase) && semanticBoundary(lower, len(phrase.phrase)) {
			return phrase, true
		}
	}
	return semanticPhrase{}, false
}

func findSemanticPhraseOutsideQuotes(text string, phrases []semanticPhrase) (int, semanticPhrase, bool) {
	lower := strings.ToLower(text)
	quoted, escaped := false, false
	for i := 0; i < len(lower); i++ {
		if escaped {
			escaped = false
			continue
		}
		if lower[i] == '\\' && quoted {
			escaped = true
			continue
		}
		if lower[i] == '"' {
			quoted = !quoted
			continue
		}
		if quoted || i > 0 && !semanticSpace(lower[i-1]) {
			continue
		}
		for _, phrase := range phrases {
			if strings.HasPrefix(lower[i:], phrase.phrase) && semanticBoundary(lower[i:], len(phrase.phrase)) {
				return i, phrase, true
			}
		}
	}
	return -1, semanticPhrase{}, false
}

func semanticBoundary(text string, end int) bool {
	return end == len(text) || semanticSpace(text[end])
}

func semanticSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func semanticOrderedType(typ string) bool {
	switch typ {
	case "integer", "number", "timestamp", "duration":
		return true
	}
	return false
}

func inferSemanticExprType(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "true" || expr == "false" {
		return "boolean"
	}
	if strings.HasPrefix(expr, "\"") && strings.HasSuffix(expr, "\"") {
		return "text"
	}
	if _, err := strconv.ParseInt(expr, 10, 64); err == nil {
		return "integer"
	}
	if _, err := strconv.ParseFloat(expr, 64); err == nil {
		return "number"
	}
	return ""
}
