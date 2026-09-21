package soslsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func interpretationScope(dir string) string {
	scope, err := filepath.Abs(dir)
	if err != nil || dir == "" {
		scope = filepath.Clean(dir)
	}
	return scope
}

// On-demand semantic analysis, shared by the custom `sos/analyze` stdio
// method and the Studio /api/analyze route. Nothing here runs automatically:
// every provider request is behind an explicit user action, the editor
// assistance policy is enforced before the core is called, and every request
// is admitted through a BudgetEditor-bucketed RequestBudget.

const (
	// analyzeTimeout caps one explicit analysis, mirroring the studio run
	// default. Clients may cancel sooner; the server never runs longer.
	analyzeTimeout = 30 * time.Second
	// analyzeEditorCap bounds the requests one explicit analysis may spend,
	// intersected with the effective configured request ceiling.
	analyzeEditorCap = 32
	// maxAnalyzeBytes bounds the source accepted for analysis.
	maxAnalyzeBytes = 1 << 20

	// codeAnalyzeFailed reports a refused or failed sos/analyze request.
	codeAnalyzeFailed = -32001
)

// AnalyzeError kinds, reused by every transport so clients see one taxonomy.
const (
	KindConfig   = "config"   // configuration could not be resolved
	KindEditor   = "editor"   // editor assistance is off; nothing was requested
	KindBudget   = "budget"   // the request ceiling is exhausted
	KindCanceled = "canceled" // the caller went away
	KindTimeout  = "timeout"  // the analysis deadline passed
	KindAnalysis = "analysis" // the analysis itself failed
)

// AnalyzeError classifies a refused or failed analysis request. Kind is one
// of the Kind constants above; Err carries the underlying cause, if any.
type AnalyzeError struct {
	Kind     string
	Message  string
	Err      error
	Analysis *sos.Analysis
}

func (e *AnalyzeError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "analysis failed"
}

func (e *AnalyzeError) Unwrap() error { return e.Err }

// AnalyzeRequest is one explicit on-demand analysis. Dir is the
// configuration root; empty uses the process working directory. Budget is
// optional: nil builds a fresh budget capped at analyzeEditorCap and the
// effective configured request ceiling. Saved, when set, is validated for
// reuse before any fresh resolution work.
type AnalyzeRequest struct {
	Filename string
	// A non-nil path limits analysis to one leaf; nil analyzes the whole document.
	CommandPath []string
	Args        map[string]any
	Source      string
	Dir         string
	Budget      *sos.RequestBudget
	Saved       *sos.Analysis
	Cache       *sos.InterpretationCache
}

// AnalyzeResult reports the analysis plus how it was obtained. Promoted is
// true when the effective interpretation mode was canonical and was promoted
// to assisted for this analysis only: such results are suggestions and never
// an executable compilation. Reused is true when the saved analysis
// validated without new resolution work.
type AnalyzeResult struct {
	Analysis *sos.Analysis
	Promoted bool
	Reused   bool
}

// Analyze runs one explicit on-demand analysis under the effective editor
// policy. It returns before any provider request when assistance is off.
func Analyze(ctx context.Context, req AnalyzeRequest) (*AnalyzeResult, error) {
	cacheScope := req.Dir
	if req.Cache == nil {
		req.Cache = sos.NewInterpretationCache(1024, 24*time.Hour)
	}
	cacheScope = interpretationScope(cacheScope)
	if len(req.Source) > maxAnalyzeBytes {
		return nil, &AnalyzeError{Kind: KindConfig, Message: "source exceeds 1 MiB limit"}
	}
	filename := req.Filename
	if filename == "" {
		filename = filepath.Join(req.Dir, "buffer.sos")
	}
	program, _ := sos.LoadProgram(filename, req.Source)
	if program == nil {
		return nil, &AnalyzeError{Kind: KindConfig, Message: "source did not parse"}
	}
	if req.CommandPath != nil {
		if program == nil {
			return nil, &AnalyzeError{Kind: KindConfig, Message: "source did not parse"}
		}
		if _, err := sos.ValidateCommandInputs(program, req.CommandPath, req.Args); err != nil {
			return nil, &AnalyzeError{Kind: KindConfig, Err: err}
		}
		selected, err := sos.SelectedSource(program, req.CommandPath)
		if err != nil {
			return nil, &AnalyzeError{Kind: KindConfig, Err: err}
		}
		req.Source = selected
	}
	cfg, err := sos.EffectiveConfig(req.Source, req.Dir)
	if err != nil {
		return nil, &AnalyzeError{Kind: KindConfig, Err: err}
	}
	if cfg.Editor == "off" {
		return nil, &AnalyzeError{
			Kind:    KindEditor,
			Message: "editor assistance is off in configuration; no analysis was requested from the provider",
		}
	}

	// Analysis-only promotion: a canonical policy may still yield editorial
	// suggestions. The source and runtime configuration are never changed,
	// and the result is marked so callers treat it as non-executable.
	promoted := cfg.Interpretation == "canonical"
	if promoted {
		cfg.Interpretation = "assisted"
	}

	limit := analyzeEditorCap
	if cfg.Requests < limit {
		limit = cfg.Requests
	}
	cfg.Requests = limit
	timeout := analyzeTimeout
	if cfg.Timeout > 0 && cfg.Timeout < timeout {
		timeout = cfg.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	budget := req.Budget
	if budget == nil {
		budget, err = sos.NewRequestBudget(limit, nil)
	} else {
		err = budget.Constrain(limit)
	}
	if err != nil {
		return nil, &AnalyzeError{Kind: KindConfig, Err: err}
	}

	// Prefer reusing the saved analysis: validate it locked, so a meaning
	// mismatch fails instead of silently re-resolving. Only a compatibility
	// error falls through to fresh work, and only while budget remains.
	if req.Saved != nil {
		reused, err := sos.Analyze(ctx, req.Source, sos.AnalyzeOptions{
			Modules:    program.Modules,
			Config:     cfg,
			Budget:     budget,
			Bucket:     sos.BudgetEditor,
			Saved:      req.Saved,
			Locked:     true,
			Cache:      req.Cache,
			CacheScope: cacheScope,
		})
		if err == nil {
			return &AnalyzeResult{Analysis: reused, Promoted: promoted, Reused: true}, nil
		}
		if ctx.Err() != nil {
			return nil, &AnalyzeError{Kind: classifyAnalyzeError(ctx, err), Err: err, Analysis: reused}
		}

	}

	analysis, err := sos.Analyze(ctx, req.Source, sos.AnalyzeOptions{
		Modules:    program.Modules,
		Config:     cfg,
		Budget:     budget,
		Bucket:     sos.BudgetEditor,
		Cache:      req.Cache,
		CacheScope: cacheScope,
	})
	if err != nil {
		return &AnalyzeResult{Analysis: analysis, Promoted: promoted}, &AnalyzeError{Kind: classifyAnalyzeError(ctx, err), Err: err, Analysis: analysis}
	}
	return &AnalyzeResult{Analysis: analysis, Promoted: promoted}, nil
}

func classifyAnalyzeError(ctx context.Context, err error) string {
	var budgetErr *sos.BudgetError
	switch {
	case errors.As(err, &budgetErr):
		return KindBudget
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	case errors.Is(err, context.Canceled):
		return KindCanceled
	}
	// Providers do not always wrap the context error faithfully; the context
	// itself is authoritative for deadline and cancellation.
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return KindTimeout
		}
		return KindCanceled
	}
	return KindAnalysis
}

// analyzeResultJSON is the transport shape shared by sos/analyze and the
// studio HTTP route: the core analysis plus how it was obtained.
func analyzeResultJSON(r *AnalyzeResult) map[string]any {
	return map[string]any{
		"analysis": r.Analysis,
		"promoted": r.Promoted,
		"reused":   r.Reused,
	}
}

// sosAnalyze handles the custom sos/analyze request. Params carry either
// inline text or a textDocument URI opened in this session. No other method
// or notification triggers analysis.
func (s *server) sosAnalyze(id json.RawMessage, params json.RawMessage) {
	var p struct {
		Args         map[string]any `json:"args"`
		Text         string         `json:"text"`
		CommandPath  []string       `json:"commandPath"`
		TextDocument *struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		s.writeError(id, codeInvalidRequest, "sos/analyze: invalid params: "+err.Error())
		return
	}
	source := p.Text
	if source == "" && p.TextDocument != nil {
		if doc, ok := s.docs[p.TextDocument.URI]; ok {
			source = doc.text
		}
	}
	if source == "" {
		s.writeError(id, codeInvalidRequest, "sos/analyze: params must carry text or reference an open textDocument")
		return
	}
	if len(source) > maxAnalyzeBytes {
		s.writeError(id, codeInvalidRequest, fmt.Sprintf("sos/analyze: source exceeds %d byte limit", maxAnalyzeBytes))
		return
	}

	// Configuration roots at the server's working directory.
	ctx, cancel := context.WithTimeout(context.Background(), analyzeTimeout)
	defer cancel()
	filename, dir := "", s.workspaceRoot
	if p.TextDocument != nil {
		filename = uriToPath(p.TextDocument.URI)
		if filename != "" {
			dir = semanticProjectDir(filename)
		}
	}
	if s.interpretationCache == nil {
		s.interpretationCache = sos.NewInterpretationCache(1024, 24*time.Hour)
	}
	result, err := Analyze(ctx, AnalyzeRequest{Filename: filename, Dir: dir, Source: source, CommandPath: p.CommandPath, Args: p.Args, Cache: s.interpretationCache})
	if err != nil {
		var ae *AnalyzeError
		var data any
		if errors.As(err, &ae) && ae.Analysis != nil {
			data = map[string]any{"analysis": ae.Analysis}
		}
		s.writeMessage(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: codeAnalyzeFailed, Message: "sos/analyze: " + analyzeErrorKind(err) + ": " + err.Error(), Data: data}})
		return
	}
	s.writeResult(id, analyzeResultJSON(result))
}

func semanticProjectDir(filename string) string {
	dir := filepath.Dir(filename)
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "sos.toml")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir
		}
	}
}

func analyzeErrorKind(err error) string {
	var ae *AnalyzeError
	if errors.As(err, &ae) {
		return ae.Kind
	}
	return KindAnalysis
}
