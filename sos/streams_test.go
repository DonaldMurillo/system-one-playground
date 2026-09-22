package sos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type observableStreamSource struct {
	nextStarted chan struct{}
	release     chan struct{}
	once        sync.Once
}

func (s *observableStreamSource) next(ctx context.Context) (any, bool, error) {
	s.once.Do(func() { close(s.nextStarted) })
	select {
	case <-s.release:
		return nil, false, context.Canceled
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (s *observableStreamSource) cancel(context.Context) error {
	select {
	case <-s.release:
	default:
		close(s.release)
	}
	return nil
}

type finiteStreamSource struct {
	items []any
	index int
}

func (s *finiteStreamSource) next(context.Context) (any, bool, error) {
	if s.index >= len(s.items) {
		return nil, false, nil
	}
	value := s.items[s.index]
	s.index++
	return value, true, nil
}
func (*finiteStreamSource) cancel(context.Context) error { return nil }

type failingAfterItemsSource struct {
	items []any
	index int
	err   error
}

func (s *failingAfterItemsSource) next(context.Context) (any, bool, error) {
	if s.index < len(s.items) {
		value := s.items[s.index]
		s.index++
		return value, true, nil
	}
	return nil, false, s.err
}
func (*failingAfterItemsSource) cancel(context.Context) error { return nil }

func TestStreamControllerSnapshotsAndStopsOnlySelectedStream(t *testing.T) {
	controller := NewStreamController()
	events := make([]StreamEvent, 0, 4)
	source := &observableStreamSource{nextStarted: make(chan struct{}), release: make(chan struct{})}
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", source)
	stream.id, stream.binding = "stream-1", "updates"
	stream.emit = func(event StreamEvent) { events = append(events, event) }
	controller.register(stream)
	stream.publish("opened")

	if err := stream.beginConsumption(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, more, err := stream.next(context.Background())
		if more {
			err = errors.New("stopped stream produced an item")
		}
		result <- err
	}()
	<-source.nextStarted
	if err := controller.Stop(context.Background(), "stream-1"); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	snapshots := controller.Snapshots()
	if len(snapshots) != 1 || snapshots[0].ID != "stream-1" || snapshots[0].Binding != "updates" || snapshots[0].State != "stopped" {
		t.Fatalf("snapshots=%#v", snapshots)
	}
	if snapshots[0].ItemsReceived != 0 || snapshots[0].EndedAt == nil {
		t.Fatalf("snapshot=%#v", snapshots[0])
	}
	if got := events[len(events)-1]; got.Event != "stopped" || got.State != "stopped" {
		t.Fatalf("events=%#v", events)
	}
}

func TestStreamLifecycleReportsItemsAndCompletion(t *testing.T) {
	source := &finiteStreamSource{items: []any{"one"}}
	var events []StreamEvent
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", source)
	stream.id, stream.binding, stream.emit = "stream-1", "updates", func(event StreamEvent) { events = append(events, event) }
	stream.publish("opened")
	if err := stream.beginConsumption(); err != nil {
		t.Fatal(err)
	}
	if value, more, err := stream.next(context.Background()); err != nil || !more || value != "one" {
		t.Fatalf("value=%v more=%v err=%v", value, more, err)
	}
	if _, more, err := stream.next(context.Background()); err != nil || more {
		t.Fatalf("more=%v err=%v", more, err)
	}
	if got := events[len(events)-1]; got.Event != "completed" || got.State != "completed" || got.ItemsReceived != 1 || got.EndedAt == nil {
		t.Fatalf("events=%#v", events)
	}
}

type testStreamSource struct{ cancelled bool }

func (*testStreamSource) next(context.Context) (any, bool, error) { return nil, false, nil }
func (s *testStreamSource) cancel(context.Context) error          { s.cancelled = true; return nil }

func TestClosingARequestedStopKeepsTheToolingOutcome(t *testing.T) {
	source := &testStreamSource{}
	var events []StreamEvent
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", source)
	stream.id, stream.binding, stream.emit = "stream-1", "updates", func(event StreamEvent) { events = append(events, event) }
	if err := stream.requestStop(); err != nil {
		t.Fatal(err)
	}
	if err := stream.closeWithReason(context.Background(), "run ended"); err != nil {
		t.Fatal(err)
	}
	got := stream.snapshot("snapshot")
	if got.State != "stopped" || got.Reason != "tooling" || got.EndedAt == nil {
		t.Fatalf("snapshot=%#v", got)
	}
	if !source.cancelled || len(events) != 1 || events[0].Event != "stopped" {
		t.Fatalf("cancelled=%v events=%#v", source.cancelled, events)
	}
}

type failingCancelStreamSource struct{ err error }

func (*failingCancelStreamSource) next(context.Context) (any, bool, error) { return nil, false, nil }
func (s *failingCancelStreamSource) cancel(context.Context) error          { return s.err }

func TestClosingARequestedStopReportsCancellationFailure(t *testing.T) {
	want := errors.New("producer refused cancellation")
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", &failingCancelStreamSource{err: want})
	stream.id = "stream-1"
	if err := stream.requestStop(); err != nil {
		t.Fatal(err)
	}
	if err := stream.closeWithReason(context.Background(), "run ended"); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	got := stream.snapshot("snapshot")
	if got.State != "failed" || got.Reason != "cancellation failed" || got.Failure == nil {
		t.Fatalf("snapshot=%#v", got)
	}
}

func TestClosingAStreamReportsCancellationFailure(t *testing.T) {
	want := errors.New("producer refused cancellation")
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", &failingCancelStreamSource{err: want})
	stream.id = "stream-1"
	if err := stream.closeWithReason(context.Background(), "close stream"); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	got := stream.snapshot("snapshot")
	if got.State != "failed" || got.Reason != "cancellation failed" || got.Failure == nil {
		t.Fatalf("snapshot=%#v", got)
	}
}

func TestStreamSyntaxAndActionMetadata(t *testing.T) {
	source := `to follow with service as text streaming text:
  finish
stream follow with "api" called events
for each event from events:
  show event
`
	p, ds := Parse(source)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	decl, err := parseActionDecl(p.Statements[0].Text)
	if err != nil || !decl.Streaming || decl.StreamItem.Name != "text" {
		t.Fatalf("decl=%#v err=%v", decl, err)
	}
	if p.Statements[1].Kind != "openStream" || p.Statements[2].Kind != "streamFor" {
		t.Fatalf("statements=%#v", p.Statements)
	}
}

func TestWrappedStreamingActionHeader(t *testing.T) {
	source := "define failure Broken:\nto follow with service as text\nstreaming text\nmay fail with Broken:\n  finish\n"
	p, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if checked := Check(source); len(checked) != 0 {
		t.Fatal(checked)
	}
	decl, err := parseActionDecl(p.Statements[1].Text)
	if err != nil || !decl.Streaming || decl.StreamItem.Name != "text" || len(decl.Failures) != 1 {
		t.Fatalf("decl=%#v err=%v", decl, err)
	}
}

func TestStreamOwnershipDiagnostics(t *testing.T) {
	cases := map[string]string{
		"abandoned": "to follow streaming text:\n  finish\nstream follow called events\n",
		"reused":    "to follow streaming text:\n  finish\nstream follow called events\nfor each event from events:\n  show event\nfor each event from events:\n  show event\n",
		"copied":    "to follow streaming text:\n  finish\nstream follow called events\nmake other events\nclose stream events\n",
		"outside":   "stop reading\n",
	}
	wants := map[string]string{"abandoned": "remains active", "reused": "already consumed", "copied": "cannot copy stream", "outside": "requires an active stream loop"}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			var joined strings.Builder
			for _, d := range Check(source) {
				joined.WriteString(d.Message)
				joined.WriteByte('\n')
			}
			if !strings.Contains(joined.String(), wants[name]) {
				t.Fatalf("diagnostics=%q want %q", joined.String(), wants[name])
			}
		})
	}
}

func TestStreamCollectionDiagnosticsExplainHowToMaterialize(t *testing.T) {
	prefix := "to follow streaming text:\n  finish\nstream follow called events\n"
	for _, operation := range []string{"keep events where true", "sort events by value", "group events by value called groups", "map each event in events with at most 2 running called values:\n  show event", "for each event in events:\n  show event"} {
		messages := []string{}
		for _, diagnostic := range Check(prefix + operation + "\nclose stream events\n") {
			messages = append(messages, diagnostic.Message)
		}
		if !strings.Contains(strings.Join(messages, "\n"), "requires a collection") {
			t.Fatalf("%q diagnostics=%v", operation, messages)
		}
	}
	for _, limit := range []string{"0", "1.5", "1000001"} {
		messages := []string{}
		for _, diagnostic := range Check(prefix + "collect at most " + limit + " items from events called items\n") {
			messages = append(messages, diagnostic.Message)
		}
		if !strings.Contains(strings.Join(messages, "\n"), "positive bounded integer") {
			t.Fatalf("limit %s diagnostics=%v", limit, messages)
		}
	}
}

func TestStreamOwnershipIsScopedAndPathSensitive(t *testing.T) {
	cases := map[string]struct{ source, want string }{
		"action leak": {`to follow streaming text:
  finish
to work:
  stream follow called events
  finish
`, "stream events remains active"},
		"duplicate active binding": {`to follow streaming text:
  finish
stream follow called events
stream follow called events
close stream events
`, "already active"},
		"branch local leak": {`to follow streaming text:
  finish
when true:
  stream follow called events
`, "stream events remains active"},
		"one branch close": {`to follow streaming text:
  finish
stream follow called events
when true:
  close stream events
otherwise:
  show "kept open"
`, "stream events remains active"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var messages []string
			for _, d := range Check(tc.source) {
				messages = append(messages, d.Message)
			}
			if !strings.Contains(strings.Join(messages, "\n"), tc.want) {
				t.Fatalf("diagnostics=%v, want %q", messages, tc.want)
			}
		})
	}
}

func TestStreamingActionMetadataIncludesItemType(t *testing.T) {
	p := mustParse(t, "to follow streaming text:\n  finish\n")
	metadata := p.ActionMetadata()
	if len(metadata) != 1 || metadata[0].StreamItem != "text" || metadata[0].Result != "" {
		t.Fatalf("metadata=%#v", metadata)
	}
}

func TestActionScopeCleanupClosesOwnedStreams(t *testing.T) {
	source := &testStreamSource{}
	closeOwnedStreams(map[string]any{"events": newStreamHandle(TypeRef{Name: "text"}, "test.follow", source)})
	if !source.cancelled {
		t.Fatal("action-scope cleanup did not cancel producer")
	}
}

func TestActionScopeCleanupPreservesCallerStreamUnderDifferentBinding(t *testing.T) {
	source := &testStreamSource{}
	handle := newStreamHandle(TypeRef{Name: "text"}, "test.follow", source)
	closeActionOwnedStreams(map[string]any{"value": handle}, map[string]any{"events": handle})
	if source.cancelled {
		t.Fatal("callee cleanup cancelled caller-owned stream alias")
	}
}

func TestRecoveredOpenDoesNotCreateAHandleInOwnershipFlow(t *testing.T) {
	source := `define failure Offline:
to follow streaming text may fail with Offline:
  finish
stream follow called events
  on failure Offline:
    recover
`
	var messages []string
	for _, d := range Check(source) {
		messages = append(messages, d.Message)
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "no stream handle exists") {
		t.Fatalf("diagnostics=%s", joined)
	}
	if strings.Contains(joined, "stream events remains active") {
		t.Fatalf("recovered failed open created unconditional ownership: %s", joined)
	}
}

func TestStreamFailuresParticipateInActionContracts(t *testing.T) {
	source := `define failure Disconnected:
to follow streaming text may fail with Disconnected:
  finish
to consume returning text:
  stream follow called events
  for each event from events:
    show event
  finish with "done"
`
	var messages []string
	for _, d := range Check(source) {
		messages = append(messages, d.Message)
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "while opening a stream") || !strings.Contains(joined, "terminal stream failure Disconnected") {
		t.Fatalf("diagnostics=%s", joined)
	}
	p := mustParse(t, source)
	metadata := p.ActionMetadata()
	var consume OperationInfo
	for _, item := range metadata {
		if item.Name == "consume" {
			consume = item
		}
	}
	if len(consume.PossibleFailures) != 1 || consume.PossibleFailures[0] != "Disconnected" {
		t.Fatalf("consume metadata=%#v", consume)
	}
}

func TestLocalStreamingActionSendsTypedItemsAndCompletes(t *testing.T) {
	source := `to count with limit as integer streaming integer:
  repeat limit times:
    send 7
  finish
stream count with 3 called numbers
for each number from numbers:
  show number
show "done"
`
	if diagnostics := Check(source); len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
	var stdout strings.Builder
	_, err := Run(context.Background(), mustParse(t, source), Options{Stdout: &stdout})
	if err != nil || stdout.String() != "7\n7\n7\ndone\n" {
		t.Fatalf("output=%q error=%v", stdout.String(), err)
	}
}

func TestLocalStreamingActionStopsWithoutStoppingItsCaller(t *testing.T) {
	source := `to count streaming integer:
  while true:
    send 7
stream count called numbers
for each number from numbers:
  show number
  stop reading
show "continued"
`
	var stdout strings.Builder
	_, err := Run(context.Background(), mustParse(t, source), Options{Stdout: &stdout})
	if err != nil || stdout.String() != "7\ncontinued\n" {
		t.Fatalf("output=%q error=%v", stdout.String(), err)
	}
}

func TestFailureHandlerCannotSwallowStopReading(t *testing.T) {
	source := `to count streaming integer:
  while true:
    send 7
stream count called numbers
for each number from numbers:
  show number
  stop reading
    on failure:
      recover
show "continued"
`
	var stdout strings.Builder
	_, err := Run(context.Background(), mustParse(t, source), Options{Stdout: &stdout})
	if err != nil || stdout.String() != "7\ncontinued\n" {
		t.Fatalf("output=%q error=%v", stdout.String(), err)
	}
}

func TestSendRequiresAStreamingActionAndItsDeclaredItemType(t *testing.T) {
	source := `send 1
to ordinary:
  send 2
to numbers streaming integer:
  send "wrong"
`
	var joined strings.Builder
	for _, diagnostic := range Check(source) {
		joined.WriteString(diagnostic.Message)
		joined.WriteByte('\n')
	}
	if strings.Count(joined.String(), "send is only valid inside a streaming action") != 2 || !strings.Contains(joined.String(), "stream item") {
		t.Fatalf("diagnostics=%s", joined.String())
	}
}

func TestStreamingActionsRejectLegacyInvocationForms(t *testing.T) {
	source := `to follow streaming text:
  finish
call follow
capture follow called outcome
`
	var messages []string
	for _, d := range Check(source) {
		messages = append(messages, d.Message)
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "must be opened with stream") || !strings.Contains(joined, "cannot capture streaming action") {
		t.Fatalf("diagnostics=%s", joined)
	}
}

func TestStaticRecoveryShapeForStreamsAndCalls(t *testing.T) {
	source := `define failure Broken:
to value returning text may fail with Broken:
  fail Broken with "broken"
to source streaming text may fail with Broken:
  finish
to use returning text may fail with Broken:
  call value called item
    on failure Broken:
      recover
  stream source called events
    on failure Broken:
      recover with "bad"
  collect at most 2 items from events called items
    on failure Broken:
      recover
  finish with item
`
	var messages []string
	for _, d := range Check(source) {
		messages = append(messages, d.Message)
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "recover requires a value") || !strings.Contains(joined, "recover with is only valid") {
		t.Fatalf("diagnostics=%s", joined)
	}
}

func TestCollectOverflowIsATypedStreamLimitFailure(t *testing.T) {
	stream := newStreamHandle(TypeRef{Name: "text"}, "events.follow", &finiteStreamSource{items: []any{"one", "two", "three"}})
	r := &runtime{ctx: context.Background(), env: map[string]any{"events": stream}}
	err := r.materializeStream(&Statement{Line: 1}, []string{"", "2", "events", "items"}, false)
	var typed *typedFailure
	if !errors.As(err, &typed) || typed.kind != "StreamLimitExceeded" {
		t.Fatalf("error=%#v", err)
	}
	value := FailureValue(err)
	if value["limit"] != float64(2) || value["received"] != float64(3) {
		t.Fatalf("failure=%#v", value)
	}
}

func TestCollectAcceptsTheBuiltInStreamLimitHandler(t *testing.T) {
	source := `to source streaming text:
  finish
to consume may fail with StreamLimitExceeded:
  stream source called events
  collect at most 2 items from events called items
    on failure StreamLimitExceeded using limit, received:
      pass failure on
  finish
`
	for _, diagnostic := range Check(source) {
		if strings.Contains(diagnostic.Message, "unknown failure") || strings.Contains(diagnostic.Message, "impossible") {
			t.Fatalf("diagnostic=%s", diagnostic.Message)
		}
	}
}

func TestBuiltInStreamLimitFailureCannotBeRedefined(t *testing.T) {
	diagnostics := Check(`define failure StreamLimitExceeded:
  limit as text
`)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "failure StreamLimitExceeded is reserved by the SysOneScript runtime") {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
}

func TestStreamingVocabularyHasNoSentencePatternsAndKeepsFailures(t *testing.T) {
	project, err := filepath.Abs("../examples/sos/streams")
	if err != nil {
		t.Fatal(err)
	}
	catalog, diagnostics := Vocabulary(filepath.Join(project, "probe.sos"), `import "example/streams" as events`)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	seen := false
	for _, entry := range catalog.Entries {
		if entry.Name != "failing" || entry.Library != "example/streams" {
			continue
		}
		seen = true
		if len(entry.Patterns) != 0 {
			t.Fatalf("streaming patterns=%v", entry.Patterns)
		}
		if len(entry.PossibleFailures) != 1 || entry.PossibleFailures[0] != "ConnectionLost" {
			t.Fatalf("failures=%v", entry.PossibleFailures)
		}
	}
	if !seen {
		t.Fatal("missing failing stream vocabulary metadata")
	}
}

func TestStreamMaterializationRejectsUnsafeBoundsBeforeAllocation(t *testing.T) {
	for name, value := range map[string]float64{"infinite": math.Inf(1), "nan": math.NaN(), "too large": maxStreamMaterializationItems + 1} {
		t.Run(name, func(t *testing.T) {
			r := &runtime{ctx: context.Background(), env: map[string]any{"limit": value}, definitions: map[string]*RecordDef{}}
			err := r.materializeStream(&Statement{}, []string{"", "limit", "events", "items"}, false)
			if err == nil || !strings.Contains(err.Error(), "positive bounded integer") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestStreamCannotBeAliasedOrOverwrittenByLegacyBindings(t *testing.T) {
	for name, operation := range map[string]string{
		"remember alias":     "remember events called other",
		"make alias":         "make other events",
		"assign overwrite":   "assign events []",
		"remember overwrite": "remember [] called events",
		"read overwrite":     "read \"x\" as text called events",
		"append alias":       "make items []\nappend events to items",
	} {
		t.Run(name, func(t *testing.T) {
			source := "to follow streaming text:\n  finish\nstream follow called events\n" + operation + "\nclose stream events\n"
			var messages []string
			for _, d := range Check(source) {
				messages = append(messages, d.Message)
			}
			joined := strings.Join(messages, "\n")
			if !strings.Contains(joined, "cannot copy stream") && !strings.Contains(joined, "cannot overwrite active stream") {
				t.Fatalf("diagnostics=%s", joined)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transformation runtime tests (spec: docs/sysonescript-stream-handling-spec.md)
// ---------------------------------------------------------------------------

const testWait = 2 * time.Second

func waitStat(t *testing.T, h StreamHandle, key string, want int64) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if stats := h.TransformStats(); stats != nil && stats[key] == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stat %s never reached %d: %v", key, want, h.TransformStats())
}

func nextWithTimeout(t *testing.T, h StreamHandle, within time.Duration) (any, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	value, ok, err := h.Next(ctx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("next: %v", err)
	}
	return value, ok
}

// waitArmedAt blocks until the engine has armed the expected deadline, so
// virtual-clock advances are deterministic (a stale arming from an earlier
// event never satisfies the wait).
func waitArmedAt(t *testing.T, h StreamHandle, want time.Duration) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if stats := h.TransformStats(); stats != nil {
			if armed, ok := stats["armed_at"].(int64); ok && armed == int64(want) {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timer never armed at %s: %v", want, h.TransformStats())
}

func failureKind(err error) string {
	if err == nil {
		return ""
	}
	kind, _ := FailureValue(err)["kind"].(string)
	return kind
}

func openTestStream(ch chan any) StreamHandle {
	return OpenChannelStream(context.Background(), TypeRef{Name: "integer"}, "test.source", ch)
}

func TestDebounceEmitsLatestAtExactVirtualClockBoundary(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Debounce(openTestStream(ch), 500*time.Millisecond, DebounceOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	waitStat(t, derived, "pending", 1)
	clock.Advance(200 * time.Millisecond)
	ch <- 2.0
	waitStat(t, derived, "replaced", 1)
	// The quiet window restarted at the second arrival (t=200ms).
	waitArmedAt(t, derived, 700*time.Millisecond)
	// One millisecond before the quiet window elapses nothing may be emitted.
	clock.Advance(699*time.Millisecond - clock.Now())
	if value, ok := nextWithTimeout(t, derived, 30*time.Millisecond); ok {
		t.Fatalf("emitted %v before the quiet window elapsed", value)
	}
	clock.Advance(time.Millisecond)
	value, ok := nextWithTimeout(t, derived, time.Second)
	if !ok || value != 2.0 {
		t.Fatalf("want latest item 2 at the exact boundary, got %v ok=%v", value, ok)
	}
	close(ch)
	if _, ok := nextWithTimeout(t, derived, time.Second); ok {
		t.Fatal("stream should complete after the quiet item")
	}
}

func TestDeounceKeyedTimersAreIndependentAndKeyBoundFails(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	item := func(key string) any { return map[string]any{"key": key, "n": 1.0} }
	derived, err := Debounce(openTestStream(ch), 100*time.Millisecond, DebounceOptions{
		Key:      func(v any) (any, error) { return v.(map[string]any)["key"], nil },
		KeyLimit: 2,
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- item("a")
	waitStat(t, derived, "pending", 1)
	clock.Advance(50 * time.Millisecond)
	ch <- item("b")
	waitStat(t, derived, "pending", 2)
	// A third distinct pending key exceeds the bound instead of evicting.
	ch <- item("c")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived.Next(ctx)
	if failureKind(err) != "StreamKeyLimitExceeded" {
		t.Fatalf("want StreamKeyLimitExceeded, got %v (%v)", err, FailureValue(err))
	}
	if FailureValue(err)["operation"] != "debounce" || FailureValue(err)["limit"] != float64(2) {
		t.Fatalf("payload=%v", FailureValue(err))
	}
}

func TestDebounceCompletionFlushesLastArrivalOrderAndFailureDiscards(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Debounce(openTestStream(ch), time.Hour, DebounceOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	waitStat(t, derived, "pending", 1)
	ch <- 2.0
	waitStat(t, derived, "replaced", 1)
	ch <- 3.0
	waitStat(t, derived, "replaced", 2)
	close(ch)
	var got []any
	for i := 0; i < 1; i++ {
		value, ok := nextWithTimeout(t, derived, time.Second)
		if !ok {
			t.Fatal("expected flushed pending item")
		}
		got = append(got, value)
	}
	if len(got) != 1 || got[0] != 3.0 {
		t.Fatalf("completion should flush the latest pending item only, got %v", got)
	}

	// Failure path discards pending items and propagates immediately.
	ch2 := make(chan any, 8)
	derived2, err := Debounce(openTestStream(ch2), time.Hour, DebounceOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch2 <- errors.New("source broke")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived2.Next(ctx)
	if err == nil || !strings.Contains(err.Error(), "source broke") {
		t.Fatalf("want upstream failure to propagate, got %v", err)
	}
}

func TestDerivedStreamConsumesOwnership(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	if _, err := Debounce(source, time.Millisecond, DebounceOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Debounce(source, time.Millisecond, DebounceOptions{}, nil); err == nil {
		t.Fatal("second transformation of a consumed stream must fail")
	} else if !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("ownership error=%v", err)
	}
	// Direct reads observe consumption through the derived stream: the
	// original handle has no remaining consumer.
	close(ch)
	if _, _, err := source.Next(context.Background()); err != nil {
		t.Fatalf("closed consumed stream must end cleanly, got %v", err)
	}
}

func TestThrottleKeepingFirstDropsAndNeverGrantsCatchUpCredit(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Throttle(openTestStream(ch), 2, time.Second, ThrottleOptions{Keeping: "first"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []float64{1, 2, 3, 4} {
		ch <- v
	}
	var got []any
	for i := 0; i < 2; i++ {
		value, ok := nextWithTimeout(t, derived, time.Second)
		if !ok {
			t.Fatal("expected admitted item")
		}
		got = append(got, value)
	}
	if got[0] != 1.0 || got[1] != 2.0 {
		t.Fatalf("keeping the first must admit in order, got %v", got)
	}
	waitStat(t, derived, "dropped", 2)
	waitArmedAt(t, derived, time.Second)
	clock.Advance(3*time.Second - clock.Now()) // long idle grants no catch-up credit
	ch <- 5.0
	value, ok := nextWithTimeout(t, derived, time.Second)
	if !ok || value != 5.0 {
		t.Fatalf("new window should admit one item, got %v ok=%v", value, ok)
	}
	close(ch)
}

func TestThrottleKeepingLatestEmitsRetainedWhenCapacityReturns(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Throttle(openTestStream(ch), 1, time.Second, ThrottleOptions{Keeping: "latest"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []float64{1, 2, 3} {
		ch <- v
	}
	if value, _ := nextWithTimeout(t, derived, time.Second); value != 1.0 {
		t.Fatalf("first item admitted, got %v", value)
	}
	waitStat(t, derived, "replaced", 1)
	waitStat(t, derived, "pending", 1)
	waitArmedAt(t, derived, time.Second)
	clock.Advance(time.Second - clock.Now())
	if value, ok := nextWithTimeout(t, derived, time.Second); !ok || value != 3.0 {
		t.Fatalf("retained latest item must be emitted at window end, got %v ok=%v", value, ok)
	}
	close(ch)
	// The retained item consumed the fresh window's single slot.
}

func TestThrottleDroppingAnOwnedItemWithoutPolicyFails(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Throttle(openTestStream(ch), 1, time.Hour, ThrottleOptions{Keeping: "first"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- NewOwnedItem(1.0, func(string) error { return nil })
	if v, _ := nextWithTimeout(t, derived, time.Second); v == nil {
		t.Fatal("first owned item admitted")
	}
	ch <- NewOwnedItem(2.0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived.Next(ctx)
	if failureKind(err) != "StreamObligationAbandoned" {
		t.Fatalf("want StreamObligationAbandoned, got %v", err)
	}
}

func TestThrottleKeyedWindowsAreIndependent(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Throttle(openTestStream(ch), 1, time.Second, ThrottleOptions{
		Keeping: "first",
		Key:     func(v any) (any, error) { return v.([]any)[0], nil },
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- []any{"a", 1.0}
	ch <- []any{"b", 1.0}
	first, _ := nextWithTimeout(t, derived, time.Second)
	second, _ := nextWithTimeout(t, derived, time.Second)
	if first == nil || second == nil {
		t.Fatal("independent keys must each admit an item")
	}
	// Same keys again within the window are dropped independently.
	ch <- []any{"a", 2.0}
	ch <- []any{"b", 2.0}
	waitStat(t, derived, "dropped", 2)
	close(ch)
}

func TestDistinctConsecutiveKeepsOnlyChanges(t *testing.T) {
	ch := make(chan any, 8)
	derived, err := DistinctConsecutive(openTestStream(ch), DistinctOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"a", "a", "b", "a", "a"} {
		ch <- v
	}
	close(ch)
	var got []string
	for {
		value, ok := nextWithTimeout(t, derived, time.Second)
		if !ok {
			break
		}
		got = append(got, value.(string))
	}
	if strings.Join(got, ",") != "a,b,a" {
		t.Fatalf("want a,b,a got %v", got)
	}
}

func TestBatchEmitsOnCountAndTimeAndFlushesPartialOnCompletion(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Batch(openTestStream(ch), 2, time.Second, BatchOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	ch <- 2.0
	batch, _ := nextWithTimeout(t, derived, time.Second)
	items, ok := batch.([]any)
	if !ok || len(items) != 2 || items[0] != 1.0 || items[1] != 2.0 {
		t.Fatalf("count-limit batch=%v", batch)
	}
	ch <- 3.0
	waitStat(t, derived, "pending", 1)
	waitArmedAt(t, derived, time.Second)
	clock.Advance(time.Second - clock.Now())
	batch, _ = nextWithTimeout(t, derived, time.Second)
	if items, ok = batch.([]any); !ok || len(items) != 1 || items[0] != 3.0 {
		t.Fatalf("time-limit batch=%v", batch)
	}
	ch <- 4.0
	waitStat(t, derived, "pending", 1)
	close(ch)
	if raw, ok2 := nextWithTimeout(t, derived, time.Second); ok2 {
		items, ok = raw.([]any)
	} else {
		items, ok = nil, false
	}
	if !ok || len(items) != 1 {
		t.Fatal("completion must flush the nonempty partial batch")
	}
	if _, ok := nextWithTimeout(t, derived, time.Second); ok {
		t.Fatal("stream should complete after the flush")
	}
}

func TestBatchAggregateItemBoundIsATypedFailure(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Batch(openTestStream(ch), 10, time.Hour, BatchOptions{AggregateItemLimit: 2}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	waitStat(t, derived, "pending", 1)
	ch <- 2.0
	waitStat(t, derived, "pending", 2)
	ch <- 3.0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived.Next(ctx)
	if failureKind(err) != "StreamLimitExceeded" {
		t.Fatalf("want StreamLimitExceeded, got %v (%v)", err, FailureValue(err))
	}
}

func TestIdleTimeoutFailsWhenSourceGoesQuiet(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := RequireIdle(openTestStream(ch), 30*time.Second, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	if value, _ := nextWithTimeout(t, derived, time.Second); value != 1.0 {
		t.Fatalf("item relayed, got %v", value)
	}
	waitStat(t, derived, "emitted", 1)
	waitArmedAt(t, derived, 30*time.Second)
	clock.Advance(30*time.Second - clock.Now())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived.Next(ctx)
	if failureKind(err) != "StreamIdleTimeout" {
		t.Fatalf("want StreamIdleTimeout, got %v (%v)", err, FailureValue(err))
	}
	if FailureValue(err)["idle_for"] != 30*time.Second {
		t.Fatalf("idle_for payload=%v", FailureValue(err)["idle_for"])
	}
}

func TestLifetimeLimitsStopNormallyOrFail(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	bounded, err := LimitLifetime(openTestStream(ch), 10*time.Minute, clock)
	if err != nil {
		t.Fatal(err)
	}
	// The bounded lifetime starts at first derived consumption: before any
	// read, no timer is armed.
	if stats := bounded.TransformStats(); stats["armed_at"] != int64(-1) {
		t.Fatalf("timer armed before consumption: %v", stats)
	}
	ch <- 1.0
	if value, _ := nextWithTimeout(t, bounded, time.Second); value != 1.0 {
		t.Fatalf("item before deadline, got %v", value)
	}
	waitArmedAt(t, bounded, 10*time.Minute)
	clock.Advance(10*time.Minute - clock.Now())
	if _, ok := nextWithTimeout(t, bounded, time.Second); ok {
		t.Fatal("bounded listening must complete normally at the deadline")
	}

	ch2 := make(chan any, 8)
	required, err := RequireDeadline(openTestStream(ch2), time.Minute, clock)
	if err != nil {
		t.Fatal(err)
	}
	if stats := required.TransformStats(); stats["armed_at"] != int64(-1) {
		t.Fatalf("timer armed before consumption: %v", stats)
	}
	// Any first read attempt starts the required lifetime; the clock sits at
	// 10 minutes from the bounded half of the test.
	nextWithTimeout(t, required, 20*time.Millisecond)
	waitArmedAt(t, required, 11*time.Minute)
	clock.Advance(11*time.Minute - clock.Now())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = required.Next(ctx)
	if failureKind(err) != "StreamDeadlineExceeded" {
		t.Fatalf("want StreamDeadlineExceeded, got %v (%v)", err, FailureValue(err))
	}
}

func TestTakeSampleCancelsUpstreamAfterBound(t *testing.T) {
	ch := make(chan any, 8)
	derived, err := TakeSample(openTestStream(ch), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	ch <- 2.0
	if v, _ := nextWithTimeout(t, derived, time.Second); v != 1.0 {
		t.Fatalf("first sample=%v", v)
	}
	if v, _ := nextWithTimeout(t, derived, time.Second); v != 2.0 {
		t.Fatalf("second sample=%v", v)
	}
	if _, ok := nextWithTimeout(t, derived, time.Second); ok {
		t.Fatal("sample must end at its bound")
	}
	if source := (<-chan any)(nil); source != nil {
		t.Fatal("unreachable")
	}
}

func TestHandleSequentiallyPreservesOrderAndDetectsAbandonedObligations(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	go func() {
		ch <- 1.0
		ch <- 2.0
		close(ch)
	}()
	var order []any
	if err := HandleSequentially(context.Background(), source, func(_ context.Context, item any) error {
		order = append(order, item)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != 1.0 || order[1] != 2.0 {
		t.Fatalf("order=%v", order)
	}

	ch2 := make(chan any, 8)
	owned2 := openTestStream(ch2)
	ch2 <- NewOwnedItem(1.0, nil)
	err := HandleSequentially(context.Background(), owned2, func(context.Context, any) error { return nil })
	if failureKind(err) != "StreamObligationAbandoned" {
		t.Fatalf("want abandoned obligation failure, got %v", err)
	}
}

func TestHandleWithBoundConcurrencyStartsInOrderAndCancelsSiblingsOnFailure(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	go func() {
		for i := 1; i <= 6; i++ {
			ch <- float64(i)
		}
		close(ch)
	}()
	var mu sync.Mutex
	var startOrder []float64
	var live, peak int
	boom := errors.New("handler three failed")
	err := HandleWithBoundConcurrency(context.Background(), source, 2, func(ctx context.Context, item any) error {
		mu.Lock()
		startOrder = append(startOrder, item.(float64))
		live++
		if live > peak {
			peak = live
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			live--
			mu.Unlock()
		}()
		if item.(float64) == 3 {
			return boom
		}
		select {
		case <-ctx.Done():
		case <-time.After(150 * time.Millisecond):
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("handler failure must propagate, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > 2 {
		t.Fatalf("concurrency peak %d exceeded bound 2", peak)
	}
	if len(startOrder) < 2 {
		t.Fatalf("handlers started: %v", startOrder)
	}
	// Admission is in source order: while items 1 and 2 hold both slots,
	// no later item may start.
	for _, v := range startOrder[:2] {
		if v > 2 {
			t.Fatalf("item %v started before the first two items finished: %v", v, startOrder)
		}
	}
	if startOrder[2] < 3 {
		t.Fatalf("third start must be a later item: %v", startOrder)
	}
}

func TestHandleLatestCancelsPreviousAndSuppressesStaleResults(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	results := make(chan any, 8)
	go func() {
		ch <- 1.0
		ch <- 2.0
		close(ch)
	}()
	var handled atomic.Int64
	err := HandleLatest(context.Background(), source, func(ctx context.Context, item any) (any, error) {
		handled.Add(1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(60 * time.Millisecond):
			return fmt.Sprintf("result-%v", item), nil
		}
	}, LatestOptions{
		Concurrency: 4,
		OnResult:    func(_, result any) { results <- result },
	})
	if err != nil {
		t.Fatal(err)
	}
	close(results)
	var got []any
	for r := range results {
		got = append(got, r)
	}
	if len(got) != 1 || got[0] != "result-2" {
		t.Fatalf("only the newest handler's result may be visible, got %v", got)
	}
}

func TestHandleLatestKeyedHandlersRunIndependentlyAndBoundsFail(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	results := make(chan string, 8)
	go func() {
		ch <- map[string]any{"customer": "a", "n": 1.0}
		ch <- map[string]any{"customer": "b", "n": 1.0}
		ch <- map[string]any{"customer": "c", "n": 1.0}
		close(ch)
	}()
	err := HandleLatest(context.Background(), source, func(ctx context.Context, item any) (any, error) {
		return item.(map[string]any)["customer"], nil
	}, LatestOptions{
		Key:         func(v any) (any, error) { return v.(map[string]any)["customer"], nil },
		KeyLimit:    2,
		Concurrency: 2,
		OnResult:    func(_, result any) { results <- result.(string) },
	})
	if failureKind(err) != "StreamKeyLimitExceeded" {
		t.Fatalf("key bound must fail, got %v (%v)", err, FailureValue(err))
	}
}

func TestHandleLatestRejectsNonIdempotentEffectsWithoutAcknowledgment(t *testing.T) {
	ch := make(chan any, 8)
	err := HandleLatest(context.Background(), openTestStream(ch), func(context.Context, any) (any, error) { return nil, nil }, LatestOptions{
		Effects: []EffectSafety{{Operation: "payments.charge", Effect: "non-idempotent", Cancellation: "cancellation-safe"}},
	})
	if err == nil || !strings.Contains(err.Error(), "acknowledgment") {
		t.Fatalf("non-idempotent effects require acknowledgment, got %v", err)
	}
	warnings, err := ValidateEffectSafety([]EffectSafety{
		{Operation: "search.run", Effect: "idempotent", Cancellation: "cancellation-delayed"},
	}, false)
	if err != nil || len(warnings) != 1 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
}

func TestHandleConflatingKeepsOnlyLatestWaiting(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	var handled []any
	started := make(chan struct{}, 8)
	go func() {
		ch <- 1.0
		<-started
		ch <- 2.0
		ch <- 3.0
		close(ch)
	}()
	err := HandleConflating(context.Background(), source, func(_ context.Context, item any) error {
		handled = append(handled, item)
		if len(handled) == 1 {
			started <- struct{}{}
			time.Sleep(40 * time.Millisecond)
		}
		return nil
	}, ConflateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(handled) != 2 || handled[0] != 1.0 || handled[1] != 3.0 {
		t.Fatalf("active work finishes, only latest waiting retained: %v", handled)
	}
}

func TestHandleExclusivelyIgnoresAndRejectsWhileBusy(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	var handled []any
	busy := make(chan struct{}, 8)
	go func() {
		ch <- 1.0
		<-busy
		ch <- 2.0
		ch <- 3.0
		close(ch)
	}()
	err := HandleExclusively(context.Background(), source, func(_ context.Context, item any) error {
		handled = append(handled, item)
		if len(handled) == 1 {
			busy <- struct{}{}
			time.Sleep(40 * time.Millisecond)
		}
		return nil
	}, ExclusiveOptions{Policy: BusyIgnore})
	if err != nil {
		t.Fatal(err)
	}
	if len(handled) != 1 || handled[0] != 1.0 {
		t.Fatalf("busy arrivals ignored: %v", handled)
	}

	ch2 := make(chan any, 8)
	source2 := openTestStream(ch2)
	var rejected []any
	var mu sync.Mutex
	var handled2 []any
	busy2 := make(chan struct{}, 8)
	go func() {
		ch2 <- NewOwnedItem(1.0, func(string) error { return nil })
		<-busy2
		ch2 <- NewOwnedItem(2.0, func(string) error { return nil })
		close(ch2)
	}()
	err = HandleExclusively(context.Background(), source2, func(_ context.Context, item any) error {
		owned := item.(*OwnedItem)
		mu.Lock()
		handled2 = append(handled2, owned.Value)
		first := len(handled2) == 1
		mu.Unlock()
		if first {
			busy2 <- struct{}{}
			time.Sleep(40 * time.Millisecond)
		}
		return owned.Complete("handled")
	}, ExclusiveOptions{
		Policy: BusyReject,
		Reject: func(item any) error {
			owned := item.(*OwnedItem)
			mu.Lock()
			rejected = append(rejected, owned.Value)
			mu.Unlock()
			return owned.Complete("rejected with status 429")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(handled2) != 1 || len(rejected) != 1 || rejected[0] != 2.0 {
		t.Fatalf("busy owned request must be rejected: handled=%v rejected=%v", handled2, rejected)
	}
}

// ---------------------------------------------------------------------------
// Runtime review regressions
// ---------------------------------------------------------------------------

func TestHandleWithBoundConcurrencyFailsFastWhileUpstreamIdle(t *testing.T) {
	ch := make(chan any, 2) // never closed: upstream stays idle after admission
	source := openTestStream(ch)
	done := make(chan error, 1)
	boom := errors.New("handler one failed")
	siblingCanceled := make(chan struct{})
	siblingStarted := make(chan struct{})
	go func() {
		done <- HandleWithBoundConcurrency(context.Background(), source, 2, func(ctx context.Context, item any) error {
			if item == 1.0 {
				<-siblingStarted
				return boom
			}
			close(siblingStarted)
			select {
			case <-ctx.Done():
				close(siblingCanceled)
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return nil
			}
		})
	}()
	ch <- 1.0
	ch <- 2.0
	// The failure must be observed even though upstream never delivers
	// another item.
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("want handler failure, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler failure was not observed while upstream was idle")
	}
	// The sibling handler (item 2, admitted before the failure) must have
	// had its context canceled.
	select {
	case <-siblingCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("sibling handler was not canceled")
	}
}

func TestHandleWithBoundConcurrencyBoundsSiblingCleanup(t *testing.T) {
	ch := make(chan any, 2)
	ch <- 1.0
	ch <- 2.0
	close(ch)
	release := make(chan struct{})
	defer close(release)
	err := HandleWithBoundConcurrencyCleanup(context.Background(), openTestStream(ch), 2, 30*time.Millisecond, func(_ context.Context, item any) error {
		if item == 1.0 {
			<-release // deliberately ignore cancellation
			return nil
		}
		return errors.New("handler failed")
	})
	if failureKind(err) != "StreamHandlerCleanupFailed" {
		t.Fatalf("want StreamHandlerCleanupFailed, got %v (%v)", err, FailureValue(err))
	}
}

func TestIdleTimeoutResetsOnArrivalAtExactBoundaryUnderBackpressure(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := RequireIdle(openTestStream(ch), time.Second, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	if value, _ := nextWithTimeout(t, derived, time.Second); value != 1.0 {
		t.Fatalf("first item, got %v", value)
	}
	// Item 2 arrives just inside the idle window while the consumer is not
	// yet reading: the arrival resets the timer even under backpressure.
	clock.Advance(900 * time.Millisecond)
	ch <- 2.0
	if value, _ := nextWithTimeout(t, derived, time.Second); value != 2.0 {
		t.Fatalf("second item, got %v", value)
	}
	// The delivered item reset the idle window from t=900ms to t=1900ms.
	waitArmedAt(t, derived, 1900*time.Millisecond)
	clock.Advance(1900*time.Millisecond - clock.Now())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived.Next(ctx)
	if failureKind(err) != "StreamIdleTimeout" {
		t.Fatalf("want StreamIdleTimeout, got %v (%v)", err, FailureValue(err))
	}
}

func TestRepeatedNextAfterTerminalReturnsTerminalIdempotently(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := RequireIdle(openTestStream(ch), 10*time.Millisecond, clock)
	if err != nil {
		t.Fatal(err)
	}
	nextWithTimeout(t, derived, 10*time.Millisecond) // start consumption
	waitArmedAt(t, derived, 10*time.Millisecond)
	clock.Advance(10 * time.Millisecond)
	for i := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err = derived.Next(ctx)
		cancel()
		if failureKind(err) != "StreamIdleTimeout" {
			t.Fatalf("read %d after terminal: want StreamIdleTimeout, got %v", i, err)
		}
	}

	// Normal completion is equally idempotent.
	ch2 := make(chan any, 8)
	batched, err := Batch(openTestStream(ch2), 10, time.Hour, BatchOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch2 <- 1.0
	waitStat(t, batched, "pending", 1)
	close(ch2)
	if _, ok := nextWithTimeout(t, batched, time.Second); !ok {
		t.Fatal("expected flushed batch")
	}
	for i := range 3 {
		if value, ok := nextWithTimeout(t, batched, 500*time.Millisecond); ok {
			t.Fatalf("read %d after completion produced %v", i, value)
		}
	}
}

func TestHandleLatestCleanupDeadlineReportsCleanupFailure(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	go func() {
		ch <- 1.0
		time.Sleep(5 * time.Millisecond)
		ch <- 2.0
		close(ch)
	}()
	err := HandleLatest(context.Background(), source, func(ctx context.Context, item any) (any, error) {
		// The first handler ignores its canceled context.
		time.Sleep(300 * time.Millisecond)
		return item, nil
	}, LatestOptions{
		Concurrency:     4,
		CleanupDeadline: 40 * time.Millisecond,
	})
	if failureKind(err) != "StreamHandlerCleanupFailed" {
		if err == nil {
			t.Fatal("want StreamHandlerCleanupFailed, got nil")
		}
		t.Fatalf("want StreamHandlerCleanupFailed, got %v (%v)", err, FailureValue(err))
	}
	if FailureValue(err)["operation"] != "handle only the newest" {
		t.Fatalf("payload=%v", FailureValue(err))
	}
}

func TestHandleLatestBoundsCleanupAfterKeyedHandlerFailure(t *testing.T) {
	ch := make(chan any, 2)
	ch <- 1.0
	ch <- 2.0
	close(ch)
	release := make(chan struct{})
	defer close(release)
	err := HandleLatest(context.Background(), openTestStream(ch), func(_ context.Context, item any) (any, error) {
		if item == 1.0 {
			<-release // deliberately ignore cancellation
			return nil, nil
		}
		return nil, errors.New("handler failed")
	}, LatestOptions{
		Key:             func(item any) (any, error) { return item, nil },
		KeyLimit:        2,
		Concurrency:     2,
		CleanupDeadline: 30 * time.Millisecond,
	})
	if failureKind(err) != "StreamHandlerCleanupFailed" {
		t.Fatalf("want StreamHandlerCleanupFailed, got %v (%v)", err, FailureValue(err))
	}
}

func TestDebounceSettlesOwnedPendingOnReplaceAndOnFailure(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Debounce(openTestStream(ch), time.Hour, DebounceOptions{
		Key: func(v any) (any, error) { return v.(*OwnedItem).ID, nil },
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	settled := make(chan string, 2)
	ch <- NewOwnedItem("request-1", func(reason string) error {
		settled <- reason
		return nil
	})
	waitStat(t, derived, "pending", 1)
	// A newer arrival for the same key replaces the pending owned item and
	// must settle its obligation.
	ch <- NewOwnedItem("request-1", func(reason string) error {
		settled <- reason
		return nil
	})
	waitStat(t, derived, "replaced", 1)
	select {
	case reason := <-settled:
		if reason != "replaced by newer arrival for the key" {
			t.Fatalf("settlement reason=%q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("replaced pending item was not settled")
	}

	// An upstream failure with an owned pending item lacking a disposal
	// policy reports the abandoned obligation.
	ch2 := make(chan any, 8)
	derived2, err := Debounce(openTestStream(ch2), time.Hour, DebounceOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch2 <- NewOwnedItem("request-2", nil)
	waitStat(t, derived2, "pending", 1)
	ch2 <- errors.New("source broke")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived2.Next(ctx)
	if failureKind(err) != "StreamObligationAbandoned" {
		t.Fatalf("want StreamObligationAbandoned, got %v (%v)", err, FailureValue(err))
	}
}

func TestThrottleEvictsExpiredKeyStateAndBoundsTimers(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 8)
	derived, err := Throttle(openTestStream(ch), 1, time.Second, ThrottleOptions{
		Keeping:    "first",
		Key:        func(v any) (any, error) { return v.([]any)[0], nil },
		TimerLimit: 1,
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- []any{"a", 1.0}
	if v, _ := nextWithTimeout(t, derived, time.Second); v == nil {
		t.Fatal("first key admitted")
	}
	waitStat(t, derived, "timers", 1)
	// A second live key exceeds the timer bound.
	ch <- []any{"b", 1.0}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = derived.Next(ctx)
	if failureKind(err) != "StreamLimitExceeded" {
		t.Fatalf("want timer bound failure, got %v (%v)", err, FailureValue(err))
	}

	// After the window elapses, the idle keyed state is evicted and a new
	// key fits the bound again.
	ch2 := make(chan any, 8)
	derived2, err := Throttle(openTestStream(ch2), 1, time.Second, ThrottleOptions{
		Keeping:    "first",
		Key:        func(v any) (any, error) { return v.([]any)[0], nil },
		TimerLimit: 1,
	}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch2 <- []any{"a", 1.0}
	if v, _ := nextWithTimeout(t, derived2, time.Second); v == nil {
		t.Fatal("first key admitted")
	}
	clock.Advance(time.Second)
	// The expired window's timer fires and evicts the idle keyed state.
	waitStat(t, derived2, "timers", 0)
	ch2 <- []any{"b", 2.0}
	if v, _ := nextWithTimeout(t, derived2, time.Second); v == nil {
		t.Fatal("new key must fit after eviction")
	}
	close(ch2)
}

func TestDistinctWithKeyRetainsOnlyTheImmediatelyPreviousScalarKey(t *testing.T) {
	ch := make(chan any, 8)
	derived, err := DistinctConsecutive(openTestStream(ch), DistinctOptions{
		Key: func(v any) (any, error) { return v.([]any)[0], nil },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		ch <- []any{"a", 1.0}
		ch <- []any{"a", 2.0} // same consecutive scalar key: suppressed
		ch <- []any{"b", 1.0}
		ch <- []any{"a", 1.0} // differs from the immediately previous key
		close(ch)
	}()
	var got []any
	for {
		value, ok := nextWithTimeout(t, derived, time.Second)
		if !ok {
			break
		}
		got = append(got, value)
	}
	if len(got) != 3 {
		t.Fatalf("want a, b, a after consecutive comparison, got %v", got)
	}
}

func TestByteAndTimerBoundsAndSnapshots(t *testing.T) {
	t.Run("debounce bytes", func(t *testing.T) {
		clock := &VirtualStreamClock{}
		ch := make(chan any, 8)
		derived, err := Debounce(openTestStream(ch), time.Hour, DebounceOptions{
			Key:       func(v any) (any, error) { return v.(string)[:1], nil },
			ByteLimit: 4,
		}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if stats := derived.TransformStats(); stats["byte_limit"] != int64(4) || stats["timer_limit"] != int64(-1) {
			t.Fatalf("snapshot bounds=%v", stats)
		}
		ch <- "aa:1"
		waitStat(t, derived, "pending", 1)
		if stats := derived.TransformStats(); stats["bytes_held"] != int64(4) || stats["timers"] != int64(1) {
			t.Fatalf("snapshot=%v", stats)
		}
		ch <- "bb:2"
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, err = derived.Next(ctx)
		if failureKind(err) != "StreamLimitExceeded" || FailureValue(err)["limit"] != float64(4) {
			t.Fatalf("want byte bound failure, got %v (%v)", err, FailureValue(err))
		}
	})
	t.Run("debounce timers", func(t *testing.T) {
		clock := &VirtualStreamClock{}
		ch := make(chan any, 8)
		derived, err := Debounce(openTestStream(ch), time.Hour, DebounceOptions{
			Key:        func(v any) (any, error) { return v.(string)[:1], nil },
			TimerLimit: 1,
		}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if stats := derived.TransformStats(); stats["timer_limit"] != int64(1) {
			t.Fatalf("snapshot bounds=%v", stats)
		}
		ch <- "a:1"
		waitStat(t, derived, "timers", 1)
		ch <- "b:2"
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, err = derived.Next(ctx)
		if failureKind(err) != "StreamLimitExceeded" {
			t.Fatalf("want timer bound failure, got %v (%v)", err, FailureValue(err))
		}
	})
	t.Run("batch bytes and timers", func(t *testing.T) {
		clock := &VirtualStreamClock{}
		ch := make(chan any, 8)
		derived, err := Batch(openTestStream(ch), 10, time.Hour, BatchOptions{
			Key:        func(v any) (any, error) { return v.(string)[:1], nil },
			ByteLimit:  100,
			TimerLimit: 2,
		}, clock)
		if err != nil {
			t.Fatal(err)
		}
		ch <- "aa:1"
		waitStat(t, derived, "timers", 1)
		ch <- "bb:2"
		waitStat(t, derived, "pending", 2)
		if stats := derived.TransformStats(); stats["timers"] != int64(2) || stats["timer_limit"] != int64(2) {
			t.Fatalf("batch timers remain within the configured bound: %v", stats)
		}
	})
	t.Run("throttle retained bytes", func(t *testing.T) {
		clock := &VirtualStreamClock{}
		ch := make(chan any, 8)
		derived, err := Throttle(openTestStream(ch), 1, time.Hour, ThrottleOptions{
			Keeping:   "latest",
			ByteLimit: 4,
		}, clock)
		if err != nil {
			t.Fatal(err)
		}
		ch <- "aa" // admitted
		if v, _ := nextWithTimeout(t, derived, time.Second); v != "aa" {
			t.Fatalf("first item, got %v", v)
		}
		ch <- "bb" // retained
		waitStat(t, derived, "pending", 1)
		if stats := derived.TransformStats(); stats["bytes_held"] != int64(2) {
			t.Fatalf("snapshot=%v", stats)
		}
		ch <- "cccc" // replacing "bb" would hold 4... "cccc" alone is 4; replace makes 4
		waitStat(t, derived, "replaced", 1)
		ch <- "ddddd" // 5 bytes over the bound of 4
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, err = derived.Next(ctx)
		if failureKind(err) != "StreamLimitExceeded" || FailureValue(err)["limit"] != float64(4) {
			t.Fatalf("want byte bound failure, got %v (%v)", err, FailureValue(err))
		}
	})
}

func TestRuntimeStreamClockUsesInjectedVirtualClock(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	virtual := NewVirtualClock(start)
	clock := newRuntimeStreamClock(virtual)
	ready := clock.After(5 * time.Second)
	virtual.Advance(4 * time.Second)
	if got := clock.Now(); got != 4*time.Second {
		t.Fatalf("elapsed = %s", got)
	}
	select {
	case <-ready:
		t.Fatal("stream timer fired early")
	default:
	}
	virtual.Advance(time.Second)
	select {
	case got := <-ready:
		if got != 5*time.Second {
			t.Fatalf("timer fired at %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("stream timer did not follow virtual time")
	}
}

func TestExclusiveRejectionIncrementsRejectedStat(t *testing.T) {
	ch := make(chan any, 8)
	source := openTestStream(ch)
	started := make(chan struct{})
	release := make(chan struct{})
	ch <- NewOwnedItem(1.0, func(string) error { return nil })
	ch <- NewOwnedItem(2.0, func(string) error { return nil })
	close(ch)
	var stats map[string]any
	done := make(chan error, 1)
	go func() {
		done <- HandleExclusively(context.Background(), source, func(_ context.Context, item any) error {
			close(started)
			<-release
			return item.(*OwnedItem).Complete("handled")
		}, ExclusiveOptions{
			Policy: BusyReject,
			Reject: func(item any) error { return item.(*OwnedItem).Complete("rejected with status 429") },
		})
	}()
	<-started
	time.Sleep(10 * time.Millisecond)
	close(release)
	err := <-done
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats = source.TransformStats()
		if stats["rejected"] == int64(1) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("rejected stat=%v", stats)
}

func TestConcurrentNextStatsAndSnapshotsAreRaceSafe(t *testing.T) {
	clock := &VirtualStreamClock{}
	ch := make(chan any, 64)
	source := openTestStream(ch)
	derived, err := Debounce(source, 5*time.Millisecond, DebounceOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	controller := NewStreamController()
	controller.Register(derived)
	go func() {
		for i := range 64 {
			ch <- float64(i)
			time.Sleep(time.Millisecond)
		}
		close(ch)
	}()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, ok, err := derived.Next(ctx)
			cancel()
			if err != nil || !ok {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			_ = derived.TransformStats()
			time.Sleep(time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			_ = controller.Snapshots()
			time.Sleep(2 * time.Millisecond)
		}
	}()
	wg.Wait()
}

func TestDistinctConsecutiveRecordsRequireADeclaredScalarKey(t *testing.T) {
	ch := make(chan any, 3)
	ch <- map[string]any{"status": "ready"}
	ch <- map[string]any{"status": "ready"}
	close(ch)
	derived, err := DistinctConsecutive(openTestStream(ch), DistinctOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := derived.Next(context.Background()); ok || err == nil || !strings.Contains(err.Error(), "require a declared scalar key") {
		t.Fatalf("want a declared-key error for a record, got ok=%v err=%v", ok, err)
	}
}

func TestSimultaneousDerivedStreamsKeepIndependentTimersAndState(t *testing.T) {
	clock := &VirtualStreamClock{}
	leftInput := make(chan any, 1)
	rightInput := make(chan any, 1)
	left, err := Debounce(openTestStream(leftInput), time.Second, DebounceOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Debounce(openTestStream(rightInput), 2*time.Second, DebounceOptions{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	leftInput <- float64(1)
	rightInput <- float64(2)
	leftResult := make(chan any, 1)
	rightResult := make(chan any, 1)
	go func() {
		value, _, _ := left.Next(context.Background())
		leftResult <- value
	}()
	go func() {
		value, _, _ := right.Next(context.Background())
		rightResult <- value
	}()
	waitArmedAt(t, left, time.Second)
	waitArmedAt(t, right, 2*time.Second)
	clock.Advance(time.Second)
	if got := <-leftResult; got != float64(1) {
		t.Fatalf("left stream emitted %v", got)
	}
	select {
	case got := <-rightResult:
		t.Fatalf("right stream advanced with the left stream: %v", got)
	default:
	}
	clock.Advance(time.Second)
	if got := <-rightResult; got != float64(2) {
		t.Fatalf("right stream emitted %v", got)
	}
	close(leftInput)
	close(rightInput)
}

func TestHandleLatestDefaultConcurrencySwitchesInsteadOfFailing(t *testing.T) {
	ch := make(chan any, 2)
	started := make(chan struct{})
	go func() {
		ch <- 1.0
		<-started
		ch <- 2.0
		close(ch)
	}()
	var results []any
	err := HandleLatest(context.Background(), openTestStream(ch), func(ctx context.Context, item any) (any, error) {
		if item == 1.0 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return item, nil
	}, LatestOptions{OnResult: func(_, result any) { results = append(results, result) }})
	if err != nil || len(results) != 1 || results[0] != 2.0 {
		t.Fatalf("default latest switch: results=%v err=%v", results, err)
	}
}

func TestHandleLatestReplacementSettlesSupersededOwnedItem(t *testing.T) {
	ch := make(chan any, 2)
	started := make(chan struct{})
	replaced := make(chan string, 1)
	oldItem := NewOwnedItem("old", nil)
	newItem := NewOwnedItem("new", nil)
	go func() {
		ch <- oldItem
		<-started
		ch <- newItem
		close(ch)
	}()
	err := HandleLatest(context.Background(), openTestStream(ch), func(ctx context.Context, item any) (any, error) {
		owned := item.(*OwnedItem)
		if owned == oldItem {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, owned.Complete("handled")
	}, LatestOptions{
		CleanupDeadline: time.Second,
		Replacement: func(item *OwnedItem) error {
			replaced <- item.Value.(string)
			return item.Complete("replaced")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-replaced; got != "old" || !oldItem.Settled() || !newItem.Settled() {
		t.Fatalf("replacement=%q old settled=%v new settled=%v", got, oldItem.Settled(), newItem.Settled())
	}
}

func TestThrottleExpiryEmitsRetainedBeforeNewArrival(t *testing.T) {
	clock := &VirtualStreamClock{}
	counters := &transformCounters{byteLimit: -1, timerLimit: -1}
	policy := &throttlePolicy{clock: clock, allowance: 1, window: time.Second, keeping: "latest", keyOf: keyExtractor(nil), counters: counters, states: map[any]*throttleState{}}
	var emitted []any
	emit := func(value any) bool { emitted = append(emitted, value); return true }
	if err := policy.onItem(1.0, emit); err != nil {
		t.Fatal(err)
	}
	if err := policy.onItem(2.0, emit); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	if err := policy.onItem(3.0, emit); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 2 || emitted[0] != 1.0 || emitted[1] != 2.0 {
		t.Fatalf("retained item was not emitted first: %v", emitted)
	}
}

func TestThrottleWindowBeginsWithFirstItem(t *testing.T) {
	clock := &VirtualStreamClock{}
	counters := &transformCounters{byteLimit: -1, timerLimit: -1}
	policy := &throttlePolicy{clock: clock, allowance: 1, window: 5 * time.Second, keeping: "first", keyOf: keyExtractor(nil), counters: counters, states: map[any]*throttleState{}}
	var emitted []any
	emit := func(value any) bool { emitted = append(emitted, value); return true }
	clock.Advance(2 * time.Second)
	if err := policy.onItem("first", emit); err != nil {
		t.Fatal(err)
	}
	clock.Advance(4 * time.Second)
	if err := policy.onItem("inside", emit); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 1 || emitted[0] != "first" {
		t.Fatalf("window was not anchored to the first item: %v", emitted)
	}
}

func TestThrottleAndBatchSettleHeldOwnedItemsOnFailure(t *testing.T) {
	boom := errors.New("upstream failed")
	for _, tc := range []struct {
		name string
		open func(*OwnedItem) (StreamHandle, error)
	}{
		{"throttle", func(owned *OwnedItem) (StreamHandle, error) {
			up := newStreamHandle(TypeRef{Name: "any"}, "test", &failingAfterItemsSource{items: []any{1.0, owned}, err: boom})
			return Throttle(up, 1, time.Hour, ThrottleOptions{Keeping: "latest"}, nil)
		}},
		{"batch", func(owned *OwnedItem) (StreamHandle, error) {
			up := newStreamHandle(TypeRef{Name: "any"}, "test", &failingAfterItemsSource{items: []any{owned}, err: boom})
			return Batch(up, 10, time.Hour, BatchOptions{}, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disposed := false
			owned := NewOwnedItem(tc.name, func(string) error { disposed = true; return nil })
			derived, err := tc.open(owned)
			if err != nil {
				t.Fatal(err)
			}
			for {
				_, ok, readErr := derived.Next(context.Background())
				if readErr != nil {
					break
				}
				if !ok {
					t.Fatal("failure became normal completion")
				}
			}
			if !disposed || !owned.Settled() {
				t.Fatalf("held owned item was not settled: disposed=%v settled=%v", disposed, owned.Settled())
			}
		})
	}
}

func TestNaNStreamKeysAreRejectedAndKeyHighWaterIsObservable(t *testing.T) {
	if _, err := scalarKey(math.NaN()); err == nil {
		t.Fatal("NaN key accepted")
	}
	clock := &VirtualStreamClock{}
	ch := make(chan any, 2)
	derived, err := Debounce(openTestStream(ch), time.Hour, DebounceOptions{Key: func(value any) (any, error) { return value, nil }, KeyLimit: 2}, clock)
	if err != nil {
		t.Fatal(err)
	}
	ch <- 1.0
	ch <- 2.0
	waitStat(t, derived, "timers", 2)
	stats := derived.TransformStats()
	if stats["keys"] != int64(2) || stats["max_keys"] != int64(2) {
		t.Fatalf("key cardinality missing from stats: %v", stats)
	}
	if snapshot := derived.snapshot("snapshot"); snapshot.Policy["keys"] != int64(2) || snapshot.Policy["max_keys"] != int64(2) {
		t.Fatalf("key cardinality missing from tooling snapshot: %v", snapshot.Policy)
	}
	close(ch)
}

func TestHandleConflatingSettlesWaitingOwnedItemOnHandlerFailure(t *testing.T) {
	ch := make(chan any, 2)
	started := make(chan struct{})
	release := make(chan struct{})
	disposed := atomic.Bool{}
	waiting := NewOwnedItem("waiting", func(string) error { disposed.Store(true); return nil })
	boom := errors.New("handler failed")
	go func() {
		ch <- 1.0
		<-started
		ch <- waiting
		time.Sleep(20 * time.Millisecond)
		close(release)
		close(ch)
	}()
	err := HandleConflating(context.Background(), openTestStream(ch), func(_ context.Context, item any) error {
		if item == 1.0 {
			close(started)
			<-release
			return boom
		}
		return nil
	}, ConflateOptions{})
	if !errors.Is(err, boom) || !disposed.Load() || !waiting.Settled() {
		t.Fatalf("err=%v disposed=%v settled=%v", err, disposed.Load(), waiting.Settled())
	}
}

func TestHandleLatestCompletionCannotSwallowHandlerFailure(t *testing.T) {
	ch := make(chan any, 2)
	ch <- 1.0
	ch <- 2.0
	close(ch)
	started := make(chan struct{}, 2)
	failNow := make(chan struct{})
	boom := errors.New("keyed handler failed")
	go func() {
		<-started
		<-started
		close(failNow)
	}()
	err := HandleLatest(context.Background(), openTestStream(ch), func(_ context.Context, item any) (any, error) {
		started <- struct{}{}
		if item == 1.0 {
			<-failNow
			return nil, boom
		}
		return item, nil
	}, LatestOptions{
		Key:             func(item any) (any, error) { return item, nil },
		KeyLimit:        2,
		Concurrency:     2,
		CleanupDeadline: time.Second,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("handler failure was swallowed at completion: %v", err)
	}
}

func TestPoliciesSettleOwnedItemsWhenEmissionIsCanceled(t *testing.T) {
	t.Run("throttle", func(t *testing.T) {
		clock := &VirtualStreamClock{}
		counters := &transformCounters{byteLimit: -1, timerLimit: -1}
		policy := &throttlePolicy{clock: clock, allowance: 1, window: time.Second, keeping: "latest", keyOf: keyExtractor(nil), counters: counters, states: map[any]*throttleState{}}
		owned := NewOwnedItem("retained", func(string) error { return nil })
		if err := policy.onItem(1.0, func(any) bool { return true }); err != nil {
			t.Fatal(err)
		}
		if err := policy.onItem(owned, func(any) bool { return true }); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
		if err := policy.onTimer(func(any) bool { return false }); err != nil {
			t.Fatal(err)
		}
		if !owned.Settled() {
			t.Fatal("retained throttle item was abandoned")
		}
	})
	t.Run("throttle in allowance", func(t *testing.T) {
		counters := &transformCounters{byteLimit: -1, timerLimit: -1}
		policy := &throttlePolicy{clock: &VirtualStreamClock{}, allowance: 1, window: time.Second, keeping: "first", keyOf: keyExtractor(nil), counters: counters, states: map[any]*throttleState{}}
		owned := NewOwnedItem("admitted", func(string) error { return nil })
		if err := policy.onItem(owned, func(any) bool { return false }); err != nil {
			t.Fatal(err)
		}
		if !owned.Settled() {
			t.Fatal("in-allowance throttle item was abandoned")
		}
	})
	t.Run("batch", func(t *testing.T) {
		counters := &transformCounters{byteLimit: -1, timerLimit: -1}
		policy := &batchPolicy{clock: &VirtualStreamClock{}, count: 1, window: time.Second, keyOf: keyExtractor(nil), counters: counters, batches: map[any]*batchState{}}
		owned := NewOwnedItem("batched", func(string) error { return nil })
		if err := policy.onItem(owned, func(any) bool { return false }); err != nil {
			t.Fatal(err)
		}
		if !owned.Settled() {
			t.Fatal("batched item was abandoned")
		}
	})
	t.Run("relay", func(t *testing.T) {
		owned := NewOwnedItem("relayed", func(string) error { return nil })
		policy := &relayPolicy{clock: &VirtualStreamClock{}, idle: time.Second}
		if err := policy.onItem(owned, func(any) bool { return false }); err != nil {
			t.Fatal(err)
		}
		if !owned.Settled() {
			t.Fatal("relayed item was abandoned")
		}
	})
}
