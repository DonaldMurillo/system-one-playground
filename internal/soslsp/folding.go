package soslsp

import (
	"encoding/json"
	"github.com/DonaldMurillo/system-one-playground/internal/sossyntax"
)

// foldingRanges uses the same tolerant SOS syntax engine as editor tokenization.
// It works while a sentence is unfinished; quoted/comment colons never open blocks.
func (s *server) foldingRanges(params json.RawMessage) any {
	uri, _, _, ok := s.documentAt(params)
	if !ok {
		return nil
	}
	tree := s.docs[uri].syntaxTree()
	ranges := make([]map[string]any, 0, len(tree.Blocks))
	for _, block := range tree.Blocks {
		ranges = append(ranges, map[string]any{"startLine": block.Start, "endLine": block.End, "kind": "region"})
	}
	return ranges
}

func (d *document) syntaxTree() *sossyntax.Document {
	if d.syntax == nil || d.syntaxText != d.text {
		d.syntax = sossyntax.Update(d.syntax, d.text)
		d.syntaxText = d.text
	}
	return d.syntax
}
