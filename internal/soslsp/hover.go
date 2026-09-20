package soslsp

import (
	"encoding/json"
	"fmt"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// hover combines a precise lexical selection with its containing sentence.
// Token spans come from the same tolerant parser used for highlighting.
func (s *server) hover(params json.RawMessage) any {
	uri, text, pos, ok := s.documentAt(params)
	if !ok {
		return nil
	}
	tree := s.docs[uri].syntaxTree()
	if pos.Line < 0 || pos.Line >= len(tree.Lines) {
		return nil
	}
	line := tree.Lines[pos.Line]
	for _, token := range line.Tokens {
		if pos.Character < token.Start || pos.Character >= token.End {
			continue
		}
		role := token.Kind
		detail := ""
		if token.Kind == "identifier" {
			role = "name"
			for _, t := range syntaxTokens(tree, s.sentHeads(uri, text)) {
				if t.line == pos.Line && t.start == token.Start {
					role = tokenLegend[t.kind]
					break
				}
			}
			if info, exists := sos.EditorMeanings(text)[pos.Line+1]; exists {
				if role == "macro" || token.Text == info.Head && token.Start == byteToChar(line.Text, len(line.Text)-len(strings.TrimLeft(line.Text, " \t"))) {
					role = info.Kind
					detail = info.Description
				}
				if role == "enumMember" {
					role = "Defined criterion"
					detail = "References `" + info.Criterion + "`. Its declaration supplies the runtime question and decision policy."
				}
			}
			switch token.Text {
			case "called":
				role = "result binding"
				detail = "Gives the result of this sentence a name."
			case "with":
				role = "argument connector"
				detail = "Introduces the inputs passed to the action."
			case "as":
				role = "modifier"
				detail = "Specifies an alias, type, or representation according to this sentence."
			}
			head := sentHead(strings.TrimSpace(stripLineComment(line.Text)))
			if target := resolveSent(head, s.wordTargets(uri, vocabFilename(uri, s.workspaceRoot), text)); target != nil {
				headStart := byteToChar(line.Text, len(line.Text)-len(strings.TrimLeft(line.Text, " \t")))
				if token.Start < headStart+len(head) {
					role = "action"
					detail = fmt.Sprintf("`%s` from `%s`.\n\n%s", entrySignature(target.Name, target.Params, target.Result), target.ImportPath, target.Doc)
					if strings.Contains(head, ".") && token.Text == strings.SplitN(head, ".", 2)[0] {
						role = "library parent"
						detail = fmt.Sprintf("Qualifies words from `%s`.", target.ImportPath)
					}
				}
			}
			if role == "variable" || role == "parameter" || role == "name" {
				// Search only preceding declarations in an enclosing indentation scope.
				ceiling := line.Indent
				for i := pos.Line; i >= 0; i-- {
					candidate := tree.Lines[i]
					code := strings.TrimSpace(stripLineComment(candidate.Text))
					if code == "" {
						continue
					}
					if candidate.Indent > ceiling {
						continue
					}
					ceiling = candidate.Indent
					found := false
					for _, pat := range bindPatterns {
						match := pat.FindStringSubmatchIndex(code)
						if match == nil || code[match[2]:match[3]] != token.Text {
							continue
						}
						start := byteToChar(candidate.Text, len(candidate.Text)-len(strings.TrimLeft(candidate.Text, " \t"))+match[2])
						if i == pos.Line && start > token.Start {
							continue
						}
						detail = fmt.Sprintf("Declared on line %d:\n\n```sos\n%s\n```", i+1, code)
						if i == pos.Line && start == token.Start {
							role = "binding"
						}
						found = true
						break
					}
					if found {
						break
					}
				}
			}
		} else {
			switch token.Kind {
			case "string":
				detail = "A literal text value."
			case "incompleteString":
				detail = "A text literal that still needs its closing quote."
			case "number":
				detail = "A literal numeric value."
			case "comment":
				detail = "Documentation for readers; this text is not executed."
			}
		}
		value := "**Selected · " + role + "**\n\n```sos\n" + token.Text + "\n```"
		if detail != "" {
			value += "\n\n" + detail
		}
		if sentence, ok := s.sentenceHover(params).(map[string]any); ok {
			contents := sentence["contents"].(map[string]any)
			value += "\n\n---\n\n**Sentence**\n\n" + contents["value"].(string)
		}
		return map[string]any{"contents": map[string]any{"kind": "markdown", "value": value}, "range": lspRange{Start: lspPosition{Line: pos.Line, Character: token.Start}, End: lspPosition{Line: pos.Line, Character: token.End}}}
	}
	return s.sentenceHover(params)
}
