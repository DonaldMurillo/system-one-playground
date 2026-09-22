package sos

import (
	"context"
	"strings"
	"testing"
)

func firstMessage(ds []Diagnostic) string {
	for _, d := range ds {
		return d.Message
	}
	return ""
}

func hasDiagnostic(ds []Diagnostic, substr string) bool {
	for _, d := range ds {
		if strings.Contains(d.Message, substr) {
			return true
		}
	}
	return false
}

const streamSourceAction = `
to watch streaming text:
  send "tick"

stream watch called changes
`

// externalStreamModule builds an external module whose actions declare effect
// and obligation metadata the static checks consume.
func externalStreamModule(t *testing.T) *Module {
	t.Helper()
	definition := `schema=1
[module]
path="test/svc"
version="1.2.3"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="charge"
effects=["non-idempotent"]
parameter=[{name="order", type="text"}]
owns=["response"]
[action.command]
program="tool"
[[action]]
name="requests"
effects=["read"]
owns=["acknowledgment"]
[action.result]
type="stream of text"
[action.command]
program="tool"
stdout="json-lines"
[[action]]
name="peek"
effects=["read"]
[action.command]
program="tool"
`
	d, err := decodeExternalModuleDefinition("svc.toml", []byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := d.module()
	if err != nil {
		t.Fatal(err)
	}
	return mod
}

func checkWithModule(source string, mod *Module) []Diagnostic {
	table := &ModuleTable{Aliases: map[string]*Module{"svc": mod}, vocab: &fileVocab{aliases: map[string]*Module{"svc": mod}}}
	return checkSource(source, table)
}

func TestStreamHandlingSentenceClassification(t *testing.T) {
	cases := []struct{ line, kind string }{
		{"wait for changes to be quiet for 500 milliseconds called settled_changes", "quietStream"},
		{"wait for each path in changes to be quiet for 500 milliseconds with at most 1000 pending paths called settled_changes", "quietStream"},
		{"limit metrics to at most 10 each second", "limitStream"},
		{"limit status_updates to one each second", "limitStream"},
		{"limit alerts for each account_id to 5 each minute", "limitStream"},
		{"keeping the first", "throttlePolicy"},
		{"keeping the latest", "throttlePolicy"},
		{"rejecting excess requests with status 429", "rejectExcess"},
		{"handle each request from requests with at most 32 at once:", "handleEachStream"},
		{"handle each job from jobs one at a time:", "handleOneStream"},
		{"handle only the newest query from queries:", "newestStream"},
		{"handle only the newest job for each customer_id from jobs:", "newestStream"},
		{"when a newer query arrives cancel the previous work", "newestCancel"},
		{"when a newer order arrives cancel remaining work", "newestCancel"},
		{"acknowledging completed effects are not reversed", "effectAck"},
		{"canceling an older request with status 409:", "cancelPolicy"},
		{"with at most 20 customers at once", "streamBound"},
		{"and at most 10000 known customers", "streamKnownBound"},
		{"handle updates one at a time", "conflateStream"},
		{"keeping only the latest waiting update:", "conflatePolicy"},
		{"handle one refresh_request at a time", "exhaustStream"},
		{"ignoring new requests while busy:", "exhaustPolicy"},
		{"rejecting new requests with status 429 while busy:", "exhaustPolicy"},
		{"group events into batches of at most 100", "batchStream"},
		{"group events for each customer_id into batches of at most 100", "batchStream"},
		{"or after 5 seconds", "batchWindow"},
		{"ignore consecutive duplicate statuses called status_changes", "distinctStream"},
		{"keep only changes in status from updates called status_changes", "distinctKeyStream"},
		{"require an update at least every 30 seconds called monitored_updates", "idleStream"},
		{"require an item from updates at least every 30 seconds called monitored_updates", "idleStream"},
		{"listen to updates for at most 10 minutes then stop normally called bounded_updates", "takeForStream"},
		{"require updates to finish within 10 minutes called bounded_updates", "deadlineStream"},
		{"keep each event from events called critical_events:", "filterStream"},
		{"keep event", "keepItem"},
		{"take a value from each event in events called messages:", "projectStream"},
		{"use message of event", "useValue"},
		{"called settled_changes", "calledName"},
	}
	for _, c := range cases {
		if kind := classifyLine(c.line); kind != c.kind {
			t.Errorf("classifyLine(%q) = %q; want %q", c.line, kind, c.kind)
		}
	}
}

func TestQuietWaitingChecks(t *testing.T) {
	if ds := Check(streamSourceAction + "wait for changes to be quiet for 500 milliseconds called settled\nfor each item from settled:\n  show item\n"); len(ds) != 0 {
		t.Fatalf("valid quiet waiting rejected: %v", firstMessage(ds))
	}
	if ds := Check(streamSourceAction + "wait for changes to be quiet for 0 milliseconds called settled\n"); !hasDiagnostic(ds, "positive duration") {
		t.Fatalf("zero duration accepted: %v", firstMessage(ds))
	}
	ds := Check(streamSourceAction + "wait for each path in changes to be quiet for 500 milliseconds called settled\n")
	if !hasDiagnostic(ds, "bounded key count") {
		t.Fatalf("keyed quiet waiting without a key bound accepted: %v", firstMessage(ds))
	}
	if ds := Check(streamSourceAction + "wait for changes to be quiet for 500 milliseconds\n"); !hasDiagnostic(ds, "name its derived stream") {
		t.Fatalf("unnamed quiet waiting accepted: %v", firstMessage(ds))
	}
}

func TestLimitStreamPolicyMandatory(t *testing.T) {
	ds := Check(streamSourceAction + "limit changes to at most 10 each second\n  keeping the first\n  called limited\nfor each item from limited:\n  show item\n")
	if len(ds) != 0 {
		t.Fatalf("valid rate limiting rejected: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "limit changes to at most 10 each second called limited\nfor each item from limited:\n  show item\n")
	if !hasDiagnostic(ds, "rate policy is mandatory") {
		t.Fatalf("missing policy accepted: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "limit changes to at most 10 each second\n  keeping the first\n")
	if !hasDiagnostic(ds, "name its derived stream") {
		t.Fatalf("missing sink accepted: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "limit changes to at most many each second\n  keeping the first\n  called limited\nfor each item from limited:\n  show item\n")
	if !hasDiagnostic(ds, "positive integer") {
		t.Fatalf("non-integer allowance accepted: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "limit changes for each key to 5 each second\n  keeping the first\n  called limited\nfor each item from limited:\n  show item\n")
	if !hasDiagnostic(ds, "bounded key count") {
		t.Fatalf("keyed limiting without a key bound accepted: %v", firstMessage(ds))
	}
}

func TestBatchRequiresWindowAndBounds(t *testing.T) {
	if ds := Check(streamSourceAction + "group changes into batches of at most 100\n  or after 5 seconds\n  called batches\nfor each item from batches:\n  show item\n"); len(ds) != 0 {
		t.Fatalf("valid batching rejected: %v", firstMessage(ds))
	}
	ds := Check(streamSourceAction + "group changes into batches of at most 100 called batches\n")
	if !hasDiagnostic(ds, "time window") {
		t.Fatalf("batching without a window accepted: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "group changes into batches of at most 100\n  or after 0 seconds\n  called batches\n")
	if !hasDiagnostic(ds, "positive duration") {
		t.Fatalf("zero window accepted: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "group changes for each key into batches of at most 100\n  or after 5 seconds\n  called batches\nfor each item from batches:\n  show item\n")
	if !hasDiagnostic(ds, "bounded key count") {
		t.Fatalf("keyed batching without a key bound accepted: %v", firstMessage(ds))
	}
}

func TestNewestHandlingRequiresCancelLine(t *testing.T) {
	ds := Check(streamSourceAction + "handle only the newest query from changes:\n\n  show \"x\"\n")
	if !hasDiagnostic(ds, "cancellation line") {
		t.Fatalf("latest-only handling without a cancel line accepted: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "handle only the newest job for each customer from changes:\n  with at most 20 customers at once\n")
	if !hasDiagnostic(ds, "both a concurrency bound") {
		t.Fatalf("keyed latest-only handling without both bounds accepted: %v", firstMessage(ds))
	}
}

func TestNewestHandlingEffectAcknowledgment(t *testing.T) {
	mod := externalStreamModule(t)
	source := `import "svc.toml" as svc

stream svc.requests called orders

handle only the newest order from orders:
  when a newer order arrives cancel the previous work

  call svc.charge with order
`
	ds := checkWithModule(source, mod)
	if !hasDiagnostic(ds, "acknowledging completed effects are not reversed") {
		t.Fatalf("non-idempotent effect without acknowledgment accepted: %v", firstMessage(ds))
	}
	if !hasDiagnostic(ds, "canceling an older") {
		t.Fatalf("owned obligation without a cancellation policy accepted: %v", firstMessage(ds))
	}
	source = `import "svc.toml" as svc

stream svc.requests called orders

handle only the newest order from orders:
  when a newer order arrives cancel the previous work
  acknowledging completed effects are not reversed
  canceling an older order with status 409:

  call svc.charge with order
`
	if ds := checkWithModule(source, mod); len(ds) != 0 {
		t.Fatalf("acknowledged latest-only handling rejected: %v", firstMessage(ds))
	}
}

func TestExhaustIgnoringOwnedItemsInvalid(t *testing.T) {
	mod := externalStreamModule(t)
	source := `import "svc.toml" as svc

stream svc.requests called trigger

handle one trigger at a time
  ignoring new trigger while busy:

  show "busy"
`
	ds := checkWithModule(source, mod)
	if !hasDiagnostic(ds, "ignoring is valid only for values without completion obligations") {
		t.Fatalf("ignoring owned items accepted: %v", firstMessage(ds))
	}
	source = `import "svc.toml" as svc

stream svc.requests called trigger

handle one trigger at a time
  rejecting new trigger with status 429 while busy:

  show "busy"
`
	if ds := checkWithModule(source, mod); len(ds) != 0 {
		t.Fatalf("rejecting busy policy rejected: %v", firstMessage(ds))
	}
}

func TestConflateRequiresPolicyLine(t *testing.T) {
	ds := Check(streamSourceAction + "handle changes one at a time\n\n  show \"x\"\n")
	if !hasDiagnostic(ds, "keeping only the latest waiting") {
		t.Fatalf("conflation without a retention line accepted: %v", firstMessage(ds))
	}
}

func TestFilterAndProjectionBlockRules(t *testing.T) {
	if ds := Check(streamSourceAction + "keep each event from changes called critical:\n  when severity of event is \"critical\":\n    keep event\nfor each item from critical:\n  show item\n"); len(ds) != 0 {
		t.Fatalf("valid filter rejected: %v", firstMessage(ds))
	}
	ds := Check(streamSourceAction + "keep each event from changes called critical:\n  when severity of event is \"critical\":\n    keep event\n    keep event\n")
	if !hasDiagnostic(ds, "at most once") {
		t.Fatalf("double keep accepted: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "keep each event from changes called critical:\n  when severity of event is \"critical\":\n    keep other\n")
	if !hasDiagnostic(ds, "must keep event, not other") {
		t.Fatalf("keeping the wrong value accepted: %v", firstMessage(ds))
	}
	if ds := Check(streamSourceAction + "take a value from each event in changes called messages:\n  use message of event\nfor each item from messages:\n  show item\n"); len(ds) != 0 {
		t.Fatalf("valid projection rejected: %v", firstMessage(ds))
	}
	ds = Check(streamSourceAction + "take a value from each event in changes called messages:\n")
	if !hasDiagnostic(ds, "exactly one value") && !hasDiagnostic(ds, "expected an indented body") {
		t.Fatalf("empty projection accepted: %v", firstMessage(ds))
	}
}

func TestDerivedStreamOwnershipConsumedOnce(t *testing.T) {
	source := streamSourceAction + "wait for changes to be quiet for 500 milliseconds called settled\ngroup settled into batches of at most 10\n  or after 5 seconds\n  called batches\nfor each item from batches:\n  show item\n"
	if ds := Check(source); len(ds) != 0 {
		t.Fatalf("chained transformations rejected: %v", firstMessage(ds))
	}
	source += "group settled into batches of at most 10\n  or after 5 seconds\n  called again\n"
	ds := Check(source)
	if !hasDiagnostic(ds, "settled was already consumed") {
		t.Fatalf("re-consuming a consumed source accepted: %v", firstMessage(ds))
	}
}

func TestStreamOpenedInsideConditionalMustBeConsumed(t *testing.T) {
	ds := Check("to watch streaming text:\n  finish\n\nwhen true:\n  stream watch called leaked\n")
	if !hasDiagnostic(ds, "stream leaked remains active") {
		t.Fatalf("conditional stream leak was accepted: %v", firstMessage(ds))
	}
}

func TestLifetimeAndIdleDurationsPositive(t *testing.T) {
	if ds := Check(streamSourceAction + "listen to changes for at most 10 minutes then stop normally called bounded\nfor each item from bounded:\n  show item\n"); len(ds) != 0 {
		t.Fatalf("valid bounded listening rejected: %v", firstMessage(ds))
	}
	if ds := Check(streamSourceAction + "require changes to finish within 10 minutes called bounded\nfor each item from bounded:\n  show item\n"); len(ds) != 0 {
		t.Fatalf("valid deadline rejected: %v", firstMessage(ds))
	}
	if ds := Check(streamSourceAction + "require an item from changes at least every 30 seconds called monitored\nfor each item from monitored:\n  show item\n"); len(ds) != 0 {
		t.Fatalf("valid idle deadline rejected: %v", firstMessage(ds))
	}
	ds := Check(streamSourceAction + "listen to changes for at most 0 minutes then stop normally called bounded\nfor each item from bounded:\n  show item\n")
	if !hasDiagnostic(ds, "positive duration") {
		t.Fatalf("zero lifetime accepted: %v", firstMessage(ds))
	}
}

func TestStreamAliasRewriteDeterministic(t *testing.T) {
	cases := []struct {
		action string
		args   []string
		sink   string
		want   string
	}{
		{"debounce", []string{"changes", "500 milliseconds"}, "settled_changes", "wait for changes to be quiet for 500 milliseconds called settled_changes"},
		{"throttle", []string{"metrics", "10", "second", "first"}, "limited_metrics", "limit metrics to 10 each second keeping the first called limited_metrics"},
		{"concat", []string{"jobs"}, "", "handle each item from jobs one at a time:"},
		{"distinct_consecutive", []string{"statuses"}, "status_changes", "ignore consecutive duplicate statuses called status_changes"},
		{"take_for", []string{"updates", "10 minutes"}, "bounded_updates", "listen to updates for at most 10 minutes then stop normally called bounded_updates"},
	}
	for _, c := range cases {
		got, ok := StreamAliasRewrite(c.action, c.args, c.sink)
		if !ok || got != c.want {
			t.Errorf("StreamAliasRewrite(%s) = %q, %v; want %q", c.action, got, ok, c.want)
		}
		again, _ := StreamAliasRewrite(c.action, c.args, c.sink)
		if again != got {
			t.Errorf("rewrite is not deterministic for %s", c.action)
		}
	}
	if _, ok := StreamAliasRewrite("throttle", []string{"metrics", "10", "second", "sometimes"}, "x"); ok {
		t.Error("invalid throttle policy rewritten")
	}
	if _, ok := StreamAliasRewrite("cannonball", nil, ""); ok {
		t.Error("unknown alias rewritten")
	}
}

func TestStreamAliasCallChecking(t *testing.T) {
	streamsModule, _ := stdModule("std/streams")
	table := &ModuleTable{Aliases: map[string]*Module{"streams": streamsModule}, vocab: &fileVocab{aliases: map[string]*Module{"streams": streamsModule}}}
	ds := checkSource(streamSourceAction+"call streams.debounce with changes, 500 milliseconds called settled\nfor each item from settled:\n  show item\n", table)
	if len(ds) != 0 {
		t.Fatalf("well-formed alias call rejected: %v", firstMessage(ds))
	}
	ds = checkSource("call streams.debounce with changes called settled\n", table)
	if !hasDiagnostic(ds, "streams.debounce expects 2 argument(s)") {
		t.Fatalf("wrong arity accepted: %v", firstMessage(ds))
	}
	ds = checkSource("call streams.debounce with changes, 500 milliseconds\n", table)
	if !hasDiagnostic(ds, "must name its derived stream") {
		t.Fatalf("alias call without a sink accepted: %v", firstMessage(ds))
	}
	ds = checkSource("call streams.cannonball with changes\n", table)
	if !hasDiagnostic(ds, "not a std/streams operation") {
		t.Fatalf("unknown alias accepted: %v", firstMessage(ds))
	}
}

func TestStreamHandlingNeverUsesJev(t *testing.T) {
	lines := []string{
		"wait for changes to be quiet for 500 milliseconds called settled_changes",
		"limit metrics to at most 10 each second",
		"handle only the newest query from queries:",
		"group events into batches of at most 100",
		"call streams.debounce with changes, 500 milliseconds called settled_changes",
	}
	for _, line := range lines {
		p, ds := Parse(line)
		if len(ds) != 0 || len(p.Statements) != 1 {
			t.Fatalf("Parse(%q) failed: %v", line, firstMessage(ds))
		}
		if UsesJev(p.Statements[0]) {
			t.Errorf("UsesJev(%q) = true", line)
		}
	}
}

func TestExternalActionOwnsValidation(t *testing.T) {
	definition := `schema=1
[module]
path="test/owns"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="charge"
owns=["Response"]
[action.command]
program="tool"
`
	if _, err := decodeExternalModuleDefinition("owns.toml", []byte(definition)); err == nil || !strings.Contains(err.Error(), "owned obligations are lowercase") {
		t.Fatalf("invalid obligation name accepted: %v", err)
	}
}

func TestReservedStreamFailuresCannotBeRedefined(t *testing.T) {
	ds := Check("define failure StreamIdleTimeout:\n  reason as text\n")
	if !hasDiagnostic(ds, "reserved") {
		t.Fatalf("reserved stream failure redefinition accepted: %v", firstMessage(ds))
	}
}

// TestCanonicalStreamSentencesStayByteForByte verifies the canonical stream
// handling sentences take the canonical-only fast path: the source is its own
// canonical program and no interpretation request is made.
func TestCanonicalStreamSentencesStayByteForByte(t *testing.T) {
	source := `to follow streaming text:
  finish

stream follow called changes
wait for changes to be quiet for 500 milliseconds called settled
for each item from settled:
  show item
`
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("canonical", "explicit")})
	if err != nil {
		t.Fatalf("canonical stream handling source failed analysis: %v", firstMessage(out.Diagnostics))
	}
	if out.Canonical != source {
		t.Fatalf("canonical program was rewritten:\n%s", out.Canonical)
	}
	for _, decision := range out.Decisions {
		if decision.Method == "jev" {
			t.Fatalf("canonical stream handling sentence reached the provider: %+v", decision)
		}
	}
}
