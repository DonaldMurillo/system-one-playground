package sos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

type timerSnapshotSource interface{ timerSnapshot() TimerSnapshot }
type scheduleSnapshotSource interface {
	scheduleSnapshot() (clock, label, zone string, next *time.Time)
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
	clock           Clock
	emit            func(StreamEvent)
	// rejected counts items a handling policy completed through its typed
	// rejection action; reading it never consumes an item.
	rejected     atomic.Int64
	handlerStats *handlingCounters
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
	return newStreamHandleWithClock(parent, HostClock{}, itemType, producer, source)
}

func newStreamHandleWithClock(parent context.Context, clock Clock, itemType TypeRef, producer string, source streamSource) *streamHandle {
	clock = clockOrDefault(clock)
	now := clock.Now().UTC()
	streamCtx, cancelRead := context.WithCancel(parent)
	return &streamHandle{source: source, itemType: itemType, producer: producer, state: streamActive, lifecycle: "open", startedAt: now, updatedAt: now, streamCtx: streamCtx, cancelRead: cancelRead, clock: clock}
}

func (s *streamHandle) snapshot(event string) StreamEvent {
	s.mu.Lock()
	snapshot := StreamEvent{ID: s.id, Line: s.line, Event: event, State: s.lifecycle, Binding: s.binding, Producer: s.producer, ItemType: s.itemType.String(), ItemsReceived: s.itemsReceived, ItemsBuffered: s.itemsBuffered, CreditAvailable: s.creditAvailable, StartedAt: s.startedAt, UpdatedAt: s.updatedAt, EndedAt: s.endedAt, Failure: s.failure, Reason: s.reason}
	handlerStats := s.handlerStats
	s.mu.Unlock()
	if metrics, ok := s.source.(streamMetricsSource); ok {
		snapshot.ItemsBuffered, snapshot.CreditAvailable = metrics.streamMetrics()
	}
	if scoped, ok := s.source.(streamScopeSource); ok {
		snapshot.Root, snapshot.Bound, snapshot.Watching = scoped.streamScope()
	}
	if timed, ok := s.source.(timerSnapshotSource); ok {
		value := timed.timerSnapshot()
		snapshot.ClockKind = value.Clock
		snapshot.Interval = value.Interval
		snapshot.TimerPolicy = value.Policy
		snapshot.EmittedTicks = value.Emitted
		snapshot.MissedTicks = value.Missed
		snapshot.CombinedTicks = value.Combined
		snapshot.SkippedTicks = value.Skipped
		snapshot.CaughtUpTicks = value.CaughtUp
		if !value.Next.IsZero() {
			next := value.Next
			snapshot.NextScheduledAt = &next
		}
		if value.Clock == "virtual" {
			now := s.clock.Now()
			snapshot.VirtualTime = &now
		}
	}
	if scheduled, ok := s.source.(scheduleSnapshotSource); ok {
		snapshot.ClockKind, snapshot.Schedule, snapshot.TimeZone, snapshot.NextScheduledAt = scheduled.scheduleSnapshot()
	}
	if stats, ok := s.source.(interface{ transformStats() map[string]any }); ok {
		snapshot.Policy = stats.transformStats()
	}
	if handlerStats != nil {
		if snapshot.Policy == nil {
			snapshot.Policy = map[string]any{}
		}
		for key, value := range handlerStats.stats() {
			snapshot.Policy[key] = value
		}
		snapshot.Policy["rejected"] = s.rejected.Load()
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
	// Merge the caller's context with the stream's cancellation so both a
	// caller timeout and closing the stream interrupt a blocked read.
	if ctx != s.streamCtx {
		merged, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(s.streamCtx, cancel)
		defer stop()
		defer cancel()
		ctx = merged
	}
	v, ok, err := s.source.next(ctx)
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
	return map[string]any{"id": snapshot.ID, "binding": snapshot.Binding, "line": snapshot.Line, "state": snapshot.State, "itemType": snapshot.ItemType, "itemsReceived": snapshot.ItemsReceived, "itemsBuffered": snapshot.ItemsBuffered, "creditAvailable": snapshot.CreditAvailable, "producer": snapshot.Producer, "policy": snapshot.Policy, "startedAt": snapshot.StartedAt, "updatedAt": snapshot.UpdatedAt, "endedAt": snapshot.EndedAt, "reason": snapshot.Reason, "failure": snapshot.Failure}
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
		if operation, ok := mod.Native[name]; ok && operation.StreamFn != nil && strings.HasPrefix(operation.Result, "stream of ") {
			if len(expressions) != len(operation.Params) {
				return nil, fmt.Errorf("%s expects %d argument(s)", actionName, len(operation.Params))
			}
			values := make([]any, len(expressions))
			for index, expression := range expressions {
				value, evalErr := r.eval(expression, nil)
				if evalErr != nil {
					return nil, evalErr
				}
				values[index] = value
			}
			if checkErr := operation.check(values); checkErr != nil {
				return nil, checkErr
			}
			source, streamErr := operation.StreamFn(r.ctx, r.opts, values)
			if streamErr != nil {
				return nil, streamErr
			}
			itemName := strings.TrimSpace(strings.TrimPrefix(operation.Result, "stream of "))
			itemType, parseErr := parseType(itemName, true)
			if parseErr != nil {
				_ = source.cancel(context.Background())
				return nil, parseErr
			}
			stream := newStreamHandleWithContext(r.ctx, itemType, actionName, source)
			stream.id = fmt.Sprintf("stream-%d", r.shared.streams.Add(1))
			stream.binding, stream.line, stream.emit = binding, line, r.opts.OnStreamEvent
			if r.opts.Streams != nil {
				r.opts.Streams.register(stream)
			}
			stream.publish("opened")
			return stream, nil
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
	case "httpGet":
		return m[5]
	case "httpPost", "httpListen":
		return m[3]
	case "httpRequest":
		return m[2]
	case "httpReadBody":
		return m[4]
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

// ---------------------------------------------------------------------------
// Stream transformations (spec: docs/sysonescript-stream-handling-spec.md)
//
// The runtime owns every transformation: canonical sentences and std/streams
// aliases lower onto the constructors and handlers below. All timers use a
// StreamClock so embedders and tests can drive deterministic virtual time;
// production defaults to the host monotonic clock. Every derived stream
// consumes ownership of its source exactly once.
// ---------------------------------------------------------------------------

// StreamClock is the deterministic time seam for stream transformations.
// Now reports monotonic elapsed time; After registers an alarm that fires no
// earlier than the requested duration and reports the elapsed time observed.
type StreamClock interface {
	Now() time.Duration
	After(time.Duration) <-chan time.Duration
}

// runtimeStreamClock adapts the language clock to stream transforms so a
// virtual-time run advances debounce, throttle, and deadline policies too.
type runtimeStreamClock struct {
	clock Clock
	epoch time.Time
}

func newRuntimeStreamClock(clock Clock) StreamClock {
	clock = clockOrDefault(clock)
	return runtimeStreamClock{clock: clock, epoch: clock.Now()}
}

func (c runtimeStreamClock) Now() time.Duration { return c.clock.Now().Sub(c.epoch) }

func (c runtimeStreamClock) After(d time.Duration) <-chan time.Duration {
	timer := c.clock.NewTimer(d)
	out := make(chan time.Duration, 1)
	go func() {
		if firedAt, ok := <-timer.C(); ok {
			out <- firedAt.Sub(c.epoch)
		}
	}()
	return out
}

var streamClockEpoch = time.Now()

// MonotonicClock is the production clock: host monotonic time, immune to
// wall-clock changes.
type MonotonicClock struct{}

func (MonotonicClock) Now() time.Duration { return time.Since(streamClockEpoch) }

func (MonotonicClock) After(d time.Duration) <-chan time.Duration {
	out := make(chan time.Duration, 1)
	if d <= 0 {
		out <- time.Since(streamClockEpoch)
		return out
	}
	timer := time.NewTimer(d)
	go func() {
		defer timer.Stop()
		<-timer.C
		out <- time.Since(streamClockEpoch)
	}()
	return out
}

type virtualAlarm struct {
	at  time.Duration
	seq uint64
	out chan time.Duration
}

// VirtualStreamClock is a manually advanced clock for deterministic tests and
// embeddings. Advance fires due alarms in deadline order (registration order
// on ties); nothing runs concurrently inside the clock.
type VirtualStreamClock struct {
	mu     sync.Mutex
	now    time.Duration
	seq    uint64
	alarms []*virtualAlarm
}

func (c *VirtualStreamClock) Now() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *VirtualStreamClock) After(d time.Duration) <-chan time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	alarm := &virtualAlarm{at: c.now + d, seq: c.seq, out: make(chan time.Duration, 1)}
	c.alarms = append(c.alarms, alarm)
	return alarm.out
}

// Advance moves the clock forward by d and returns the new elapsed time.
func (c *VirtualStreamClock) Advance(d time.Duration) time.Duration {
	c.mu.Lock()
	c.now += d
	now := c.now
	due := make([]*virtualAlarm, 0, len(c.alarms))
	remaining := c.alarms[:0]
	for _, alarm := range c.alarms {
		if alarm.at <= now {
			due = append(due, alarm)
		} else {
			remaining = append(remaining, alarm)
		}
	}
	c.alarms = remaining
	sort.SliceStable(due, func(i, j int) bool {
		if due[i].at != due[j].at {
			return due[i].at < due[j].at
		}
		return due[i].seq < due[j].seq
	})
	c.mu.Unlock()
	for _, alarm := range due {
		alarm.out <- now
	}
	return now
}

func streamClockOr(clock StreamClock) StreamClock {
	if clock == nil {
		return MonotonicClock{}
	}
	return clock
}

// Reserved stream failure constructors. Payloads use language numbers
// (float64) and durations (time.Duration).
func streamFailure(kind, message string, fields map[string]any) *typedFailure {
	value := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		value[k] = v
	}
	value["message"] = message
	return &typedFailure{kind: kind, value: value}
}

func errStreamKeyLimit(operation string, limit int) error {
	return streamFailure("StreamKeyLimitExceeded",
		fmt.Sprintf("%s exceeded its key bound of %d", operation, limit),
		map[string]any{"limit": float64(limit), "operation": operation})
}

func errStreamConcurrencyLimit(limit int) error {
	return streamFailure("StreamConcurrencyLimitExceeded",
		fmt.Sprintf("stream handling exceeded its concurrency bound of %d", limit),
		map[string]any{"limit": float64(limit)})
}

func errStreamIdleTimeout(idleFor time.Duration) error {
	return streamFailure("StreamIdleTimeout",
		fmt.Sprintf("stream source was idle for %s", idleFor),
		map[string]any{"idle_for": idleFor})
}

func errStreamDeadlineExceeded(deadline time.Duration) error {
	return streamFailure("StreamDeadlineExceeded",
		fmt.Sprintf("stream exceeded its required deadline of %s", deadline),
		map[string]any{"deadline": deadline})
}

func errStreamHandlerCleanup(operation, reason string) error {
	return streamFailure("StreamHandlerCleanupFailed",
		fmt.Sprintf("%s handler cleanup failed: %s", operation, reason),
		map[string]any{"operation": operation, "reason": reason})
}

func errStreamObligationAbandoned(operation, item string) error {
	fields := map[string]any{"operation": operation}
	if item != "" {
		fields["item"] = item
	}
	return streamFailure("StreamObligationAbandoned",
		fmt.Sprintf("%s dropped an item with an unresolved obligation", operation),
		fields)
}

func errStreamByteLimit(operation string, held, limit int) error {
	return streamFailure("StreamLimitExceeded",
		fmt.Sprintf("%s would hold %d bytes of items, over its bound of %d", operation, held, limit),
		map[string]any{"limit": float64(limit), "received": float64(held)})
}

func errStreamTimerLimit(operation string, timers, limit int) error {
	return streamFailure("StreamLimitExceeded",
		fmt.Sprintf("%s would hold %d live timers, over its bound of %d", operation, timers, limit),
		map[string]any{"limit": float64(limit), "received": float64(timers)})
}

// streamItemSize estimates the retained bytes of one held item for byte
// bounds. It is a deterministic estimate, not an allocation guarantee:
// scalars count their width and containers count members plus a fixed
// per-entry header.
func streamItemSize(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return len(v)
	case bool:
		return 1
	case float64, int64, time.Duration:
		return 8
	case *OwnedItem:
		return streamItemSize(v.Value)
	case []any:
		n := 8 * len(v)
		for _, entry := range v {
			n += streamItemSize(entry)
		}
		return n
	case map[string]any:
		n := 16
		for k, entry := range v {
			n += len(k) + streamItemSize(entry)
		}
		return n
	default:
		return 16
	}
}

// OwnedItem couples a stream value with a completion obligation such as an
// HTTP response or a queue acknowledgment. Dispose is the explicit
// completion policy; when it is nil, dropping the item is invalid.
type OwnedItem struct {
	Value   any
	ID      string
	Dispose func(reason string) error
	settled atomic.Bool
}

func NewOwnedItem(value any, dispose func(reason string) error) *OwnedItem {
	return &OwnedItem{Value: value, Dispose: dispose}
}

// Complete settles the obligation through the declared policy.
func (o *OwnedItem) Complete(reason string) error {
	if o == nil {
		return nil
	}
	if o.settled.Swap(true) {
		return nil
	}
	if o.Dispose == nil {
		return nil
	}
	return o.Dispose(reason)
}

func (o *OwnedItem) Settled() bool { return o == nil || o.settled.Load() }

// settleOrAbandoned applies the disposal policy to an item a policy is about
// to drop, replace, or leave unfinished. Plain values are free; owned items
// without a policy fail with StreamObligationAbandoned.
func settleOrAbandoned(operation string, item any, reason string) error {
	owned, ok := item.(*OwnedItem)
	if !ok || owned.Settled() {
		return nil
	}
	if owned.Dispose == nil {
		return errStreamObligationAbandoned(operation, owned.ID)
	}
	if err := owned.Complete(reason); err != nil {
		return errStreamHandlerCleanup(operation, err.Error())
	}
	return nil
}

// EffectSafety is the per-operation effect and cancellation metadata the
// runtime attaches to handling policies.
type EffectSafety struct {
	Operation    string
	Effect       string // pure, read-only, idempotent, compensatable, non-idempotent
	Cancellation string // cancellation-safe or cancellation-delayed
}

// ValidateEffectSafety reports diagnostics for a cancellation-capable policy.
// Non-idempotent effects require an explicit acknowledgment that completed
// effects are not reversed; delayed cancellation is always warned about.
func ValidateEffectSafety(effects []EffectSafety, acknowledged bool) ([]string, error) {
	var warnings, unacknowledged []string
	for _, effect := range effects {
		switch effect.Effect {
		case "pure", "read-only", "idempotent", "compensatable", "non-idempotent":
		default:
			return nil, fmt.Errorf("effect %q for %s is not a known effect class", effect.Effect, effect.Operation)
		}
		switch effect.Cancellation {
		case "", "cancellation-safe", "cancellation-delayed":
		default:
			return nil, fmt.Errorf("cancellation %q for %s is not a known cancellation class", effect.Cancellation, effect.Operation)
		}
		if effect.Effect == "non-idempotent" {
			unacknowledged = append(unacknowledged, effect.Operation)
		}
		if effect.Cancellation == "cancellation-delayed" {
			warnings = append(warnings, fmt.Sprintf("%s reports delayed cancellation; completed effects may outlive cancellation", effect.Operation))
		}
	}
	if len(unacknowledged) > 0 && !acknowledged {
		return warnings, fmt.Errorf("non-idempotent effects (%s) require an explicit acknowledgment that completed effects are not reversed", strings.Join(unacknowledged, ", "))
	}
	return warnings, nil
}

// StreamHandle exposes the runtime-owned stream surface to hosts and the
// transformation constructors below.
type StreamHandle = *streamHandle

// Next reads one item, honoring the caller's context for interruption.
func (s *streamHandle) Next(ctx context.Context) (any, bool, error) {
	type readResult struct {
		value any
		ok    bool
		err   error
	}
	results := make(chan readResult, 1)
	go func() {
		value, ok, err := s.next(ctx)
		results <- readResult{value, ok, err}
	}()
	select {
	case r := <-results:
		return r.value, r.ok, r.err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// Close releases the stream and its owned source.
func (s *streamHandle) Close(ctx context.Context) error { return s.close(ctx) }

type channelStreamSource struct {
	items <-chan any
}

func (s *channelStreamSource) next(ctx context.Context) (any, bool, error) {
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case value, open := <-s.items:
		if !open {
			return nil, false, nil
		}
		if err, ok := value.(error); ok {
			return nil, false, err
		}
		return value, true, nil
	}
}

func (*channelStreamSource) cancel(context.Context) error { return nil }

// OpenChannelStream opens a host-owned stream whose items arrive on ch.
// Closing ch completes the stream; sending an error fails it with that error.
func OpenChannelStream(parent context.Context, itemType TypeRef, producer string, ch <-chan any) StreamHandle {
	return newStreamHandleWithContext(parent, itemType, producer, &channelStreamSource{items: ch})
}

// Register adds a host-opened or derived stream to the controller.
func (c *StreamController) Register(s StreamHandle) { c.register(s) }

// TransformStats returns live policy counters (emitted, dropped, replaced,
// rejected, pending) for derived streams. Reading it never consumes an item
// or advances a timer.
func (s *streamHandle) TransformStats() map[string]any {
	if stats, ok := s.source.(interface{ transformStats() map[string]any }); ok {
		return stats.transformStats()
	}
	return map[string]any{
		"emitted": int64(0), "dropped": int64(0), "replaced": int64(0),
		"rejected": s.rejected.Load(), "pending": int64(0), "armed_at": int64(-1),
		"timers": int64(0), "timer_limit": int64(-1),
		"keys": int64(0), "max_keys": int64(0),
		"bytes_held": int64(0), "byte_limit": int64(-1),
	}
}

// deriveStream enforces single ownership: the derived stream becomes the only
// consumer of parent. A second transformation or a direct read fails.
func deriveStream(parent *streamHandle, operation string, itemType TypeRef, source streamSource) (*streamHandle, error) {
	if err := parent.beginConsumption(); err != nil {
		return nil, fmt.Errorf("derive %s: %w", operation, err)
	}
	// The derived stream owns an independent cancellation domain: closing
	// or completing the consumed upstream must not cancel it. Propagation
	// flows through the derived source's own cancel path instead.
	child := newStreamHandleWithContext(context.Background(), itemType, parent.producer+"/"+operation, source)
	child.binding = parent.binding
	return child, nil
}

func scalarKey(value any) (any, error) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) {
			return nil, fmt.Errorf("stream key must not be NaN")
		}
		return typed, nil
	case string, bool, int64, time.Duration, time.Time:
		return value, nil
	}
	return nil, fmt.Errorf("stream key must be a comparable scalar value, not %s", typeNameOf(value))
}

func typeNameOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case []any:
		return "list"
	case map[string]any:
		return "record"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func valueEquals(a, b any) bool {
	return a == b
}

type upstreamEvent struct {
	value any
	ok    bool
	err   error
}

// upstreamPuller reads upstream only while the policy grants admission, so
// transformations cannot buffer beyond their declared bounds.
type upstreamPuller struct {
	items chan upstreamEvent
	grant chan struct{}
}

func newUpstreamPuller(ctx context.Context, h *streamHandle) *upstreamPuller {
	p := &upstreamPuller{items: make(chan upstreamEvent, 1), grant: make(chan struct{}, 1)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, open := <-p.grant:
				if !open {
					return
				}
				value, ok, err := h.next(context.Background())
				select {
				case p.items <- upstreamEvent{value: value, ok: ok, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return p
}

func (p *upstreamPuller) request() {
	select {
	case p.grant <- struct{}{}:
	default:
	}
}

func pullWithContext(ctx context.Context, h *streamHandle) (any, bool, error) {
	type readResult struct {
		value any
		ok    bool
		err   error
	}
	results := make(chan readResult, 1)
	go func() {
		value, ok, err := h.next(ctx)
		results <- readResult{value, ok, err}
	}()
	select {
	case r := <-results:
		return r.value, r.ok, r.err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

type transformCounters struct {
	emitted, dropped, replaced, rejected, pending atomic.Int64
	// timers and bytesHeld publish the live timer and retained-byte
	// accounting; reading them never advances a timer or consumes an item.
	timers, bytesHeld atomic.Int64
	// keys and maxKeys expose current and high-water key cardinality.
	keys, maxKeys atomic.Int64
	// byteLimit and timerLimit publish the configured bounds (-1 =
	// unbounded). They are set before the engine starts and read-only
	// afterwards.
	byteLimit, timerLimit int64
	// armedAt publishes the currently armed timer deadline (nanoseconds,
	// -1 when disarmed) so deterministic clocks can observe arming before
	// advancing. Reading it never advances a timer.
	armedAt              atomic.Int64
	stateMu              sync.Mutex
	batchSizes           []int
	operation, technical string
	mayReorder           bool
}

type handlingCounters struct {
	active, waiting      atomic.Int64
	operation, technical string
	mayReorder           bool
	warnings             []string
	cleanup              atomic.Value
}

func newHandlingCounters(operation, technical string, mayReorder bool, warnings []string) *handlingCounters {
	c := &handlingCounters{operation: operation, technical: technical, mayReorder: mayReorder, warnings: append([]string(nil), warnings...)}
	c.cleanup.Store("idle")
	return c
}

func (c *handlingCounters) stats() map[string]any {
	return map[string]any{
		"active_handlers": c.active.Load(), "waiting_handlers": c.waiting.Load(),
		"cleanup_state": c.cleanup.Load(), "effect_warnings": append([]string(nil), c.warnings...),
		"canonical_operation": c.operation, "technical_name": c.technical, "may_reorder": c.mayReorder,
	}
}

func attachHandlingCounters(up StreamHandle, counters *handlingCounters) {
	if up == nil {
		return
	}
	up.mu.Lock()
	up.handlerStats = counters
	up.mu.Unlock()
}

func (c *transformCounters) setBatchSizes(sizes []int) {
	c.stateMu.Lock()
	c.batchSizes = append(c.batchSizes[:0], sizes...)
	c.stateMu.Unlock()
}

func (c *transformCounters) observeKeys(count int) {
	current := int64(count)
	c.keys.Store(current)
	for maximum := c.maxKeys.Load(); current > maximum; maximum = c.maxKeys.Load() {
		if c.maxKeys.CompareAndSwap(maximum, current) {
			break
		}
	}
}

func (c *transformCounters) stats() map[string]any {
	c.stateMu.Lock()
	batchSizes := append([]int(nil), c.batchSizes...)
	c.stateMu.Unlock()
	return map[string]any{
		"emitted":             c.emitted.Load(),
		"dropped":             c.dropped.Load(),
		"replaced":            c.replaced.Load(),
		"rejected":            c.rejected.Load(),
		"pending":             c.pending.Load(),
		"timers":              c.timers.Load(),
		"keys":                c.keys.Load(),
		"max_keys":            c.maxKeys.Load(),
		"timer_limit":         c.timerLimit,
		"bytes_held":          c.bytesHeld.Load(),
		"byte_limit":          c.byteLimit,
		"armed_at":            c.armedAt.Load(),
		"batch_sizes":         batchSizes,
		"canonical_operation": c.operation,
		"technical_name":      c.technical,
		"may_reorder":         c.mayReorder,
	}
}

// policyBehavior is one transformation's bounded state machine. All methods
// run on the engine's single loop goroutine.
type policyBehavior interface {
	// onItem applies one upstream item.
	onItem(value any, emit func(any) bool) error
	// onTimer runs when the policy's current deadline is due.
	onTimer(emit func(any) bool) error
	// deadline reports the next due time (ok=false disarms the timer).
	deadline() (time.Duration, bool)
	// canAccept reports whether bounded state may admit another item.
	canAccept() bool
	// onDrained runs when upstream ends normally.
	onDrained(emit func(any) bool) error
}

// policyStream is the shared engine for timer-aware transformations: it owns
// admission, emission, backpressure, and terminal state so every policy has
// the same bounded shape.
type policyStream struct {
	clock    StreamClock
	up       *streamHandle
	puller   *upstreamPuller
	out      chan any
	terminal chan error
	parent   context.Context
	loopCtx  context.Context
	stopLoop context.CancelFunc
	finish   sync.Once
	startMu  sync.Mutex
	started  bool
	counters *transformCounters
	policy   policyBehavior
	// terminalErr replays an already-delivered terminal result so repeated
	// reads after the end keep returning the same terminal outcome instead
	// of blocking forever.
	terminalErr error
	terminalSet atomic.Bool
}

func newPolicyStream(clock StreamClock, up *streamHandle, policy policyBehavior, counters *transformCounters) *policyStream {
	counters.armedAt.Store(-1)
	return &policyStream{clock: streamClockOr(clock), up: up, out: make(chan any), terminal: make(chan error, 1), counters: counters, policy: policy}
}

// start eagerly launches the engine loop under parent.
func (s *policyStream) start(parent context.Context) {
	s.parent = parent
	s.ensureStarted()
}

// ensureStarted launches the engine loop at most once, on the first read,
// stats-independent cancellation, or eager construction.
func (s *policyStream) ensureStarted() {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.started {
		return
	}
	s.started = true
	s.loopCtx, s.stopLoop = context.WithCancel(s.parent)
	s.puller = newUpstreamPuller(s.loopCtx, s.up)
	go s.serve()
}

func (s *policyStream) serve() {
	s.admit()
	for {
		if deadline, armed := s.policy.deadline(); armed {
			wait := deadline - s.clock.Now()
			if wait <= 0 {
				if err := s.policy.onTimer(s.emit); err != nil {
					s.stop(err)
					return
				}
				continue
			}
			alarm := s.clock.After(wait)
			// Publish only after the alarm is registered so virtual-clock
			// observers can safely advance once they see the deadline.
			s.counters.armedAt.Store(int64(deadline))
			select {
			case <-s.loopCtx.Done():
				s.stop(s.settleCancellation())
				return
			case event := <-s.puller.items:
				if err := s.apply(event); err != nil {
					s.finishApply(err)
					return
				}
			case <-alarm:
				// An arrival concurrent with the alarm wins
				// deterministically: its arrival resets the deadline, so
				// the timer cannot fire through a boundary item.
				select {
				case event := <-s.puller.items:
					if err := s.apply(event); err != nil {
						s.finishApply(err)
						return
					}
				default:
					if err := s.policy.onTimer(s.emit); err != nil {
						s.stop(err)
						return
					}
				}
			}
		} else {
			s.counters.armedAt.Store(-1)
			select {
			case <-s.loopCtx.Done():
				s.stop(s.settleCancellation())
				return
			case event := <-s.puller.items:
				if err := s.apply(event); err != nil {
					s.finishApply(err)
					return
				}
			}
		}
		s.admit()
	}
}

func (s *policyStream) settleCancellation() error {
	if settler, ok := s.policy.(interface{ onFailed(error) error }); ok {
		return settler.onFailed(context.Canceled)
	}
	return nil
}

// errPolicyDrained reports normal upstream completion inside the engine.
var errPolicyDrained = errors.New("upstream drained")

func (s *policyStream) apply(event upstreamEvent) error {
	switch {
	case event.err != nil:
		return event.err
	case event.ok:
		return s.policy.onItem(event.value, s.emit)
	default:
		if err := s.policy.onDrained(s.emit); err != nil {
			return err
		}
		return errPolicyDrained
	}
}

// finishApply terminates the engine after applying an upstream event: a
// normal drain completes the derived stream, anything else fails it after
// giving the policy one chance to settle its obligations.
func (s *policyStream) finishApply(err error) {
	if errors.Is(err, errPolicyDrained) {
		s.stop(nil)
		return
	}
	if settler, ok := s.policy.(interface{ onFailed(error) error }); ok {
		if settleErr := settler.onFailed(err); settleErr != nil {
			err = settleErr
		}
	}
	s.stop(err)
}

func (s *policyStream) admit() {
	if s.policy.canAccept() {
		s.puller.request()
	}
}

func (s *policyStream) emit(value any) bool {
	select {
	case s.out <- value:
		s.counters.emitted.Add(1)
		return true
	case <-s.loopCtx.Done():
		return false
	}
}

func (s *policyStream) stop(err error) {
	// stopReadingValue is the engine's normal end sentinel (bounded
	// listening reached its deadline); hosts observe clean completion.
	if _, ok := err.(stopReadingValue); ok {
		err = nil
	}
	s.finish.Do(func() {
		s.terminalErr = err
		s.terminalSet.Store(true)
		s.stopLoop()
		s.terminal <- err
	})
}

func (s *policyStream) next(ctx context.Context) (any, bool, error) {
	s.ensureStarted()
	// Prefer ready engine state: a caller whose context expired between
	// receives must not hide an emission or the terminal state.
	select {
	case value := <-s.out:
		if ctx.Err() == nil {
			return value, true, nil
		}
		// This caller gave up before the item arrived; hand the item to
		// the next receiver instead of dropping it. The engine keeps
		// running: only closing the derived stream cancels it.
		go func() {
			select {
			case s.out <- value:
			case <-s.loopCtx.Done():
			}
		}()
		return nil, false, ctx.Err()
	default:
	}
	// An already-delivered terminal outcome replays idempotently.
	if s.terminalSet.Load() {
		_ = s.up.close(context.Background())
		return nil, false, s.terminalErr
	}
	select {
	case value := <-s.out:
		if ctx.Err() == nil {
			return value, true, nil
		}
		go func() {
			select {
			case s.out <- value:
			case <-s.loopCtx.Done():
			}
		}()
		return nil, false, ctx.Err()
	case err := <-s.terminal:
		_ = s.up.close(context.Background())
		return nil, false, err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (s *policyStream) cancel(ctx context.Context) error {
	s.startMu.Lock()
	stop := s.stopLoop
	s.startMu.Unlock()
	if stop != nil {
		stop()
	}
	return s.up.close(ctx)
}

func (s *policyStream) transformStats() map[string]any { return s.counters.stats() }

func keyExtractor(key func(item any) (any, error)) func(item any) (any, error) {
	if key == nil {
		return func(any) (any, error) { return true, nil }
	}
	return key
}

func checkKeyBound(operation string, seen map[any]bool, limit int, key any) error {
	if limit <= 0 || seen[key] {
		return nil
	}
	if len(seen) >= limit {
		return errStreamKeyLimit(operation, limit)
	}
	return nil
}

func keySet[V any](m map[any]V) map[any]bool {
	out := make(map[any]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// limitOrUnbounded normalizes an optional bound for the read-only counters
// (-1 = unbounded).
func limitOrUnbounded(limit int) int64 {
	if limit > 0 {
		return int64(limit)
	}
	return -1
}

// ---------------------------------------------------------------------------
// Debounce: wait for quiet
// ---------------------------------------------------------------------------

type DebounceOptions struct {
	// Key groups items into independent quiet timers. It must return a
	// comparable scalar.
	Key func(item any) (any, error)
	// KeyLimit bounds distinct pending keys; exceeding it fails with
	// StreamKeyLimitExceeded instead of evicting a key.
	KeyLimit int
	// ByteLimit bounds the estimated retained bytes of pending items.
	ByteLimit int
	// TimerLimit bounds distinct pending quiet timers.
	TimerLimit int
}

type debounceEntry struct {
	value    any
	deadline time.Duration
	seq      uint64
}

type debouncePolicy struct {
	clock      StreamClock
	quiet      time.Duration
	keyOf      func(any) (any, error)
	keyLimit   int
	byteLimit  int
	timerLimit int
	counters   *transformCounters
	pending    map[any]*debounceEntry
	seq        uint64
	bytes      int
}

func (p *debouncePolicy) onItem(value any, _ func(any) bool) error {
	key, err := p.keyOf(value)
	if err != nil {
		return err
	}
	if key, err = scalarKey(key); err != nil {
		return err
	}
	if err := checkKeyBound("debounce", keySet(p.pending), p.keyLimit, key); err != nil {
		return err
	}
	size := streamItemSize(value)
	if old, replaced := p.pending[key]; replaced {
		oldSize := streamItemSize(old.value)
		if p.byteLimit > 0 && p.bytes-oldSize+size > p.byteLimit {
			return errStreamByteLimit("debounce", p.bytes-oldSize+size, p.byteLimit)
		}
		// The replaced pending item's obligation is settled, never
		// silently dropped.
		if err := settleOrAbandoned("debounce", old.value, "replaced by newer arrival for the key"); err != nil {
			return err
		}
		p.bytes -= oldSize
		p.counters.replaced.Add(1)
	} else {
		if p.timerLimit > 0 && len(p.pending) >= p.timerLimit {
			return errStreamTimerLimit("debounce", len(p.pending)+1, p.timerLimit)
		}
		if p.byteLimit > 0 && p.bytes+size > p.byteLimit {
			return errStreamByteLimit("debounce", p.bytes+size, p.byteLimit)
		}
		p.counters.pending.Add(1)
	}
	p.seq++
	p.pending[key] = &debounceEntry{value: value, deadline: p.clock.Now() + p.quiet, seq: p.seq}
	p.bytes += size
	p.publishState()
	return nil
}

// publishState mirrors the engine-goroutine-local bounds into the atomic
// counters so snapshots never lock policy state.
func (p *debouncePolicy) publishState() {
	p.counters.timers.Store(int64(len(p.pending)))
	p.counters.bytesHeld.Store(int64(p.bytes))
	p.counters.observeKeys(len(p.pending))
}

// expire emits every entry whose quiet window has fully elapsed, in the
// stable order of each key's last arrival.
func (p *debouncePolicy) expire(now time.Duration, emit func(any) bool) error {
	type dueEntry struct {
		key   any
		entry *debounceEntry
	}
	var expired []dueEntry
	for key, entry := range p.pending {
		if entry.deadline <= now {
			expired = append(expired, dueEntry{key: key, entry: entry})
		}
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].entry.seq < expired[j].entry.seq })
	for _, due := range expired {
		delete(p.pending, due.key)
		p.counters.pending.Add(-1)
		p.bytes -= streamItemSize(due.entry.value)
		p.publishState()
		if !emit(due.entry.value) {
			if err := settleOrAbandoned("debounce", due.entry.value, "downstream canceled before emission"); err != nil {
				return err
			}
			return p.onFailed(context.Canceled)
		}
	}
	return nil
}

func (p *debouncePolicy) onTimer(emit func(any) bool) error {
	return p.expire(p.clock.Now(), emit)
}

func (p *debouncePolicy) onDrained(emit func(any) bool) error {
	type orderedEntry struct {
		key   any
		entry *debounceEntry
	}
	entries := make([]orderedEntry, 0, len(p.pending))
	for key, entry := range p.pending {
		entries = append(entries, orderedEntry{key: key, entry: entry})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].entry.seq < entries[j].entry.seq })
	for _, ordered := range entries {
		delete(p.pending, ordered.key)
		p.counters.pending.Add(-1)
		p.bytes -= streamItemSize(ordered.entry.value)
		p.publishState()
		if !emit(ordered.entry.value) {
			if err := settleOrAbandoned("debounce", ordered.entry.value, "downstream canceled before completion flush"); err != nil {
				return err
			}
			return p.onFailed(context.Canceled)
		}
	}
	return nil
}

// onFailed settles every pending obligation when the engine fails: owned
// items are completed or reported abandoned instead of vanishing silently.
// The first settlement failure replaces the upstream error.
func (p *debouncePolicy) onFailed(error) error {
	var firstErr error
	for key, entry := range p.pending {
		delete(p.pending, key)
		if err := settleOrAbandoned("debounce", entry.value, "upstream failed"); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	p.counters.pending.Store(0)
	p.bytes = 0
	p.publishState()
	return firstErr
}

func (p *debouncePolicy) deadline() (time.Duration, bool) {
	var nearest time.Duration
	found := false
	for _, entry := range p.pending {
		if !found || entry.deadline < nearest {
			nearest, found = entry.deadline, true
		}
	}
	return nearest, found
}

func (p *debouncePolicy) canAccept() bool {
	// Admission stops one item past the bound: the extra arrival either
	// replaces an existing key (bounded) or fails with
	// StreamKeyLimitExceeded, never a silent eviction.
	return p.keyLimit <= 0 || len(p.pending) <= p.keyLimit
}

// Debounce derives a stream that emits only the latest item per key after a
// complete quiet duration with no newer arrival for that key. Upstream
// completion flushes pending items in last-arrival order; upstream failure
// discards them after settling their obligations and propagates immediately.
func Debounce(up StreamHandle, quiet time.Duration, opts DebounceOptions, clock StreamClock) (StreamHandle, error) {
	if quiet <= 0 {
		return nil, fmt.Errorf("debounce quiet duration must be positive")
	}
	counters := &transformCounters{byteLimit: limitOrUnbounded(opts.ByteLimit), timerLimit: limitOrUnbounded(opts.TimerLimit), operation: "wait for activity to be quiet", technical: "debounce"}
	engine := newPolicyStream(clock, up, &debouncePolicy{
		clock:      streamClockOr(clock),
		quiet:      quiet,
		keyOf:      keyExtractor(opts.Key),
		keyLimit:   opts.KeyLimit,
		byteLimit:  opts.ByteLimit,
		timerLimit: opts.TimerLimit,
		counters:   counters,
		pending:    map[any]*debounceEntry{},
	}, counters)
	derived, err := deriveStream(up, "debounce", up.itemType, engine)
	if err != nil {
		return nil, err
	}
	engine.start(derived.streamCtx)
	return derived, nil
}

// ---------------------------------------------------------------------------
// Throttle: limit emission rate
// ---------------------------------------------------------------------------

type ThrottleOptions struct {
	// Keeping is mandatory: "first" drops later items in the window;
	// "latest" retains at most one suppressed item and emits it when
	// capacity returns.
	Keeping  string
	Key      func(item any) (any, error)
	KeyLimit int
	// ByteLimit bounds the estimated retained bytes of suppressed items
	// held for later emission.
	ByteLimit int
	// TimerLimit bounds distinct live keyed windows.
	TimerLimit int
	// OnDrop is the explicit disposal policy for dropped or replaced items
	// that own obligations. Without it, dropping an owned item fails with
	// StreamObligationAbandoned.
	OnDrop func(item any) error
}

type throttleState struct {
	windowStart time.Duration
	used        int
	retained    any
	retainedSet bool
}

type throttlePolicy struct {
	clock      StreamClock
	allowance  int
	window     time.Duration
	keeping    string
	keyOf      func(any) (any, error)
	keyLimit   int
	byteLimit  int
	timerLimit int
	onDrop     func(any) error
	counters   *transformCounters
	states     map[any]*throttleState
	keyOrder   []any
	bytes      int
}

func (p *throttlePolicy) drop(operation string, value any) error {
	p.counters.dropped.Add(1)
	if p.onDrop != nil {
		if err := p.onDrop(value); err != nil {
			return errStreamHandlerCleanup(operation, err.Error())
		}
		return nil
	}
	return settleOrAbandoned(operation, value, "dropped by rate limit")
}

// publishState mirrors the engine-goroutine-local bounds into the atomic
// counters so snapshots never lock policy state.
func (p *throttlePolicy) publishState() {
	p.counters.timers.Store(int64(len(p.states)))
	p.counters.bytesHeld.Store(int64(p.bytes))
	p.counters.observeKeys(len(p.states))
}

// evictExpired forgets keyed state whose window has fully elapsed and that
// holds no retained item, so keyed state stays bounded by live windows: a
// returning key simply starts a fresh window.
func (p *throttlePolicy) evictExpired(now time.Duration) {
	for i := 0; i < len(p.keyOrder); {
		key := p.keyOrder[i]
		st := p.states[key]
		if st == nil {
			p.keyOrder = append(p.keyOrder[:i], p.keyOrder[i+1:]...)
			continue
		}
		if now-st.windowStart >= p.window && !st.retainedSet {
			delete(p.states, key)
			p.keyOrder = append(p.keyOrder[:i], p.keyOrder[i+1:]...)
			continue
		}
		i++
	}
	p.publishState()
}

func (p *throttlePolicy) onItem(value any, emit func(any) bool) error {
	key, err := p.keyOf(value)
	if err != nil {
		return err
	}
	if key, err = scalarKey(key); err != nil {
		return err
	}
	now := p.clock.Now()
	p.evictExpired(now)
	if err := checkKeyBound("throttle", keySet(p.states), p.keyLimit, key); err != nil {
		return err
	}
	st, existing := p.states[key]
	if !existing {
		if p.timerLimit > 0 && len(p.states) >= p.timerLimit {
			return errStreamTimerLimit("throttle", len(p.states)+1, p.timerLimit)
		}
		st = &throttleState{windowStart: now}
		p.states[key] = st
		p.keyOrder = append(p.keyOrder, key)
	}
	if now-st.windowStart >= p.window {
		if st.retainedSet {
			retained := st.retained
			p.bytes -= streamItemSize(retained)
			p.counters.pending.Add(-1)
			st.retained, st.retainedSet = nil, false
			// Capacity returned before this arrival, so the previously retained
			// item is emitted first and occupies one slot in the fresh window.
			st.windowStart, st.used = now, 1
			if !emit(retained) {
				return p.drop("throttle", retained)
			}
		} else {
			st.windowStart, st.used = now, 0
		}
	}
	defer p.publishState()
	if st.used < p.allowance {
		st.used++
		if !emit(value) {
			return p.drop("throttle", value)
		}
		return nil
	}
	if p.keeping == "latest" {
		size := streamItemSize(value)
		if st.retainedSet {
			oldSize := streamItemSize(st.retained)
			if p.byteLimit > 0 && p.bytes-oldSize+size > p.byteLimit {
				return errStreamByteLimit("throttle", p.bytes-oldSize+size, p.byteLimit)
			}
			p.counters.replaced.Add(1)
			if err := p.drop("throttle", st.retained); err != nil {
				return err
			}
			p.bytes -= oldSize
		} else {
			if p.byteLimit > 0 && p.bytes+size > p.byteLimit {
				return errStreamByteLimit("throttle", p.bytes+size, p.byteLimit)
			}
			p.counters.pending.Add(1)
		}
		st.retained, st.retainedSet = value, true
		p.bytes += size
		return nil
	}
	return p.drop("throttle", value)
}

func (p *throttlePolicy) onTimer(emit func(any) bool) error {
	now := p.clock.Now()
	p.evictExpired(now)
	for _, key := range append([]any(nil), p.keyOrder...) {
		st := p.states[key]
		if st == nil || now-st.windowStart < p.window {
			continue
		}
		if st.retainedSet {
			retained := st.retained
			st.retained, st.retainedSet = nil, false
			p.counters.pending.Add(-1)
			p.bytes -= streamItemSize(retained)
			// The retained item takes one slot of the fresh window.
			st.windowStart, st.used = now, 1
			p.publishState()
			if !emit(retained) {
				return p.drop("throttle", retained)
			}
		}
		// An expired empty window was evicted above; an expired window
		// grants no catch-up credit.
	}
	return nil
}

func (p *throttlePolicy) onDrained(emit func(any) bool) error {
	for _, key := range p.keyOrder {
		if st := p.states[key]; st != nil && st.retainedSet {
			retained := st.retained
			st.retained, st.retainedSet = nil, false
			p.counters.pending.Add(-1)
			p.bytes -= streamItemSize(retained)
			p.publishState()
			if !emit(retained) {
				return p.drop("throttle", retained)
			}
		}
	}
	return nil
}

func (p *throttlePolicy) onFailed(error) error {
	var firstErr error
	for key, st := range p.states {
		if st.retainedSet {
			if err := p.drop("throttle", st.retained); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(p.states, key)
	}
	p.keyOrder = nil
	p.bytes = 0
	p.counters.pending.Store(0)
	p.publishState()
	return firstErr
}

func (p *throttlePolicy) deadline() (time.Duration, bool) {
	var nearest time.Duration
	found := false
	for _, st := range p.states {
		due := st.windowStart + p.window
		if !found || due < nearest {
			nearest, found = due, true
		}
	}
	return nearest, found
}

func (*throttlePolicy) canAccept() bool { return true }

// Throttle derives a stream limited to allowance items per window per key.
// keeping must be "first" or "latest"; there is no ambiguous default.
func Throttle(up StreamHandle, allowance int, window time.Duration, opts ThrottleOptions, clock StreamClock) (StreamHandle, error) {
	if opts.Keeping != "first" && opts.Keeping != "latest" {
		return nil, fmt.Errorf(`throttle keeping policy must be "first" or "latest"`)
	}
	if allowance <= 0 || window <= 0 {
		return nil, fmt.Errorf("throttle allowance and window must be positive")
	}
	counters := &transformCounters{byteLimit: limitOrUnbounded(opts.ByteLimit), timerLimit: limitOrUnbounded(opts.TimerLimit), operation: "limit emission rate", technical: "throttle"}
	engine := newPolicyStream(clock, up, &throttlePolicy{
		clock:      streamClockOr(clock),
		allowance:  allowance,
		window:     window,
		keeping:    opts.Keeping,
		keyOf:      keyExtractor(opts.Key),
		keyLimit:   opts.KeyLimit,
		byteLimit:  opts.ByteLimit,
		timerLimit: opts.TimerLimit,
		onDrop:     opts.OnDrop,
		counters:   counters,
		states:     map[any]*throttleState{},
	}, counters)
	derived, err := deriveStream(up, "throttle", up.itemType, engine)
	if err != nil {
		return nil, err
	}
	engine.start(derived.streamCtx)
	return derived, nil
}

// ---------------------------------------------------------------------------
// Distinct until changed
// ---------------------------------------------------------------------------

type DistinctOptions struct {
	// Key selects the scalar value used for consecutive comparison. Composite
	// items require a key so the runtime never hides an unbounded or surprising
	// deep-equality policy behind this bounded transform.
	Key func(item any) (any, error)
}

type distinctSource struct {
	up      *streamHandle
	keyOf   func(any) (any, error)
	last    any
	hasLast bool
	done    bool
}

func (s *distinctSource) next(ctx context.Context) (any, bool, error) {
	for {
		if s.done {
			return nil, false, nil
		}
		value, ok, err := pullWithContext(ctx, s.up)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			s.done = true
			return nil, false, nil
		}
		comparison := value
		if s.keyOf != nil {
			comparison, err = s.keyOf(value)
			if err != nil {
				return nil, false, err
			}
		}
		comparison, err = scalarKey(comparison)
		if err != nil {
			return nil, false, fmt.Errorf("distinct consecutive values require a declared scalar key: %w", err)
		}
		if s.hasLast && valueEquals(s.last, comparison) {
			continue
		}
		s.last = comparison
		s.hasLast = true
		return value, true, nil
	}
}

func (s *distinctSource) cancel(ctx context.Context) error { return s.up.close(ctx) }

// DistinctConsecutive derives a stream that suppresses an item when it
// equals the immediately previous item (per key). Only the previous key is
// retained; there is no global uniqueness set.
func DistinctConsecutive(up StreamHandle, opts DistinctOptions, _ StreamClock) (StreamHandle, error) {
	return deriveStream(up, "distinct_consecutive", up.itemType, &distinctSource{
		up:    up,
		keyOf: opts.Key,
	})
}

// ---------------------------------------------------------------------------
// Bounded batching
// ---------------------------------------------------------------------------

type BatchOptions struct {
	Key func(item any) (any, error)
	// KeyLimit bounds distinct partial-batch keys.
	KeyLimit int
	// AggregateItemLimit bounds items held across all partial batches.
	AggregateItemLimit int
	// ByteLimit bounds the estimated retained bytes of partial batches.
	ByteLimit int
	// TimerLimit bounds distinct partial batches with an armed time limit.
	TimerLimit int
	// OnDrop disposes items discarded when upstream fails. Owned items without
	// a policy fail with StreamObligationAbandoned instead of vanishing.
	OnDrop func(item any) error
}

type batchState struct {
	items     []any
	startedAt time.Duration
	firstSeq  uint64
}

type batchPolicy struct {
	clock      StreamClock
	count      int
	window     time.Duration
	keyOf      func(any) (any, error)
	keyLimit   int
	itemLimit  int
	byteLimit  int
	timerLimit int
	onDrop     func(any) error
	counters   *transformCounters
	batches    map[any]*batchState
	keyOrder   []any
	seq        uint64
	held       int
	bytes      int
}

// publishState mirrors the engine-goroutine-local bounds into the atomic
// counters so snapshots never lock policy state.
func (p *batchPolicy) publishState() {
	p.counters.pending.Store(int64(p.held))
	p.counters.timers.Store(int64(len(p.batches)))
	p.counters.bytesHeld.Store(int64(p.bytes))
	p.counters.observeKeys(len(p.batches))
	sizes := make([]int, 0, len(p.batches))
	for _, key := range p.keyOrder {
		if batch := p.batches[key]; batch != nil {
			sizes = append(sizes, len(batch.items))
		}
	}
	p.counters.setBatchSizes(sizes)
}

func (p *batchPolicy) onItem(value any, emit func(any) bool) error {
	key, err := p.keyOf(value)
	if err != nil {
		return err
	}
	if key, err = scalarKey(key); err != nil {
		return err
	}
	if err := checkKeyBound("batch", keySet(p.batches), p.keyLimit, key); err != nil {
		return err
	}
	if p.itemLimit > 0 && p.held >= p.itemLimit {
		return streamFailure("StreamLimitExceeded",
			fmt.Sprintf("batching held %d items across partial batches, over its bound of %d", p.held, p.itemLimit),
			map[string]any{"operation": "batch", "limit": float64(p.itemLimit), "received": float64(p.held)})
	}
	size := streamItemSize(value)
	if p.byteLimit > 0 && p.bytes+size > p.byteLimit {
		return errStreamByteLimit("batch", p.bytes+size, p.byteLimit)
	}
	st, ok := p.batches[key]
	if !ok {
		if p.timerLimit > 0 && len(p.batches) >= p.timerLimit {
			return errStreamTimerLimit("batch", len(p.batches)+1, p.timerLimit)
		}
		p.seq++
		st = &batchState{startedAt: p.clock.Now(), firstSeq: p.seq}
		p.batches[key] = st
		p.keyOrder = append(p.keyOrder, key)
	}
	st.items = append(st.items, value)
	p.held++
	p.bytes += size
	p.publishState()
	if len(st.items) >= p.count {
		return p.flush(key, st, emit)
	}
	return nil
}

func (p *batchPolicy) flush(key any, st *batchState, emit func(any) bool) error {
	batch := append([]any(nil), st.items...)
	delete(p.batches, key)
	p.held -= len(st.items)
	for _, item := range batch {
		p.bytes -= streamItemSize(item)
	}
	p.publishState()
	if emit(batch) {
		return nil
	}
	var firstErr error
	for _, item := range batch {
		p.counters.dropped.Add(1)
		var err error
		if p.onDrop != nil {
			err = p.onDrop(item)
			if err != nil {
				err = errStreamHandlerCleanup("batch", err.Error())
			}
		} else {
			err = settleOrAbandoned("batch", item, "downstream canceled before batch emission")
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (p *batchPolicy) onTimer(emit func(any) bool) error {
	now := p.clock.Now()
	for _, key := range append([]any(nil), p.keyOrder...) {
		if st := p.batches[key]; st != nil && len(st.items) > 0 && now-st.startedAt >= p.window {
			if err := p.flush(key, st, emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *batchPolicy) onDrained(emit func(any) bool) error {
	for _, key := range append([]any(nil), p.keyOrder...) {
		if st := p.batches[key]; st != nil && len(st.items) > 0 {
			if err := p.flush(key, st, emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *batchPolicy) onFailed(error) error {
	var firstErr error
	for key, batch := range p.batches {
		for _, item := range batch.items {
			p.counters.dropped.Add(1)
			var err error
			if p.onDrop != nil {
				err = p.onDrop(item)
				if err != nil {
					err = errStreamHandlerCleanup("batch", err.Error())
				}
			} else {
				err = settleOrAbandoned("batch", item, "upstream failed")
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(p.batches, key)
	}
	p.keyOrder = nil
	p.held, p.bytes = 0, 0
	p.publishState()
	return firstErr
}

func (p *batchPolicy) deadline() (time.Duration, bool) {
	var nearest time.Duration
	found := false
	for _, st := range p.batches {
		if len(st.items) == 0 {
			continue
		}
		due := st.startedAt + p.window
		if !found || due < nearest {
			nearest, found = due, true
		}
	}
	return nearest, found
}

func (p *batchPolicy) canAccept() bool {
	// One item past the aggregate bound is admitted so the bound fails
	// observably (StreamLimitExceeded) instead of stalling upstream.
	if p.itemLimit > 0 && p.held > p.itemLimit {
		return false
	}
	if p.keyLimit > 0 && len(p.batches) >= p.keyLimit {
		// Existing keys may still grow; admission resumes and the bound is
		// re-checked per arrival by checkKeyBound.
		return true
	}
	return true
}

// Batch derives a stream of bounded lists. A batch is emitted when its count
// limit or its time limit (armed by the first item of the batch) is reached.
// Empty periodic batches are never emitted; upstream completion flushes each
// nonempty partial batch before the derived stream completes.
func Batch(up StreamHandle, count int, window time.Duration, opts BatchOptions, clock StreamClock) (StreamHandle, error) {
	if count <= 0 || window <= 0 {
		return nil, fmt.Errorf("batch count and window must be positive")
	}
	counters := &transformCounters{byteLimit: limitOrUnbounded(opts.ByteLimit), timerLimit: limitOrUnbounded(opts.TimerLimit), operation: "group into bounded batches", technical: "batch"}
	engine := newPolicyStream(clock, up, &batchPolicy{
		clock:      streamClockOr(clock),
		count:      count,
		window:     window,
		keyOf:      keyExtractor(opts.Key),
		keyLimit:   opts.KeyLimit,
		itemLimit:  opts.AggregateItemLimit,
		byteLimit:  opts.ByteLimit,
		timerLimit: opts.TimerLimit,
		onDrop:     opts.OnDrop,
		counters:   counters,
		batches:    map[any]*batchState{},
	}, counters)
	itemType := up.itemType.base()
	derived, err := deriveStream(up, "batch", TypeRef{Element: &itemType}, engine)
	if err != nil {
		return nil, err
	}
	engine.start(derived.streamCtx)
	return derived, nil
}

// ---------------------------------------------------------------------------
// Idle timeout and lifetime limits
// ---------------------------------------------------------------------------

type relayPolicy struct {
	clock    StreamClock
	idle     time.Duration
	lifetime time.Duration
	// failAtDeadline selects the required-deadline form (StreamDeadlineExceeded)
	// over intentional bounded listening (cancel upstream, complete normally).
	failAtDeadline bool
	lastArrival    time.Duration
	startedAt      time.Duration
	started        bool
}

func (p *relayPolicy) onItem(value any, emit func(any) bool) error {
	if !emit(value) {
		return settleOrAbandoned("stream relay", value, "downstream canceled before emission")
	}
	// The idle window rearms from delivery: waiting caused solely by
	// downstream backpressure (the blocked emission above) never burns it.
	p.lastArrival = p.clock.Now()
	return nil
}

func (p *relayPolicy) onTimer(_ func(any) bool) error {
	now := p.clock.Now()
	if p.lifetime > 0 {
		if p.failAtDeadline {
			return errStreamDeadlineExceeded(p.lifetime)
		}
		return stopReadingValue{}
	}
	return errStreamIdleTimeout(now - p.lastArrival)
}

func (p *relayPolicy) onDrained(_ func(any) bool) error { return nil }

func (p *relayPolicy) deadline() (time.Duration, bool) {
	if !p.started {
		p.startedAt, p.started = p.clock.Now(), true
		p.lastArrival = p.startedAt
	}
	if p.lifetime > 0 {
		return p.startedAt + p.lifetime, true
	}
	return p.lastArrival + p.idle, true
}

func (*relayPolicy) canAccept() bool { return true }

func newRelay(up StreamHandle, idle, deadline time.Duration, failAtDeadline bool, clock StreamClock, operation string) (StreamHandle, error) {
	if idle <= 0 && deadline <= 0 {
		return nil, fmt.Errorf("a duration must be positive unless a construction explicitly permits zero")
	}
	canonical := map[string]string{"idle_timeout": "require continued activity", "take_for": "limit total listening time", "deadline": "require completion within a deadline"}[operation]
	counters := &transformCounters{byteLimit: -1, timerLimit: -1, operation: canonical, technical: operation}
	engine := newPolicyStream(clock, up, &relayPolicy{clock: streamClockOr(clock), idle: idle, lifetime: deadline, failAtDeadline: failAtDeadline}, counters)
	derived, err := deriveStream(up, operation, up.itemType, engine)
	if err != nil {
		return nil, err
	}
	// Lifetime and idle timers start at the first read, not construction:
	// before consumption nothing is armed.
	engine.parent = derived.streamCtx
	return derived, nil
}

// RequireIdle fails the derived stream with StreamIdleTimeout and cancels
// upstream when no item is delivered within idle. Waiting caused solely by
// downstream backpressure does not count: the window rearms from delivery,
// after the blocked emission returns.
func RequireIdle(up StreamHandle, idle time.Duration, clock StreamClock) (StreamHandle, error) {
	return newRelay(up, idle, 0, false, clock, "idle_timeout")
}

// LimitLifetime stops normally at the deadline (bounded listening).
func LimitLifetime(up StreamHandle, lifetime time.Duration, clock StreamClock) (StreamHandle, error) {
	return newRelay(up, 0, lifetime, false, clock, "take_for")
}

// RequireDeadline fails with StreamDeadlineExceeded at the deadline.
func RequireDeadline(up StreamHandle, lifetime time.Duration, clock StreamClock) (StreamHandle, error) {
	return newRelay(up, 0, lifetime, true, clock, "deadline")
}

// ---------------------------------------------------------------------------
// Bounded sample
// ---------------------------------------------------------------------------

type takeSource struct {
	up    *streamHandle
	limit int
	taken int
	done  bool
}

func (s *takeSource) next(ctx context.Context) (any, bool, error) {
	if s.done {
		return nil, false, nil
	}
	if s.taken >= s.limit {
		s.done = true
		_ = s.up.close(context.Background())
		return nil, false, nil
	}
	value, ok, err := pullWithContext(ctx, s.up)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		s.done = true
		return nil, false, nil
	}
	s.taken++
	return value, true, nil
}

func (s *takeSource) cancel(ctx context.Context) error { return s.up.close(ctx) }

// TakeSample consumes up to limit items, then cancels upstream and succeeds
// (possibly with fewer items when upstream completes first).
func TakeSample(up StreamHandle, limit int, _ StreamClock) (StreamHandle, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("sample limit must be positive")
	}
	return deriveStream(up, "take", up.itemType, &takeSource{up: up, limit: limit})
}

// ---------------------------------------------------------------------------
// Handling policies
// ---------------------------------------------------------------------------

// StreamItemHandler processes one item under a cancellable context.
type StreamItemHandler func(ctx context.Context, item any) error

// StreamValueHandler processes one item and produces a result value.
type StreamValueHandler func(ctx context.Context, item any) (any, error)

const handlerObligationOperation = "handle"
const defaultHandlerCleanupDeadline = 5 * time.Second

// HandleSequentially handles every item one at a time in source order. The
// next item is requested only after the current handler finishes.
func HandleSequentially(ctx context.Context, up StreamHandle, handler StreamItemHandler) error {
	counters := newHandlingCounters("handle items sequentially", "concat", false, nil)
	attachHandlingCounters(up, counters)
	defer func() { _ = up.Close(context.Background()) }()
	for {
		value, ok, err := pullWithContext(ctx, up)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		counters.active.Add(1)
		handlerErr := handler(ctx, value)
		counters.active.Add(-1)
		if handlerErr != nil {
			_ = settleOrAbandoned(handlerObligationOperation, value, "handler failed")
			return handlerErr
		}
		if err := settleOrAbandoned(handlerObligationOperation, value, "handler returned without completing item"); err != nil {
			return err
		}
	}
}

// HandleWithBoundConcurrency handles items with at most limit child contexts.
// Items start in source order; completion order is unconstrained. A handler
// failure cancels upstream and sibling handlers, waits for them, and
// propagates unless the handler recovers.
func HandleWithBoundConcurrency(ctx context.Context, up StreamHandle, limit int, handler StreamItemHandler) error {
	return HandleWithBoundConcurrencyCleanup(ctx, up, limit, defaultHandlerCleanupDeadline, handler)
}

// HandleWithBoundConcurrencyCleanup is HandleWithBoundConcurrency with an
// explicit deadline for sibling cleanup after cancellation or failure.
func HandleWithBoundConcurrencyCleanup(ctx context.Context, up StreamHandle, limit int, cleanupDeadline time.Duration, handler StreamItemHandler) error {
	if limit <= 0 {
		return errStreamConcurrencyLimit(limit)
	}
	if cleanupDeadline <= 0 {
		return fmt.Errorf("handler cleanup deadline must be positive")
	}
	counters := newHandlingCounters("handle each with bounded concurrency", "merge", true, nil)
	attachHandlingCounters(up, counters)
	defer func() { _ = up.Close(context.Background()) }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	waitCleanup := func() error {
		counters.cleanup.Store("cleaning")
		defer counters.cleanup.Store("idle")
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		timer := time.NewTimer(cleanupDeadline)
		defer timer.Stop()
		select {
		case <-done:
			return nil
		case <-timer.C:
			return errStreamHandlerCleanup("handle each with bounded concurrency", "cleanup deadline exceeded")
		}
	}
	failures := make(chan error, limit)
	type pullResult struct {
		value any
		ok    bool
		err   error
	}
	for {
		select {
		case slots <- struct{}{}:
		case err := <-failures:
			cancel()
			if cleanupErr := waitCleanup(); cleanupErr != nil {
				return cleanupErr
			}
			return err
		case <-runCtx.Done():
			if cleanupErr := waitCleanup(); cleanupErr != nil {
				return cleanupErr
			}
			return runCtx.Err()
		}
		pulled := make(chan pullResult, 1)
		go func() {
			value, ok, err := pullWithContext(runCtx, up)
			pulled <- pullResult{value: value, ok: ok, err: err}
		}()
		var value any
		var ok bool
		var err error
		select {
		case result := <-pulled:
			value, ok, err = result.value, result.ok, result.err
		case handlerErr := <-failures:
			cancel()
			if cleanupErr := waitCleanup(); cleanupErr != nil {
				return cleanupErr
			}
			return handlerErr
		case <-runCtx.Done():
			if cleanupErr := waitCleanup(); cleanupErr != nil {
				return cleanupErr
			}
			return runCtx.Err()
		}
		if err != nil {
			cancel()
			if cleanupErr := waitCleanup(); cleanupErr != nil {
				return cleanupErr
			}
			select {
			case handlerErr := <-failures:
				return handlerErr
			default:
				return err
			}
		}
		if !ok {
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
				select {
				case handlerErr := <-failures:
					return handlerErr
				default:
					return nil
				}
			case handlerErr := <-failures:
				cancel()
				if cleanupErr := waitCleanup(); cleanupErr != nil {
					return cleanupErr
				}
				return handlerErr
			case <-runCtx.Done():
				if cleanupErr := waitCleanup(); cleanupErr != nil {
					return cleanupErr
				}
				return runCtx.Err()
			}
		}
		wg.Add(1)
		go func(item any) {
			defer wg.Done()
			defer func() { <-slots }()
			counters.active.Add(1)
			defer counters.active.Add(-1)
			if handlerErr := handler(runCtx, item); handlerErr != nil {
				_ = settleOrAbandoned(handlerObligationOperation, item, "handler failed")
				select {
				case failures <- handlerErr:
				default:
				}
				return
			}
			if abandonErr := settleOrAbandoned(handlerObligationOperation, item, "handler returned without completing item"); abandonErr != nil {
				select {
				case failures <- abandonErr:
				default:
				}
			}
		}(value)
	}
}

// LatestOptions configures latest-only (switch) handling.
type LatestOptions struct {
	Key         func(item any) (any, error)
	KeyLimit    int
	Concurrency int
	// OnResult observes results from surviving (non-stale) handlers only.
	OnResult func(item, result any)
	// CleanupDeadline bounds the wait for a canceled handler to finish
	// before its replacement starts. Zero starts the replacement at once.
	CleanupDeadline time.Duration
	// Replacement is the explicit policy applied to an owned item whose
	// handler is being canceled (for example, reject with status 409).
	Replacement func(item *OwnedItem) error
	// Effects declares handler effect metadata; non-idempotent effects
	// require AcknowledgeEffects.
	Effects            []EffectSafety
	AcknowledgeEffects bool
	Clock              StreamClock
}
type latestSlot struct {
	done   chan struct{}
	cancel context.CancelFunc
	item   any
}

// HandleLatest runs only the newest item's handler: a newer arrival (per
// key) cancels the previous handler's context, suppresses its later result,
// and starts the newest item. Only the newest waiting item is retained, so
// the retained queue is bounded to one per key.
func HandleLatest(ctx context.Context, up StreamHandle, handler StreamValueHandler, opts LatestOptions) error {
	warnings, err := ValidateEffectSafety(opts.Effects, opts.AcknowledgeEffects)
	if err != nil {
		return err
	}
	counters := newHandlingCounters("handle only the newest", "switch_latest", true, warnings)
	attachHandlingCounters(up, counters)
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if opts.KeyLimit < 0 {
		return fmt.Errorf("latest key limit must not be negative")
	}
	clock := streamClockOr(opts.Clock)
	defer func() { _ = up.Close(context.Background()) }()
	runCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	keyOf := keyExtractor(opts.Key)

	var mu sync.Mutex
	gens := map[any]uint64{}
	slots := map[any]*latestSlot{}
	keys := map[any]bool{}
	var active atomic.Int64
	failures := make(chan error, 1)

	waitAll := func() {
		for {
			mu.Lock()
			pending := make([]*latestSlot, 0, len(slots))
			for _, slot := range slots {
				pending = append(pending, slot)
			}
			mu.Unlock()
			if len(pending) == 0 {
				return
			}
			for _, slot := range pending {
				<-slot.done
			}
		}
	}
	waitAllCleanup := func() error {
		if opts.CleanupDeadline <= 0 {
			return nil
		}
		done := make(chan struct{})
		counters.cleanup.Store("cleaning")
		defer counters.cleanup.Store("idle")
		go func() {
			waitAll()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-clock.After(opts.CleanupDeadline):
			return errStreamHandlerCleanup("handle only the newest", "cleanup deadline exceeded")
		}
	}

	start := func(key, value any) {
		mu.Lock()
		gens[key]++
		gen := gens[key]
		handlerCtx, cancel := context.WithCancel(runCtx)
		done := make(chan struct{})
		slots[key] = &latestSlot{done: done, cancel: cancel, item: value}
		mu.Unlock()
		active.Add(1)
		counters.active.Add(1)
		go func() {
			defer active.Add(-1)
			defer counters.active.Add(-1)
			defer cancel()
			defer close(done)
			result, err := handler(handlerCtx, value)
			mu.Lock()
			stale := gens[key] != gen
			mu.Unlock()
			if stale {
				// A newer item replaced this handler; its result never
				// becomes visible.
				return
			}
			if err != nil {
				select {
				case failures <- err:
				default:
				}
			} else if opts.OnResult != nil {
				opts.OnResult(value, result)
			}
			// Keep the slot visible until failure publication or result delivery
			// completes, so upstream completion cannot race ahead of either.
			mu.Lock()
			if gens[key] == gen {
				delete(slots, key)
			}
			mu.Unlock()
		}()
	}

	// replaceOwned applies the explicit replacement policy to an owned item
	// whose handler is about to be canceled.
	replaceOwned := func(value any) error {
		owned, ok := value.(*OwnedItem)
		if !ok {
			return nil
		}
		if opts.Replacement != nil {
			return opts.Replacement(owned)
		}
		if !owned.Settled() && owned.Dispose == nil {
			return errStreamObligationAbandoned("handle only the newest", owned.ID)
		}
		return settleOrAbandoned("handle only the newest", owned, "canceled by newer item")
	}

	waitSlot := func(slot *latestSlot) error {
		if opts.CleanupDeadline <= 0 {
			return nil
		}
		counters.cleanup.Store("cleaning")
		defer counters.cleanup.Store("idle")
		select {
		case <-slot.done:
			return nil
		case <-clock.After(opts.CleanupDeadline):
			return errStreamHandlerCleanup("handle only the newest", "cleanup deadline exceeded")
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}

	puller := newUpstreamPuller(runCtx, up)
	puller.request()
	for {
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case err := <-failures:
			cancelAll()
			if cleanupErr := waitAllCleanup(); cleanupErr != nil {
				return cleanupErr
			}
			return err
		case event := <-puller.items:
			if event.err != nil {
				cancelAll()
				if cleanupErr := waitAllCleanup(); cleanupErr != nil {
					return cleanupErr
				}
				return event.err
			}
			if !event.ok {
				// Completion waits for every admitted handler to finish;
				// cancellation is not imposed on surviving work.
				done := make(chan struct{})
				go func() {
					waitAll()
					close(done)
				}()
				select {
				case <-done:
					cancelAll()
					select {
					case handlerErr := <-failures:
						return handlerErr
					default:
						return nil
					}
				case handlerErr := <-failures:
					cancelAll()
					if cleanupErr := waitAllCleanup(); cleanupErr != nil {
						return cleanupErr
					}
					return handlerErr
				case <-runCtx.Done():
					if cleanupErr := waitAllCleanup(); cleanupErr != nil {
						return cleanupErr
					}
					return runCtx.Err()
				}
			}
			key, kerr := keyOf(event.value)
			if kerr == nil {
				key, kerr = scalarKey(key)
			}
			if kerr != nil {
				cancelAll()
				if cleanupErr := waitAllCleanup(); cleanupErr != nil {
					return cleanupErr
				}
				return kerr
			}
			mu.Lock()
			_, known := keys[key]
			if opts.KeyLimit > 0 && !known && len(keys) >= opts.KeyLimit {
				mu.Unlock()
				cancelAll()
				if cleanupErr := waitAllCleanup(); cleanupErr != nil {
					return cleanupErr
				}
				return errStreamKeyLimit("handle only the newest", opts.KeyLimit)
			}
			if !known {
				keys[key] = true
			}
			slot := slots[key]
			mu.Unlock()
			hadSlot := slot != nil
			if !hadSlot && int(active.Load()) >= concurrency {
				cancelAll()
				if cleanupErr := waitAllCleanup(); cleanupErr != nil {
					return cleanupErr
				}
				return errStreamConcurrencyLimit(concurrency)
			}
			if slot != nil {
				counters.waiting.Store(1)
				select {
				case <-slot.done:
				default:
					if err := replaceOwned(slot.item); err != nil {
						cancelAll()
						if cleanupErr := waitAllCleanup(); cleanupErr != nil {
							return cleanupErr
						}
						return err
					}
					// Supersede first: bump the key's generation so the
					// canceled handler's later result is stale even if it
					// finishes during the cleanup wait.
					mu.Lock()
					gens[key]++
					delete(slots, key)
					mu.Unlock()
					slot.cancel()
					if err := waitSlot(slot); err != nil {
						return err
					}
				}
				counters.waiting.Store(0)
			}
			start(key, event.value)
			puller.request()
		}
	}
}

// ConflateOptions configures finish-active-and-replace-queued handling.
type ConflateOptions struct {
	// OnDrop disposes a replaced waiting item; owned items without a policy
	// fail with StreamObligationAbandoned.
	OnDrop func(item any) error
}

// HandleConflating handles items one at a time and never cancels the active
// handler: while it runs, at most one waiting item is retained and replaced
// by newer arrivals.
func HandleConflating(ctx context.Context, up StreamHandle, handler StreamItemHandler, opts ConflateOptions) error {
	counters := newHandlingCounters("finish active work and keep latest waiting", "conflate", false, nil)
	attachHandlingCounters(up, counters)
	defer func() { _ = up.Close(context.Background()) }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	puller := newUpstreamPuller(runCtx, up)
	puller.request()
	var activeDone chan struct{}
	var waiting any
	hasWaiting := false
	failures := make(chan error, 1)
	drop := func(item any) error {
		if opts.OnDrop != nil {
			if err := opts.OnDrop(item); err != nil {
				return errStreamHandlerCleanup("conflate", err.Error())
			}
			return nil
		}
		return settleOrAbandoned("conflate", item, "replaced by newer waiting item")
	}
	dropWaiting := func() error {
		if !hasWaiting {
			return nil
		}
		item := waiting
		waiting, hasWaiting = nil, false
		return drop(item)
	}
	for {
		if activeDone == nil && hasWaiting {
			item := waiting
			waiting, hasWaiting = nil, false
			done := make(chan struct{})
			activeDone = done
			counters.waiting.Store(0)
			counters.active.Add(1)
			go func() {
				defer counters.active.Add(-1)
				defer close(done)
				if err := handler(runCtx, item); err != nil {
					select {
					case failures <- err:
					default:
					}
					return
				}
				if err := settleOrAbandoned("conflate", item, "handler returned without completing item"); err != nil {
					select {
					case failures <- err:
					default:
					}
				}
			}()
			puller.request()
		}
		select {
		case <-runCtx.Done():
			if err := dropWaiting(); err != nil {
				return err
			}
			if activeDone != nil {
				<-activeDone
			}
			return runCtx.Err()
		case err := <-failures:
			cancel()
			if dropErr := dropWaiting(); dropErr != nil {
				return dropErr
			}
			if activeDone != nil {
				<-activeDone
			}
			return err
		case <-activeDone:
			activeDone = nil
			puller.request()
		case event := <-puller.items:
			switch {
			case event.err != nil:
				cancel()
				if dropErr := dropWaiting(); dropErr != nil {
					return dropErr
				}
				if activeDone != nil {
					<-activeDone
				}
				return event.err
			case event.ok:
				if hasWaiting {
					if err := drop(waiting); err != nil {
						cancel()
						if activeDone != nil {
							<-activeDone
						}
						return err
					}
				}
				waiting, hasWaiting = event.value, true
				counters.waiting.Store(1)
				if activeDone == nil {
					// Handled at the top of the next iteration.
				} else {
					puller.request()
				}
			default:
				if activeDone != nil {
					<-activeDone
					activeDone = nil
				}
				select {
				case err := <-failures:
					cancel()
					if dropErr := dropWaiting(); dropErr != nil {
						return dropErr
					}
					return err
				default:
				}
				if hasWaiting {
					item := waiting
					waiting, hasWaiting = nil, false
					if err := handler(runCtx, item); err != nil {
						cancel()
						return err
					}
					if err := settleOrAbandoned("conflate", item, "handler returned without completing item"); err != nil {
						cancel()
						return err
					}
				}
				cancel()
				select {
				case err := <-failures:
					return err
				default:
					return nil
				}
			}
		}
	}
}

// BusyPolicy selects the arrival-while-busy behavior of exclusive handling.
type BusyPolicy string

const (
	// BusyIgnore drops new arrivals while a handler is active. It is valid
	// only for values without completion obligations.
	BusyIgnore BusyPolicy = "ignore"
	// BusyReject completes new arrivals through the typed reject action.
	BusyReject BusyPolicy = "reject"
)

type ExclusiveOptions struct {
	Policy BusyPolicy
	// Reject must successfully complete the item (for example, with a 429
	// response). Required for BusyReject.
	Reject func(item any) error
	// OnDrop is the disposal policy for BusyIgnore items; owned items
	// without it fail with StreamObligationAbandoned.
	OnDrop func(item any) error
}

// HandleExclusively handles one item at a time. Arrivals while the handler is
// busy are ignored or rejected according to the policy; ignoring a value
// with an unresolved obligation is invalid.
func HandleExclusively(ctx context.Context, up StreamHandle, handler StreamItemHandler, opts ExclusiveOptions) error {
	if opts.Policy != BusyIgnore && opts.Policy != BusyReject {
		return fmt.Errorf(`exclusive busy policy must be "ignore" or "reject"`)
	}
	if opts.Policy == BusyReject && opts.Reject == nil {
		return fmt.Errorf("rejecting new requests while busy requires a typed rejection action")
	}
	counters := newHandlingCounters("handle one while busy", "exhaust", false, nil)
	attachHandlingCounters(up, counters)
	defer func() { _ = up.Close(context.Background()) }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	puller := newUpstreamPuller(runCtx, up)
	puller.request()
	var activeDone chan struct{}
	failures := make(chan error, 1)
	dispose := func(item any, reason string) error {
		if opts.Policy == BusyReject {
			if err := opts.Reject(item); err != nil {
				return errStreamHandlerCleanup("exhaust", err.Error())
			}
			up.rejected.Add(1)
			return nil
		}
		if opts.OnDrop != nil {
			if err := opts.OnDrop(item); err != nil {
				return errStreamHandlerCleanup("exhaust", err.Error())
			}
			return nil
		}
		return settleOrAbandoned("exhaust", item, reason)
	}
	for {
		select {
		case <-runCtx.Done():
			if activeDone != nil {
				<-activeDone
			}
			return runCtx.Err()
		case err := <-failures:
			cancel()
			if activeDone != nil {
				<-activeDone
			}
			return err
		case <-activeDone:
			activeDone = nil
			puller.request()
		case event := <-puller.items:
			switch {
			case event.err != nil:
				cancel()
				if activeDone != nil {
					<-activeDone
				}
				return event.err
			case !event.ok:
				if activeDone != nil {
					<-activeDone
				}
				cancel()
				select {
				case err := <-failures:
					return err
				default:
					return nil
				}
			default:
				if activeDone != nil {
					counters.waiting.Store(1)
					if err := dispose(event.value, "ignored while busy"); err != nil {
						cancel()
						<-activeDone
						return err
					}
					counters.waiting.Store(0)
					puller.request()
					continue
				}
				done := make(chan struct{})
				activeDone = done
				counters.active.Add(1)
				go func(item any) {
					defer close(done)
					defer counters.active.Add(-1)
					if err := handler(runCtx, item); err != nil {
						select {
						case failures <- err:
						default:
						}
						return
					}
					if err := settleOrAbandoned("exhaust", item, "handler returned without completing item"); err != nil {
						select {
						case failures <- err:
						default:
						}
					}
				}(event.value)
				puller.request()
			}
		}
	}
}
