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
}
type returnValue struct{ value any }

func (r returnValue) Error() string { return "return outside action" }

// Run resolves permitted source constructions, validates the entire lowered
// program, and executes it with a shared interpretation/runtime request budget.
func Run(ctx context.Context, p *Program, opts Options) (result *Result, err error) {
	if p == nil {
		return nil, fmt.Errorf("missing program")
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
	r := &runtime{shared: &executionState{}, logicalPath: logicalPath, ctx: ctx, p: p, opts: opts, replay: replay, env: map[string]any{}, functions: map[string]*Statement{}, schemas: map[string]*Statement{}, imports: map[string]*Module{}, result: &Result{Traces: []Trace{}}}
	if p.Modules != nil {
		r.imports = p.Modules.Aliases
		r.vocab = p.Modules.vocab
	}
	for k, v := range opts.Args {
		r.env[k] = v
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
		r.result.Variables = r.env
		r.result.Steps = int(r.shared.steps.Load())
		r.result.Usage = opts.Budget.Snapshot()
		result = r.result
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
func (r *runtime) eval(s string, item any) (any, error) { return evaluate(s, r.env, item) }
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
			var exit interface{ ExitCode() int }
			var replayErr *ReplayIntegrityError
			if errors.As(err, &exit) || errors.As(err, &replayErr) {
				return err
			}
			if errors.As(err, &ret) {
				return err
			}
			if r.ctx.Err() != nil {
				return fmt.Errorf("line %d: %w", s.Line, r.ctx.Err())
			}
			handled := false
			for _, h := range s.Body {
				if h.Kind == "handler" && match("handler", h.Text)[1] == "failure" {
					r.env["error"] = err.Error()
					r.env["failure"] = FailureValue(err)
					outerFailure := r.activeFailure
					r.activeFailure = err
					err = r.handler(h)
					r.activeFailure = outerFailure
					handled = true
					break
				}
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
	frame := DebugFrame{Path: r.logicalPath, Line: s.Line, Column: 1, Kind: s.Kind, Text: s.Text, Depth: r.depth}
	stack := make([]DebugFrame, len(r.debugStack))
	copy(stack, r.debugStack)
	variables, err := cloneValue(r.env)
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
func (r *runtime) execute(s *Statement) error {
	m := match(s.Kind, s.Text)
	switch s.Kind {
	case "command", "parameter", "import", "package", "export":
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
		return r.activeFailure
	case "return":
		v, e := r.eval(m[1], nil)
		if e != nil {
			return e
		}
		return returnValue{v}
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
		fm := match("to", fn.Text)
		names := []string{}
		if fm[2] != "" {
			names = strings.Split(fm[2], ",")
		}
		vals, e := splitExpressions(m[2])
		if e != nil {
			return e
		}
		if len(vals) != len(names) {
			return fmt.Errorf("action %s expects %d arguments", m[1], len(names))
		}
		local := map[string]any{}
		if r.module == nil {
			for k, v := range r.env {
				local[k] = v
			}
		}
		for i, name := range names {
			v, e := r.eval(vals[i], nil)
			if e != nil {
				return e
			}
			local[strings.TrimSpace(name)] = v
		}
		outer := r.env
		outerImports, outerVocab := r.imports, r.vocab
		if r.module != nil {
			r.imports = r.module.scope(m[1])
			r.vocab = r.module.vocabulary(m[1])
		}
		r.env = local
		r.depth++
		r.debugStack = append(r.debugStack, DebugFrame{Path: r.logicalPath, Line: fn.Line, Column: 1, Name: m[1], Kind: "action", Text: fn.Text, Depth: r.depth})
		e = r.block(fn.Body)
		r.debugStack = r.debugStack[:len(r.debugStack)-1]
		r.depth--
		r.env = outer
		r.imports = outerImports
		r.vocab = outerVocab
		var ret returnValue
		if errors.As(e, &ret) {
			if m[3] != "" {
				r.env[m[3]] = ret.value
			}
			return nil
		}
		if e == nil && m[3] != "" {
			return fmt.Errorf("action %s did not return a value", m[1])
		}
		return e
	case "remember":
		v, e := r.eval(m[1], nil)
		if e == nil {
			r.env[m[2]] = v
		}
		return e
	case "make":
		name, expr := m[1], strings.TrimPrefix(m[2], "as ")
		if !strings.HasPrefix(s.Text, "make ") {
			if _, ok := r.env[name]; !ok {
				return fmt.Errorf("cannot assign unknown name %q", name)
			}
		}
		if expr == "with:" {
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
				record[key] = v
			}
			r.env[name] = record
			return nil
		}
		v, e := r.eval(expr, nil)
		if e == nil {
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
			return fmt.Errorf("script stopped")
		}
		v, e := r.text(m[1])
		if e != nil {
			return e
		}
		return fmt.Errorf("%s", v)
	default:
		return fmt.Errorf("construction %q is not executable here", s.Kind)
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
		args := make([]any, 0, len(vals))
		for _, v := range vals {
			x, e := r.eval(v, nil)
			if e != nil {
				return e
			}
			args = append(args, x)
		}
		if e := op.check(args); e != nil {
			return e
		}
		var out any
		var e error
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
			return fmt.Errorf("%s: %w", display, e)
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
	fm := match("to", fn.Text)
	names := []string{}
	if fm[2] != "" {
		names = strings.Split(fm[2], ",")
	}
	if len(vals) != len(names) {
		return fmt.Errorf("action %s expects %d arguments", display, len(names))
	}
	if r.depth >= 128 {
		return fmt.Errorf("action recursion limit exceeded")
	}
	local := map[string]any{}
	for i, name := range names {
		v, e := r.eval(vals[i], nil)
		if e != nil {
			return e
		}
		local[strings.TrimSpace(name)] = v
	}
	outer := r.env
	outerFns, outerSchemas, outerImports, outerVocab := r.functions, r.schemas, r.imports, r.vocab
	outerModule := r.module
	r.module = mod
	imports := mod.scope(action)
	if imports == nil {
		imports = map[string]*Module{}
	}
	r.env = local
	r.functions, r.schemas, r.imports, r.vocab = copyStatements(mod.Actions), copyStatements(mod.Schemas), imports, mod.vocabulary(action)
	r.depth++
	modulePath := mod.Key
	if !filepath.IsAbs(modulePath) && r.logicalPath != "" {
		modulePath = filepath.Join(r.opts.Dir, modulePath)
	}
	r.debugStack = append(r.debugStack, DebugFrame{Path: modulePath, Line: fn.Line, Column: 1, Name: display, Kind: "action", Text: fn.Text, Depth: r.depth})
	e := r.block(fn.Body)
	r.debugStack = r.debugStack[:len(r.debugStack)-1]
	r.depth--
	r.env = outer
	r.functions, r.schemas, r.imports, r.vocab = outerFns, outerSchemas, outerImports, outerVocab
	r.module = outerModule
	var ret returnValue
	if errors.As(e, &ret) {
		if called != "" {
			r.env[called] = ret.value
		}
		return nil
	}
	if e != nil {
		// Lines inside a module body are module-relative; name the module.
		return fmt.Errorf("module %s: %w", mod.Name, e)
	}
	if called != "" {
		return fmt.Errorf("action %s did not return a value", display)
	}
	return nil
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
		e = json.Unmarshal(data, &v)
		return v, e
	case "lines of json":
		out := []any{}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var v any
			if e = json.Unmarshal([]byte(line), &v); e != nil {
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
