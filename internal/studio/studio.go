// Package studio serves the SysOneScript editor workbench: the embedded web
// assets built from studio/ into webdist, plus a localhost-only JSON API over
// the language core (parse/check/format/run). One Server is one session: it
// holds an unpredictable token, permits one run at a time, and can cancel it.
package studio

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sos"
	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

//go:embed all:webdist
var webdistFS embed.FS

//go:embed examples/*.sos
var examplesFS embed.FS

const (
	maxBodyBytes    = 2 << 20 // request body cap
	maxSourceBytes  = 1 << 20 // editor buffer cap
	maxTraces       = 512
	maxStreamEvents = 2048
	defaultTimeout  = 30 * time.Second
	maxTimeout      = 2 * time.Minute
	defaultMaxSteps = 100_000
	defaultMaxCalls = 32
	maxSaveBytes    = 512 << 10
)

// Options configures a studio session.
type Options struct {
	Dir string // absolute working directory for runs
}

// Server is one studio session bound to one working directory.
type Server struct {
	projectMu             sync.Mutex
	dir                   string
	token                 string
	mux                   *http.ServeMux
	mu                    sync.Mutex
	cancel                context.CancelFunc // non-nil while a run is active
	streams               *sos.StreamController
	streamLog             []sos.StreamEvent
	streamEventsTruncated bool
	runSeq                uint64
	// On-demand semantic analysis state, guarded by mu. Independent of a run.
	analysisCancel      context.CancelFunc // non-nil while an analysis is active
	analysisCache       *analysisEntry     // last successful analysis, one source
	interpretationCache *sos.InterpretationCache
}

// New creates a session. The token is unpredictable and exposed only to the
// local launcher via Token, so it can build the one localhost URL.
func New(opts Options) (*Server, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	dir, err := projectRoot(opts.Dir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		dir:                 dir,
		token:               hex.EncodeToString(raw),
		interpretationCache: sos.NewInterpretationCache(1024, 24*time.Hour),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project", s.guard(s.handleProject))
	mux.HandleFunc("/api/build", s.guard(s.handleBuild))
	mux.HandleFunc("/api/session", s.guard(s.handleSession))
	mux.HandleFunc("/api/examples", s.guard(s.handleExamples))
	mux.HandleFunc("/api/open", s.guard(s.handleOpen))
	mux.HandleFunc("/api/lsp", s.guard(s.handleLSP))
	mux.HandleFunc("/api/check", s.guard(s.handleCheck))
	mux.HandleFunc("/api/format", s.guard(s.handleFormat))
	mux.HandleFunc("/api/analyze", s.guard(s.handleAnalyze))
	mux.HandleFunc("/api/run", s.guard(s.handleRun))
	mux.HandleFunc("/api/cancel", s.guard(s.handleCancel))
	mux.HandleFunc("/api/streams", s.guard(s.handleStreams))
	mux.HandleFunc("/api/streams/stop", s.guard(s.handleStopStream))
	mux.HandleFunc("/api/capabilities", s.guard(s.handleCapabilities))
	mux.HandleFunc("/api/save", s.guard(s.handleSave))
	mux.HandleFunc("/", s.handleAssets)
	s.mux = mux
	return s, nil
}

func projectRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working folder must be a directory")
	}
	layers, loadErr := sosconfig.Load(abs)
	if loadErr != nil {
		return "", loadErr
	}
	for i := len(layers) - 1; i >= 0; i-- {
		if filepath.Base(layers[i].Name) == "sos.toml" {
			return filepath.Dir(layers[i].Name), nil
		}
	}
	return abs, nil
}

// Token exposes the session token to the local launcher.
func (s *Server) Token() string { return s.token }

// Dir exposes the run working directory.
func (s *Server) Dir() string { s.mu.Lock(); defer s.mu.Unlock(); return s.dir }

// ServeHTTP implements http.Handler with body limits and safety headers.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if host != "localhost" && host != "wails.localhost" && host != "wails" && !(ip != nil && ip.IsLoopback()) {
		writeError(w, http.StatusForbidden, "host", "local host required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// guard authenticates API requests: token must match, and any Origin header
// must be the serving origin (or the Wails webview origin on desktop).
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Studio-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" || !constantTimeEqual(token, s.token) {
			writeError(w, http.StatusUnauthorized, "auth", "missing or invalid studio session token")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !s.originAllowed(origin, r.Host) {
				writeError(w, http.StatusForbidden, "origin", "cross-origin API request rejected")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) originAllowed(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Host == host {
		return true
	}
	// Desktop shell: the Wails asset server origin differs from r.Host.
	switch origin {
	case "http://wails.localhost", "https://wails.localhost", "wails://wails.localhost", "wails://wails":
		return true
	}
	return false
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range len(a) {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// ---- session metadata ----

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "GET required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":              s.Dir(),
		"version":          sos.Version,
		"keywords":         sos.Keywords(),
		"defaultTimeoutMs": defaultTimeout.Milliseconds(),
		"maxTimeoutMs":     maxTimeout.Milliseconds(),
	})
}

func (s *Server) handleExamples(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "GET required")
		return
	}
	names, err := fs.Glob(examplesFS, "examples/*.sos")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	sort.Strings(names)
	out := make([]map[string]string, 0, len(names))
	for _, n := range names {
		title := strings.TrimSuffix(path.Base(n), ".sos")
		switch title {
		case "semantic-dictionary":
			title = "Dictionary fast path · 0 Jev requests"
		case "jev-language-composer":
			title = "Jev grammar composer · 1 request"
		case "semantic-gauntlet":
			title = "Semantic Gauntlet · 8 decisions · 1 batched Jev request"
		}
		out = append(out, map[string]string{"name": strings.TrimSuffix(path.Base(n), ".sos"), "title": title})
	}
	writeJSON(w, http.StatusOK, map[string]any{"examples": out})
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "POST required")
		return
	}
	var req struct{ Name string }
	if !decodeBody(w, r, &req) {
		return
	}
	names, err := fs.Glob(examplesFS, "examples/*.sos")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	for _, n := range names {
		if strings.TrimSuffix(path.Base(n), ".sos") == req.Name {
			b, err := examplesFS.ReadFile(n)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"source": string(b)})
			return
		}
	}
	writeError(w, http.StatusNotFound, "example", "unknown example "+strconv.Quote(req.Name))
}

// ---- language services ----

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string
		Path   string
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if !checkSource(w, req.Source) {
		return
	}
	filename, err := s.sourceFilename(req.Path)
	if err != nil {
		writeError(w, 400, "path", err.Error())
		return
	}
	program, diagnostics := sos.LoadProgram(filename, req.Source)
	if diagnostics == nil {
		diagnostics = []sos.Diagnostic{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"diagnostics":      sos.EditorDiagnostics(filename, req.Source, diagnostics),
		"commands":         commandsMeta(program),
		"actions":          program.ActionMetadata(),
		"possibleFailures": failureMetadata(program),
	})
}

func failureMetadata(program *sos.Program) map[string][]string {
	result := map[string][]string{}
	for _, action := range program.ActionMetadata() {
		if len(action.PossibleFailures) > 0 {
			result[action.Name] = append([]string(nil), action.PossibleFailures...)
		}
	}
	return result
}

func (s *Server) handleFormat(w http.ResponseWriter, r *http.Request) {
	var req struct{ Source string }
	if !decodeBody(w, r, &req) {
		return
	}
	if !checkSource(w, req.Source) {
		return
	}
	formatted, diagnostics := sos.Format(req.Source)
	if diagnostics == nil {
		diagnostics = []sos.Diagnostic{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": formatted, "diagnostics": diagnostics})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path        string         `json:"path"`
		Source      string         `json:"source"`
		TimeoutMs   int64          `json:"timeoutMs"`
		Args        map[string]any `json:"args"`
		CommandPath []string       `json:"commandPath"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if !checkSource(w, req.Source) {
		return
	}
	timeout := defaultTimeout
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
		if timeout > maxTimeout {
			timeout = maxTimeout
		}
	}

	// One run per session; a second concurrent request gets a clear answer.
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "busy", "a run is already active; stop it first")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.cancel = cancel
	s.streams = sos.NewStreamController()
	s.streamLog = nil
	s.streamEventsTruncated = false
	s.runSeq++
	runID := s.runSeq
	streams := s.streams
	runDir := s.dir
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}()
	started := time.Now()
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
	program, diagnostics := sos.LoadProgram(filename, req.Source)
	if diagnostics == nil {
		diagnostics = []sos.Diagnostic{}
	}
	resp := map[string]any{
		"ok":          false,
		"output":      "",
		"stderr":      "",
		"traces":      []sos.Trace{},
		"steps":       0,
		"durationMs":  time.Since(started).Milliseconds(),
		"diagnostics": diagnostics,
		"runId":       runID,
	}
	if program == nil {
		resp["error"] = map[string]string{"kind": "parse", "message": "source did not parse"}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	stdout, stderr := limitedOutput{}, limitedOutput{}
	traces := make([]sos.Trace, 0, 16)
	resolution := s.analysisSnapshot(req.Path+"\x00"+req.Source, req.CommandPath, req.Args)
	result, err := sos.Run(ctx, program, sos.Options{
		Resolution: resolution, Locked: resolution != nil,
		Dir:         runDir,
		Args:        req.Args,
		CommandPath: req.CommandPath,
		Stdout:      &stdout,
		Stderr:      &stderr,
		MaxSteps:    defaultMaxSteps,
		MaxCalls:    defaultMaxCalls,
		OnTrace: func(t sos.Trace) {
			if len(traces) < maxTraces {
				traces = append(traces, t)
			}
		},
		Streams: streams,
		OnStreamEvent: func(event sos.StreamEvent) {
			s.mu.Lock()
			if len(s.streamLog) < maxStreamEvents {
				s.streamLog = append(s.streamLog, event)
			} else {
				s.streamEventsTruncated = true
			}
			s.mu.Unlock()
		},
	})
	resp["output"] = stdout.String()
	resp["stderr"] = stderr.String()
	resp["traces"] = traces
	resp["durationMs"] = time.Since(started).Milliseconds()
	resp["streams"] = streams.Snapshots()
	s.mu.Lock()
	resp["streamEvents"] = append([]sos.StreamEvent(nil), s.streamLog...)
	resp["streamEventsTruncated"] = s.streamEventsTruncated
	s.mu.Unlock()
	if result != nil {
		resp["traces"] = result.Traces[:min(len(result.Traces), maxTraces)]
		resp["usage"] = result.Usage
		if result.Analysis != nil {
			resp["analysis"] = result.Analysis
			resp["diagnostics"] = result.Analysis.Diagnostics
		}
	}

	var budgetErr *sos.BudgetError
	var exit interface{ ExitCode() int }
	switch {
	case errors.As(err, &exit):
		resp["exitCode"] = exit.ExitCode()
		resp["ok"] = exit.ExitCode() == 0
		if exit.ExitCode() != 0 {
			resp["error"] = map[string]string{"kind": "exit", "message": err.Error()}
		}
	case errors.As(err, &budgetErr):
		resp["error"] = map[string]string{"kind": "budget", "message": err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		resp["error"] = map[string]string{"kind": "timeout", "message": "run exceeded its time budget and was stopped; partial output is included"}
	case errors.Is(err, context.Canceled):
		resp["error"] = map[string]string{"kind": "cancelled", "message": "run was stopped"}
	case err != nil:
		if result != nil && result.Failure != nil {
			resp["error"] = result.Failure
		} else {
			resp["error"] = map[string]string{"kind": "runtime", "message": err.Error()}
		}
	default:
		resp["ok"] = true
		if result != nil {
			resp["steps"] = result.Steps
			resp["variables"] = result.Variables
			if result.Traces != nil {
				resp["traces"] = result.Traces
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel == nil {
		writeError(w, http.StatusConflict, "idle", "no run is active")
		return
	}
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStreams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "GET required")
		return
	}
	s.mu.Lock()
	controller := s.streams
	running := s.cancel != nil
	runID := s.runSeq
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	if since < 0 || since > len(s.streamLog) {
		since = 0
	}
	events := append([]sos.StreamEvent(nil), s.streamLog[since:]...)
	next := len(s.streamLog)
	truncated := s.streamEventsTruncated
	s.mu.Unlock()
	streams := []sos.StreamEvent{}
	if controller != nil {
		streams = controller.Snapshots()
	}
	writeJSON(w, http.StatusOK, map[string]any{"runId": runID, "running": running, "streams": streams, "events": events, "next": next, "eventsTruncated": truncated})
}

func (s *Server) handleStopStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method", "POST required")
		return
	}
	var req struct {
		ID    string `json:"id"`
		RunID uint64 `json:"runId"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeError(w, http.StatusBadRequest, "stream", "stream id is required")
		return
	}
	s.mu.Lock()
	controller := s.streams
	running := s.cancel != nil
	runID := s.runSeq
	s.mu.Unlock()
	if controller == nil || !running {
		writeError(w, http.StatusConflict, "idle", "no run is active")
		return
	}
	if req.RunID != 0 && req.RunID != runID {
		writeError(w, http.StatusConflict, "stale", "that stream belongs to an earlier run")
		return
	}
	if err := controller.Stop(r.Context(), req.ID); err != nil {
		writeError(w, http.StatusNotFound, "stream", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": req.ID})
}

// ---- save / download ----

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Source) > maxSaveBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "size", "buffer too large to download")
		return
	}
	name := sanitizeFileName(req.Name)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(req.Source)) //nolint:errcheck
}

// sanitizeFileName reduces a client-supplied name to a single safe basename.
func sanitizeFileName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name = strings.Trim(b.String(), "-.")
	if name == "" || name == "." || name == ".." {
		name = "script.sos"
	}
	if !strings.HasSuffix(name, ".sos") && !strings.HasSuffix(name, ".sos") {
		name += ".sos"
	}
	return name
}

// ---- static assets ----

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method", "GET required")
		return
	}
	upath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if upath == "" {
		upath = "index.html"
	}
	data, err := webdistFS.ReadFile("webdist/" + upath)
	if err != nil {
		writeError(w, http.StatusNotFound, "notfound", "no such asset")
		return
	}
	ctype := mime.TypeByExtension(path.Ext(upath))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	if strings.Contains(ctype, "html") && strings.HasSuffix(upath, ".html") {
		// The desktop webview has no URL bar to carry a token: inject the
		// session token into the document itself. Served only on localhost.
		data = bytes.Replace(data,
			[]byte("</head>"),
			[]byte(`<script nonce="`+s.token+`">window.__SOS_STUDIO_TOKEN__="`+s.token+`";</script></head>`),
			1)
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'nonce-"+s.token+"'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; worker-src 'self' blob:; connect-src 'self'")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data) //nolint:errcheck
}

// ---- helpers ----

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "request", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func checkSource(w http.ResponseWriter, source string) bool {
	if len(source) > maxSourceBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "size", "source exceeds 1 MiB limit")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"kind": kind, "message": message}})
}

// limitedOutput stops scripts from exhausting the editor with repeated output.
type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("output exceeds 1 MiB limit")
	}
	return b.Buffer.Write(p)
}

// SetDir changes the native session's working folder while idle.
func (s *Server) SetDir(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil || s.analysisCancel != nil {
		return fmt.Errorf("stop the active run before changing folders")
	}
	abs, e := projectRoot(dir)
	if e != nil {
		return e
	}
	s.analysisCache = nil
	s.dir = abs
	return nil
}
