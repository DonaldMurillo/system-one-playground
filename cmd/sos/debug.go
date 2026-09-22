package main

// This file implements the Debug Adapter Protocol boundary used by the VS
// Code extension. The language runtime owns stepping and snapshots; this
// adapter translates those capabilities into ordinary DAP requests/events so
// other DAP clients can use SysOneScript too.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

type dapRequest struct {
	Type      string          `json:"type"`
	Seq       int             `json:"seq"`
	Command   string          `json:"command"`
	Arguments json.RawMessage `json:"arguments"`
}

type dapBreakpoint struct {
	Line         int
	Condition    string
	LogMessage   string
	HitCondition string
	Hits         int
	Verified     bool
}

type debugLaunch struct {
	Program       string   `json:"program"`
	Cwd           string   `json:"cwd"`
	Args          []string `json:"args"`
	StopOnEntry   bool     `json:"stopOnEntry"`
	Model         string   `json:"model"`
	MaxCalls      int      `json:"maxCalls"`
	StreamControl string   `json:"streamControl"`
	StreamToken   string   `json:"streamToken"`
	StreamSession string   `json:"streamSession"`
}

type debugSnapshot struct {
	Frame     sos.DebugFrame
	Stack     []sos.DebugFrame
	Variables map[string]any
}

type debugSession struct {
	server *dapServer
	mu     sync.Mutex
	cond   *sync.Cond

	current          debugSnapshot
	hasCurrent       bool
	paused           bool
	pauseReason      string
	pauseRequested   bool
	terminated       bool
	stopOnEntry      bool
	firstStop        bool
	stepMode         string
	stepDepth        int
	stepLine         int
	breakOnException bool
	breakpoints      map[string]map[int]*dapBreakpoint
	variableRefs     map[int]any
	nextVarRef       int
}

func newDebugSession(server *dapServer) *debugSession {
	s := &debugSession{server: server, breakpoints: map[string]map[int]*dapBreakpoint{}, variableRefs: map[int]any{}, nextVarRef: 100}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *debugSession) BeforeStatement(ctx context.Context, frame sos.DebugFrame, stack []sos.DebugFrame, variables map[string]any) error {
	s.mu.Lock()
	s.current = debugSnapshot{Frame: frame, Stack: append([]sos.DebugFrame(nil), stack...), Variables: variables}
	s.hasCurrent = true
	shouldStop, reason, log := s.shouldStopLocked(frame, variables)
	if log != "" {
		s.mu.Unlock()
		s.server.output("console", log+"\n")
		s.mu.Lock()
	}
	if s.pauseRequested {
		shouldStop = true
		reason = "pause"
		s.pauseRequested = false
	}
	if !shouldStop || s.terminated {
		s.mu.Unlock()
		if s.terminated {
			return context.Canceled
		}
		return ctx.Err()
	}
	s.paused = true
	s.pauseReason = reason
	s.variableRefs = map[int]any{1: variables}
	s.nextVarRef = 100
	s.mu.Unlock()
	s.server.event("stopped", map[string]any{"reason": reason, "threadId": 1, "allThreadsStopped": true})

	s.mu.Lock()
	defer s.mu.Unlock()
	for s.paused && !s.terminated {
		s.cond.Wait()
	}
	if s.terminated {
		return context.Canceled
	}
	return ctx.Err()
}

func (s *debugSession) shouldStopLocked(frame sos.DebugFrame, variables map[string]any) (bool, string, string) {
	path := cleanDebugPath(frame.Path)
	points := s.breakpoints[path]
	if point := points[frame.Line]; point != nil {
		point.Hits++
		if point.HitCondition != "" && !hitConditionMatches(point.HitCondition, point.Hits) {
			return false, "", ""
		}
		if point.Condition != "" {
			value, err := sos.EvaluateDebugExpression(point.Condition, variables)
			if err != nil || value != true {
				return false, "", ""
			}
		}
		if point.LogMessage != "" {
			return false, "", interpolateLogpoint(point.LogMessage, variables)
		}
		return true, "breakpoint", ""
	}
	if s.stopOnEntry && !s.firstStop {
		s.firstStop = true
		return true, "entry", ""
	}
	if s.stepMode != "" {
		switch s.stepMode {
		case "next":
			if frame.Line != s.stepLine && frame.Depth <= s.stepDepth {
				s.stepMode = ""
				return true, "step", ""
			}
		case "in":
			if frame.Line != s.stepLine {
				s.stepMode = ""
				return true, "step", ""
			}
		case "out":
			if frame.Depth < s.stepDepth {
				s.stepMode = ""
				return true, "step", ""
			}
		}
	}
	return false, "", ""
}

func (s *debugSession) resume(mode string) {
	s.mu.Lock()
	if s.hasCurrent && mode != "continue" {
		s.stepMode = mode
		s.stepDepth = s.current.Frame.Depth
		s.stepLine = s.current.Frame.Line
	} else {
		s.stepMode = ""
	}
	s.paused = false
	s.cond.Broadcast()
	s.mu.Unlock()
	s.server.event("continued", map[string]any{"threadId": 1, "allThreadsContinued": true})
}

func (s *debugSession) requestPause() {
	s.mu.Lock()
	s.pauseRequested = true
	s.mu.Unlock()
}

func (s *debugSession) terminate() {
	s.mu.Lock()
	s.terminated = true
	s.paused = false
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *debugSession) snapshot() (debugSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasCurrent {
		return debugSnapshot{}, false
	}
	return s.current, true
}

type dapServer struct {
	in          *bufio.Reader
	out         io.Writer
	errOut      io.Writer
	writeMu     sync.Mutex
	seq         int
	session     *debugSession
	launch      *debugLaunch
	started     bool
	cancel      context.CancelFunc
	processDone chan struct{}
}

func runDebugServer(input io.Reader, output, errOutput io.Writer) int {
	server := &dapServer{in: bufio.NewReader(input), out: output, errOut: errOutput, seq: 1}
	server.session = newDebugSession(server)
	for {
		request, err := readDAP(server.in)
		if errors.Is(err, io.EOF) {
			server.stop()
			return 0
		}
		if err != nil {
			fmt.Fprintln(errOutput, "sos debug:", err)
			server.stop()
			return 1
		}
		if err := server.handle(request); err != nil {
			server.respond(request.Seq, request.Command, nil, &dapError{Code: 0, Message: err.Error()})
		}
	}
}

func (s *dapServer) handle(request dapRequest) error {
	switch request.Command {
	case "initialize":
		s.respond(request.Seq, request.Command, map[string]any{
			"supportsConfigurationDoneRequest":  true,
			"supportsConditionalBreakpoints":    true,
			"supportsHitConditionalBreakpoints": true,
			"supportsLogPoints":                 true,
			"supportsEvaluateForHovers":         true,
			"supportsExceptionInfoRequest":      true,
			"supportsTerminateRequest":          true,
			"supportsRestartRequest":            false,
			"supportsSetVariable":               false,
		}, nil)
		s.event("initialized", map[string]any{})
		return nil
	case "launch":
		var launch debugLaunch
		if err := json.Unmarshal(request.Arguments, &launch); err != nil {
			return fmt.Errorf("invalid launch arguments: %w", err)
		}
		if launch.Program == "" {
			return errors.New("launch requires program")
		}
		if !filepath.IsAbs(launch.Program) {
			base := launch.Cwd
			if base == "" {
				base, _ = os.Getwd()
			}
			launch.Program = filepath.Join(base, launch.Program)
		}
		launch.Program, _ = filepath.Abs(launch.Program)
		if launch.Cwd == "" {
			launch.Cwd = filepath.Dir(launch.Program)
		}
		s.launch = &launch
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	case "setBreakpoints":
		return s.setBreakpoints(request)
	case "configurationDone":
		if err := s.start(); err != nil {
			s.respond(request.Seq, request.Command, nil, &dapError{Message: err.Error()})
			return nil
		}
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	case "threads":
		if s.started {
			s.respond(request.Seq, request.Command, map[string]any{"threads": []any{map[string]any{"id": 1, "name": "SysOneScript"}}}, nil)
		} else {
			s.respond(request.Seq, request.Command, map[string]any{"threads": []any{}}, nil)
		}
		return nil
	case "stackTrace":
		return s.stackTrace(request)
	case "scopes":
		return s.scopes(request)
	case "variables":
		return s.variables(request)
	case "evaluate":
		return s.evaluate(request)
	case "setExceptionBreakpoints":
		var args struct {
			Filters []string `json:"filters"`
		}
		if err := json.Unmarshal(request.Arguments, &args); err != nil {
			return err
		}
		s.session.mu.Lock()
		s.session.breakOnException = len(args.Filters) > 0
		s.session.mu.Unlock()
		s.respond(request.Seq, request.Command, map[string]any{"breakpoints": []any{}}, nil)
		return nil
	case "continue":
		s.session.resume("continue")
		s.respond(request.Seq, request.Command, map[string]any{"allThreadsContinued": true}, nil)
		return nil
	case "next":
		s.session.resume("next")
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	case "stepIn":
		s.session.resume("in")
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	case "stepOut":
		s.session.resume("out")
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	case "pause":
		s.session.requestPause()
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	case "terminate", "disconnect":
		s.stop()
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	case "exceptionInfo":
		s.respond(request.Seq, request.Command, map[string]any{"exceptionId": "runtime", "description": "SysOneScript runtime exception"}, nil)
		return nil
	default:
		// VS Code sends a few optional requests (for example cancel). A clean
		// empty response keeps the adapter compatible without pretending to
		// support a capability it has not advertised.
		s.respond(request.Seq, request.Command, map[string]any{}, nil)
		return nil
	}
}

func (s *dapServer) setBreakpoints(request dapRequest) error {
	var args struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
		Breakpoints []struct {
			Line         int    `json:"line"`
			Condition    string `json:"condition"`
			LogMessage   string `json:"logMessage"`
			HitCondition string `json:"hitCondition"`
		} `json:"breakpoints"`
	}
	if err := json.Unmarshal(request.Arguments, &args); err != nil {
		return fmt.Errorf("invalid breakpoints: %w", err)
	}
	path := cleanDebugPath(args.Source.Path)
	points := map[int]*dapBreakpoint{}
	result := []any{}
	for _, raw := range args.Breakpoints {
		if raw.Line < 1 {
			continue
		}
		point := &dapBreakpoint{Line: raw.Line, Condition: raw.Condition, LogMessage: raw.LogMessage, HitCondition: raw.HitCondition, Verified: true}
		points[raw.Line] = point
		result = append(result, map[string]any{"verified": true, "line": raw.Line})
	}
	s.session.mu.Lock()
	s.session.breakpoints[path] = points
	s.session.mu.Unlock()
	s.respond(request.Seq, request.Command, map[string]any{"breakpoints": result}, nil)
	return nil
}

func (s *dapServer) start() error {
	if s.started {
		return nil
	}
	if s.launch == nil {
		return errors.New("configurationDone received before launch")
	}
	programPath := s.launch.Program
	source, err := os.ReadFile(programPath)
	if err != nil {
		return fmt.Errorf("read program: %w", err)
	}
	program, diagnostics := sos.LoadProgram(programPath, string(source))
	if len(diagnostics) > 0 {
		return fmt.Errorf("program has diagnostics at line %d: %s", diagnostics[0].Line, diagnostics[0].Message)
	}
	selection, err := sos.SelectCommand(program, s.launch.Args)
	if err != nil {
		return err
	}
	if selection.Help {
		return errors.New("debug launch cannot use --help")
	}
	if err := sos.LoadEnv(filepath.Join(s.launch.Cwd, ".env")); err != nil {
		return fmt.Errorf("load project environment: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.started = true
	s.session.stopOnEntry = s.launch.StopOnEntry
	s.session.firstStop = false
	s.processDone = make(chan struct{})
	go func() {
		defer close(s.processDone)
		streamController := sos.NewStreamController()
		var streamControl *streamControlClient
		if s.launch.StreamControl != "" {
			streamToken := s.launch.StreamToken
			if streamToken == "" {
				streamToken = os.Getenv("SOS_STREAM_TOKEN")
			}
			connected, connectErr := connectStreamControl(ctx, s.launch.StreamControl, streamToken, s.launch.StreamSession, streamController)
			if connectErr != nil {
				s.output("stderr", "Stream inspector unavailable: "+connectErr.Error()+"\n")
			} else {
				streamControl = connected
			}
		}
		if streamControl != nil {
			defer streamControl.close()
		}
		result, runErr := sos.Run(ctx, program, sos.Options{
			Dir:         s.launch.Cwd,
			SourcePath:  programPath,
			Args:        selection.Values,
			CommandPath: selection.Path,
			Stdout:      dapOutput{server: s, category: "stdout"},
			Stderr:      dapOutput{server: s, category: "stderr"},
			Model:       s.launch.Model,
			MaxCalls:    s.launch.MaxCalls,
			Debugger:    s.session,
			Streams:     streamController,
			OnStreamEvent: func(event sos.StreamEvent) {
				if streamControl != nil {
					streamControl.event(event)
				}
			},
			OnTrace: func(trace sos.Trace) {
				trace.Item = redactDebugSecrets(trace.Item)
				data, _ := json.Marshal(trace)
				s.output("console", "Jev trace: "+string(data)+"\n")
			},
		})
		if runErr != nil {
			s.output("stderr", "SysOneScript error: "+runErr.Error()+"\n")
			s.session.mu.Lock()
			breakOnException := s.session.breakOnException && !s.session.terminated
			s.session.mu.Unlock()
			if breakOnException {
				s.event("stopped", map[string]any{"reason": "exception", "threadId": 1, "allThreadsStopped": true})
			}
		}
		if result != nil && runErr == nil {
			data, _ := json.Marshal(map[string]any{"steps": result.Steps, "usage": result.Usage})
			s.output("console", "SysOneScript completed: "+string(data)+"\n")
		}
		s.session.terminate()
		exitCode := 0
		if runErr != nil {
			exitCode = 1
		}
		s.event("exited", map[string]any{"exitCode": exitCode})
		s.event("terminated", map[string]any{})
	}()
	return nil
}

type dapOutput struct {
	server   *dapServer
	category string
}

func (w dapOutput) Write(p []byte) (int, error) {
	w.server.output(w.category, string(p))
	return len(p), nil
}

func (s *dapServer) stackTrace(request dapRequest) error {
	snapshot, ok := s.session.snapshot()
	if !ok {
		s.respond(request.Seq, request.Command, map[string]any{"stackFrames": []any{}, "totalFrames": 0}, nil)
		return nil
	}
	frames := []any{map[string]any{
		"id":     1,
		"name":   displayFrameName(snapshot.Frame),
		"line":   maxInt(snapshot.Frame.Line, 1),
		"column": maxInt(snapshot.Frame.Column, 1),
		"source": map[string]any{"name": filepath.Base(snapshot.Frame.Path), "path": snapshot.Frame.Path},
	}}
	for i, frame := range snapshot.Stack {
		frames = append(frames, map[string]any{
			"id":     i + 2,
			"name":   displayFrameName(frame),
			"line":   maxInt(frame.Line, 1),
			"column": maxInt(frame.Column, 1),
			"source": map[string]any{"name": filepath.Base(frame.Path), "path": frame.Path},
		})
	}
	s.respond(request.Seq, request.Command, map[string]any{"stackFrames": frames, "totalFrames": len(frames)}, nil)
	return nil
}

func (s *dapServer) scopes(request dapRequest) error {
	snapshot, ok := s.session.snapshot()
	if !ok {
		return errors.New("no stopped debug frame")
	}
	s.session.mu.Lock()
	s.session.variableRefs[1] = snapshot.Variables
	s.session.mu.Unlock()
	s.respond(request.Seq, request.Command, map[string]any{"scopes": []any{
		map[string]any{"name": "Locals", "variablesReference": 1, "expensive": false},
	}}, nil)
	return nil
}

func (s *dapServer) variables(request dapRequest) error {
	var args struct {
		VariablesReference int `json:"variablesReference"`
	}
	if err := json.Unmarshal(request.Arguments, &args); err != nil {
		return err
	}
	knownTypes := map[string]string{}
	if args.VariablesReference == 1 {
		if snapshot, ok := s.session.snapshot(); ok {
			knownTypes = snapshot.Frame.VariableTypes
		}
	}
	s.session.mu.Lock()
	value := s.session.variableRefs[args.VariablesReference]
	variables := debugVariables(value, &s.session.variableRefs, &s.session.nextVarRef, knownTypes)
	s.session.mu.Unlock()
	s.respond(request.Seq, request.Command, map[string]any{"variables": variables}, nil)
	return nil
}

func (s *dapServer) evaluate(request dapRequest) error {
	var args struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(request.Arguments, &args); err != nil {
		return err
	}
	snapshot, ok := s.session.snapshot()
	if !ok {
		return errors.New("no stopped debug frame")
	}
	value, err := sos.EvaluateDebugExpression(args.Expression, snapshot.Variables)
	if err != nil {
		return err
	}
	s.session.mu.Lock()
	result := debugValue(args.Expression, value)
	if debugCanExpand(args.Expression, value) {
		result["variablesReference"] = s.nextVariableRefLocked(value)
	}
	s.session.mu.Unlock()
	s.respond(request.Seq, request.Command, map[string]any{"result": result["value"], "type": result["type"], "variablesReference": result["variablesReference"]}, nil)
	return nil
}

func (s *dapServer) nextVariableRefLocked(value any) int {
	ref := s.session.nextVarRef
	s.session.nextVarRef++
	s.session.variableRefs[ref] = value
	return ref
}

func (s *dapServer) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.session.terminate()
}

type dapError struct {
	Code    int
	Message string
}

func (s *dapServer) respond(requestSeq int, command string, body any, failure *dapError) {
	message := map[string]any{"type": "response", "seq": s.nextSeq(), "request_seq": requestSeq, "success": failure == nil, "command": command}
	if failure != nil {
		message["message"] = failure.Message
	} else if body != nil {
		message["body"] = body
	}
	s.writeMessage(message)
}

func (s *dapServer) event(event string, body any) {
	s.writeMessage(map[string]any{"type": "event", "seq": s.nextSeq(), "event": event, "body": body})
}

func (s *dapServer) output(category, text string) {
	s.event("output", map[string]any{"category": category, "output": text})
}

func (s *dapServer) nextSeq() int {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n := s.seq
	s.seq++
	return n
}

func (s *dapServer) writeMessage(message map[string]any) {
	data, err := json.Marshal(message)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(data), data)
}

func readDAP(reader *bufio.Reader) (dapRequest, error) {
	var out dapRequest
	length := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return out, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return out, fmt.Errorf("invalid DAP header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 {
				return out, errors.New("invalid DAP Content-Length")
			}
		}
	}
	if length == 0 {
		return out, errors.New("missing DAP Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func cleanDebugPath(value string) string {
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		value, _ = filepath.Abs(value)
	}
	return filepath.Clean(value)
}

func displayFrameName(frame sos.DebugFrame) string {
	if frame.Name != "" {
		return frame.Name
	}
	if frame.Text != "" {
		return frame.Text
	}
	return frame.Kind
}

func maxInt(value, fallback int) int {
	if value < fallback {
		return fallback
	}
	return value
}

func hitConditionMatches(condition string, hits int) bool {
	condition = strings.TrimSpace(condition)
	if n, err := strconv.Atoi(condition); err == nil {
		return hits >= n
	}
	if strings.HasPrefix(condition, "%") {
		n, err := strconv.Atoi(strings.TrimPrefix(condition, "%"))
		return err == nil && n > 0 && hits%n == 0
	}
	return true
}

func interpolateLogpoint(message string, variables map[string]any) string {
	for {
		start := strings.Index(message, "{")
		if start < 0 {
			return message
		}
		end := strings.Index(message[start+1:], "}")
		if end < 0 {
			return message
		}
		end += start + 1
		expression := message[start+1 : end]
		value, err := sos.EvaluateDebugExpression(expression, variables)
		replacement := "<error>"
		if err == nil {
			replacement = debugDisplay(expression, value)
		}
		message = message[:start] + replacement + message[end+1:]
	}
}

func isDebugContainer(value any) bool {
	if _, ok := value.(interface{ DebugStreamState() map[string]any }); ok {
		return true
	}
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func debugCanExpand(name string, value any) bool {
	return isDebugContainer(value) && !secretValue(name, value)
}

func debugValue(name string, value any) map[string]any {
	result := map[string]any{"value": debugDisplay(name, value), "type": fmt.Sprintf("%T", value), "variablesReference": 0}
	if _, ok := value.(interface{ DebugStreamState() map[string]any }); ok {
		result["type"] = "stream"
	}
	return result
}

func debugDisplay(name string, value any) string {
	if value == nil {
		return "null"
	}
	if secretValue(name, value) {
		return "<redacted>"
	}
	if stream, ok := value.(interface{ DebugStreamState() map[string]any }); ok {
		data, _ := json.Marshal(redactDebugSecrets(stream.DebugStreamState()))
		return string(data)
	}
	if isDebugContainer(value) {
		data, _ := json.Marshal(redactDebugSecrets(value))
		return string(data)
	}
	return fmt.Sprint(value)
}

func redactDebugSecrets(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if secretValue(key, item) {
				out[key] = "<redacted>"
			} else {
				out[key] = redactDebugSecrets(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactDebugSecrets(item)
		}
		return out
	default:
		return value
	}
}

func secretValue(name string, value any) bool {
	if strings.Contains(strings.ToLower(name), "key") || strings.Contains(strings.ToLower(name), "token") || strings.Contains(strings.ToLower(name), "secret") || strings.Contains(strings.ToLower(name), "password") {
		return true
	}
	_, _ = value, name
	return false
}

func debugVariables(value any, refs *map[int]any, next *int, knownTypes map[string]string) []any {
	var out []any
	if stream, ok := value.(interface{ DebugStreamState() map[string]any }); ok {
		value = stream.DebugStreamState()
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := typed[key]
			result := debugValue(key, item)
			if namedType := knownTypes[key]; namedType != "" {
				result["type"] = namedType
			}
			result["name"] = key
			if secretValue(key, item) {
				result["value"] = "<redacted>"
				result["variablesReference"] = 0
			} else if isDebugContainer(item) {
				ref := *next
				*next++
				if stream, ok := item.(interface{ DebugStreamState() map[string]any }); ok {
					// debugValue already took the read-only snapshot used for the
					// summary. A fresh snapshot is safe and still never receives an
					// item or changes producer credit.
					(*refs)[ref] = stream.DebugStreamState()
				} else {
					(*refs)[ref] = item
				}
				result["variablesReference"] = ref
			}
			out = append(out, result)
		}
	case []any:
		for i, item := range typed {
			result := debugValue("", item)
			result["name"] = strconv.Itoa(i)
			if isDebugContainer(item) {
				ref := *next
				*next++
				(*refs)[ref] = item
				result["variablesReference"] = ref
			}
			out = append(out, result)
		}
	}
	return out
}
