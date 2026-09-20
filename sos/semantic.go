package sos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Semantic resolution versions. The registry version changes when candidate
// construction changes; the prompt version changes when the discrimination
// question or state shape changes. Both participate in the policy hash.
const (
	semanticAnalysisVersion = 5
	semanticRegistryVersion = "4"
	semanticPromptVersion   = "3"
	// semanticMinConfidence is the conservative acceptance policy for model
	// selections. It is evidence, not a correctness guarantee.
	semanticMinConfidence   = 0.8
	semanticDefaultRequests = 100
)

// AnalyzeOptions configures one semantic analysis. Budget is shared with the
// caller when supplied; otherwise a budget sized by Config.Requests is used.
type AnalyzeOptions struct {
	Config sosconfig.Effective `json:"config"`
	Budget *RequestBudget      `json:"budget"`
	Model  string              `json:"model"`
	Bucket BudgetBucket        `json:"bucket"`
	Saved  *Analysis           `json:"saved"`
	Locked bool                `json:"locked"`
	// Modules supplies the import graph resolved by LoadProgram so the
	// canonical checker can validate qualified calls. Nil rejects them.
	Modules *ModuleTable `json:"-"`
}

// Interpretation is one resolved noncanonical sentence (or criterion
// declaration) with its canonical lowering and selection provenance.
type Interpretation struct {
	Line        int             `json:"line"`
	Source      string          `json:"source"`
	Canonical   string          `json:"canonical"`
	Candidate   string          `json:"candidate"`
	Confidence  float64         `json:"confidence"`
	Method      string          `json:"method"`
	Explanation string          `json:"explanation"`
	Matches     []SemanticMatch `json:"matches,omitempty"`
	InputTokens int             `json:"input_tokens,omitempty"`
	UsageKnown  bool            `json:"usage_known,omitempty"`
}

// Analysis is a complete resolution: the canonical program, its map back to
// original source lines, per-sentence decisions, diagnostics, and usage.
// Canonical is empty on failure; diagnostics and usage are always reported.
type Analysis struct {
	Version         int              `json:"version"`
	RegistryVersion string           `json:"registry_version"`
	SourceHash      string           `json:"source_hash"`
	PolicyHash      string           `json:"policy_hash"`
	Model           string           `json:"model"`
	Canonical       string           `json:"canonical"`
	SourceMap       []int            `json:"source_map"`
	Decisions       []Interpretation `json:"decisions"`
	Diagnostics     []Diagnostic     `json:"diagnostics"`
	Usage           BudgetSnapshot   `json:"usage"`
}

// Analyze interprets SysOneScript source into a validated canonical program.
// It never executes user effects, never contacts the provider for
// canonical-only source, and consults Jev only to discriminate between
// structurally valid, competing candidates using the shared provider
// gateway. The canonical checker is the final authority on the lowered
// program; its diagnostics are remapped to original source lines.
func Analyze(ctx context.Context, source string, opts AnalyzeOptions) (*Analysis, error) {
	out := &Analysis{Version: semanticAnalysisVersion, RegistryVersion: semanticRegistryVersion, Decisions: []Interpretation{}, Diagnostics: []Diagnostic{}}
	policy, requests, err := semanticNormalizePolicy(opts.Config)
	if err != nil {
		out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, err.Error()})
		return out, fmt.Errorf("semantic analysis: %w", err)
	}
	budget := opts.Budget
	if budget == nil {
		if budget, err = NewRequestBudget(requests, nil); err != nil {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, err.Error()})
			return out, err
		}
	} else if requests < budget.Snapshot().TotalLimit {
		// The configured request ceiling is an intersection: a caller with a
		// stricter policy cannot spend a wider shared allowance.
		if err := budget.Constrain(requests); err != nil {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, err.Error()})
			return out, err
		}
	}
	bucket := opts.Bucket
	if bucket == "" {
		bucket = BudgetInterpretation
	}
	if !budgetBuckets[bucket] {
		err := fmt.Errorf("unknown budget bucket %q", bucket)
		out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, err.Error()})
		return out, err
	}
	// The configured timeout is a ceiling on the whole analysis, including
	// canonical fast paths: an expired deadline refuses before any work.
	if timeout := opts.Config.Timeout; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	a := &semanticAnalysis{ctx: ctx, opts: opts, policy: policy, budget: budget, bucket: bucket, edits: map[int]semanticEdit{}}
	defer func() { out.Usage = budget.Snapshot() }()
	if err := ctx.Err(); err != nil {
		out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, "analysis canceled: " + err.Error()})
		return out, err
	}
	if len(source) > 1<<20 {
		err := errors.New("source exceeds 1 MiB limit")
		out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, err.Error()})
		return out, err
	}
	if _, _, err := sosconfig.Extract(source); err != nil {
		var positioned *sosconfig.Error
		if errors.As(err, &positioned) {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{positioned.Line, positioned.Column, positioned.Message})
			return out, fmt.Errorf("line %d: %s", positioned.Line, positioned.Message)
		}
		out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, err.Error()})
		return out, err
	}
	out.SourceHash = semanticSourceHash(source)
	out.PolicyHash = semanticPolicyHash(policy)
	a.scan = scanSemantic(source)
	if opts.Modules != nil && opts.Modules.vocab != nil {
		// Resolvable sentence calls are canonical: they must never fall
		// through to the paid semantic engine.
		a.scan.applyVocabulary(opts.Modules.vocab)
	}
	out.Diagnostics = append(out.Diagnostics, a.scan.diagnostics...)

	if opts.Locked && opts.Saved == nil {
		err := errors.New("locked analysis requires a saved resolution")
		out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, err.Error()})
		return out, err
	}
	if opts.Saved != nil {
		reason := a.savedMismatch(source, opts.Saved)
		if reason == "" {
			res, problems := a.replaySaved(source, opts.Saved)
			if len(problems) == 0 {
				res.Usage = budget.Snapshot()
				return res, nil
			}
			reason = "saved resolution is invalid: " + strings.Join(problems, "; ")
		} else {
			reason = "saved resolution does not apply: " + reason
		}
		if opts.Locked {
			// Mismatches are rejected before any provider request.
			out.Diagnostics = append(out.Diagnostics, Diagnostic{1, 1, "locked analysis cannot reuse saved resolution: " + reason})
			return out, errors.New(out.Diagnostics[0].Message)
		}
	}

	// Fresh analysis. The model is resolved and recorded so saved runs can
	// prefer it over ambient environment drift.
	a.model = opts.Model
	if a.model == "" {
		a.model = providerModel(ctx, "")
	}
	out.Model = a.model

	if a.scan.onlyCanonical() && len(a.scan.diagnostics) == 0 {
		ds := checkSource(source, opts.Modules)
		if len(ds) == 0 {
			// Canonical-only fast path: the source is its own canonical
			// program, byte for byte, including headers and comments.
			out.Canonical = source
			out.SourceMap = identitySourceMap(len(a.scan.lines))
			return out, nil
		}
		out.Diagnostics = append(out.Diagnostics, ds...)
		return out, firstDiagnosticError(out.Diagnostics, nil)
	}

	a.resolve(a.scan.nodes, newSemScope())
	out.Decisions = a.decisions
	out.Diagnostics = append(out.Diagnostics, a.diagnostics...)
	if a.stopped || len(a.diagnostics) > 0 {
		sortSemanticDiagnostics(out.Diagnostics)
		return out, firstDiagnosticError(out.Diagnostics, a.failureCause)
	}
	canonical, sourceMap := a.assemble()
	ds := checkSource(canonical, opts.Modules)
	if len(ds) > 0 {
		out.Diagnostics = append(out.Diagnostics, remapDiagnostics(ds, sourceMap)...)
		sortSemanticDiagnostics(out.Diagnostics)
		return out, firstDiagnosticError(out.Diagnostics, a.failureCause)
	}
	out.Canonical = canonical
	out.SourceMap = sourceMap
	return out, nil
}

type semPolicy struct {
	interpretation string
	runtime        string
}

type semanticEdit struct {
	repl  []string
	mapTo int
}

type semanticAnalysis struct {
	ctx          context.Context
	opts         AnalyzeOptions
	policy       semPolicy
	budget       *RequestBudget
	bucket       BudgetBucket
	model        string
	scan         *semanticScan
	edits        map[int]semanticEdit
	decisions    []Interpretation
	diagnostics  []Diagnostic
	stopped      bool
	failureCause error
	// replay state
	replay        bool
	savedIdx      map[int]Interpretation
	covered       map[int]bool
	savedProblems []string
}

func (a *semanticAnalysis) diagAt(line int, format string, args ...any) {
	a.diagnostics = append(a.diagnostics, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
}

func (a *semanticAnalysis) savedProblem(format string, args ...any) {
	a.savedProblems = append(a.savedProblems, fmt.Sprintf(format, args...))
}

// resolve walks the statement tree, interpreting noncanonical sentences and
// tracking collection and criterion scope conservatively: bindings made in
// actions, loops, and branches never leak to following siblings.
func (a *semanticAnalysis) resolve(nodes []*semNode, scope *semScope) {
	for _, n := range nodes {
		if a.stopped {
			return
		}
		switch n.role {
		case "criterion":
			a.doCriterion(n, scope)
		case "semantic":
			a.doSemantic(n, scope)
		case "canonical":
			applyCanonicalEffects(scope, n)
			a.resolve(n.children, scopeForChildren(n.form, scope))
		}
	}
}

func (a *semanticAnalysis) doCriterion(n *semNode, scope *semScope) {
	if a.policy.interpretation != "semantic" {
		a.diagAt(n.line.num, "semantic criteria require interpretation mode semantic; interpretation mode is %q", a.policy.interpretation)
		return
	}
	decl := parseCriterionDecl(n)
	if _, dup := scope.criteria[decl.name]; dup {
		a.diagAt(n.line.num, "duplicate criterion %s", decl.name)
	}
	scope.criteria[decl.name] = decl
	for _, p := range decl.problems {
		a.diagAt(n.line.num, "%s", p)
	}
	if !decl.valid() {
		return
	}
	for _, num := range decl.lineNums {
		a.edits[num] = semanticEdit{repl: []string{""}, mapTo: num}
	}
	expected := strings.Join(make([]string, len(decl.lineNums)), "\n")
	if a.replay {
		decision, ok := a.savedIdx[n.line.num]
		if !ok {
			a.savedProblem("missing decision for line %d (%s)", n.line.num, n.text)
			return
		}
		if a.covered[n.line.num] {
			a.savedProblem("duplicate decision for line %d", n.line.num)
			return
		}
		a.covered[n.line.num] = true
		switch {
		case decision.Source != n.text:
			a.savedProblem("decision source text for line %d does not match the source", n.line.num)
		case decision.Method != "criterion":
			a.savedProblem("decision method %q for line %d must be %q", decision.Method, n.line.num, "criterion")
		case decision.Candidate != "criterion:"+decl.name:
			a.savedProblem("decision candidate %q is not valid for line %d", decision.Candidate, n.line.num)
		case decision.Canonical != expected:
			a.savedProblem("decision canonical text for line %d does not match the blank-line lowering", n.line.num)
		case decision.Confidence != 1:
			a.savedProblem("decision confidence for line %d must be 1 for criterion declarations", n.line.num)
		}
		return
	}
	a.decisions = append(a.decisions, Interpretation{
		Line:        n.line.num,
		Source:      n.text,
		Canonical:   expected,
		Candidate:   "criterion:" + decl.name,
		Confidence:  1,
		Method:      "criterion",
		Explanation: fmt.Sprintf("criterion %s declares a runtime judgment question and policy; the declaration lowers to blank lines and only its applications appear in the canonical program", decl.name),
	})
}

func (a *semanticAnalysis) doSemantic(n *semNode, scope *semScope) {
	cands, problems := a.candidatesFor(n, scope)
	for _, p := range problems {
		a.diagAt(n.line.num, "%s", p)
	}
	if a.policy.interpretation == "canonical" {
		if len(problems) > 0 {
			return
		}
		msg := "interpretation mode canonical requires canonical sentences"
		if len(cands) == 1 {
			msg += "; write: " + strings.Join(indentApply(n.line.indent, cands[0].lines), "\n")
		}
		a.diagAt(n.line.num, "%s", msg)
		return
	}
	if len(problems) > 0 || len(cands) == 0 {
		return
	}
	if !a.childrenSupported(n) {
		return
	}
	var (
		cand        semCandidate
		conf        = 1.0
		method      = "deterministic"
		explanation string
		inputTokens int
		usageKnown  bool
	)
	if a.replay {
		expected := "jev"
		if len(cands) == 1 && !cands[0].requiresJev {
			expected = "deterministic"
		}
		dec, ok := a.replayDecisionDetail(n, cands, expected)
		if !ok {
			return
		}
		for _, c := range cands {
			if c.id == dec.Candidate {
				cand = c
			}
		}
		conf = dec.Confidence
		method = dec.Method
		explanation = dec.Explanation
	} else if len(cands) == 1 && !cands[0].requiresJev {
		cand = cands[0]
	} else {
		before := a.budget.Snapshot().Buckets[a.bucket]
		var ok bool
		cand, conf, ok = a.chooseInterpretation(n, cands, scope)
		if !ok {
			return
		}
		after := a.budget.Snapshot().Buckets[a.bucket]
		inputTokens = after.ReportedInputTokens - before.ReportedInputTokens
		usageKnown = after.Unresolved == before.Unresolved && after.Requests == before.Requests+1
		method = "jev"
	}
	repl := indentApply(n.line.indent, cand.lines)
	if len(cand.lines) == 1 && cand.lines[0] == n.text {
		// The sentence is already canonical in effect (for example a
		// reference word that names a visible collection): keep the
		// original line bytes, comments and line endings included.
	} else {
		if tail := strings.TrimSpace(commentTail(a.scan.lines[n.line.num-1])); tail != "" {
			repl[0] += " " + tail
		}
		a.edits[n.line.num] = semanticEdit{repl: repl, mapTo: n.line.num}
	}
	if explanation == "" {
		explanation = a.explain(n, cand, conf, method, scope)
	}
	a.decisions = append(a.decisions, Interpretation{
		Line:        n.line.num,
		Source:      n.text,
		Canonical:   strings.Join(repl, "\n"),
		Candidate:   cand.id,
		Confidence:  conf,
		Method:      method,
		Explanation: explanation,
		Matches:     cand.matches,
		InputTokens: inputTokens,
		UsageKnown:  usageKnown,
	})
	applySemanticEffects(scope, n, cand)
}

func (a *semanticAnalysis) childrenSupported(n *semNode) bool {
	allowed := map[string]bool{"handler": true}
	if n.form == "keep-where-ref" && len(n.m) >= 3 && n.m[2] == "jev:" {
		allowed["ask"] = true
		allowed["using"] = true
		allowed["accept"] = true
		allowed["model"] = true
	}
	before := len(a.diagnostics)
	for _, c := range n.children {
		if c.role == "canonical" && allowed[c.form] {
			continue
		}
		a.diagAt(c.line.num, "unsupported indented construction under %s", n.text)
	}
	return len(a.diagnostics) == before
}

// chooseInterpretation asks the real provider gateway to select one of the
// constrained candidates, or to reject them all. A selection below the
// conservative confidence policy is refused.
func (a *semanticAnalysis) chooseInterpretation(n *semNode, cands []semCandidate, scope *semScope) (semCandidate, float64, bool) {
	labels := map[string]any{"reject": "None of the listed meanings matches the sentence."}
	views := make([]map[string]any, 0, len(cands))
	for _, c := range cands {
		labels[c.id] = maskSemanticStringLiterals(c.meaning)
		views = append(views, map[string]any{"id": c.id, "meaning": maskSemanticStringLiterals(c.meaning), "canonical": maskSemanticStringLiterals(strings.Join(indentApply(n.line.indent, c.lines), "\n")), "matches": c.matches})
	}
	visible := make([]map[string]any, 0, len(scope.collections))
	for _, name := range scope.collectionNames() {
		visible = append(visible, map[string]any{"name": name, "origin": scope.collections[name]})
	}
	state := map[string]any{
		"role":                "SysOneScript source interpretation",
		"sentence":            maskSemanticStringLiterals(n.text),
		"line":                n.line.num,
		"visible_collections": visible,
		"visible_bindings":    scope.bindings,
		"candidates":          views,
	}
	question := typesafe.Choice(semanticChoiceInstructions(), labels)
	description := fmt.Sprintf("interpret SysOneScript line %d: %s", n.line.num, n.text)
	evaluation, err := Evaluate(a.ctx, EvaluationRequest{
		State:       state,
		Question:    question,
		Model:       a.model,
		Budget:      a.budget,
		Bucket:      a.bucket,
		Line:        n.line.num,
		Description: description,
	})
	if err != nil {
		var budgetErr *BudgetError
		switch {
		case errors.As(err, &budgetErr):
			// Keep the typed cause so callers can distinguish refusal.
			a.failureCause = budgetErr
			a.diagAt(n.line.num, "interpretation not resolved: request budget is exhausted")
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			a.failureCause = err
			a.stopped = true
			a.diagAt(n.line.num, "interpretation canceled: %s", err.Error())
			return semCandidate{}, 0, false
		default:
			a.diagAt(n.line.num, "interpretation provider failed: %s", err.Error())
		}
		a.stopped = true
		return semCandidate{}, 0, false
	}
	if err := validateAnswerMap(evaluation.Answer, question); err != nil {
		a.diagAt(n.line.num, "interpretation answer invalid: %s", err.Error())
		a.stopped = true
		return semCandidate{}, 0, false
	}
	value, _ := evaluation.Answer["value"].(string)
	conf, confOK := number(evaluation.Answer["confidence"])
	if value == "reject" {
		a.diagAt(n.line.num, "Jev rejected every listed meaning of the sentence")
		a.stopped = true
		return semCandidate{}, 0, false
	}
	if !confOK || conf < semanticMinConfidence {
		a.diagAt(n.line.num, "interpretation confidence %g is below the policy minimum %g", conf, semanticMinConfidence)
		a.stopped = true
		return semCandidate{}, 0, false
	}
	for _, c := range cands {
		if c.id == value {
			return c, conf, true
		}
	}
	a.diagAt(n.line.num, "provider selected unknown candidate %q", value)
	a.stopped = true
	return semCandidate{}, 0, false
}

// maskSemanticStringLiterals keeps already-tokenized data out of the model's
// grammatical decision. The host retains the original candidate and restores
// it after selection; Jev sees only stable, typed placeholders.
func maskSemanticStringLiterals(text string) string {
	var out strings.Builder
	literal := 0
	for i := 0; i < len(text); {
		if text[i] != '"' {
			out.WriteByte(text[i])
			i++
			continue
		}
		literal++
		out.WriteString(fmt.Sprintf("<text-literal-%d>", literal))
		i++
		for i < len(text) {
			if text[i] == '\\' && i+1 < len(text) {
				i += 2
				continue
			}
			if text[i] == '"' {
				i++
				break
			}
			i++
		}
	}
	return out.String()
}

func semanticChoiceInstructions() string {
	return "You are interpreting one SysOneScript sentence. Select the single listed meaning that matches the sentence exactly. " +
		"Each option shows its retrieved dictionary matches, type-valid meaning, and canonical lowering. " +
		"Choose reject when none of the listed meanings matches, when the sentence is too ambiguous, or when it asks for something the options do not describe."
}

// assemble lowers the original source into the canonical program. Every
// original line either passes through unchanged or is replaced by the
// canonical lowering of its sentence; sourceMap maps each canonical line
// back to its owning original one-based line.
func (a *semanticAnalysis) assemble() (string, []int) {
	out := make([]string, 0, len(a.scan.lines))
	sourceMap := make([]int, 0, len(a.scan.lines))
	for i, raw := range a.scan.lines {
		if edit, ok := a.edits[i+1]; ok {
			for _, line := range edit.repl {
				out = append(out, line)
				sourceMap = append(sourceMap, edit.mapTo)
			}
			continue
		}
		out = append(out, raw)
		sourceMap = append(sourceMap, i+1)
	}
	text := strings.Join(out, "\n")
	if a.scan.trailingNL {
		text += "\n"
	}
	return text, sourceMap
}

func indentApply(indent int, lines []string) []string {
	out := make([]string, len(lines))
	pad := strings.Repeat(" ", indent)
	inner := strings.Repeat(" ", indent+2)
	for i, line := range lines {
		if i == 0 {
			out[i] = pad + line
		} else {
			out[i] = inner + line
		}
	}
	return out
}

func identitySourceMap(n int) []int {
	m := make([]int, n)
	for i := range m {
		m[i] = i + 1
	}
	return m
}

func remapDiagnostics(ds []Diagnostic, sourceMap []int) []Diagnostic {
	out := make([]Diagnostic, 0, len(ds))
	for _, d := range ds {
		line := 1
		if d.Line >= 1 && d.Line <= len(sourceMap) {
			line = sourceMap[d.Line-1]
		}
		out = append(out, Diagnostic{Line: line, Column: d.Column, Message: d.Message})
	}
	return out
}

func sortSemanticDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Line != ds[j].Line {
			return ds[i].Line < ds[j].Line
		}
		return ds[i].Column < ds[j].Column
	})
}

func firstDiagnosticError(ds []Diagnostic, cause error) error {
	if len(ds) == 0 {
		return nil
	}
	if cause != nil {
		return fmt.Errorf("line %d: %s: %w", ds[0].Line, ds[0].Message, cause)
	}
	return fmt.Errorf("line %d: %s", ds[0].Line, ds[0].Message)
}
func semanticSourceHash(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// semanticPolicyHash covers the identity of a resolution's meaning:
// language, registry, prompt, interpretation and runtime policy, and the
// selection confidence policy. Budgets, editor preferences, and origins are
// deliberately excluded so spending changes can reuse saved resolutions.
func semanticPolicyHash(p semPolicy) string {
	parts := []string{
		"sysonescript-semantic-policy-1",
		Version,
		semanticRegistryVersion,
		semanticPromptVersion,
		p.interpretation,
		p.runtime,
		strconv.FormatFloat(semanticMinConfidence, 'g', -1, 64),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func semanticNormalizePolicy(cfg sosconfig.Effective) (semPolicy, int, error) {
	p := semPolicy{interpretation: cfg.Interpretation, runtime: cfg.Runtime}
	requests := cfg.Requests
	unset := cfg.Editor == "" && cfg.Interpretation == "" && cfg.Runtime == "" && cfg.Requests == 0 && cfg.Timeout == 0 && len(cfg.Origins) == 0
	if p.interpretation == "" {
		p.interpretation = "canonical"
	}
	if p.runtime == "" {
		p.runtime = "explicit"
	}
	if !semanticInterpretationModes[p.interpretation] {
		return p, 0, fmt.Errorf("interpretation mode %q is not canonical, assisted, or semantic", p.interpretation)
	}
	if !semanticRuntimeModes[p.runtime] {
		return p, 0, fmt.Errorf("runtime judgment mode %q is not deny, explicit, or semantic", p.runtime)
	}
	if unset {
		requests = semanticDefaultRequests
	}
	if requests < 0 {
		return p, 0, fmt.Errorf("request ceiling %d is negative", requests)
	}
	return p, requests, nil
}

var semanticInterpretationModes = map[string]bool{"canonical": true, "assisted": true, "semantic": true}
var semanticRuntimeModes = map[string]bool{"deny": true, "explicit": true, "semantic": true}
