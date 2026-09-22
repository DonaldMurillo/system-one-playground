package sos

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
