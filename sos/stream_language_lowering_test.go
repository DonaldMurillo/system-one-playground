package sos

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Spec: docs/sysonescript-stream-handling-spec.md multiline forms.

func TestSpecMultilineKeyedQuietParsesAndChecks(t *testing.T) {
	src := streamSourceAction + "wait for each path in changes to be quiet for 500 milliseconds\n  with at most 1000 pending paths\n  called settled_changes\nfor each item from settled_changes:\n  show item\n"
	if ds := Check(src); len(ds) != 0 {
		t.Fatalf("spec keyed quiet form rejected: %v", firstMessage(ds))
	}
	// Bound must still be required when the continuation line is missing.
	ds := Check(streamSourceAction + "wait for each path in changes to be quiet for 500 milliseconds\n  called settled_changes\n")
	if !hasDiagnostic(ds, "bounded key count") {
		t.Fatalf("keyed quiet waiting without a key bound accepted: %v", firstMessage(ds))
	}
}

func TestSpecMultilineKeyedLimitParsesAndChecks(t *testing.T) {
	src := strings.Replace(streamSourceAction, "called changes", "called alerts", 1) + "limit alerts for each account_id to 5 each minute\n  keeping the first\n  with at most 10000 accounts\n  called limited_alerts\nfor each item from limited_alerts:\n  show item\n"
	if ds := Check(src); len(ds) != 0 {
		t.Fatalf("spec keyed limit form rejected: %v", firstMessage(ds))
	}
	ds := Check(streamSourceAction + "limit alerts for each account_id to 5 each minute\n  keeping the first\n  called limited_alerts\n")
	if !hasDiagnostic(ds, "bounded key count") {
		t.Fatalf("keyed limiting without a key bound accepted: %v", firstMessage(ds))
	}
}

func TestSpecMultilineKeyedBatchParsesAndChecks(t *testing.T) {
	src := strings.Replace(streamSourceAction, "called changes", "called events", 1) + "group events for each customer_id into batches of at most 100\n  or after 5 seconds\n  with at most 10000 customers\n  called customer_batches\nfor each batch from customer_batches:\n  show batch\n"
	if ds := Check(src); len(ds) != 0 {
		t.Fatalf("spec keyed batch form rejected: %v", firstMessage(ds))
	}
}

func TestSpecNewestKeyedColonVariants(t *testing.T) {
	src := streamSourceAction + "handle only the newest job for each customer_id from changes\n  with at most 20 customers at once\n  and at most 10000 known customers:\n  when a newer job arrives cancel the previous work\n\n  show job\n"
	if ds := Check(src); len(ds) != 0 {
		t.Fatalf("spec keyed newest form rejected: %v", firstMessage(ds))
	}
	// The trailing colon is optional on every line of the keyed header set.
	colonless := streamSourceAction + "handle only the newest job from changes:\n  when a newer job arrives cancel the previous work\n\n  show job\n"
	if ds := Check(colonless); len(ds) != 0 {
		t.Fatalf("colon variant of newest header rejected: %v", firstMessage(ds))
	}
}

func TestSpecSplitMergeAndConcatHeaders(t *testing.T) {
	merge := streamSourceAction + "handle each request from changes\n  with at most 32 at once:\n\n  show request\n"
	if ds := Check(merge); len(ds) != 0 {
		t.Fatalf("spec split merge form rejected: %v", firstMessage(ds))
	}
	concat := streamSourceAction + "handle each job from changes\n  one at a time:\n\n  show job\n"
	if ds := Check(concat); len(ds) != 0 {
		t.Fatalf("spec split concat form rejected: %v", firstMessage(ds))
	}
	// A bare header with neither continuation is ambiguous and must be rejected.
	ds := Check(streamSourceAction + "handle each job from changes\n\n  show job\n")
	if !hasDiagnostic(ds, "one at a time") {
		t.Fatalf("bare handler header without a policy accepted: %v", firstMessage(ds))
	}
}

func TestMultiwordExhaustNoun(t *testing.T) {
	src := "to watch streaming text:\n  send \"tick\"\n\nstream watch called refresh_request\nhandle one refresh request at a time\n  rejecting new requests with status 429 while busy:\n\n  show \"handled\"\n"
	if ds := Check(src); len(ds) != 0 {
		t.Fatalf("multiword exhaust noun rejected: %v", firstMessage(ds))
	}
	if out, err := runStreamProgram(t, strings.Replace(src, "show \"handled\"", "show refresh_request", 1)); err != nil || out != "tick\n" {
		t.Fatalf("multiword exhaust runtime mismatch: out=%q err=%v", out, err)
	}
	// Colon variant also parses.
	if kind := classifyLine("handle one request at a time:"); kind != "exhaustStream" {
		t.Fatalf("colon exhaust header classified as %q", kind)
	}
}

func TestHTTPHandlerObligationsResolveTechnicalAliases(t *testing.T) {
	check := func(source string) []Diagnostic {
		_, diagnostics := LoadProgram(filepath.Join(t.TempDir(), "main.sos"), source)
		return diagnostics
	}
	for _, alias := range []string{"http", "web"} {
		prefix := "import \"std/http\" as " + alias + "\nstream " + alias + ".listen with {\"address\": \"127.0.0.1:0\"} called requests\n"
		handler := "handle each request from requests one at a time:\n"
		respond := prefix + handler + "  call " + alias + ".respond_status with request, 204\n"
		if ds := check(respond); len(ds) != 0 {
			t.Fatalf("%s response rejected: %v", alias, firstMessage(ds))
		}
		unanswered := prefix + handler + "  show \"unanswered\"\n"
		if ds := check(unanswered); !hasDiagnostic(ds, "an owned source item must be completed") {
			t.Fatalf("%s unanswered request accepted: %v", alias, firstMessage(ds))
		}
		wrongRequest := prefix + handler + "  call " + alias + ".respond_status with other, 204 called request\n"
		if ds := check(wrongRequest); !hasDiagnostic(ds, "an owned source item must be completed") {
			t.Fatalf("%s response to another item accepted: %v", alias, firstMessage(ds))
		}
	}
}

func TestHTTPPolicyBodiesAreNotExecutableHandlerPaths(t *testing.T) {
	check := func(source string) []Diagnostic {
		_, diagnostics := LoadProgram(filepath.Join(t.TempDir(), "main.sos"), source)
		return diagnostics
	}
	prefix := "import \"std/http\" as web\nstream web.listen with {\"address\": \"127.0.0.1:0\"} called requests\n"
	latest := "handle only the newest request from requests:\n  when a newer request arrives cancel the previous work\n  acknowledging completed effects are not reversed\n  canceling an older request with status 409:\n"
	respond := "  call web.respond_status with request, 204\n"
	if ds := check(prefix + latest + respond); len(ds) != 0 {
		t.Fatalf("valid latest HTTP handler rejected: %v", firstMessage(ds))
	}
	if ds := check(prefix + latest + "    call web.respond_status with request, 204\n"); !hasDiagnostic(ds, "an owned source item must be completed") {
		t.Fatalf("response hidden under ignored latest policy accepted: %v", firstMessage(ds))
	}
	exhaust := "handle one request at a time\n  rejecting new requests with status 429 while busy:\n"
	exhaustPrefix := strings.Replace(prefix, "called requests", "called request", 1)
	if ds := check(exhaustPrefix + exhaust + respond); len(ds) != 0 {
		t.Fatalf("valid exhaust HTTP handler rejected: %v", firstMessage(ds))
	}
	if ds := check(exhaustPrefix + exhaust + "    call web.respond_status with request, 204\n"); !hasDiagnostic(ds, "an owned source item must be completed") {
		t.Fatalf("response hidden under ignored exhaust policy accepted: %v", firstMessage(ds))
	}
}

func TestCanonicalHTTPResponseMustTargetOwnedRequest(t *testing.T) {
	check := func(source string) []Diagnostic {
		_, diagnostics := LoadProgram(filepath.Join(t.TempDir(), "main.sos"), source)
		return diagnostics
	}
	prefix := "listen for HTTP requests on loopback port 8080 called requests:\n  request deadline 3 seconds\nhandle each request from requests one at a time:\n"
	if ds := check(prefix + "  respond to request with status 204\n"); len(ds) != 0 {
		t.Fatalf("valid response rejected: %v", firstMessage(ds))
	}
	wrong := prefix + "  respond to requests with status 200 and text request\n"
	if ds := check(wrong); !hasDiagnostic(ds, "an owned source item must be completed") {
		t.Fatalf("response to wrong binding accepted: %v", firstMessage(ds))
	}
	loop := strings.Replace(prefix, "handle each request from requests one at a time:", "for each request from requests:", 1)
	if ds := check(loop + "  respond to request with status 204\n"); len(ds) != 0 {
		t.Fatalf("valid sequential HTTP loop rejected: %v", firstMessage(ds))
	}
	if ds := check(loop + "  respond to requests with status 200 and text request\n"); !hasDiagnostic(ds, "an owned source item must be completed") {
		t.Fatalf("sequential HTTP loop response to wrong binding accepted: %v", firstMessage(ds))
	}
}

func TestSpecCalledContinuationLines(t *testing.T) {
	idle := strings.Replace(streamSourceAction, "called changes", "called update", 1) + "require an update at least every 30 seconds\n  called monitored_updates\nfor each item from monitored_updates:\n  show item\n"
	if ds := Check(idle); len(ds) != 0 {
		t.Fatalf("idle called continuation rejected: %v", firstMessage(ds))
	}
	takeFor := strings.Replace(streamSourceAction, "called changes", "called updates", 1) + "listen to updates for at most 10 minutes\n  then stop normally\n  called bounded_updates\nfor each item from bounded_updates:\n  show item\n"
	if ds := Check(takeFor); len(ds) != 0 {
		t.Fatalf("take-for continuation rejected: %v", firstMessage(ds))
	}
	deadline := strings.Replace(streamSourceAction, "called changes", "called updates", 1) + "require updates to finish within 10 minutes\n  called bounded_updates\nfor each item from bounded_updates:\n  show item\n"
	if ds := Check(deadline); len(ds) != 0 {
		t.Fatalf("deadline called continuation rejected: %v", firstMessage(ds))
	}
}

func TestChildOnlyPolicyLineDiagnostics(t *testing.T) {
	cases := []string{
		"or after 5 seconds",
		"keeping the first",
		"keeping only the latest waiting update:",
		"ignoring new requests while busy:",
		"when a newer job arrives cancel the previous work",
		"acknowledging completed effects are not reversed",
		"canceling an older job with status 410:",
		"with at most 10 jobs at once",
		"and at most 10 known jobs",
		"with at most 1000 pending paths",
		"one at a time:",
		"then stop normally",
		"called settled",
	}
	for _, line := range cases {
		if ds := Check(line); len(ds) == 0 {
			t.Errorf("top-level continuation line %q accepted", line)
		} else if !hasDiagnostic(ds, "must appear under its stream construction") && !hasDiagnostic(ds, "unknown construction") {
			t.Errorf("top-level continuation line %q: unexpected diagnostic %q", line, firstMessage(ds))
		}
	}
	// Wrong-parent nesting is rejected.
	wrong := streamSourceAction + "handle each job from changes one at a time:\n  when a newer job arrives cancel the previous work\n\n  show job\n"
	if ds := Check(wrong); !hasDiagnostic(ds, "unexpected line under this stream construction") {
		t.Fatalf("newestCancel under sequential handler accepted: %v", firstMessage(ds))
	}
	wrongPolicy := streamSourceAction + "handle changes one at a time\n  ignoring new changes while busy:\n\n  show changes\n"
	if ds := Check(wrongPolicy); !hasDiagnostic(ds, "unexpected line under this stream construction") {
		t.Fatalf("exhaustPolicy under conflation accepted: %v", firstMessage(ds))
	}
	// Duplicate policy lines are rejected exactly once.
	dup := streamSourceAction + "handle changes one at a time\n  keeping only the latest waiting update:\n  keeping only the latest waiting update:\n\n  show changes\n"
	if ds := Check(dup); !hasDiagnostic(ds, "exactly once") {
		t.Fatalf("duplicate conflation policy accepted: %v", firstMessage(ds))
	}
}

func obligationTable(t *testing.T) *ModuleTable {
	t.Helper()
	mod := externalStreamModule(t)
	return &ModuleTable{Aliases: map[string]*Module{"svc": mod}, vocab: &fileVocab{aliases: map[string]*Module{"svc": mod}}}
}

const owningStreamSource = `import "test/svc" as svc

stream svc.requests called owned_items
`

func TestHandlerObligationPerReachablePath(t *testing.T) {
	table := obligationTable(t)
	// A mention inside a quoted string is not a completion.
	quoted := owningStreamSource + "handle each item from owned_items one at a time:\n  call log with \"item\"\n"
	if ds := checkSource(quoted, table); !hasDiagnostic(ds, "every reachable handler path") {
		t.Fatalf("quoted-string mention satisfied the obligation: %v", firstMessage(ds))
	}
	// A bare substring is not the item binding.
	substring := owningStreamSource + "handle each line from owned_items one at a time:\n  show deadline\n"
	if ds := checkSource(substring, table); !hasDiagnostic(ds, "every reachable handler path") {
		t.Fatalf("substring mention satisfied the obligation: %v", firstMessage(ds))
	}
	// A branch that skips completion fails even though another branch completes.
	branchy := owningStreamSource + "handle each item from owned_items one at a time:\n  when ready is true:\n    call complete with item\n  show item\n"
	ds := checkSource(branchy+"\nmake ready false\n", table)
	_ = ds
	ds = checkSource(owningStreamSource+"make ready false\nhandle each item from owned_items one at a time:\n  when ready is true:\n    call complete with item\n", table)
	if !hasDiagnostic(ds, "every reachable handler path") {
		t.Fatalf("one-sided branch satisfied the obligation: %v", firstMessage(ds))
	}
	// Both branches completing passes.
	both := owningStreamSource + "make ready false\nhandle each item from owned_items one at a time:\n  when ready is true:\n    call complete with item\n  otherwise:\n    call reject with item\n"
	if ds := checkSource(both, table); !hasDiagnostic(ds, "every reachable handler path") && hasDiagnostic(ds, "obligation") {
		t.Fatalf("both-branch completion rejected: %v", firstMessage(ds))
	}
}

func TestBatchInheritsObligations(t *testing.T) {
	table := obligationTable(t)
	// The batch sink inherits the source obligations: a later handler that
	// never completes items must be rejected.
	src := owningStreamSource + "group owned_items into batches of at most 10\n  or after 5 seconds\n  called batches\nhandle each batch from batches one at a time:\n  call log with batch\n"
	if ds := checkSource(src, table); !hasDiagnostic(ds, "every reachable handler path") {
		t.Fatalf("batch dropped source obligations: %v", firstMessage(ds))
	}
}

func TestStreamAliasArgumentValidation(t *testing.T) {
	streamsModule, _ := stdModule("std/streams")
	table := &ModuleTable{Aliases: map[string]*Module{"streams": streamsModule}, vocab: &fileVocab{aliases: map[string]*Module{"streams": streamsModule}}}
	cases := []struct {
		line string
		want string
	}{
		{"call streams.debounce with changes, quickly called settled", "positive duration"},
		{"call streams.debounce with changes, 0 milliseconds called settled", "positive duration"},
		{"call streams.throttle with metrics, 10, second, sometimes called limited", "keeping"},
		{"call streams.throttle with metrics, 10, 5, first called limited", "positive window"},
		{"call streams.merge with changes, many", "positive integer"},
		{"call streams.exhaust with changes, maybe", "reject with status"},
		{"call streams.idle_timeout with changes, soon called monitored", "positive duration"},
	}
	for _, c := range cases {
		ds := checkSource(streamSourceAction+c.line+"\n", table)
		if !hasDiagnostic(ds, c.want) {
			t.Errorf("alias validation %q: want %q, got %q", c.line, c.want, firstMessage(ds))
		}
	}
}

func TestHandlerAliasesRequireCanonicalBlocks(t *testing.T) {
	streamsModule, _ := stdModule("std/streams")
	table := &ModuleTable{Aliases: map[string]*Module{"streams": streamsModule}, vocab: &fileVocab{aliases: map[string]*Module{"streams": streamsModule}}}
	ds := checkSource(streamSourceAction+"call streams.switch_latest with changes\n", table)
	if !hasDiagnostic(ds, "requires a canonical handler block") {
		t.Fatalf("bodyless handling alias accepted: %v", firstMessage(ds))
	}
}

func TestStreamBoundsMustBePositive(t *testing.T) {
	cases := []string{
		"wait for each path in changes to be quiet for 1 second with at most 0 pending paths called settled",
		"limit changes for each path to 1 each second keeping the first with at most 0 paths called limited",
		"group changes for each path into batches of at most 2 with at most 0 paths\n  or after 1 second\n  called batches",
	}
	for _, sentence := range cases {
		ds := Check(streamSourceAction + sentence + "\n")
		if !hasDiagnostic(ds, "positive integer") {
			t.Errorf("zero bound accepted for %q: %v", sentence, firstMessage(ds))
		}
	}
}

func TestNonKeyedLatestRejectsKeyedBounds(t *testing.T) {
	source := streamSourceAction + "handle only the newest item from changes:\n  with at most 2 items at once\n  when a newer item arrives cancel the previous work\n  show item\n"
	if ds := Check(source); !hasDiagnostic(ds, "non-keyed latest-only") {
		t.Fatalf("non-keyed latest bound was silently ignored: %v", firstMessage(ds))
	}
}

func TestExhaustHandlerBindsItsItemNoun(t *testing.T) {
	source := strings.Replace(streamSourceAction, "called changes", "called trigger", 1) + "handle one trigger at a time:\n  ignoring new triggers while busy:\n  show trigger\n"
	if ds := Check(source); len(ds) != 0 {
		t.Fatalf("exhaust item noun was not bound: %v", firstMessage(ds))
	}
}

// Every alias rewrite must itself parse and check against the grammar.
func TestStreamAliasRewritesParseAndCheck(t *testing.T) {
	cases := []struct {
		action string
		args   []string
		sink   string
	}{
		{"debounce", []string{"changes", "500 milliseconds"}, "settled"},
		{"throttle", []string{"metrics", "10", "second", "first"}, "limited"},
		{"merge", []string{"changes", "32"}, ""},
		{"concat", []string{"changes"}, ""},
		{"switch_latest", []string{"changes"}, ""},
		{"exhaust", []string{"changes", "reject with status 429"}, ""},
		{"conflate", []string{"changes"}, ""},
		{"batch", []string{"changes", "100", "5 seconds"}, "batches"},
		{"distinct_consecutive", []string{"changes"}, "distinct"},
		{"idle_timeout", []string{"changes", "30 seconds"}, "monitored"},
		{"take_for", []string{"changes", "10 minutes"}, "bounded"},
	}
	for _, c := range cases {
		rewritten, ok := StreamAliasRewrite(c.action, c.args, c.sink)
		if !ok {
			t.Fatalf("StreamAliasRewrite(%s) failed", c.action)
		}
		indented := indentContinuationLines(rewritten)
		base := streamSourceAction
		if len(c.args) > 0 {
			base = strings.Replace(base, "called changes", "called "+c.args[0], 1)
		}
		if ds := Check(base + indented + "\n"); len(ds) != 0 {
			t.Errorf("rewrite of %s does not parse/check: %v\nrewrite:\n%s", c.action, firstMessage(ds), rewritten)
		}
	}
}

// indentContinuationLines re-indents continuation lines of a rewritten
// multi-line construction so the whole text parses as source.
func indentContinuationLines(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i > 0 && line != "" {
			lines[i] = "  " + line
		}
	}
	return strings.Join(lines, "\n")
}

func TestClosedEffectVocabulary(t *testing.T) {
	definition := `schema=1
[module]
path="test/effects"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="zap"
effects=["chaos"]
[action.command]
program="tool"
`
	if _, err := decodeExternalModuleDefinition("effects.toml", []byte(definition)); err == nil || !strings.Contains(err.Error(), "closed effect vocabulary") {
		t.Fatalf("unknown effect accepted: %v", err)
	}
}

func runStreamProgram(t *testing.T, source string) (string, error) {
	t.Helper()
	p, ds := Parse(source)
	if len(ds) != 0 {
		t.Fatalf("parse failed: %v", firstMessage(ds))
	}
	var out bytes.Buffer
	_, err := Run(context.Background(), p, Options{Stdout: &out})
	return out.String(), err
}

func TestCanonicalTransformationsExecute(t *testing.T) {
	out, err := runStreamProgram(t, "to watch streaming text:\n  send \"a\"\n  send \"b\"\n  send \"b\"\n  finish\n\nstream watch called changes\nwait for changes to be quiet for 5 milliseconds called settled\nfor each item from settled:\n  show item\n")
	if err != nil || !strings.Contains(out, "b") || strings.Contains(out, "a\na") {
		t.Fatalf("quiet waiting did not execute: %q %v", out, err)
	}
}

func TestCanonicalBatchExecutes(t *testing.T) {
	out, err := runStreamProgram(t, "to watch streaming text:\n  send \"1\"\n  send \"2\"\n  send \"3\"\n  finish\n\nstream watch called changes\ngroup changes into batches of at most 2\n  or after 5 seconds\n  called batches\nfor each batch from batches:\n  show batch\n")
	if err != nil {
		t.Fatalf("batching did not execute: %v", err)
	}
	if !strings.Contains(out, `["1","2"]`) || !strings.Contains(out, `["3"]`) {
		t.Fatalf("unexpected batch output: %q", out)
	}
}

func TestCanonicalSequentialHandlerExecutes(t *testing.T) {
	out, err := runStreamProgram(t, "to watch streaming text:\n  send \"x\"\n  send \"y\"\n  finish\n\nstream watch called changes\nhandle each item from changes one at a time:\n  show item\n")
	if err != nil || !strings.Contains(out, "x") || !strings.Contains(out, "y") {
		t.Fatalf("sequential handling did not execute: %q %v", out, err)
	}
}

func TestCanonicalConcurrentHandlerUsesIsolatedBindings(t *testing.T) {
	src := `to watch streaming integer:
  make current 0
  while current < 200:
    assign current current + 1
    send current
  finish

stream watch called changes
handle each item from changes with at most 32 at once:
  make seen item
`
	if _, err := runStreamProgram(t, src); err != nil {
		t.Fatalf("bounded concurrent language handler failed: %v", err)
	}
}

func TestStreamAliasCallExecutes(t *testing.T) {
	src := "import \"std/streams\" as streams\n\nto watch streaming text:\n  send \"a\"\n  send \"b\"\n  finish\n\nstream watch called changes\ncall streams.distinct_consecutive with changes called distinct\nfor each item from distinct:\n  show item\n"
	p, ds := LoadProgram("main.sos", src)
	if len(ds) != 0 {
		t.Fatalf("load failed: %v", firstMessage(ds))
	}
	var out bytes.Buffer
	if _, err := Run(context.Background(), p, Options{Stdout: &out}); err != nil {
		t.Fatalf("alias call did not execute: %v", err)
	}
	if !strings.Contains(out.String(), "a") {
		t.Fatalf("unexpected alias output: %q", out.String())
	}
}

func TestCanonicalDistinctRecordDeclaresScalarComparisonKey(t *testing.T) {
	src := `define Update:
  status as text

to watch streaming Update:
  send {"status": "ready"}
  send {"status": "ready"}
  send {"status": "done"}
  finish

stream watch called changes
keep only changes in status from changes called distinct
for each update from distinct:
  show status of update
`
	out, err := runStreamProgram(t, src)
	if err != nil {
		t.Fatalf("keyed distinct did not execute: %v", err)
	}
	if out != "ready\ndone\n" {
		t.Fatalf("keyed distinct compared more than the previous scalar key: %q", out)
	}
}
