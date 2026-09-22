package sos

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
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
}

func newStreamHandle(itemType TypeRef, producer string, source streamSource) *streamHandle {
	return &streamHandle{source: source, itemType: itemType, producer: producer, state: streamActive}
}

func (s *streamHandle) beginConsumption() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != streamActive {
		return fmt.Errorf("stream was already consumed")
	}
	s.state = streamConsumed
	return nil
}

func (s *streamHandle) next(ctx context.Context) (any, bool, error) {
	v, ok, err := s.source.next(ctx)
	if ok {
		s.mu.Lock()
		s.itemsReceived++
		s.mu.Unlock()
	}
	return v, ok, err
}

func (s *streamHandle) close(ctx context.Context) error {
	s.mu.Lock()
	if s.state == streamClosed {
		s.mu.Unlock()
		return nil
	}
	s.state = streamClosed
	s.mu.Unlock()
	return s.source.cancel(ctx)
}

func (s *streamHandle) debugValue() map[string]any {
	s.mu.Lock()
	state, received := s.state, s.itemsReceived
	buffered, credit := s.itemsBuffered, s.creditAvailable
	itemType, producer := s.itemType.String(), s.producer
	s.mu.Unlock()
	if metrics, ok := s.source.(streamMetricsSource); ok {
		buffered, credit = metrics.streamMetrics()
	}
	return map[string]any{"state": string(state), "itemType": itemType, "itemsReceived": received, "itemsBuffered": buffered, "creditAvailable": credit, "producer": producer}
}

// DebugStreamState returns a non-consuming snapshot for debugger and Studio
// adapters. It never requests producer credit or receives an item.
func (s *streamHandle) DebugStreamState() map[string]any { return s.debugValue() }

type stopReadingValue struct{}

func (stopReadingValue) Error() string { return "stop reading" }

func (r *runtime) openExternalStream(actionName string, expressions []string) (*streamHandle, error) {
	alias, name, qualified := strings.Cut(actionName, ".")
	if !qualified {
		return nil, fmt.Errorf("streaming action %s has no host implementation", actionName)
	}
	mod := r.imports[alias]
	if mod == nil || mod.external == nil {
		return nil, fmt.Errorf("unknown external streaming action %s", actionName)
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
	return newStreamHandle(itemType, actionName, source), nil
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
		return fmt.Errorf("%s was already consumed", name)
	}
	items := make([]any, 0, int(limit))
	for len(items) < int(limit) {
		item, more, err := stream.next(r.ctx)
		if err != nil {
			_ = stream.close(context.Background())
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
		_ = stream.close(context.Background())
		return err
	}
	if more {
		_ = stream.close(r.ctx)
		return fmt.Errorf("collect at most %d items exceeded its limit", int(limit))
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
