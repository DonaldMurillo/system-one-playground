package sos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// streamSource is the runtime boundary between producers (native, command, or
// stdio modules) and the language's single-owner stream operations.
type streamSource interface {
	next(context.Context) (any, bool, error)
	cancel(context.Context) error
}

// streamMetricsSource is an optional, non-consuming producer snapshot. The
// values describe data already buffered by the host and currently outstanding
// producer credit; querying them must never read, grant credit, or block.
type streamMetricsSource interface {
	streamMetrics() (itemsBuffered, creditAvailable int)
}

type streamScopeSource interface {
	streamScope() (root string, bound int, watching bool)
}

// streamStopRequester lets a multiplexed producer request its own graceful
// end without cancelling the process-wide read context.
type streamStopRequester interface{ requestStreamStop() error }

type localActionStreamSource struct {
	ctx       context.Context
	cancelRun context.CancelFunc
	start     func(context.Context, chan<- any) error
	items     chan any
	done      chan struct{}
	startOnce sync.Once
	endOnce   sync.Once
	err       error
}

func newLocalActionStreamSource(parent context.Context, start func(context.Context, chan<- any) error) *localActionStreamSource {
	ctx, cancel := context.WithCancel(parent)
	return &localActionStreamSource{ctx: ctx, cancelRun: cancel, start: start, items: make(chan any), done: make(chan struct{})}
}

func (s *localActionStreamSource) ensureStarted() {
	s.startOnce.Do(func() {
		go func() {
			err := s.start(s.ctx, s.items)
			s.endOnce.Do(func() {
				s.err = err
				close(s.items)
				close(s.done)
			})
		}()
	})
}

func (s *localActionStreamSource) next(ctx context.Context) (any, bool, error) {
	s.ensureStarted()
	select {
	case value, ok := <-s.items:
		if ok {
			return value, true, nil
		}
		return nil, false, s.err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (s *localActionStreamSource) cancel(ctx context.Context) error {
	s.cancelRun()
	// An unopened local producer owns no resources and needs no goroutine just
	// to acknowledge cancellation.
	started := false
	s.startOnce.Do(func() {
		started = true
		s.endOnce.Do(func() {
			close(s.items)
			close(s.done)
		})
	})
	if started {
		return nil
	}
	select {
	case <-s.done:
		if errors.Is(s.err, context.Canceled) {
			return nil
		}
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type streamState string

const maxStreamMaterializationItems = 1_000_000

const (
	streamActive   streamState = "active"
	streamConsumed streamState = "consumed"
	streamClosed   streamState = "closed"
)

type streamHandle struct {
	mu              sync.Mutex
	source          streamSource
	itemType        TypeRef
	producer        string
	state           streamState
	itemsReceived   int
	itemsBuffered   int
	creditAvailable int
	id              string
	binding         string
	line            int
	startedAt       time.Time
	updatedAt       time.Time
	endedAt         *time.Time
	lifecycle       string
	failure         map[string]any
	reason          string
	stopRequested   bool
	streamCtx       context.Context
	cancelRead      context.CancelFunc
	emit            func(StreamEvent)
}

// StreamController is the host-facing registry for streams in one run. Its
// snapshots never consume producer data, and Stop cancels only the selected
// stream so execution can continue after its read loop.
type StreamController struct {
	mu      sync.Mutex
	streams map[string]*streamHandle
	order   []string
}

func NewStreamController() *StreamController {
	return &StreamController{streams: map[string]*streamHandle{}}
}

func (c *StreamController) register(stream *streamHandle) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.streams == nil {
		c.streams = map[string]*streamHandle{}
	}
	if _, exists := c.streams[stream.id]; !exists {
		c.order = append(c.order, stream.id)
	}
	c.streams[stream.id] = stream
	c.mu.Unlock()
}

// Snapshots returns every stream observed during the run in creation order.
func (c *StreamController) Snapshots() []StreamEvent {
	if c == nil {
		return []StreamEvent{}
	}
	c.mu.Lock()
	streams := make([]*streamHandle, 0, len(c.order))
	for _, id := range c.order {
		streams = append(streams, c.streams[id])
	}
	c.mu.Unlock()
	out := make([]StreamEvent, 0, len(streams))
	for _, stream := range streams {
		out = append(out, stream.snapshot("snapshot"))
	}
	return out
}

// Stop requests graceful termination of one stream. A blocked read is
// interrupted and appears to the language as the end of that stream.
func (c *StreamController) Stop(ctx context.Context, id string) error {
	if c == nil {
		return fmt.Errorf("stream %s is not active", id)
	}
	c.mu.Lock()
	stream := c.streams[id]
	c.mu.Unlock()
	if stream == nil {
		return fmt.Errorf("unknown stream %s", id)
	}
	return stream.requestStop()
}

func newStreamHandle(itemType TypeRef, producer string, source streamSource) *streamHandle {
	return newStreamHandleWithContext(context.Background(), itemType, producer, source)
}

func newStreamHandleWithContext(parent context.Context, itemType TypeRef, producer string, source streamSource) *streamHandle {
	now := time.Now().UTC()
	streamCtx, cancelRead := context.WithCancel(parent)
	return &streamHandle{source: source, itemType: itemType, producer: producer, state: streamActive, lifecycle: "open", startedAt: now, updatedAt: now, streamCtx: streamCtx, cancelRead: cancelRead}
}

func (s *streamHandle) snapshot(event string) StreamEvent {
	s.mu.Lock()
	snapshot := StreamEvent{ID: s.id, Line: s.line, Event: event, State: s.lifecycle, Binding: s.binding, Producer: s.producer, ItemType: s.itemType.String(), ItemsReceived: s.itemsReceived, ItemsBuffered: s.itemsBuffered, CreditAvailable: s.creditAvailable, StartedAt: s.startedAt, UpdatedAt: s.updatedAt, EndedAt: s.endedAt, Failure: s.failure, Reason: s.reason}
	s.mu.Unlock()
	if metrics, ok := s.source.(streamMetricsSource); ok {
		snapshot.ItemsBuffered, snapshot.CreditAvailable = metrics.streamMetrics()
	}
	if scoped, ok := s.source.(streamScopeSource); ok {
		snapshot.Root, snapshot.Bound, snapshot.Watching = scoped.streamScope()
	}
	return snapshot
}

func (s *streamHandle) publish(event string) {
	if s.emit != nil {
		s.emit(s.snapshot(event))
	}
}

func (s *streamHandle) transition(event, state, reason string, err error) {
	now := time.Now().UTC()
	s.mu.Lock()
	if s.lifecycle == "completed" || s.lifecycle == "stopped" || s.lifecycle == "cancelled" || s.lifecycle == "failed" || s.lifecycle == "closed" {
		s.mu.Unlock()
		return
	}
	s.lifecycle, s.updatedAt = state, now
	s.reason = reason
	if state == "completed" || state == "stopped" || state == "cancelled" || state == "failed" || state == "closed" {
		s.endedAt = &now
	}
	if err != nil {
		s.failure = FailureValue(err)
	}
	s.mu.Unlock()
	s.publish(event)
}

func (s *streamHandle) beginConsumption() error {
	s.mu.Lock()
	if s.state != streamActive {
		s.mu.Unlock()
		return fmt.Errorf("stream was already consumed")
	}
	s.state = streamConsumed
	stopping := s.stopRequested
	if !stopping {
		s.lifecycle = "reading"
	}
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()
	if !stopping {
		s.publish("reading")
	}
	return nil
}

func (s *streamHandle) next(ctx context.Context) (any, bool, error) {
	v, ok, err := s.source.next(s.streamCtx)
	s.mu.Lock()
	stopped := s.stopRequested
	s.mu.Unlock()
	if stopped {
		cancelErr := s.source.cancel(context.Background())
		if cancelErr != nil {
			s.transition("failed", "failed", "cancellation failed", cancelErr)
			return nil, false, cancelErr
		}
		s.transition("stopped", "stopped", "tooling", nil)
		return nil, false, nil
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.transition("cancelled", "cancelled", "run cancelled", err)
			return nil, false, err
		}
		s.transition("failed", "failed", "failure", err)
		return nil, false, err
	}
	if ok {
		s.mu.Lock()
		s.itemsReceived++
		s.updatedAt = time.Now().UTC()
		s.mu.Unlock()
	} else {
		s.transition("completed", "completed", "producer completed", nil)
	}
	return v, ok, err
}

func (s *streamHandle) close(ctx context.Context) error {
	return s.closeWithReason(ctx, "closed")
}

func (s *streamHandle) closeWithReason(ctx context.Context, reason string) error {
	s.mu.Lock()
	if s.state == streamClosed {
		s.mu.Unlock()
		return nil
	}
	s.state = streamClosed
	terminal := s.lifecycle == "completed" || s.lifecycle == "stopped" || s.lifecycle == "cancelled" || s.lifecycle == "failed"
	stopping := s.stopRequested
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()
	s.cancelRead()
	err := s.source.cancel(ctx)
	if err != nil && !terminal {
		s.transition("failed", "failed", "cancellation failed", err)
		return err
	}
	if stopping && !terminal {
		s.transition("stopped", "stopped", "tooling", nil)
	} else if !terminal {
		s.transition("closed", "closed", reason, nil)
	}
	return err
}

func (s *streamHandle) requestStop() error {
	s.mu.Lock()
	if s.lifecycle == "completed" || s.lifecycle == "stopping" || s.lifecycle == "stopped" || s.lifecycle == "cancelled" || s.lifecycle == "failed" || s.lifecycle == "closed" {
		s.mu.Unlock()
		return nil
	}
	s.stopRequested = true
	s.lifecycle = "stopping"
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()
	if requester, ok := s.source.(streamStopRequester); ok {
		if requester.requestStreamStop() == nil {
			return nil
		}
	}
	s.cancelRead()
	return nil
}

func (s *streamHandle) debugValue() map[string]any {
	snapshot := s.snapshot("snapshot")
	return map[string]any{"id": snapshot.ID, "binding": snapshot.Binding, "line": snapshot.Line, "state": snapshot.State, "itemType": snapshot.ItemType, "itemsReceived": snapshot.ItemsReceived, "itemsBuffered": snapshot.ItemsBuffered, "creditAvailable": snapshot.CreditAvailable, "producer": snapshot.Producer, "startedAt": snapshot.StartedAt, "updatedAt": snapshot.UpdatedAt, "endedAt": snapshot.EndedAt, "reason": snapshot.Reason, "failure": snapshot.Failure, "root": snapshot.Root, "bound": snapshot.Bound, "watching": snapshot.Watching}
}

// DebugStreamState returns a non-consuming snapshot for debugger and Studio
// adapters. It never requests producer credit or receives an item.
func (s *streamHandle) DebugStreamState() map[string]any { return s.debugValue() }

type stopReadingValue struct{}

func (stopReadingValue) Error() string { return "stop reading" }

func (r *runtime) openExternalStream(actionName, binding string, line int, expressions []string) (*streamHandle, error) {
	alias, name, qualified := strings.Cut(actionName, ".")
	if !qualified {
		fn := r.functions[actionName]
		if fn == nil {
			return nil, fmt.Errorf("unknown streaming action %s", actionName)
		}
		return r.openLocalActionStream(actionName, binding, line, expressions, fn, nil)
	}
	mod := r.imports[alias]
	if mod == nil {
		return nil, fmt.Errorf("unknown external streaming action %s", actionName)
	}
	if mod.external == nil {
		if op, native := mod.Native[name]; native && op.StreamFn != nil {
			return r.openNativeModuleStream(mod, op, actionName, binding, line, expressions)
		}
		fn := mod.Actions[name]
		if fn == nil {
			return nil, fmt.Errorf("unknown streaming action %s", actionName)
		}
		return r.openLocalActionStream(actionName, binding, line, expressions, fn, mod)
	}
	var action *ExternalAction
	for i := range mod.external.Actions {
		if mod.external.Actions[i].Name == name {
			action = &mod.external.Actions[i]
			break
		}
	}
	if action == nil || !strings.HasPrefix(action.Result.Type, "stream of ") {
		return nil, fmt.Errorf("%s is not a streaming action", actionName)
	}
	if len(action.Targets) > 0 && !containsString(action.Targets, "native") && !containsString(action.Targets, currentExternalTarget()) {
		return nil, fmt.Errorf("%s is unavailable on %s", actionName, currentExternalTarget())
	}
	if len(expressions) != len(action.Parameters) {
		return nil, fmt.Errorf("%s expects %d argument(s)", actionName, len(action.Parameters))
	}
	values := make([]any, len(expressions))
	for i, expression := range expressions {
		value, err := r.eval(expression, nil)
		if err != nil {
			return nil, err
		}
		values[i] = value
		typ, parseErr := parseType(action.Parameters[i].Type, true)
		if parseErr != nil || !typeMatchesRef(value, typ, mod.Definitions) {
			return nil, fmt.Errorf("%s argument %s must be %s", actionName, action.Parameters[i].Name, action.Parameters[i].Type)
		}
	}
	itemName := strings.TrimSpace(strings.TrimPrefix(action.Result.Type, "stream of "))
	var source streamSource
	var err error
	if mod.external.Runtime.Kind == "command" {
		source, err = mod.external.openCommandStream(r.ctx, r.opts, *action, values)
	} else {
		source, err = mod.external.openStdioStream(r.ctx, r.opts, *action, values)
	}
	if err != nil {
		return nil, err
	}
	itemType, err := parseType(itemName, false)
	if err != nil {
		_ = source.cancel(context.Background())
		return nil, err
	}
	stream := newStreamHandleWithContext(r.ctx, itemType, actionName, source)
	stream.id = fmt.Sprintf("stream-%d", r.shared.streams.Add(1))
	stream.binding = binding
	stream.line = line
	stream.emit = r.opts.OnStreamEvent
	if r.opts.Streams != nil {
		r.opts.Streams.register(stream)
	}
	stream.publish("opened")
	return stream, nil
}

func (r *runtime) openLocalActionStream(actionName, binding string, line int, expressions []string, fn *Statement, mod *Module) (*streamHandle, error) {
	decl, err := parseActionDecl(fn.Text)
	if err != nil {
		return nil, err
	}
	if !decl.Streaming {
		return nil, fmt.Errorf("%s is not a streaming action", actionName)
	}
	if len(expressions) != len(decl.Params) {
		return nil, fmt.Errorf("%s expects %d argument(s)", actionName, len(decl.Params))
	}
	definitions := r.definitions
	if mod != nil {
		definitions, _ = mod.definitionScope(mod.scope(decl.Name))
	}
	local := map[string]any{}
	if mod == nil {
		for name, value := range r.env {
			local[name] = value
		}
	}
	localTypes := map[string]TypeRef{}
	for index, parameter := range decl.Params {
		value, evalErr := r.eval(expressions[index], nil)
		if evalErr != nil {
			return nil, evalErr
		}
		if parameter.Type.Name != "any" && !typeMatchesRef(value, parameter.Type, definitions) {
			return nil, fmt.Errorf("%s.%s must be %s; received %s", actionName, parameter.Name, parameter.Type.String(), valueTypeName(value))
		}
		local[parameter.Name] = value
		localTypes[parameter.Name] = parameter.Type.base()
	}
	if len(decl.Using) > 0 {
		for _, field := range decl.Using {
			value, fieldType, fieldErr := propertyTyped(local[decl.Params[0].Name], field, decl.Params[0].Type.base(), definitions)
			if fieldErr != nil {
				return nil, fieldErr
			}
			local[field], localTypes[field] = value, fieldType
		}
	}
	parentEnv := make(map[string]any, len(r.env))
	for name, value := range r.env {
		parentEnv[name] = value
	}
	source := newLocalActionStreamSource(r.ctx, func(ctx context.Context, output chan<- any) error {
		child := *r
		child.ctx, child.env, child.types = ctx, local, localTypes
		child.definitions = definitions
		child.streamOutput, child.streamItemType = output, decl.StreamItem
		child.activeFailure = nil
		child.depth++
		if mod != nil {
			child.module = mod
			child.functions, child.schemas = copyStatements(mod.Actions), copyStatements(mod.Schemas)
			child.imports, child.vocab = mod.scope(decl.Name), mod.vocabulary(decl.Name)
			child.failures, _ = visibleFailuresFrom(mod.Failures, child.imports, "the current package")
		}
		actionPath := child.logicalPath
		if mod != nil {
			actionPath = mod.actionPaths[decl.Name]
		}
		child.debugStack = append(append([]DebugFrame(nil), r.debugStack...), DebugFrame{Path: actionPath, Line: fn.Line, Column: 1, Name: actionName, Kind: "action", Text: fn.Text, Depth: child.depth})
		runErr := child.block(fn.Body)
		closeActionOwnedStreams(child.env, parentEnv)
		var returned returnValue
		if errors.As(runErr, &returned) {
			if returned.hasValue {
				return fmt.Errorf("streaming action %s cannot finish with a value", actionName)
			}
			return nil
		}
		return runErr
	})
	stream := newStreamHandleWithContext(r.ctx, decl.StreamItem, actionName, source)
	stream.id = fmt.Sprintf("stream-%d", r.shared.streams.Add(1))
	stream.binding, stream.line, stream.emit = binding, line, r.opts.OnStreamEvent
	if r.opts.Streams != nil {
		r.opts.Streams.register(stream)
	}
	stream.publish("opened")
	return stream, nil
}

func (r *runtime) materializeStream(s *Statement, m []string, truncate bool) error {
	limitValue, err := r.eval(m[1], nil)
	if err != nil {
		return err
	}
	limit, ok := number(limitValue)
	if !ok || math.IsNaN(limit) || math.IsInf(limit, 0) || limit <= 0 || limit != math.Trunc(limit) || limit > maxStreamMaterializationItems {
		return fmt.Errorf("stream item limit must be a positive bounded integer (at most %d)", maxStreamMaterializationItems)
	}
	name := strings.TrimSpace(m[2])
	stream, ok := r.env[name].(*streamHandle)
	if !ok {
		return fmt.Errorf("%s is not an owned stream", name)
	}
	if err := stream.beginConsumption(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	items := make([]any, 0, int(limit))
	for len(items) < int(limit) {
		item, more, err := stream.next(r.ctx)
		if err != nil {
			_ = stream.closeWithReason(context.Background(), "scope exit")
			return err
		}
		if !more {
			r.env[m[3]] = items
			return nil
		}
		if !typeMatchesRef(item, stream.itemType, r.definitions) {
			_ = stream.close(context.Background())
			return fmt.Errorf("stream item must be %s; received %s", stream.itemType.String(), valueTypeName(item))
		}
		items = append(items, item)
	}
	if truncate {
		if err := stream.close(r.ctx); err != nil {
			return err
		}
		r.env[m[3]] = items
		return nil
	}
	_, more, err := stream.next(r.ctx)
	if err != nil {
		_ = stream.closeWithReason(context.Background(), "action scope exit")
		return err
	}
	if more {
		_ = stream.close(r.ctx)
		return &typedFailure{kind: "StreamLimitExceeded", value: map[string]any{
			"kind": "StreamLimitExceeded", "message": fmt.Sprintf("collect at most %d items exceeded its limit", int(limit)),
			"retryable": false, "limit": limit, "received": limit + 1,
		}}
	}
	r.env[m[3]] = items
	return nil
}

func statementBindingName(s *Statement) string {
	if s == nil {
		return ""
	}
	m := match(s.Kind, s.Text)
	switch s.Kind {
	case "remember":
		return m[2]
	case "make":
		return m[1]
	case "read":
		return m[3]
	case "readEach":
		return m[3]
	case "find":
		return m[3]
	case "group":
		return m[3]
	case "map":
		return m[4]
	case "take":
		return m[4]
	case "collectStream":
		return m[3]
	case "evaluate", "classify", "score", "judge":
		return m[3]
	case "capture":
		return m[3]
	case "call":
		return m[3]
	case "sent":
		return matchSent(s.Text)[4]
	case "openStream":
		return m[3]
	case "streamFiles", "watchFolder":
		return m[3]
	}
	return ""
}

func closeOwnedStreams(env map[string]any) {
	for _, value := range env {
		if stream, ok := value.(*streamHandle); ok {
			_ = stream.close(context.Background())
		}
	}
}

func closeActionOwnedStreams(local, outer map[string]any) {
	callerOwned := map[*streamHandle]bool{}
	for _, value := range outer {
		if stream, ok := value.(*streamHandle); ok {
			callerOwned[stream] = true
		}
	}
	for _, value := range local {
		stream, ok := value.(*streamHandle)
		if !ok {
			continue
		}
		if callerOwned[stream] {
			continue
		}
		_ = stream.close(context.Background())
	}
}

func containsStreamHandle(value any) bool {
	switch v := value.(type) {
	case *streamHandle:
		return true
	case []any:
		for _, item := range v {
			if containsStreamHandle(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsStreamHandle(item) {
				return true
			}
		}
	}
	return false
}
