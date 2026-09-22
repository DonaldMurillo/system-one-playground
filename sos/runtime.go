package sos

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type runtime struct {
	shared           *executionState
	logicalPath      string
	parallelSequence int
	parallelDepth    int
	activeFailure    error
	ctx              context.Context
	p                *Program
	opts             Options
	env              map[string]any
	functions        map[string]*Statement
	schemas          map[string]*Statement
	definitions      map[string]*RecordDef
	failures         map[string]*FailureDef
	types            map[string]TypeRef
	imports          map[string]*Module
	module           *Module
	vocab            *fileVocab
	result           *Result
	calls            int
	recording        []record
	replay           []record
	replayIndex      int
	depth            int
	condition        bool
	debugStack       []DebugFrame
	streamOutput     chan<- any
	streamItemType   TypeRef
}
type returnValue struct {
	value    any
	hasValue bool
}

type recoveryValue struct {
	value    any
	hasValue bool
}

func (recoveryValue) Error() string { return "recover" }

func (r returnValue) Error() string { return "return outside action" }

// Run resolves permitted source constructions, validates the entire lowered
// program, and executes it with a shared interpretation/runtime request budget.
func Run(ctx context.Context, p *Program, opts Options) (result *Result, err error) {
	if p == nil {
		return nil, fmt.Errorf("missing program")
	}
	if opts.externalSession == nil {
		opts.externalSession = newExternalSessionKey()
	}
	if opts.httpServers == nil {
		opts.httpServers = newHTTPServerRegistry()
	}
	// Validate declarations and inputs before admitting any provider requests.
	// The import graph from LoadProgram survives re-parsing: canonical
	// assembly preserves import statements, and modules carry their own
	// definition-site bindings. The resolved vocabulary re-applies so
	// sentence calls parse canonically on the rebuilt statement tree.
	modules := p.Modules
	parsed, syntaxDiagnostics := ParseWithVocabulary(p.Source, modules)
	for _, d := range syntaxDiagnostics {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			return nil, fmt.Errorf("line %d: %s", d.Line, d.Message)
		}
	}
	p = parsed
	p.Modules = modules
	selected, params, selectionErr := commandAt(p.RootCommand, opts.CommandPath)
	if selectionErr != nil {
		return nil, selectionErr
	}
	if selected != nil {
		if len(selected.Commands) > 0 {
			return nil, fmt.Errorf("select a leaf command")
		}
		opts.Args, selectionErr = validateCommandValues(params, opts.Args)
		if selectionErr != nil {
			return nil, selectionErr
		}
	} else if len(opts.Args) > 0 {
		return nil, fmt.Errorf("plain scripts do not declare command inputs")
	}
	analysisSource, selectionErr := SelectedSource(p, opts.CommandPath)
	if selectionErr != nil {
		return nil, selectionErr
	}
	// A whole-source saved record is validated against the whole source by Analyze.
	// Selected records retain their exact selected-source identity.
	wholeSaved := opts.Resolution != nil && opts.Resolution.SourceHash == semanticSourceHash(p.Source)
	if wholeSaved {
		analysisSource = p.Source
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Dir == "" {
		opts.Dir = "."
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 100000
	}
	var replay []record
	if opts.Record != "" && opts.Replay != "" {
		return nil, fmt.Errorf("record and replay are mutually exclusive")
	}
	if opts.Replay != "" {
		data, e := os.ReadFile(opts.Replay)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(data, &replay); e != nil {
			return nil, e
		}
	}
	timeout, configErr := configureRun(p, &opts)
	if configErr != nil {
		return nil, configErr
	}
	if modules != nil {
		defer modules.closeExternalSession(opts.externalSession)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var analysis *Analysis
	defer func() {
		if result == nil {
			result = &Result{Traces: []Trace{}}
		}
		result.Usage = opts.Budget.Snapshot()
		result.Analysis = analysis
	}()
	interpretationModel := opts.Model
	if opts.Resolution != nil {
		interpretationModel = opts.Resolution.Model
	}
	analysis, err = Analyze(ctx, analysisSource, AnalyzeOptions{Config: *opts.Config, Budget: opts.Budget, Model: interpretationModel, Bucket: BudgetInterpretation, Saved: opts.Resolution, Locked: opts.Locked || wholeSaved, Modules: modules})
	if err != nil && wholeSaved && !opts.Locked {
		selectedSource, e := SelectedSource(p, opts.CommandPath)
		if e != nil {
			return nil, e
		}
		analysis, err = Analyze(ctx, selectedSource, AnalyzeOptions{Config: *opts.Config, Budget: opts.Budget, Model: interpretationModel, Bucket: BudgetInterpretation, Modules: modules})
	}
	if err != nil {
		return nil, err
	}
	resolved, diags := ParseWithVocabulary(analysis.Canonical, modules)
	if len(diags) > 0 {
		return nil, fmt.Errorf("invalid resolved program at line %d: %s", diags[0].Line, diags[0].Message)
	}
	remapStatements(resolved.Statements, analysis.SourceMap)
	p = resolved
	p.Modules = modules
	logicalPath := opts.SourcePath
	if logicalPath != "" {
		if absolute, e := filepath.Abs(logicalPath); e == nil {
			logicalPath = absolute
		}
	}
	r := &runtime{shared: &executionState{}, logicalPath: logicalPath, ctx: ctx, p: p, opts: opts, replay: replay, env: map[string]any{}, functions: map[string]*Statement{}, schemas: map[string]*Statement{}, definitions: p.Definitions, failures: p.Failures, types: map[string]TypeRef{}, imports: map[string]*Module{}, result: &Result{Traces: []Trace{}}}
	if p.Modules != nil {
		r.imports = p.Modules.Aliases
		r.vocab = p.Modules.vocab
	}
	r.definitions, _ = visibleDefinitions(p.Definitions, r.imports)
	r.failures, _ = visibleFailuresFrom(p.Failures, r.imports, "the current file")
	for k, v := range opts.Args {
		r.env[k] = v
	}
	// Declarations are scope-visible independent of source order, matching the
	// checker and allowing a call before the action's definition line.
	for _, statement := range p.Statements {
		switch statement.Kind {
		case "to":
			r.functions[match("to", statement.Text)[1]] = statement
		case "schema":
			r.schemas[match("schema", statement.Text)[1]] = statement
		}
	}
	execution := p.Statements
	if p.RootCommand != nil {
		leaf, _, e := commandAt(p.RootCommand, opts.CommandPath)
		if e != nil {
			return nil, e
		}
		execution = nil
		for _, st := range p.Statements {
			if st.Kind != "command" {
				execution = append(execution, st)
			}
		}
		execution = append(execution, leaf.Statements...)
	}
	defer func() {
		variables := make(map[string]any, len(r.env))
		for name, value := range r.env {
			if stream, ok := value.(*streamHandle); ok {
				_ = stream.closeWithReason(context.Background(), "run ended")
				variables[name] = stream.debugValue()
			} else {
				variables[name] = value
			}
		}
		r.result.Variables = variables
		r.result.Steps = int(r.shared.steps.Load())
		r.result.Usage = opts.Budget.Snapshot()
		if opts.Streams != nil {
			r.result.Streams = opts.Streams.Snapshots()
		}
		result = r.result
		var typed *typedFailure
		if errors.As(err, &typed) {
			r.result.Failure = FailureValue(err)
		}
		if err == nil && opts.Replay != "" && r.replayIndex != len(r.replay) {
			err = fmt.Errorf("replay has unused judgments")
		}
		if opts.Record != "" {
			data, e := json.MarshalIndent(r.recording, "", "  ")
			if e == nil {
				e = os.WriteFile(opts.Record, data, 0600)
			}
			if err == nil {
				err = e
			}
		}
	}()
	err = r.block(execution)
	return r.result, err
}
func (r *runtime) tick() error {
	if e := r.ctx.Err(); e != nil {
		return e
	}
	steps := r.shared.steps.Add(1)
	if steps > int64(r.opts.MaxSteps) {
		return &executionLimitError{fmt.Sprintf("execution step limit exceeded (%d)", r.opts.MaxSteps)}
	}
	return nil
}
func (r *runtime) eval(s string, item any) (any, error) {
	return evaluateWithTypes(s, r.env, item, r.types, r.definitions)
}
func (r *runtime) text(s string) (string, error) {
	v, e := r.eval(s, nil)
	if e != nil {
		return "", e
	}
	x, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("expected text, got %T", v)
	}
	return x, nil
}
func (r *runtime) path(s string) (string, error) {
	v, e := r.text(s)
	if e != nil {
		return "", e
	}
	if filepath.IsAbs(v) {
		return filepath.Clean(v), nil
	}
	return filepath.Join(r.opts.Dir, v), nil
}
func (r *runtime) list(name string) ([]any, error) {
	v, ok := r.env[name]
	if !ok {
		return nil, fmt.Errorf("unknown name %q", name)
	}
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", name)
	}
	return a, nil
}
func (r *runtime) block(sts []*Statement) error {
	for i, s := range sts {
		if s.Kind == "handler" || s.Kind == "otherwise" {
			continue
		}
		if err := r.tick(); err != nil {
			return fmt.Errorf("line %d: %w", s.Line, err)
		}
		if debuggableStatement(s) {
			if err := r.debugBefore(s); err != nil {
				return err
			}
		}
		err := r.execute(s)
		if err != nil {
			var ret returnValue
			var stopReading stopReadingValue
			var exit interface{ ExitCode() int }
			var replayErr *ReplayIntegrityError
			var recovered recoveryValue
			if errors.As(err, &exit) || errors.As(err, &replayErr) {
				return err
			}
			if fatalParallel(err) {
				return err
			}
			if errors.As(err, &ret) || errors.As(err, &recovered) || errors.As(err, &stopReading) {
				return err
			}
			if r.ctx.Err() != nil {
				return fmt.Errorf("line %d: %w", s.Line, r.ctx.Err())
			}
			handled := false
			for _, h := range s.Body {
				if h.Kind != "handler" || match("handler", h.Text)[1] != "failure" || !r.matchesFailureHandler(h, err) {
					continue
				}
				restoreBindings := r.localFailureHandlerBindings(h)
				r.env["error"] = err.Error()
				r.env["failure"] = FailureValue(err)
				r.bindFailureHandler(h, err)
				outerFailure := r.activeFailure
				r.activeFailure = err
				handledErr := r.handler(h)
				r.activeFailure = outerFailure
				restoreBindings()
				var recovered recoveryValue
				if errors.As(handledErr, &recovered) {
					if recovered.hasValue && !r.callHasResult(s) {
						return fmt.Errorf("line %d: recover with is only valid for a value-returning operation", h.Line)
					}
					if !recovered.hasValue && r.callHasResult(s) {
						return fmt.Errorf("line %d: recover requires a value for this operation", h.Line)
					}
					if recovered.hasValue {
						if wanted, ok := r.callResultType(s); ok && !typeMatchesRef(recovered.value, wanted, r.definitions) {
							return fmt.Errorf("line %d: recover with requires %s; received %s", h.Line, wanted.String(), valueTypeName(recovered.value))
						}
					}
					if recovered.hasValue && !bindCallResult(s, recovered.value, r) {
						return fmt.Errorf("line %d: recover with cannot bind operation result", h.Line)
					}
					handledErr = nil
				}
				err = handledErr
				handled = true
				break
			}
			if err != nil {
				return fmt.Errorf("line %d: %w", s.Line, err)
			}
			if handled {
				continue
			}
		} else {
			for _, h := range s.Body {
				if h.Kind == "handler" && match("handler", h.Text)[1] == "success" {
					if e := r.handler(h); e != nil {
						return fmt.Errorf("line %d: %w", h.Line, e)
					}
				}
			}
		}
		if s.Kind == "when" && i+1 < len(sts) && sts[i+1].Kind == "otherwise" { // condition is captured by execute, not evaluated twice
			if !r.condition {
				if e := r.block(sts[i+1].Body); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func (r *runtime) localFailureHandlerBindings(h *Statement) func() {
	_, binding, fields, _ := failureHandlerHeader(h.Text)
	names := []string{"error", "failure"}
	if binding != "" {
		names = append(names, binding)
	} else {
		names = append(names, fields...)
	}
	type previousBinding struct {
		value any
		set   bool
	}
	previous := make(map[string]previousBinding, len(names))
	for _, name := range names {
		value, set := r.env[name]
		previous[name] = previousBinding{value: value, set: set}
	}
	return func() {
		for name, binding := range previous {
			if binding.set {
				r.env[name] = binding.value
			} else {
				delete(r.env, name)
			}
		}
	}
}

func debuggableStatement(s *Statement) bool {
	if s == nil {
		return false
	}
	switch s.Kind {
	case "command", "parameter", "import", "package", "export", "schema", "to", "handler", "otherwise":
		return false
	default:
		return true
	}
}

func (r *runtime) debugBefore(s *Statement) error {
	if r.opts.Debugger == nil {
		return nil
	}
	knownTypes := map[string]string{}
	for name, typ := range r.types {
		if typ.Name != "" && typ.Name != "any" {
			knownTypes[name] = typ.Name
		}
	}
	frame := DebugFrame{Path: r.logicalPath, Line: s.Line, Column: 1, Kind: s.Kind, Text: s.Text, Depth: r.depth, VariableTypes: knownTypes}
	stack := make([]DebugFrame, len(r.debugStack))
	copy(stack, r.debugStack)
	debugEnv := make(map[string]any, len(r.env))
	for name, value := range r.env {
		if stream, ok := value.(*streamHandle); ok {
			debugEnv[name] = stream.debugValue()
		} else {
			debugEnv[name] = value
		}
	}
	variables, err := cloneValue(debugEnv)
	if err != nil {
		return fmt.Errorf("debugger snapshot: %w", err)
	}
	snapshot, ok := variables.(map[string]any)
	if !ok {
		return fmt.Errorf("debugger snapshot is not an object")
	}
	if err := r.opts.Debugger.BeforeStatement(r.ctx, frame, stack, snapshot); err != nil {
		return err
	}
	return nil
}
func (r *runtime) handler(h *Statement) error {
	m := match("handler", h.Text)
	if m[2] != "" {
		if m[1] == "failure" {
			if _, _, _, typed := failureHandlerHeader(h.Text); typed {
				return r.block(h.Body)
			}
		}
		text := m[2]
		if text == "discard" {
			return nil
		}
		kind := classifyLine(text)
		if kind == "" {
			return fmt.Errorf("unknown handler action %q", text)
		}
		return r.block([]*Statement{{Kind: kind, Text: text, Line: h.Line}})
	}
	return r.block(h.Body)
}

func failureHandlerHeader(text string) (kind, binding string, fields []string, typed bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, "on failure"))
	if rest == "" {
		return "", "", nil, false
	}
	if rest == "discard" || strings.HasPrefix(rest, "stop ") || rest == "stop" || rest == "rethrow" || rest == "pass failure on" || strings.HasPrefix(rest, "recover") {
		return "", "", nil, false
	}
	m := regexp.MustCompile(`^([A-Z][A-Za-z0-9_]*)(?: called ([a-z_][A-Za-z0-9_]*)| using (.+))?$`).FindStringSubmatch(strings.TrimSuffix(rest, ":"))
	if m == nil {
		return "", "", nil, false
	}
	if m[2] != "" {
		return m[1], m[2], nil, true
	}
	if m[3] != "" {
		for _, field := range strings.Split(m[3], ",") {
			fields = append(fields, strings.TrimSpace(field))
		}
	}
	return m[1], "", fields, true
}

func (r *runtime) matchesFailureHandler(h *Statement, err error) bool {
	kind, _, _, typed := failureHandlerHeader(h.Text)
	if !typed {
		return true
	}
	var tf *typedFailure
	if !errors.As(err, &tf) {
		return false
	}
	return tf.kind == kind
}

func (r *runtime) bindFailureHandler(h *Statement, err error) {
	kind, binding, fields, typed := failureHandlerHeader(h.Text)
	if !typed {
		return
	}
	value := FailureValue(err)
	if binding != "" {
		r.env[binding] = value
		return
	}
	for _, field := range fields {
		if v, ok := value[field]; ok {
			r.env[field] = v
		} else {
			r.env[field] = nil
		}
	}
	_ = kind
}

func (r *runtime) callHasResult(s *Statement) bool {
	if s == nil {
		return false
	}
	if s.Kind == "call" {
		m := match("call", s.Text)
		if strings.Contains(m[1], ".") {
			alias, action, _ := strings.Cut(m[1], ".")
			if mod := r.imports[alias]; mod != nil {
				if op, ok := mod.Native[action]; ok {
					return op.Result != "" && op.Result != "none"
				}
				if fn := mod.Actions[action]; fn != nil {
					decl, _ := parseActionDecl(fn.Text)
					return decl.HasResult
				}
			}
			return false
		}
		if fn := r.functions[m[1]]; fn != nil {
			decl, _ := parseActionDecl(fn.Text)
			return decl.HasResult
		}
		return false
	}
	if s.Kind == "collectStream" || s.Kind == "take" {
		return true
	}
	if s.Kind == "sent" {
		m := matchSent(s.Text)
		mod, action, ok := r.vocabLookup(m[1])
		if !ok {
			return false
		}
		if op, native := mod.Native[action]; native {
			return op.Result != "" && op.Result != "none"
		}
		if mod.Actions[action] == nil {
			return false
		}
		decl, _ := parseActionDecl(mod.Actions[action].Text)
		return decl.HasResult
	}
	return false
}

func (r *runtime) callResultType(s *Statement) (TypeRef, bool) {
	if s == nil {
		return TypeRef{}, false
	}
	var declaration *Statement
	var nativeResult string
	if s.Kind == "collectStream" || s.Kind == "take" {
		m := match(s.Kind, s.Text)
		streamName := m[2]
		if s.Kind == "take" {
			streamName = m[3]
		}
		if stream, ok := r.env[strings.TrimSpace(streamName)].(*streamHandle); ok {
			item := stream.itemType
			return TypeRef{Element: &item}, true
		}
		return TypeRef{}, false
	}
	if s.Kind == "call" {
		m := match("call", s.Text)
		if strings.Contains(m[1], ".") {
			alias, action, _ := strings.Cut(m[1], ".")
			if module := r.imports[alias]; module != nil {
				declaration = module.Actions[action]
				if op, ok := module.Native[action]; ok {
					nativeResult = op.Result
				}
			}
		} else {
			declaration = r.functions[m[1]]
		}
	} else if s.Kind == "sent" {
		m := matchSent(s.Text)
		if mod, action, ok := r.vocabLookup(m[1]); ok {
			declaration = mod.Actions[action]
			if op, native := mod.Native[action]; native {
				nativeResult = op.Result
			}
		}
	}
	if nativeResult != "" && nativeResult != "none" {
		typ, err := parseType(nativeResult, true)
		return typ, err == nil
	}
	if declaration == nil {
		return TypeRef{}, false
	}
	decl, err := parseActionDecl(declaration.Text)
	if err != nil || !decl.HasResult {
		return TypeRef{}, false
	}
	return decl.Result, true
}

func bindCallResult(s *Statement, value any, r *runtime) bool {
	if s == nil {
		return true
	}
	if s.Kind == "call" {
		name := match("call", s.Text)[3]
		if name != "" {
			r.env[name] = value
		}
		return true
	}
	if s.Kind == "collectStream" {
		r.env[match("collectStream", s.Text)[3]] = value
		return true
	}
	if s.Kind == "take" {
		r.env[match("take", s.Text)[4]] = value
		return true
	}
	if s.Kind == "sent" {
		name := matchSent(s.Text)[4]
		if name != "" {
			r.env[name] = value
		}
		return true
	}
	return false
}

func (r *runtime) failureFrames() []map[string]any {
	frames := make([]map[string]any, 0, len(r.debugStack)+1)
	for _, frame := range r.debugStack {
		frames = append(frames, map[string]any{
			"name": frame.Name, "path": frame.Path, "line": frame.Line,
			"column": frame.Column, "kind": frame.Kind, "text": frame.Text,
		})
	}
	return frames
}

func (r *runtime) failureStatementFrame(s *Statement) map[string]any {
	if s == nil {
		return nil
	}
	path := r.logicalPath
	if len(r.debugStack) > 0 && r.debugStack[len(r.debugStack)-1].Path != "" {
		path = r.debugStack[len(r.debugStack)-1].Path
	}
	return map[string]any{
		"name": path, "path": path, "line": s.Line,
		"column": 1, "kind": s.Kind, "text": s.Text,
	}
}

func (r *runtime) propagateFailure(err error, s *Statement) error {
	var failure *typedFailure
	if !errors.As(err, &failure) {
		return err
	}
	value := map[string]any{}
	for key, item := range failure.value {
		value[key] = item
	}
	frames := append([]map[string]any(nil), failure.frames...)
	if frame := r.failureStatementFrame(s); frame != nil {
		frames = append(frames, frame)
	}
	value["frames"] = frames
	return &typedFailure{kind: failure.kind, value: value, frames: frames}
}

func (r *runtime) makeFailure(s *Statement, kind, message string) error {
	definition := r.failures[kind]
	if definition == nil {
		return fmt.Errorf("unknown failure %s", kind)
	}
	frames := r.failureFrames()
	value := map[string]any{
		"kind": kind, "message": message, "retryable": false,
		"frames": frames,
	}
	if frame := r.failureStatementFrame(s); frame != nil {
		frames = append(frames, frame)
		value["frames"] = frames
	}
	seen := map[string]bool{}
	for _, field := range s.Body {
		if field.Kind != "field" {
			continue
		}
		name, expression, ok := strings.Cut(field.Text, " from ")
		if !ok {
			return fmt.Errorf("failure %s field must use NAME from VALUE", kind)
		}
		if seen[name] {
			return fmt.Errorf("failure %s field %s appears more than once", kind, name)
		}
		seen[name] = true
		declared, ok := failureFieldsByName(definition)[name]
		if !ok {
			return fmt.Errorf("failure %s has no field %s", kind, name)
		}
		v, err := r.eval(expression, nil)
		if err != nil {
			return err
		}
		if !typeMatchesRef(v, declared.Type, r.definitions) {
			return fmt.Errorf("%s.%s must be %s; received %s", kind, name, declared.Type.String(), valueTypeName(v))
		}
		value[name] = v
	}
	for _, field := range definition.Fields {
		if !field.Type.Optional && !seen[field.Name] {
			return fmt.Errorf("%s requires field %s as %s", kind, field.Name, field.Type.String())
		}
	}
	return &typedFailure{kind: kind, value: value, frames: frames}
}

func isFatalFailure(err error) bool {
	if err == nil {
		return false
	}
	var typed *typedFailure
	return !errors.As(err, &typed) && fatalParallel(err)
}

func (r *runtime) execute(s *Statement) error {
	m := match(s.Kind, s.Text)
	if binding := statementBindingName(s); binding != "" && s.Kind != "openStream" && s.Kind != "httpListen" {
		if existing, ok := r.env[binding].(*streamHandle); ok {
			existing.mu.Lock()
			active := existing.state == streamActive
			existing.mu.Unlock()
			if active {
				return fmt.Errorf("cannot overwrite active stream %s; consume or close it first", binding)
			}
		}
	}
	switch s.Kind {
	case "command", "parameter", "import", "package", "export", "define", "failure":
		return nil
	case "schema":
		r.schemas[m[1]] = s
		return nil
	case "to":
		r.functions[m[1]] = s
		return nil
	case "rethrow":
		if r.activeFailure == nil {
			return fmt.Errorf("rethrow requires an active failure handler")
		}
		return r.propagateFailure(r.activeFailure, s)
	case "passFailure":
		if r.activeFailure == nil {
			return fmt.Errorf("pass failure on requires an active failure handler")
		}
		return r.propagateFailure(r.activeFailure, s)
	case "finish":
		if m[1] == "" {
			return returnValue{hasValue: false}
		}
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		return returnValue{value: v, hasValue: true}
	case "send":
		if r.streamOutput == nil {
			return fmt.Errorf("send is only valid inside a streaming action")
		}
		value, err := r.eval(m[1], nil)
		if err != nil {
			return err
		}
		if !typeMatchesRef(value, r.streamItemType, r.definitions) {
			return fmt.Errorf("stream item must be %s; received %s", r.streamItemType.String(), valueTypeName(value))
		}
		select {
		case r.streamOutput <- value:
			return nil
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	case "recover":
		if r.activeFailure == nil {
			return fmt.Errorf("recover requires an active failure handler")
		}
		if m[1] == "" {
			return recoveryValue{}
		}
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		return recoveryValue{value: v, hasValue: true}
	case "fail":
		message := ""
		if m[2] != "" {
			var e error
			message, e = r.text(m[2])
			if e != nil {
				return e
			}
		}
		return r.makeFailure(s, m[1], message)
	case "capture":
		return r.capture(s, m)
	case "return":
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		return returnValue{value: v, hasValue: true}
	case "sent":
		m := matchSent(s.Text)
		mod, action, ok := r.vocabLookup(m[1])
		if !ok {
			return fmt.Errorf("unknown vocabulary word %q", m[1])
		}
		if e := r.tick(); e != nil {
			return e
		}
		vals, e := sentArguments(m)
		if e != nil {
			return e
		}
		return r.callModule(s, mod, action, m[1], vals, m[4])
	case "call":
		if strings.Contains(m[1], ".") {
			return r.callImported(s, m)
		}
		fn, ok := r.functions[m[1]]
		if !ok {
			return fmt.Errorf("unknown action %q", m[1])
		}
		decl, declErr := parseActionDecl(fn.Text)
		if declErr != nil {
			return declErr
		}
		vals, e := splitExpressions(m[2])
		if e != nil {
			return e
		}
		if len(vals) != len(decl.Params) {
			return fmt.Errorf("action %s expects %d arguments", m[1], len(decl.Params))
		}
		local := map[string]any{}
		if r.module == nil {
			for k, v := range r.env {
				local[k] = v
			}
		}
		localTypes := map[string]TypeRef{}
		for i, param := range decl.Params {
			v, e := r.eval(vals[i], nil)
			if e != nil {
				return e
			}
			if param.Type.Name != "any" && !typeMatchesRef(v, param.Type, r.definitions) {
				return fmt.Errorf("%s.%s must be %s; received %s", m[1], param.Name, param.Type.String(), valueTypeName(v))
			}
			local[param.Name] = v
			localTypes[param.Name] = param.Type.base()
		}
		if len(decl.Using) > 0 {
			fieldTypes := map[string]TypeRef{}
			def := r.definitions[decl.Params[0].Type.Name]
			if def != nil {
				for _, field := range def.Fields {
					fieldTypes[field.Name] = field.Type
				}
			}
			for _, field := range decl.Using {
				v, fieldType, err := propertyTyped(local[decl.Params[0].Name], field, decl.Params[0].Type.base(), r.definitions)
				if err != nil {
					return err
				}
				local[field] = v
				if declared, ok := fieldTypes[field]; ok {
					localTypes[field] = declared.base()
				} else {
					localTypes[field] = fieldType
				}
			}
		}
		outer := r.env
		outerImports, outerVocab, outerTypes, outerDefs, outerFailures := r.imports, r.vocab, r.types, r.definitions, r.failures
		if r.module != nil {
			r.imports = r.module.scope(m[1])
			r.vocab = r.module.vocabulary(m[1])
		}
		r.env = local
		r.types = localTypes
		definitionBase := r.p.Definitions
		if r.module != nil {
			definitionBase = r.module.Definitions
		}
		if r.module != nil {
			r.definitions, _ = r.module.definitionScope(r.imports)
		} else {
			r.definitions, _ = visibleDefinitions(definitionBase, r.imports)
		}
		if r.module != nil {
			r.failures, _ = visibleFailuresFrom(r.module.Failures, r.imports, "the current package")
		} else {
			r.failures, _ = visibleFailuresFrom(r.p.Failures, r.imports, "the current file")
		}
		r.depth++
		actionPath := r.logicalPath
		if r.module != nil && r.module.actionPaths[m[1]] != "" {
			actionPath = r.module.actionPaths[m[1]]
		}
		r.debugStack = append(r.debugStack, DebugFrame{Path: actionPath, Line: fn.Line, Column: 1, Name: m[1], Kind: "action", Text: fn.Text, Depth: r.depth})
		e = r.block(fn.Body)
		closeActionOwnedStreams(r.env, outer)
		r.debugStack = r.debugStack[:len(r.debugStack)-1]
		r.depth--
		calleeDefinitions := r.definitions
		r.env = outer
		r.imports = outerImports
		r.vocab = outerVocab
		r.types = outerTypes
		r.definitions = outerDefs
		r.failures = outerFailures
		var ret returnValue
		if errors.As(e, &ret) {
			if decl.HasResult && !ret.hasValue {
				return fmt.Errorf("action %s finished without a value", m[1])
			}
			if decl.HasResult && !typeMatchesRef(ret.value, decl.Result, calleeDefinitions) {
				return fmt.Errorf("action %s must finish with %s; received %s", m[1], decl.Result.String(), valueTypeName(ret.value))
			}
			if m[3] != "" && ret.hasValue {
				r.env[m[3]] = ret.value
			} else if m[3] != "" {
				return fmt.Errorf("action %s did not return a value", m[1])
			}
			return nil
		}
		if e == nil && decl.HasResult {
			return fmt.Errorf("action %s did not finish with a value", m[1])
		}
		if e == nil && m[3] != "" {
			return fmt.Errorf("action %s did not return a value", m[1])
		}
		return e
	case "httpGet", "httpPost", "httpRequest", "httpReadBody":
		return r.runHTTPCall(s, m)
	case "httpListen":
		return r.runHTTPListen(s, m)
	case "httpRespond", "httpRespondComplete":
		return r.runHTTPRespond(s, m)
	case "openStream":
		if existing, ok := r.env[m[3]].(*streamHandle); ok {
			existing.mu.Lock()
			active := existing.state == streamActive
			existing.mu.Unlock()
			if active {
				return fmt.Errorf("stream %s is already active; consume or close it before reopening", m[3])
			}
		}
		args, e := splitExpressions(m[2])
		if e != nil {
			return e
		}
		stream, e := r.openExternalStream(m[1], m[3], s.Line, args)
		if e == nil {
			r.env[m[3]] = stream
		}
		return e
	case "closeStream":
		stream, ok := r.env[m[1]].(*streamHandle)
		if !ok {
			return fmt.Errorf("%s is not an owned stream", m[1])
		}
		if e := stream.beginConsumption(); e != nil {
			return fmt.Errorf("%s: %w", m[1], e)
		}
		return stream.closeWithReason(r.ctx, "close stream")
	case "stopReading":
		return stopReadingValue{}
	case "streamFor":
		name := strings.TrimSpace(m[2])
		stream, ok := r.env[name].(*streamHandle)
		if !ok {
			return fmt.Errorf("%s is not an owned stream", name)
		}
		if e := stream.beginConsumption(); e != nil {
			return fmt.Errorf("%s: %w", name, e)
		}
		old, had := r.env[m[1]]
		defer func() {
			if had {
				r.env[m[1]] = old
			} else {
				delete(r.env, m[1])
			}
		}()
		for {
			item, more, e := stream.next(r.ctx)
			if e != nil {
				_ = stream.close(context.Background())
				return e
			}
			if !more {
				return nil
			}
			if !typeMatchesRef(item, stream.itemType, r.definitions) {
				_ = stream.close(context.Background())
				return fmt.Errorf("stream item must be %s; received %s", stream.itemType.String(), valueTypeName(item))
			}
			r.env[m[1]] = item
			e = r.block(s.Body)
			var stopped stopReadingValue
			if errors.As(e, &stopped) {
				var responseErr error
				if r.opts.httpServers != nil {
					responseErr = r.opts.httpServers.ensureResponded(item)
				}
				if responseErr != nil {
					r.recordHTTPRuntimeFailure(s.Line, responseErr)
				}
				return stream.closeWithReason(r.ctx, "stop reading")
			}
			if e != nil {
				var exit interface{ ExitCode() int }
				var replayErr *ReplayIntegrityError
				if !errors.As(e, &exit) && !errors.As(e, &replayErr) && !fatalParallel(e) && r.opts.httpServers != nil && r.opts.httpServers.completeUnhandledFailure(item, e) {
					r.recordHTTPRuntimeFailure(s.Line, e)
					continue
				}
				_ = stream.close(context.Background())
				return e
			}
			var responseErr error
			if r.opts.httpServers != nil {
				responseErr = r.opts.httpServers.ensureResponded(item)
			}
			if responseErr != nil {
				r.recordHTTPRuntimeFailure(s.Line, responseErr)
			}
		}
	case "collectStream":
		return r.materializeStream(s, m, false)
	case "remember":
		v, e := r.eval(m[1], nil)
		if containsStreamHandle(v) {
			return fmt.Errorf("cannot copy a stream with remember")
		}
		if e == nil {
			r.env[m[2]] = v
		}
		return e
	case "make":
		name, expr := m[1], strings.TrimPrefix(m[2], "as ")
		if source, ok := r.env[strings.TrimSpace(expr)].(*streamHandle); ok && source != nil {
			return fmt.Errorf("cannot copy stream %s with make", strings.TrimSpace(expr))
		}
		if !strings.HasPrefix(s.Text, "make ") {
			if _, ok := r.env[name]; !ok {
				return fmt.Errorf("cannot assign unknown name %q", name)
			}
		}
		if expr == "with:" || strings.HasSuffix(expr, " with:") {
			typeText := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(m[2], "as ")), "with:"))
			if typeText == "" {
				record := map[string]any{}
				for _, f := range s.Body {
					if f.Kind != "field" {
						continue
					}
					key, value, _ := strings.Cut(f.Text, " from ")
					v, e := r.eval(value, nil)
					if e != nil {
						return e
					}
					if containsStreamHandle(v) {
						return fmt.Errorf("cannot copy a stream with make")
					}
					record[key] = v
				}
				r.env[name] = record
				return nil
			}
			definition, ok := r.definitions[typeText]
			if !ok {
				return fmt.Errorf("unknown record type %q", typeText)
			}
			record := map[string]any{}
			seen := map[string]bool{}
			fields := fieldsByName(definition)
			for _, f := range s.Body {
				if f.Kind != "field" {
					continue
				}
				key, value, _ := strings.Cut(f.Text, " from ")
				if seen[key] {
					return fmt.Errorf("%s field %s appears more than once", typeText, key)
				}
				seen[key] = true
				field, known := fields[key]
				if !known {
					return fmt.Errorf("%s has no field %s", typeText, key)
				}
				v, e := r.eval(value, nil)
				if e != nil {
					return e
				}
				if containsStreamHandle(v) {
					return fmt.Errorf("cannot copy a stream with make")
				}
				if !typeMatchesRef(v, field.Type, r.definitions) {
					return fmt.Errorf("%s.%s must be %s; received %s", typeText, key, field.Type.String(), valueTypeName(v))
				}
				record[key] = v
			}
			for _, field := range definition.Fields {
				if !field.Type.Optional && !seen[field.Name] {
					return fmt.Errorf("%s requires field %s as %s", typeText, field.Name, field.Type.String())
				}
			}
			r.env[name] = record
			r.types[name] = TypeRef{Name: typeText}
			return nil
		}
		v, e := r.eval(expr, nil)
		if e == nil && containsStreamHandle(v) {
			return fmt.Errorf("cannot copy a stream with make")
		}
		if e == nil {
			if strings.HasPrefix(strings.TrimSpace(m[2]), "as ") {
				typeText := strings.TrimSpace(strings.TrimPrefix(m[2], "as "))
				t, typeErr := parseType(typeText, false)
				if typeErr == nil {
					if !typeMatchesRef(v, t, r.definitions) {
						return fmt.Errorf("%s must be %s; received %s", name, t.String(), valueTypeName(v))
					}
					r.types[name] = t.base()
				}
			}
			r.env[name] = v
		}
		return e
	case "read":
		path, e := r.path(m[1])
		if e != nil {
			return e
		}
		v, e := readData(path, m[2])
		if e == nil {
			r.env[m[3]] = v
		}
		return e
	case "find":
		dir, e := r.path(m[1])
		if e != nil {
			return e
		}
		pat, e := r.text(m[2])
		if e != nil {
			return e
		}
		if strings.ContainsAny(pat, "/\\") {
			return fmt.Errorf("matching pattern must be a filename glob")
		}
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		files := []any{}
		for _, entry := range entries {
			ok, e := filepath.Match(pat, entry.Name())
			if e != nil {
				return e
			}
			if ok && !entry.IsDir() {
				files = append(files, map[string]any{"path": filepath.Join(dir, entry.Name()), "name": entry.Name()})
			}
		}
		r.env[m[3]] = files
		return nil
	case "readEach":
		if existing, ok := r.env[m[1]].(*streamHandle); ok {
			existing.mu.Lock()
			active := existing.state == streamActive
			existing.mu.Unlock()
			if active {
				return fmt.Errorf("cannot overwrite active stream %s; consume or close it first", m[1])
			}
		}
		v, e := r.eval(m[2], nil)
		if e != nil {
			return e
		}
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("read each requires list")
		}
		all := []any{}
		for _, item := range a {
			if e = r.tick(); e != nil {
				return e
			}
			r.env[m[1]] = item
			var path string
			if x, ok := item.(string); ok {
				path = x
			} else {
				v, e := property(item, "path")
				if e != nil {
					return e
				}
				path, _ = v.(string)
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(r.opts.Dir, path)
			}
			v, e := readData(path, "lines of json")
			if e != nil {
				return e
			}
			all = append(all, v.([]any)...)
		}
		r.env[m[3]] = all
		return nil
	case "require":
		schema, ok := r.schemas[m[3]]
		if !ok {
			return fmt.Errorf("unknown schema %q", m[3])
		}
		v, e := r.eval(m[2], nil)
		if e != nil {
			return e
		}
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("require each expects list")
		}
		for i, item := range a {
			obj, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("record %d must be an object", i+1)
			}
			for _, f := range schema.Body {
				key, typ, _ := strings.Cut(f.Text, " as ")
				v, ok := obj[key]
				if !ok || !typeMatches(v, typ) {
					return fmt.Errorf("record %d field %s must be %s", i+1, key, typ)
				}
			}
		}
		return nil
	case "keep":
		a, e := r.list(m[1])
		if e != nil {
			return e
		}
		out := []any{}
		predicate := m[2]
		for _, item := range a {
			if e = r.tick(); e != nil {
				return e
			}
			var v any
			if jevPredicate(predicate) {
				v, e = r.judgePredicate(s, predicate, item)
			} else {
				v, e = r.eval(predicate, item)
			}
			if e != nil {
				return e
			}
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("where requires a boolean; inspect a judgment probability explicitly")
			}
			if b {
				out = append(out, item)
			}
		}
		r.env[m[1]] = out
		return nil
	case "sort":
		a, e := r.list(m[1])
		if e != nil {
			return e
		}
		type keyed struct{ v, key any }
		items := make([]keyed, len(a))
		for i, v := range a {
			k, e := r.eval(m[2], v)
			if e != nil {
				return e
			}
			items[i] = keyed{v, k}
		}
		var sortErr error
		sort.SliceStable(items, func(i, j int) bool {
			op := "<"
			if m[3] == "descending" {
				op = ">"
			}
			v, e := binary(op, items[i].key, items[j].key)
			if e != nil {
				sortErr = e
				return false
			}
			return v.(bool)
		})
		if sortErr != nil {
			return sortErr
		}
		out := make([]any, len(a))
		for i := range items {
			out[i] = items[i].v
		}
		r.env[m[1]] = out
		return nil
	case "group":
		a, e := r.list(m[1])
		if e != nil {
			return e
		}
		groups := []any{}
		indices := map[string]int{}
		for _, v := range a {
			k, e := r.eval(m[2], v)
			if e != nil {
				return e
			}
			key := fmt.Sprintf("%T:%s", k, display(k))
			idx, ok := indices[key]
			if !ok {
				idx = len(groups)
				indices[key] = idx
				groups = append(groups, map[string]any{"key": k, "items": []any{}})
			}
			g := groups[idx].(map[string]any)
			g["items"] = append(g["items"].([]any), v)
		}
		r.env[m[3]] = groups
		return nil
	case "folder":
		p, e := r.path(m[1])
		if e != nil {
			return e
		}
		return os.MkdirAll(p, 0755)
	case "map":
		return r.parallelMap(s, m)
	case "for":
		v, e := r.eval(m[2], nil)
		if e != nil {
			return e
		}
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("for each expects list")
		}
		start := 1
		if m[3] != "" {
			start, _ = strconv.Atoi(m[3])
		}
		old, had := r.env[m[1]]
		oldN, hadN := r.env["number"]
		defer func() {
			if had {
				r.env[m[1]] = old
			} else {
				delete(r.env, m[1])
			}
			if hadN {
				r.env["number"] = oldN
			} else {
				delete(r.env, "number")
			}
		}()
		for i, v := range a {
			if e = r.tick(); e != nil {
				return e
			}
			r.env[m[1]] = v
			r.env["number"] = float64(start + i)
			if e = r.block(s.Body); e != nil {
				return e
			}
		}
		return nil
	case "when":
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("when requires boolean")
		}
		if b {
			e = r.block(s.Body)
		}
		r.condition = b
		return e
	case "while":
		for {
			if e := r.tick(); e != nil {
				return e
			}
			v, e := r.eval(m[1], nil)
			if e != nil {
				return e
			}
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("while requires boolean")
			}
			if !b {
				return nil
			}
			if e = r.block(s.Body); e != nil {
				return e
			}
		}
	case "repeat":
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		n, ok := number(v)
		if !ok || n < 0 || n != math.Trunc(n) {
			return fmt.Errorf("repeat requires nonnegative integer")
		}
		for i := 0.; i < n; i++ {
			if e = r.tick(); e != nil {
				return e
			}
			if e = r.block(s.Body); e != nil {
				return e
			}
		}
		return nil
	case "take":
		if m[1] == "first" {
			if _, ok := r.env[strings.TrimSpace(m[3])].(*streamHandle); ok {
				adapted := []string{m[0], m[2], m[3], m[4]}
				return r.materializeStream(s, adapted, true)
			}
		}
		n, e := r.eval(m[2], nil)
		if e != nil {
			return e
		}
		v, e := r.eval(m[3], nil)
		if e != nil {
			return e
		}
		v, e = sliceItems(v, n, m[1] == "last")
		if e == nil {
			r.env[m[4]] = v
		}
		return e
	case "evaluate":
		return r.evaluateBatch(s, m)
	case "classify", "score", "judge":
		return r.classify(s, m)
	case "append":
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		if containsStreamHandle(v) {
			return fmt.Errorf("cannot copy a stream with append")
		}
		a, e := r.list(m[2])
		if e != nil {
			return e
		}
		r.env[m[2]] = append(a, v)
		return nil
	case "save":
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		var path string
		if m[3] != "" {
			path, e = r.path(m[3])
		} else {
			var dir, name string
			dir, e = r.path(m[4])
			if e == nil {
				name, e = r.text(m[5])
				if filepath.Base(name) != name || name == "." || name == ".." {
					return fmt.Errorf("named requires a filename without path separators")
				}
				path = filepath.Join(dir, name)
			}
		}
		if e != nil {
			return e
		}
		var data []byte
		if m[2] == "json" {
			data, e = json.MarshalIndent(v, "", "  ")
			data = append(data, '\n')
		} else {
			data = []byte(display(v))
		}
		if e != nil {
			return e
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		for _, h := range s.Body {
			if h.Kind == "handler" && h.Text == "on existing replace" {
				flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}
		}
		f, e := os.OpenFile(path, flags, 0644)
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	case "show":
		expr := m[1]
		if before, after, ok := splitOutside(expr, " as table"); ok {
			v, e := r.eval(before, nil)
			if e != nil {
				return e
			}
			a, ok := v.([]any)
			if !ok {
				return fmt.Errorf("table requires a list")
			}
			cols := []string{}
			if strings.HasPrefix(after, " with ") {
				for _, c := range strings.Split(strings.TrimPrefix(after, " with "), ",") {
					cols = append(cols, strings.TrimSpace(c))
				}
			} else if len(a) > 0 {
				if row, ok := a[0].(map[string]any); ok {
					cols = sortedKeys(row)
				}
			}
			w := tabwriter.NewWriter(r.opts.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, strings.Join(cols, "\t"))
			for _, item := range a {
				row := []string{}
				for _, c := range cols {
					v, e := property(item, c)
					if e != nil {
						return e
					}
					row = append(row, strings.ReplaceAll(display(v), "\n", " "))
				}
				fmt.Fprintln(w, strings.Join(row, "\t"))
			}
			return w.Flush()
		}
		v, e := r.eval(expr, nil)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(r.opts.Stdout, display(v))
		return e
	case "stop":
		if m[1] == "" {
			return &StopError{Message: "script stopped"}
		}
		v, e := r.text(m[1])
		if e != nil {
			return e
		}
		return &StopError{Message: v}
	default:
		return fmt.Errorf("construction %q is not executable here", s.Kind)
	}
}

func (r *runtime) recordHTTPRuntimeFailure(line int, err error) {
	trace := Trace{Line: line, Question: "HTTP request response obligation", Model: "http", Decision: "safe fallback response", Reason: err.Error()}
	var failure *typedFailure
	if errors.As(err, &failure) {
		trace.Answer = failure.value
	}
	r.result.Traces = append(r.result.Traces, trace)
	if r.opts.OnTrace != nil {
		r.opts.OnTrace(trace)
	}
}

// callImported executes one qualified call with definition-site semantics:
// the target's module scope (its own actions, schemas, and imports) replaces
// the caller's, and the body runs in a fresh environment holding only its
// arguments. Caller variables never leak in.
func (r *runtime) callImported(s *Statement, m []string) error {
	alias, action, _ := strings.Cut(m[1], ".")
	mod := r.imports[alias]
	if mod == nil {
		return fmt.Errorf("unknown module alias %q", alias)
	}
	if !mod.Exports[action] {
		if mod.Actions[action] != nil || mod.Native[action].Name != "" {
			return fmt.Errorf("action %s is not exported by module %s", action, mod.Name)
		}
		return fmt.Errorf("unknown action %s.%s", alias, action)
	}
	if e := r.tick(); e != nil {
		return e
	}
	vals, e := splitExpressions(m[2])
	if e != nil {
		return e
	}
	return r.callModule(s, mod, action, m[1], vals, m[3])
}

// vocabLookup resolves a sentence-call name at runtime: qualified names use
// the active import scope, bare names the active definition-site vocabulary.
func (r *runtime) vocabLookup(name string) (*Module, string, bool) {
	if alias, word, found := strings.Cut(name, "."); found {
		mod := r.imports[alias]
		if mod == nil {
			return nil, "", false
		}
		action := mod.Words[word]
		if action == "" || !mod.Exports[action] {
			return nil, "", false
		}
		return mod, action, true
	}
	if r.vocab == nil {
		return nil, "", false
	}
	t, ok := r.vocab.words[name]
	if !ok {
		return nil, "", false
	}
	return t.module, t.action, true
}

// callModule executes one resolved module operation for both qualified calls
// and sentence calls: native operations run their typed Go function, actions
// run with definition-site scope, fresh argument environment, and restored
// caller state afterwards.
func (r *runtime) callModule(s *Statement, mod *Module, action, display string, vals []string, called string) error {
	if op, ok := mod.Native[action]; ok {
		if mod.external != nil && len(op.Targets) > 0 && !containsString(op.Targets, "native") && !containsString(op.Targets, currentExternalTarget()) {
			return fmt.Errorf("%s is unavailable on %s", display, currentExternalTarget())
		}
		args := make([]any, 0, len(vals))
		for _, v := range vals {
			x, e := r.eval(v, nil)
			if e != nil {
				return e
			}
			args = append(args, x)
		}
		if mod.external != nil {
			if len(args) != len(op.Params) {
				return fmt.Errorf("%s expects %d argument(s), got %d", op.Name, len(op.Params), len(args))
			}
			for i, parameter := range op.Params {
				typ, parseErr := parseType(parameter.Type, true)
				if parseErr != nil || !typeMatchesRef(args[i], typ, mod.Definitions) {
					return fmt.Errorf("%s argument %s must be %s", op.Name, parameter.Name, parameter.Type)
				}
			}
		} else if e := op.check(args); e != nil {
			return e
		}
		var out any
		var e error
		started := time.Now()
		if mod.external != nil && r.opts.Debugger != nil {
			frame := DebugFrame{Name: display, Path: mod.external.definitionPath, Line: 1, Column: 1, Kind: "external", Text: "external module call", Depth: r.depth + 1}
			if debugErr := r.opts.Debugger.BeforeStatement(r.ctx, frame, append(append([]DebugFrame(nil), r.debugStack...), frame), map[string]any{}); debugErr != nil {
				return debugErr
			}
		}
		if op.ContextFn != nil {
			if r.parallelDepth > 0 {
				for _, effect := range stdDocs[mod.Key+"."+op.Name].effects {
					if effect == "stdin" {
						return fmt.Errorf("stdin is not available inside parallel maps")
					}
				}
			}
			out, e = op.ContextFn(r.ctx, r.opts, args)
		} else {
			out, e = op.Fn(args)
		}
		if e != nil {
			r.externalTrace(mod, action, started, e)
			return fmt.Errorf("%s: %w", display, e)
		}
		r.externalTrace(mod, action, started, nil)
		if op.Result != "" && op.Result != "none" && op.Result != "any" {
			matches := typeMatches(out, op.Result)
			if mod.external != nil {
				typ, parseErr := parseType(op.Result, true)
				matches = parseErr == nil && typeMatchesRef(out, typ, mod.Definitions)
			}
			if !matches {
				return fmt.Errorf("%s returned %s; expected %s", display, valueTypeName(out), op.Result)
			}
		}
		if called != "" {
			r.env[called] = out
		}
		return nil
	}
	fn := mod.Actions[action]
	if fn == nil {
		return fmt.Errorf("%s is not a callable action", display)
	}
	decl, declErr := parseActionDecl(fn.Text)
	if declErr != nil {
		return declErr
	}
	if len(vals) != len(decl.Params) {
		return fmt.Errorf("action %s expects %d arguments", display, len(decl.Params))
	}
	if r.depth >= 128 {
		return fmt.Errorf("action recursion limit exceeded")
	}
	local := map[string]any{}
	localTypes := map[string]TypeRef{}
	actionDefs, _ := mod.definitionScope(mod.scope(action))
	for i, param := range decl.Params {
		v, e := r.eval(vals[i], nil)
		if e != nil {
			return e
		}
		if param.Type.Name != "any" && !typeMatchesRef(v, param.Type, actionDefs) {
			return fmt.Errorf("%s.%s must be %s; received %s", display, param.Name, param.Type.String(), valueTypeName(v))
		}
		local[param.Name] = v
		localTypes[param.Name] = param.Type.base()
	}
	if len(decl.Using) > 0 {
		for _, field := range decl.Using {
			v, fieldType, err := propertyTyped(local[decl.Params[0].Name], field, decl.Params[0].Type.base(), actionDefs)
			if err != nil {
				return err
			}
			local[field] = v
			localTypes[field] = fieldType
		}
	}
	outer := r.env
	outerFns, outerSchemas, outerImports, outerVocab, outerTypes, outerDefs, outerFailures := r.functions, r.schemas, r.imports, r.vocab, r.types, r.definitions, r.failures
	outerModule := r.module
	r.module = mod
	imports := mod.scope(action)
	if imports == nil {
		imports = map[string]*Module{}
	}
	r.env = local
	r.functions, r.schemas, r.imports, r.vocab = copyStatements(mod.Actions), copyStatements(mod.Schemas), imports, mod.vocabulary(action)
	r.types = localTypes
	r.definitions = actionDefs
	r.failures, _ = visibleFailuresFrom(mod.Failures, imports, "the current package")
	r.depth++
	modulePath := mod.actionPaths[action]
	if modulePath == "" {
		modulePath = mod.Key
	}
	if !filepath.IsAbs(modulePath) && r.logicalPath != "" {
		modulePath = filepath.Join(r.opts.Dir, modulePath)
	}
	r.debugStack = append(r.debugStack, DebugFrame{Path: modulePath, Line: fn.Line, Column: 1, Name: display, Kind: "action", Text: fn.Text, Depth: r.depth})
	e := r.block(fn.Body)
	closeActionOwnedStreams(r.env, outer)
	r.debugStack = r.debugStack[:len(r.debugStack)-1]
	r.depth--
	r.env = outer
	r.functions, r.schemas, r.imports, r.vocab = outerFns, outerSchemas, outerImports, outerVocab
	r.types, r.definitions = outerTypes, outerDefs
	r.failures = outerFailures
	r.module = outerModule
	var ret returnValue
	if errors.As(e, &ret) {
		if decl.HasResult && !ret.hasValue {
			return fmt.Errorf("action %s finished without a value", display)
		}
		if decl.HasResult && !typeMatchesRef(ret.value, decl.Result, actionDefs) {
			return fmt.Errorf("action %s must finish with %s; received %s", display, decl.Result.String(), valueTypeName(ret.value))
		}
		if called != "" && ret.hasValue {
			r.env[called] = ret.value
		} else if called != "" {
			return fmt.Errorf("action %s did not return a value", display)
		}
		return nil
	}
	if e != nil {
		// Lines inside a module body are module-relative; name the module.
		return fmt.Errorf("module %s: %w", mod.Name, e)
	}
	if decl.HasResult {
		return fmt.Errorf("action %s did not finish with a value", display)
	}
	if called != "" {
		return fmt.Errorf("action %s did not return a value", display)
	}
	return nil
}

func (r *runtime) externalTrace(mod *Module, action string, started time.Time, err error) {
	if mod == nil || mod.external == nil {
		return
	}
	trace := Trace{Line: 0, Question: mod.Name + "." + action, Model: "external", Decision: "invoke", Milliseconds: time.Since(started).Milliseconds()}
	if err != nil {
		trace.Reason = "failed"
	} else {
		trace.Reason = "completed"
	}
	r.result.Traces = append(r.result.Traces, trace)
	if r.opts.OnTrace != nil {
		r.opts.OnTrace(trace)
	}
}

func (r *runtime) capture(s *Statement, m []string) error {
	const hiddenResult = "__captured_value"
	previous, hadPrevious := r.env[hiddenResult]
	defer func() {
		if hadPrevious {
			r.env[hiddenResult] = previous
		} else {
			delete(r.env, hiddenResult)
		}
	}()
	callText := "call " + m[1]
	if m[2] != "" {
		callText += " with " + m[2]
	}
	if r.captureTargetHasResult(m[1]) {
		callText += " called " + hiddenResult
	}
	call := &Statement{Kind: "call", Text: callText, Line: s.Line}
	err := r.execute(call)
	if err == nil {
		outcome := map[string]any{
			"succeeded": true,
			"value":     nil,
			"failure":   nil,
		}
		if value, ok := r.env[hiddenResult]; ok {
			outcome["value"] = value
		}
		r.env[m[3]] = outcome
		return nil
	}
	var typed *typedFailure
	if !errors.As(err, &typed) || isFatalFailure(err) {
		return err
	}
	r.env[m[3]] = map[string]any{
		"succeeded": false,
		"value":     nil,
		"failure":   FailureValue(err),
	}
	return nil
}

func (r *runtime) captureTargetHasResult(target string) bool {
	if strings.Contains(target, ".") {
		alias, action, _ := strings.Cut(target, ".")
		if mod := r.imports[alias]; mod != nil {
			if _, ok := mod.Native[action]; ok {
				return true
			}
			if fn := mod.Actions[action]; fn != nil {
				decl, _ := parseActionDecl(fn.Text)
				return decl.HasResult
			}
		}
		return false
	}
	if fn := r.functions[target]; fn != nil {
		decl, _ := parseActionDecl(fn.Text)
		return decl.HasResult
	}
	return false
}

func readData(path, format string) (any, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if info.Size() > 16<<20 {
		return nil, fmt.Errorf("input exceeds 16 MiB limit: %s", path)
	}
	data, e := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("input exceeds 16 MiB limit")
	}
	switch format {
	case "text":
		return string(data), nil
	case "json":
		var v any
		if e = decodeExternalJSON(data, &v); e != nil {
			return nil, e
		}
		return normalizeExternalAny(v)
	case "lines of json":
		out := []any{}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var v any
			if e = decodeExternalJSON([]byte(line), &v); e != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, i+1, e)
			}
			v, e = normalizeExternalAny(v)
			if e != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, i+1, e)
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown format %s", format)
}
func typeMatches(v any, t string) bool {
	switch t {
	case "text":
		_, ok := v.(string)
		return ok
	case "timestamp":
		s, ok := v.(string)
		if !ok {
			return false
		}
		_, e := time.Parse(time.RFC3339Nano, s)
		return e == nil
	case "number":
		_, ok := number(v)
		return ok
	case "integer":
		n, ok := number(v)
		return ok && n == math.Trunc(n)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "record":
		_, ok := v.(map[string]any)
		return ok
	case "list":
		_, ok := v.([]any)
		return ok
	}
	return false
}
func splitExpressions(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []string
	start, depth := 0, 0
	quoted, esc := false, false
	for i, c := range s {
		if esc {
			esc = false
			continue
		}
		if quoted && c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if quoted {
			continue
		}
		if c == '(' || c == '[' || c == '{' {
			depth++
		}
		if c == ')' || c == ']' || c == '}' {
			depth--
		}
		if c == ',' && depth == 0 {
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if quoted || depth != 0 {
		return nil, fmt.Errorf("unbalanced argument list")
	}
	return append(out, strings.TrimSpace(s[start:])), nil
}

// LoadEnv loads local KEY=VALUE credentials without changing existing environment values.
func LoadEnv(path string) error {
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if os.Getenv(key) == "" {
			if e = os.Setenv(key, value); e != nil {
				return e
			}
		}
	}
	return sc.Err()
}

func remapStatements(statements []*Statement, sourceMap []int) {
	for _, s := range statements {
		if s.Line > 0 && s.Line <= len(sourceMap) {
			s.Line = sourceMap[s.Line-1]
		}
		remapStatements(s.Body, sourceMap)
	}
}
