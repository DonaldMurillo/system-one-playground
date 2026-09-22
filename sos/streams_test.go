package sos

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

type testStreamSource struct{ cancelled bool }

func (*testStreamSource) next(context.Context) (any, bool, error) { return nil, false, nil }
func (s *testStreamSource) cancel(context.Context) error          { s.cancelled = true; return nil }

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

func TestLocalStreamingDeclarationCannotOpenWithoutHost(t *testing.T) {
	source := "to follow streaming text:\n  finish\nstream follow called events\nclose stream events\n"
	var messages []string
	for _, d := range Check(source) {
		messages = append(messages, d.Message)
	}
	if !strings.Contains(strings.Join(messages, "\n"), "has no host implementation") {
		t.Fatalf("diagnostics=%v", messages)
	}
	_, err := Run(context.Background(), mustParse(t, source), Options{})
	if err == nil || !strings.Contains(err.Error(), "has no host implementation") {
		t.Fatalf("error=%v", err)
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
