package sos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

type executionState struct {
	steps   atomic.Int64
	calls   atomic.Int64
	output  atomic.Int64
	streams atomic.Int64
	traceMu sync.Mutex
}

type executionLimitError struct{ message string }

func (e *executionLimitError) Error() string { return e.message }

type operationTimeoutError struct{ message string }

func (e *operationTimeoutError) Error() string { return e.message }

// FailureValue exposes portable error fields without requiring string matching.
// Existing handlers retain their error text; failure is the structured companion.
func FailureValue(err error) map[string]any {
	var typed *typedFailure
	if errors.As(err, &typed) {
		out := make(map[string]any, len(typed.value)+1)
		for k, v := range typed.value {
			out[k] = v
		}
		if _, ok := out["kind"]; !ok {
			out["kind"] = typed.kind
		}
		if _, ok := out["message"]; !ok {
			out["message"] = typed.Error()
		}
		if _, ok := out["retryable"]; !ok {
			out["retryable"] = false
		}
		if len(typed.frames) > 0 {
			out["frames"] = typed.frames
		}
		return out
	}
	var recorded *recordedFailure
	if errors.As(err, &recorded) {
		out := make(map[string]any, len(recorded.value))
		for k, v := range recorded.value {
			out[k] = v
		}
		out["message"] = err.Error()
		return out
	}
	out := map[string]any{"kind": "runtime", "message": err.Error(), "retryable": false, "status": float64(0)}
	var api *typesafe.APIError
	var budget *BudgetError
	var limit *executionLimitError
	var valueLimit *valueLimitError
	var operationTimeout *operationTimeoutError
	switch {
	case errors.Is(err, context.Canceled):
		out["kind"] = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		out["kind"] = "timeout"
	case errors.As(err, &budget):
		out["kind"] = "budget"
	case errors.As(err, &limit) || errors.As(err, &valueLimit):
		out["kind"] = "limit"
	case errors.As(err, &operationTimeout):
		out["kind"] = "operation_timeout"
		out["retryable"] = true
	case errors.As(err, &api):
		out["kind"] = "provider"
		out["status"] = float64(api.Status)
		out["retryable"] = api.Retryable()
		out["code"] = api.Type
		if strings.Contains(api.Message, "max_tokens_exceeded") || strings.Contains(api.Type, "max_tokens") {
			out["kind"] = "input_too_large"
		}
	default:
		var transport *typesafe.ConnectionError
		if errors.As(err, &transport) {
			out["kind"] = "connection"
			out["retryable"] = true
		}
	}
	return out
}

type typedFailure struct {
	kind   string
	value  map[string]any
	frames []map[string]any
}

func (e *typedFailure) Error() string {
	if message, ok := e.value["message"].(string); ok {
		return message
	}
	return e.kind
}

func (e *typedFailure) failureKind() string { return e.kind }

func fatalParallel(err error) bool {
	var b *BudgetError
	var l *executionLimitError
	var exit interface{ ExitCode() int }
	var replay *ReplayIntegrityError
	var valueLimit *valueLimitError
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &b) || errors.As(err, &l) || errors.As(err, &exit) || errors.As(err, &replay) || errors.As(err, &valueLimit)
}

// cloneValue copies the language value domain while preserving typed temporal
// values that a JSON round-trip would silently coerce.
func cloneValue(value any) (any, error) {
	switch current := value.(type) {
	case nil, string, bool, float64, int64, time.Duration, time.Time:
		return current, nil
	case []any:
		out := make([]any, len(current))
		for i, item := range current {
			copy, err := cloneValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = copy
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			copy, err := cloneValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = copy
		}
		return out, nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var out any
		err = json.Unmarshal(data, &out)
		return out, err
	}
}

// parallelMap forks lexical bindings, not shared mutable runtimes. Children
// join before returning; shared budgets and step counters constrain all workers.
func (r *runtime) parallelMap(s *Statement, m []string) error {
	if r.parallelDepth > 0 {
		return fmt.Errorf("nested parallel maps are not supported")
	}
	values, err := r.eval(m[2], nil)
	if err != nil {
		return err
	}
	items, ok := values.([]any)
	if !ok {
		return fmt.Errorf("parallel map requires a list")
	}
	count, err := r.eval(m[3], nil)
	if err != nil {
		return err
	}
	n, ok := number(count)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 1 || n > 128 {
		return fmt.Errorf("parallel worker limit must be an integer from 1 to 128")
	}
	if len(items) > 100000 {
		return fmt.Errorf("parallel input exceeds 100000 items")
	}
	workers := min(int(n), len(items))
	collecting := m[5] != ""
	r.parallelSequence++
	base := fmt.Sprintf("%s/map-%d-%d/", r.logicalPath, s.Line, r.parallelSequence)
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	type completed struct {
		child          *runtime
		value          any
		err            error
		stdout, stderr workerOutput
	}
	results := make([]completed, len(items))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var firstError error
	var errorMu sync.Mutex
	fail := func(err error) {
		errorMu.Lock()
		if firstError == nil {
			firstError = err
			cancel()
		}
		errorMu.Unlock()
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				slot := &results[index]
				slot.stdout.total = &r.shared.output
				slot.stderr.total = &r.shared.output
				env, err := cloneValue(r.env)
				if err != nil {
					fail(err)
					return
				}
				item, err := cloneValue(items[index])
				if err != nil {
					fail(err)
					return
				}
				child := *r
				child.ctx = ctx
				child.env = env.(map[string]any)
				child.env[m[1]] = item
				child.env["number"] = float64(index + 1)
				child.functions = copyStatements(r.functions)
				child.schemas = copyStatements(r.schemas)
				child.types = copyTypes(r.types)
				child.debugStack = append([]DebugFrame(nil), r.debugStack...)
				child.result = &Result{Traces: []Trace{}}
				child.recording = nil
				child.replay = nil
				child.replayIndex = 0
				child.logicalPath = fmt.Sprintf("%s%d", base, index)
				child.parallelDepth = r.parallelDepth + 1
				child.parallelSequence = 0
				child.opts = r.opts
				child.opts.Stdout = &slot.stdout
				child.opts.Stderr = &slot.stderr
				child.opts.Stdin = nil
				for _, rec := range r.replay {
					if rec.Path == child.logicalPath {
						child.replay = append(child.replay, rec)
					}
				}
				slot.child = &child
				err = child.block(s.Body)
				var ret returnValue
				if errors.As(err, &ret) {
					if !ret.hasValue {
						err = fmt.Errorf("parallel iteration %d finished without a value", index+1)
					} else {
						slot.value = ret.value
						err = nil
					}
				} else if err == nil {
					err = fmt.Errorf("parallel iteration %d completed without return", index+1)
				}
				if err == nil && r.opts.Replay != "" && child.replayIndex != len(child.replay) {
					err = &ReplayIntegrityError{fmt.Sprintf("parallel replay has unused judgments for iteration %d", index+1)}
				}
				slot.err = err
				if err != nil && (!collecting || fatalParallel(err)) {
					fail(err)
					return
				}
			}
		}()
	}
dispatch:
	for i := range items {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	valuesOut := make([]any, len(items))
	for i := range results {
		slot := &results[i]
		if slot.child == nil {
			continue
		}
		r.result.Traces = append(r.result.Traces, slot.child.result.Traces...)
		r.recording = append(r.recording, slot.child.recording...)
		r.replayIndex += slot.child.replayIndex
		if _, err := io.Copy(r.opts.Stdout, &slot.stdout.buffer); err != nil && firstError == nil {
			firstError = err
		}
		if _, err := io.Copy(r.opts.Stderr, &slot.stderr.buffer); err != nil && firstError == nil {
			firstError = err
		}
		if collecting {
			outcome := map[string]any{"ok": slot.err == nil, "value": slot.value, "error": nil}
			if slot.err != nil {
				outcome["error"] = FailureValue(slot.err)
			}
			valuesOut[i] = outcome
		} else {
			valuesOut[i] = slot.value
		}
	}
	if firstError != nil {
		return firstError
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	r.env[m[4]] = valuesOut
	return nil
}
func copyStatements(in map[string]*Statement) map[string]*Statement {
	out := make(map[string]*Statement, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ReplayIntegrityError is not a recoverable provider outcome.
type ReplayIntegrityError struct{ Message string }

func (e *ReplayIntegrityError) Error() string { return e.Message }

type recordedFailure struct{ value map[string]any }

func (e *recordedFailure) Error() string { return e.value["message"].(string) }

type workerOutput struct {
	buffer bytes.Buffer
	total  *atomic.Int64
}

func (w *workerOutput) Write(data []byte) (int, error) {
	if w.total.Add(int64(len(data))) > 16<<20 {
		return 0, &executionLimitError{"parallel output exceeds 16 MiB limit"}
	}
	return w.buffer.Write(data)
}
