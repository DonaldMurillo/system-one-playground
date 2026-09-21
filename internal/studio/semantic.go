package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/DonaldMurillo/system-one-playground/internal/soslsp"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// On-demand semantic analysis over HTTP. The Analyze button is the only
// trigger: nothing here runs on keystrokes, editor-assistance policy is
// enforced before the provider is contacted, and every request is admitted
// through the editor budget bucket.

// analysisEntry is the last successful analysis, kept for one source AND one
// command/input selection: a different selected leaf is a different program
// to interpret, so its saved analysis must not be reused. Guarded by s.mu;
// never handed out except as a copy.
type analysisEntry struct {
	source    string
	selection string
	analysis  *sos.Analysis
	promoted  bool
}

// handleAnalyze serves POST /api/analyze. One analysis at a time per session;
// it may run concurrently with a run, which keeps its own cancel handle.
func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "POST required")
		return
	}
	var req struct {
		Path        string         `json:"path"`
		Source      string         `json:"source"`
		CommandPath []string       `json:"commandPath"`
		Args        map[string]any `json:"args"`
	}
	if !decodeBody(w, r, &req) || !checkSource(w, req.Source) {
		return
	}

	s.mu.Lock()
	if s.analysisCancel != nil {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "busy", "an analysis is already active; wait for it or cancel the request")
		return
	}
	// Client aborts cancel through the request context; the deadline caps the
	// server side. A canceled request may still have spent budget; it is
	// never refunded and its result is discarded.
	ctx, cancel := context.WithTimeout(r.Context(), defaultTimeout)
	s.analysisCancel = cancel
	selection := selectionKey(req.CommandPath, req.Args)
	var saved *sos.Analysis
	if s.analysisCache != nil && s.analysisCache.source == req.Path+"\x00"+req.Source && s.analysisCache.selection == selection {
		saved = s.analysisCache.analysis
	}
	runDir := s.dir
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.analysisCancel = nil
		s.mu.Unlock()
		cancel()
	}()

	filename, err := s.sourceFilename(req.Path)
	if err != nil {
		writeError(w, 400, "path", err.Error())
		return
	}
	ctx, err = projectContext(ctx, runDir)
	if err != nil {
		writeError(w, 400, "environment", err.Error())
		return
	}
	result, err := soslsp.Analyze(ctx, soslsp.AnalyzeRequest{
		Filename:    filename,
		Source:      req.Source,
		CommandPath: req.CommandPath,
		Args:        req.Args,
		Dir:         runDir,
		Saved:       saved,
		Cache:       s.interpretationCache,
	})
	if err != nil {
		writeAnalysisError(w, err)
		return
	}

	s.mu.Lock()
	s.analysisCache = &analysisEntry{
		source:    req.Path + "\x00" + req.Source,
		selection: selection,
		analysis:  result.Analysis,
		promoted:  result.Promoted,
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"analysis": result.Analysis,
		"promoted": result.Promoted,
		"reused":   result.Reused,
	})
}

// analysisSnapshot returns a private copy of the last successful analysis for
// exactly this source and command/input selection, or nil when no match is
// cached. Analyses produced under a promoted canonical-to-assisted policy are
// suggestions only and are never returned for execution.
func (s *Server) analysisSnapshot(source string, commandPath []string, args map[string]any) *sos.Analysis {
	s.mu.Lock()
	entry := s.analysisCache
	s.mu.Unlock()
	if entry == nil || entry.source != source || entry.selection != selectionKey(commandPath, args) || entry.promoted {
		return nil
	}
	return copyAnalysis(entry.analysis)
}

// selectionKey canonicalizes a command selection for cache identity.
func selectionKey(commandPath []string, args map[string]any) string {
	type selection struct {
		Path []string       `json:"path"`
		Args map[string]any `json:"args"`
	}
	b, err := json.Marshal(selection{Path: commandPath, Args: args})
	if err != nil {
		return ""
	}
	return string(b)
}

// copyAnalysis deep-copies an analysis through its documented JSON form, so
// callers cannot mutate the cached entry. Sources are capped at 1 MiB.
func copyAnalysis(a *sos.Analysis) *sos.Analysis {
	if a == nil {
		return nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil
	}
	var out sos.Analysis
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return &out
}

func writeAnalysisError(w http.ResponseWriter, err error) {
	var ae *soslsp.AnalyzeError
	if !errors.As(err, &ae) {
		writeError(w, http.StatusInternalServerError, "analysis", err.Error())
		return
	}
	status, message := http.StatusUnprocessableEntity, err.Error()
	switch ae.Kind {
	case soslsp.KindConfig:
		status = http.StatusBadRequest
	case soslsp.KindEditor:
		status = http.StatusForbidden
	case soslsp.KindBudget:
		status = http.StatusTooManyRequests
	case soslsp.KindTimeout:
		status = http.StatusGatewayTimeout
		message = "analysis exceeded its time budget and was stopped"
	case soslsp.KindCanceled:
		status = http.StatusBadRequest
		message = "analysis was cancelled"
	}
	response := map[string]any{"error": map[string]string{"kind": ae.Kind, "message": message}}
	if ae.Analysis != nil {
		response["analysis"] = ae.Analysis
	}
	writeJSON(w, status, response)
}
