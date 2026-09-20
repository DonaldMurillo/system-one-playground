package soslsp

import (
	"strings"
	"github.com/DonaldMurillo/system-one-playground/internal/sossyntax"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// Tokens keep their UTF-16 spans from our tolerant syntax engine. Semantic roles
// overlay that structure; the role matcher never sees a token inside a string.
var lexicalOnlyWords = func() map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.Fields("each in as into at most running collecting failures on called with where by from under named matching if missing numbered otherwise and or not of count first last items default choices is ascending descending existing off jev classify judge confidence value p_yes noul choice score true false null now") {
		words[w] = true
	}
	return words
}()

func syntaxTokens(tree *sossyntax.Document, sentHeads map[int]string) []lexToken {
	var result []lexToken
	lines := make([]string, len(tree.Lines))
	for i, line := range tree.Lines {
		lines[i] = line.Text
	}
	meanings := sos.EditorMeanings(strings.Join(lines, "\n"))
	for lineNo, line := range tree.Lines {
		trim := strings.TrimLeft(line.Text, " \t")
		indent := len(line.Text) - len(trim)
		roles := lineRoles(trim)
		head, hasHead := sentHeads[lineNo]
		headWord, headQualified := headSplit(head)
		firstIdent := true
		for _, token := range line.Tokens {
			kind := -1
			switch token.Kind {
			case "string", "incompleteString":
				kind = tokString
			case "comment":
				kind = tokComment
			case "number":
				kind = tokNumber
			case "identifier":
				kind = tokVariable
				if keywordWords[token.Text] {
					kind = tokKeyword
				}
				if lexicalOnlyWords[token.Text] {
					kind = -1
				}
				start := charToByte(line.Text, token.Start) - indent
				end := charToByte(line.Text, token.End) - indent
				for _, role := range roles {
					if role.start <= start && end <= role.end {
						kind = role.kind
						break
					}
				}
				// A sentence-call head resolving in the enabled vocabulary
				// highlights as a function: the leading word, and the word
				// after the qualifier dot.
				if kind == tokVariable && hasHead && firstIdent {
					kind = tokFunction
					firstIdent = false
				} else if kind == tokVariable && headQualified && token.Text == headWord {
					kind = tokFunction
				}
				if token.Kind == "identifier" && firstIdent && kind != tokFunction {
					firstIdent = false
				}
			}
			if info, ok := meanings[lineNo+1]; ok && token.Kind == "identifier" {
				if token.Start == byteToChar(line.Text, indent) {
					kind = tokKeyword
					if info.Kind == "Semantic phrase" {
						kind = tokSemanticPhrase
					}
				}
				// Only the criterion slot is a criterion, not similarly named operands.
				prefix := strings.Fields(strings.TrimSpace(line.Text[:charToByte(line.Text, token.Start)]))
				if info.Criterion == token.Text && (len(prefix) == 1 || len(prefix) == 2 && prefix[1] == "the") {
					kind = tokCriterion
				}
			}
			if kind >= 0 {
				result = append(result, lexToken{line: lineNo, start: token.Start, len: token.End - token.Start, kind: kind, text: token.Text})
			}
		}
	}
	return result
}

// headSplit splits a sentence head into its trailing word and whether it was
// alias-qualified.
func headSplit(head string) (word string, qualified bool) {
	if i := strings.LastIndex(head, "."); i >= 0 {
		return head[i+1:], true
	}
	return head, false
}
