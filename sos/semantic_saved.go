package sos

import (
	"fmt"
	"strings"
)

// savedMismatch checks the identity of a saved resolution against the
// current request: analysis and registry versions, source hash, semantic
// policy hash, and model. An empty model request prefers the saved model so
// ambient environment drift cannot invalidate reuse. Budgets, editor
// preferences, and origins are not part of the identity.
func (a *semanticAnalysis) savedMismatch(source string, saved *Analysis) string {
	if saved == nil {
		return "no saved resolution supplied"
	}
	if saved.Version != semanticAnalysisVersion {
		return fmt.Sprintf("saved analysis version %d is not %d", saved.Version, semanticAnalysisVersion)
	}
	if saved.RegistryVersion != semanticRegistryVersion {
		return fmt.Sprintf("saved registry version %q is not %q", saved.RegistryVersion, semanticRegistryVersion)
	}
	if saved.SourceHash != semanticSourceHash(source) {
		return "source changed since the saved analysis"
	}
	if saved.PolicyHash != semanticPolicyHash(a.policy) {
		return "semantic policy changed since the saved analysis"
	}
	if saved.Model == "" {
		return "saved analysis records no model"
	}
	requested := a.opts.Model
	if requested == "" {
		requested = saved.Model
	}
	if requested != saved.Model {
		return fmt.Sprintf("requested model %q does not match saved model %q", requested, saved.Model)
	}
	if len(saved.Diagnostics) > 0 {
		return "saved analysis recorded diagnostics"
	}
	return ""
}

// replaySaved reconstructs the canonical program from source plus the saved
// decision selections, with no provider requests. Candidate ids, canonical
// lowerings, methods, confidences, the canonical program, and the source
// map are all recomputed and must match the saved artifact exactly, so an
// edited saved file cannot inject different canonical code.
func (a *semanticAnalysis) replaySaved(source string, saved *Analysis) (*Analysis, []string) {
	// Replay runs on isolated state: a failed validation never leaks into a
	// fresh analysis fallback.
	replay := &semanticAnalysis{
		ctx:      a.ctx,
		opts:     a.opts,
		policy:   a.policy,
		budget:   a.budget,
		bucket:   a.bucket,
		model:    saved.Model,
		scan:     a.scan,
		edits:    map[int]semanticEdit{},
		replay:   true,
		savedIdx: map[int]Interpretation{},
		covered:  map[int]bool{},
	}
	for _, d := range saved.Decisions {
		if _, dup := replay.savedIdx[d.Line]; dup {
			return nil, []string{fmt.Sprintf("duplicate decision for line %d", d.Line)}
		}
		replay.savedIdx[d.Line] = d
	}
	replay.resolve(replay.scan.nodes, newSemScope())
	problems := replay.savedProblems
	for _, d := range saved.Decisions {
		if !replay.covered[d.Line] {
			problems = append(problems, fmt.Sprintf("unused decision for line %d", d.Line))
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	canonical, sourceMap := replay.assemble()
	if ds := checkSource(canonical, a.opts.Modules); len(ds) > 0 {
		return nil, []string{"reconstructed program does not check cleanly: line " + fmt.Sprint(ds[0].Line) + ": " + ds[0].Message}
	}
	if canonical != saved.Canonical {
		return nil, []string{"saved canonical program does not match the reconstructed lowering"}
	}
	for _, v := range saved.SourceMap {
		if v < 1 || v > len(a.scan.lines) {
			return nil, []string{fmt.Sprintf("saved source map entry %d is outside the source", v)}
		}
	}
	if len(saved.SourceMap) != len(sourceMap) {
		return nil, []string{fmt.Sprintf("saved source map has %d entries but the reconstruction has %d", len(saved.SourceMap), len(sourceMap))}
	}
	for i, v := range saved.SourceMap {
		if v != sourceMap[i] {
			return nil, []string{fmt.Sprintf("saved source map entry %d does not match the reconstruction", i+1)}
		}
	}
	return &Analysis{
		Version:         semanticAnalysisVersion,
		RegistryVersion: semanticRegistryVersion,
		SourceHash:      semanticSourceHash(source),
		PolicyHash:      semanticPolicyHash(a.policy),
		Model:           saved.Model,
		Canonical:       canonical,
		SourceMap:       sourceMap,
		Decisions:       saved.Decisions,
		Diagnostics:     []Diagnostic{},
	}, nil
}

// replayDecisionDetail validates the saved decision for one interpreted
// sentence: it must exist for exactly this line, select a currently valid
// candidate, record that candidate's exact canonical lowering, carry the
// method this site requires, and a confidence consistent with policy.
func (a *semanticAnalysis) replayDecisionDetail(n *semNode, cands []semCandidate, method string) (Interpretation, bool) {
	decision, ok := a.savedIdx[n.line.num]
	if !ok {
		a.savedProblem("missing decision for line %d (%s)", n.line.num, n.text)
		return Interpretation{}, false
	}
	if a.covered[n.line.num] {
		a.savedProblem("duplicate decision for line %d", n.line.num)
		return Interpretation{}, false
	}
	a.covered[n.line.num] = true
	if decision.Source != n.text {
		a.savedProblem("decision source text for line %d does not match the source", n.line.num)
		return Interpretation{}, false
	}
	if decision.Method != method {
		a.savedProblem("decision method %q for line %d must be %q", decision.Method, n.line.num, method)
		return Interpretation{}, false
	}
	var cand semCandidate
	for _, c := range cands {
		if c.id == decision.Candidate {
			cand = c
		}
	}
	if cand.id == "" {
		a.savedProblem("decision candidate %q is not valid for line %d", decision.Candidate, n.line.num)
		return Interpretation{}, false
	}
	expected := strings.Join(indentApply(n.line.indent, cand.lines), "\n")
	if decision.Canonical != expected {
		a.savedProblem("decision canonical text for line %d does not match candidate %s", n.line.num, cand.id)
		return Interpretation{}, false
	}
	if method == "jev" {
		if decision.Confidence < semanticMinConfidence || decision.Confidence > 1 {
			a.savedProblem("decision confidence for line %d is below the policy minimum %g or invalid", n.line.num, semanticMinConfidence)
			return Interpretation{}, false
		}
	} else if decision.Confidence != 1 {
		a.savedProblem("decision confidence for line %d must be 1 for %s resolutions", n.line.num, method)
		return Interpretation{}, false
	}
	return decision, true
}
