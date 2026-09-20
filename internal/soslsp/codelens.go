package soslsp

import (
	"encoding/json"
	"sort"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

func (s *server) codeLens(params json.RawMessage) any {
	var p textDocumentOnly
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	doc := s.docs[p.TextDocument.URI]
	if doc == nil {
		return nil
	}
	meanings := sos.EditorMeanings(doc.text)
	lines := make([]int, 0, len(meanings))
	for line := range meanings {
		lines = append(lines, line)
	}
	sort.Ints(lines)
	out := []map[string]any{}
	for _, line := range lines {
		info := meanings[line]
		title := "Semantic phrase · Analyze meaning"
		if info.Criterion != "" {
			if info.Head == "criterion" {
				title = "Defines criterion '" + info.Criterion + "' · Jev evaluates at runtime"
			} else {
				title = "Uses criterion '" + info.Criterion + "' · Jev at runtime"
			}
		}
		out = append(out, map[string]any{"range": lspRange{Start: lspPosition{Line: line - 1}, End: lspPosition{Line: line - 1}}, "command": map[string]any{"title": title, "command": "sos.analyze"}})
	}
	return out
}
