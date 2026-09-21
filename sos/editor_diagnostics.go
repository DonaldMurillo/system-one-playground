package sos

import (
	"path/filepath"
	"strings"
)

// EditorDiagnostic distinguishes errors from recognized sentences awaiting
// explicit semantic analysis. Computing these diagnostics never calls Jev.
type EditorDiagnostic struct {
	Diagnostic
	Severity string `json:"severity"`
}

// EditorDiagnostics preserves canonical errors, but marks registered semantic
// forms as pending interpretation when the effective language policy permits them.
func EditorDiagnostics(filename, source string, diagnostics []Diagnostic) []EditorDiagnostic {
	if canonical, resolveErr := filepath.EvalSymlinks(filename); resolveErr == nil {
		filename = canonical
	}
	cfg, err := EffectiveConfig(source, filepath.Dir(filename))
	meanings := EditorMeanings(source)
	pending := map[int]bool{}
	problems := map[int][]string{}
	if err == nil && cfg.Interpretation != "canonical" {
		var visit func([]*semNode)
		visit = func(nodes []*semNode) {
			for _, n := range nodes {
				if n.role == "semantic" {
					pending[n.line.num] = true
				}
				if n.role == "criterion" && cfg.Interpretation == "semantic" {
					d := parseCriterionDecl(n)
					if len(d.problems) == 0 {
						pending[n.line.num] = true
					} else {
						problems[n.line.num] = d.problems
					}
				}
				visit(n.children)
			}
		}
		visit(scanSemantic(source).nodes)
	}
	out := make([]EditorDiagnostic, 0, len(diagnostics))
	for _, d := range diagnostics {
		severity := "error"
		if strings.HasPrefix(d.Message, "unknown construction:") {
			if messages := problems[d.Line]; len(messages) > 0 {
				d.Message = strings.Join(messages, "; ")
			} else if pending[d.Line] {
				if cfg.Interpretation == "semantic" && meanings[d.Line].LocallyDefined && (meanings[d.Line].Head == "criterion" || cfg.Runtime == "semantic") {
					continue
				}
				severity = "information"
				d.Message = "Semantic phrase: interpretation pending. Analyze to inspect its canonical meaning."
			}
		}
		out = append(out, EditorDiagnostic{d, severity})
	}
	return out
}
