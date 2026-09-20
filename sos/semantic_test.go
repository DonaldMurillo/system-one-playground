package sos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

func semanticTestEnv(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	t.Setenv("TYPESAFE_BASE_URL", baseURL)
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-test")
}

// fixtureReq is one decoded provider request seen by the HTTP fixture.
type fixtureReq struct {
	State    map[string]any
	Model    string
	Labels   []string
	Sentence any
}

// fixtureQuestion is the one question the resolver sends per request.
type fixtureQuestion struct {
	Type     string         `json:"type"`
	Criteria map[string]any `json:"criteria"`
}

// semanticChoiceServer is an HTTP fixture for the provider gateway. It is a
// wire-level fixture for exercising the resolver's discrimination contract,
// not a fake product provider; live evaluation runs against the real API.
func semanticChoiceServer(t *testing.T, pick func(req fixtureReq) (string, float64)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Errorf("fixture: unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			State     map[string]any             `json:"state"`
			Model     string                     `json:"model"`
			Questions map[string]fixtureQuestion `json:"questions"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("fixture: invalid request body: %v", err)
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		if len(req.Questions) != 1 {
			t.Errorf("fixture: want exactly one question, got %d", len(req.Questions))
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		var question fixtureQuestion
		for _, q := range req.Questions {
			question = q
		}
		if question.Type != "choice" {
			t.Errorf("fixture: want choice question, got %q", question.Type)
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		labels := make([]string, 0, len(question.Criteria))
		for label := range question.Criteria {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		freq := fixtureReq{State: req.State, Model: req.Model, Labels: labels, Sentence: req.State["sentence"]}
		label, confidence := pick(freq)
		rest := 0.0
		if n := len(labels) - 1; n > 0 {
			rest = (1 - confidence) / float64(n)
		}
		dist := map[string]float64{}
		for _, l := range labels {
			dist[l] = rest
		}
		dist[label] = confidence
		resp := map[string]any{
			"model": "jev-fixture",
			"answers": map[string]any{
				"answer": map[string]any{
					"type":          "choice",
					"choice":        label,
					"confidence":    confidence,
					"probabilities": dist,
				},
			},
			"usage": map[string]any{"input_tokens": 42},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("fixture: encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func semanticConfig(interpretation, runtime string) sosconfig.Effective {
	return sosconfig.Effective{Interpretation: interpretation, Runtime: runtime, Requests: 25}
}

func requireSuccess(t *testing.T, out *Analysis, err error) *Analysis {
	t.Helper()
	if err != nil {
		t.Fatalf("want success, got error %v (diagnostics %+v)", err, out.Diagnostics)
	}
	if out == nil {
		t.Fatal("want analysis result")
	}
	if len(out.Diagnostics) > 0 {
		t.Fatalf("want no diagnostics, got %+v", out.Diagnostics)
	}
	return out
}

func requireFailure(t *testing.T, out *Analysis, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want failure containing %q, got success %+v", substr, out)
	}
	if out == nil {
		t.Fatal("failure must still return an analysis with diagnostics and usage")
	}
	if out.Canonical != "" {
		t.Fatalf("failed analysis must not expose a canonical program, got %q", out.Canonical)
	}
	found := false
	for _, d := range out.Diagnostics {
		if strings.Contains(d.Message, substr) {
			found = true
		}
	}
	if !found {
		t.Fatalf("want diagnostic containing %q, got %+v", substr, out.Diagnostics)
	}
}

func TestSemanticCanonicalFastpathPreservesSourceByteForByte(t *testing.T) {
	sources := []string{
		"read \"a.json\" as json called a\nshow a\n",
		"read \"a.json\" as json called a\n# a comment\n\nshow \"retain them where quoted\"\nsort a by created descending\n",
		"+++\nversion = 1\n\n[budget.run]\nrequests = 5\n+++\nread \"a.json\" as json called a\nshow a\n",
		"read \"a.json\" as json called a\r\nshow a\r\n",
		"show \"done\"",
	}
	for _, policy := range []string{"canonical", "semantic"} {
		for _, source := range sources {
			semanticTestEnv(t, "http://127.0.0.1:1")
			out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig(policy, "deny")})
			requireSuccess(t, out, err)
			if out.Canonical != source {
				t.Fatalf("canonical fast path must preserve the source exactly:\nwant %q\ngot  %q", source, out.Canonical)
			}
			lines := strings.Count(strings.TrimSuffix(source, "\n"), "\n") + 1
			if len(out.SourceMap) != lines {
				t.Fatalf("source map length %d, want %d", len(out.SourceMap), lines)
			}
			for i, v := range out.SourceMap {
				if v != i+1 {
					t.Fatalf("source map[%d] = %d, want identity", i, v)
				}
			}
			if len(out.Decisions) != 0 {
				t.Fatalf("canonical source needs no decisions, got %+v", out.Decisions)
			}
			if got := out.Usage.Buckets[BudgetInterpretation].Requests; got != 0 {
				t.Fatalf("canonical source made %d interpretation requests", got)
			}
			if out.Model != "jev-test" {
				t.Fatalf("model %q, want ambient jev-test", out.Model)
			}
		}
	}
}

func TestSemanticDeterministicParaphraseLowering(t *testing.T) {
	cases := []struct {
		name      string
		sentence  string
		want      string
		candidate string
	}{
		{"retain", `retain tickets where status is "open"`, `keep tickets where status is "open"`, "keep-where:tickets"},
		{"remove", `remove tickets where priority < 2`, `keep tickets where not (priority < 2)`, "keep-where-not:tickets"},
		{"order plain", `order tickets by created`, `sort tickets by created ascending`, "sort-ascending:tickets"},
		{"order descending", `order tickets by created descending`, `sort tickets by created descending`, "sort-descending:tickets"},
		{"order highest first", `order tickets by priority highest first`, `sort tickets by priority descending`, "sort-descending:tickets"},
		{"order lowest first", `order tickets by priority lowest first`, `sort tickets by priority ascending`, "sort-ascending:tickets"},
		{"sort highest first", `sort tickets by priority highest first`, `sort tickets by priority descending`, "sort-descending:tickets"},
		{"sort lowest first", `sort tickets by priority lowest first`, `sort tickets by priority ascending`, "sort-ascending:tickets"},
		{"collect", `collect tickets by team called teams`, `group tickets by team called teams`, "group-by:tickets"},
		{"load called", `load "tickets.json" as json called tickets`, `read "tickets.json" as json called tickets`, "read-json"},
		{"load into", `load "tickets.json" as json into tickets`, `read "tickets.json" as json called tickets`, "read-json"},
		{"read into", `read "tickets.json" as json into tickets`, `read "tickets.json" as json called tickets`, "read-json"},
		{"write to", `write tickets as json to "out.json"`, `save tickets as json in "out.json"`, "save-in"},
		{"store in", `store tickets as json in "out.json"`, `save tickets as json in "out.json"`, "save-in"},
		{"group them", `group them by team called teams`, `group tickets by team called teams`, "group-by:tickets"},
		{"sort them", `sort them by created`, `sort tickets by created ascending`, "sort-ascending:tickets"},
		{"keep them", `keep them where status is "open"`, `keep tickets where status is "open"`, "keep-where:tickets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			semanticTestEnv(t, "http://127.0.0.1:1")
			source := "read \"tickets.json\" as json called tickets\n" + tc.sentence + "\n"
			out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
			requireSuccess(t, out, err)
			want := "read \"tickets.json\" as json called tickets\n" + tc.want + "\n"
			if out.Canonical != want {
				t.Fatalf("canonical lowering:\nwant %q\ngot  %q", want, out.Canonical)
			}
			if len(out.Decisions) != 1 {
				t.Fatalf("want one decision, got %+v", out.Decisions)
			}
			d := out.Decisions[0]
			if d.Line != 2 || d.Source != tc.sentence || d.Candidate != tc.candidate {
				t.Fatalf("decision %+v", d)
			}
			if d.Method != "deterministic" || d.Confidence != 1 {
				t.Fatalf("decision must be deterministic with confidence 1, got %+v", d)
			}
			if got := out.Usage.Buckets[BudgetInterpretation].Requests; got != 0 {
				t.Fatalf("unambiguous sentence made %d provider requests", got)
			}
			if len(out.SourceMap) != 2 || out.SourceMap[0] != 1 || out.SourceMap[1] != 2 {
				t.Fatalf("source map %+v", out.SourceMap)
			}
		})
	}
}

func TestSemanticKeepThemJevBlockPreservesMembers(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "read \"tickets.json\" as json called tickets\nkeep them where jev:\n  ask \"The customer is blocked\"\n  using message\n  accept probability at least 0.85\n  on uncertain discard\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, out, err)
	want := "read \"tickets.json\" as json called tickets\nkeep tickets where jev:\n  ask \"The customer is blocked\"\n  using message\n  accept probability at least 0.85\n  on uncertain discard\n"
	if out.Canonical != want {
		t.Fatalf("canonical:\nwant %q\ngot  %q", want, out.Canonical)
	}
	if got := out.Usage.TotalAdmitted; got != 0 {
		t.Fatalf("single-referent block form must not call the provider, got %d", got)
	}
}

func TestSemanticCanonicalModeDeniesParaphraseWithoutCalls(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "read \"tickets.json\" as json called tickets\nretain tickets where status is \"open\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("canonical", "explicit")})
	requireFailure(t, out, err, "interpretation mode canonical requires canonical sentences")
	found := false
	for _, d := range out.Diagnostics {
		if strings.Contains(d.Message, "write: keep tickets where status is \"open\"") {
			found = true
		}
	}
	if !found {
		t.Fatalf("canonical denial should suggest the exact canonical form, got %+v", out.Diagnostics)
	}
	if out.Diagnostics[0].Line != 2 {
		t.Fatalf("diagnostic line %d, want 2", out.Diagnostics[0].Line)
	}
	if got := out.Usage.TotalAdmitted; got != 0 {
		t.Fatalf("canonical mode made %d provider requests", got)
	}
}

func TestSemanticAmbiguousReferentResolvedByProvider(t *testing.T) {
	var seen fixtureReq
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		seen = req
		return "keep-where:events", 0.91
	})
	semanticTestEnv(t, srv.URL)
	source := "read \"tickets.json\" as json called tickets\nread \"events.json\" as json called events\nkeep them where status is \"open\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, out, err)
	want := "read \"tickets.json\" as json called tickets\nread \"events.json\" as json called events\nkeep events where status is \"open\"\n"
	if out.Canonical != want {
		t.Fatalf("canonical:\nwant %q\ngot  %q", want, out.Canonical)
	}
	wantLabels := []string{"keep-where:events", "keep-where:tickets", "reject"}
	if fmt.Sprint(seen.Labels) != fmt.Sprint(wantLabels) {
		t.Fatalf("candidate labels %+v, want %+v", seen.Labels, wantLabels)
	}
	if seen.Sentence != "keep them where status is \"open\"" {
		t.Fatalf("state sentence %v", seen.Sentence)
	}
	if seen.Model != "jev-test" {
		t.Fatalf("request model %q, want jev-test", seen.Model)
	}
	if len(out.Decisions) != 1 || out.Decisions[0].Method != "jev" || out.Decisions[0].Candidate != "keep-where:events" {
		t.Fatalf("decisions %+v", out.Decisions)
	}
	if math.Abs(out.Decisions[0].Confidence-0.91) > 1e-9 {
		t.Fatalf("confidence %v", out.Decisions[0].Confidence)
	}
	bucket := out.Usage.Buckets[BudgetInterpretation]
	if bucket.Requests != 1 || bucket.ReportedInputTokens != 42 || bucket.Unresolved != 0 {
		t.Fatalf("interpretation usage %+v", bucket)
	}
}

func TestSemanticFilterDirectionResolvedByProvider(t *testing.T) {
	var seenLabels []string
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		seenLabels = req.Labels
		return "keep-where-not:tickets", 0.88
	})
	semanticTestEnv(t, srv.URL)
	source := "read \"tickets.json\" as json called tickets\nfilter tickets where status is \"open\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, out, err)
	want := "read \"tickets.json\" as json called tickets\nkeep tickets where not (status is \"open\")\n"
	if out.Canonical != want {
		t.Fatalf("canonical:\nwant %q\ngot  %q", want, out.Canonical)
	}
	wantLabels := []string{"keep-where-not:tickets", "keep-where:tickets", "reject"}
	if fmt.Sprint(seenLabels) != fmt.Sprint(wantLabels) {
		t.Fatalf("candidate labels %+v, want %+v (explicit reject alternative required)", seenLabels, wantLabels)
	}
	if out.Decisions[0].Method != "jev" {
		t.Fatalf("filter direction must be a model decision, got %+v", out.Decisions[0])
	}
}

func TestSemanticProviderRejectYieldsDiagnostic(t *testing.T) {
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		return "reject", 0.9
	})
	semanticTestEnv(t, srv.URL)
	source := "read \"tickets.json\" as json called tickets\nfilter tickets where status is \"open\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireFailure(t, out, err, "Jev rejected every listed meaning")
	if got := out.Usage.Buckets[BudgetInterpretation].Requests; got != 1 {
		t.Fatalf("rejection still consumed one request, got %d", got)
	}
}

func TestSemanticLowConfidenceSelectionRefused(t *testing.T) {
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		return "keep-where:tickets", 0.5
	})
	semanticTestEnv(t, srv.URL)
	source := "read \"tickets.json\" as json called tickets\nfilter tickets where status is \"open\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireFailure(t, out, err, "below the policy minimum 0.8")
}

func TestSemanticMissingSlotsDiagnoseBeforeCalls(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	cases := []struct {
		name   string
		source string
		substr string
	}{
		{"own file destination", "read \"t.json\" as json called tickets\nsave each group in its own file\n", "unknown construction"},
		{"group without result name", "read \"t.json\" as json called tickets\ngroup them by team\n", "unknown construction"},
		{"order without field", "read \"t.json\" as json called tickets\norder tickets by\n", "unknown construction"},
		{"unbalanced expression", "read \"t.json\" as json called tickets\nremove tickets where ((status is \"open\")\n", "unclosed bracket"},
		{"negated jev predicate", "read \"t.json\" as json called tickets\nremove tickets where jev \"urgent\"\n", "cannot be negated"},
		{"no visible collection", "show \"start\"\nkeep them where status is \"open\"\n", "no visible collection"},
		{"unsupported english", "please urgently triage everything\n", "unknown construction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Analyze(context.Background(), tc.source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
			requireFailure(t, out, err, tc.substr)
			if got := out.Usage.TotalAdmitted; got != 0 {
				t.Fatalf("structural diagnostics must precede provider calls, got %d requests", got)
			}
		})
	}
}

func TestSemanticScopeDoesNotLeakFromActionsAndBranches(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")

	// A collection bound inside an action is not visible afterwards.
	source := "read \"tickets.json\" as json called tickets\nto prepare with src:\n  read \"events.json\" as json called events\nsort them by created\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, out, err)
	if !strings.Contains(out.Canonical, "sort tickets by created ascending") {
		t.Fatalf("action-local collection must not be a referent, got %q", out.Canonical)
	}

	// Nothing visible means a precise diagnostic, never a guess.
	source = "to prepare with src:\n  read \"events.json\" as json called events\nsort them by created\n"
	out, err = Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireFailure(t, out, err, "no visible collection")

	// A collection bound inside a branch does not leak to the following line.
	source = "read \"tickets.json\" as json called tickets\nwhen tickets is empty:\n  read \"events.json\" as json called events\ngroup them by team called teams\n"
	out, err = Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, out, err)
	if !strings.Contains(out.Canonical, "group tickets by team called teams") {
		t.Fatalf("branch-local collection must not be a referent, got %q", out.Canonical)
	}

	// Inner statements still see outer collections.
	source = "read \"tickets.json\" as json called tickets\nfor each ticket in tickets:\n  keep them where owner is \"me\"\n"
	out, err = Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, out, err)
	if !strings.Contains(out.Canonical, "  keep tickets where owner is \"me\"") {
		t.Fatalf("inner reference must see the outer collection, got %q", out.Canonical)
	}
	if got := out.Usage.TotalAdmitted; got != 0 {
		t.Fatalf("inner single referent must resolve without calls, got %d", got)
	}
}

func TestSemanticCriterionDeclarationAndApplication(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "read \"tickets.json\" as json called tickets\ncriterion urgent:\n  ask \"The customer is blocked from doing their work\"\n  using message, status\n  accept probability at least 0.85\n  on uncertain discard\nkeep the urgent ones\nshow tickets\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("semantic", "semantic")})
	requireSuccess(t, out, err)
	want := "read \"tickets.json\" as json called tickets\n\n\n\n\n\nkeep tickets where jev:\n  ask \"The customer is blocked from doing their work\"\n  using message, status\n  accept probability at least 0.85\n  on uncertain discard\nshow tickets\n"
	if out.Canonical != want {
		t.Fatalf("canonical:\nwant %q\ngot  %q", want, out.Canonical)
	}
	wantMap := []int{1, 2, 3, 4, 5, 6, 7, 7, 7, 7, 7, 8}
	if fmt.Sprint(out.SourceMap) != fmt.Sprint(wantMap) {
		t.Fatalf("source map %+v, want %+v", out.SourceMap, wantMap)
	}
	if len(out.Decisions) != 2 {
		t.Fatalf("want criterion declaration and application decisions, got %+v", out.Decisions)
	}
	decl, app := out.Decisions[0], out.Decisions[1]
	if decl.Line != 2 || decl.Method != "criterion" || decl.Candidate != "criterion:urgent" || decl.Confidence != 1 {
		t.Fatalf("declaration decision %+v", decl)
	}
	if app.Line != 7 || app.Method != "deterministic" || app.Candidate != "keep-criterion:tickets" || app.Confidence != 1 {
	}
	if !strings.Contains(app.Explanation, "criterion") {
		t.Fatalf("application explanation %q", app.Explanation)
	}
	if got := out.Usage.TotalAdmitted; got != 0 {
		t.Fatalf("explicit-name criterion application must not call the provider, got %d", got)
	}

	// Explicit-collection form lowers identically.
	source2 := strings.Replace(source, "keep the urgent ones", "keep urgent tickets", 1)
	out2, err := Analyze(context.Background(), source2, AnalyzeOptions{Config: semanticConfig("semantic", "semantic")})
	requireSuccess(t, out2, err)
	if out2.Canonical != want {
		t.Fatalf("explicit-collection form canonical:\nwant %q\ngot  %q", want, out2.Canonical)
	}
}

func TestSemanticCriterionPolicyGates(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	decl := "criterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 0.9\n  on uncertain discard\n"
	cases := []struct {
		name           string
		interpretation string
		runtime        string
		source         string
		substr         string
	}{
		{"assisted interpretation", "assisted", "semantic", decl, "require interpretation mode semantic"},
		{"canonical interpretation", "canonical", "semantic", decl, "require interpretation mode semantic"},
		{"explicit runtime", "semantic", "explicit", decl + "read \"t.json\" as json called tickets\nkeep the urgent ones\n", "runtime judgment mode"},
		{"deny runtime", "semantic", "deny", decl + "read \"t.json\" as json called tickets\nkeep the urgent ones\n", "runtime judgment mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Analyze(context.Background(), tc.source, AnalyzeOptions{Config: semanticConfig(tc.interpretation, tc.runtime)})
			requireFailure(t, out, err, tc.substr)
			if got := out.Usage.TotalAdmitted; got != 0 {
				t.Fatalf("policy denial must not call the provider, got %d", got)
			}
		})
	}
}

func TestSemanticCriterionValidation(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	cases := []struct {
		name   string
		source string
		substr string
	}{
		{"missing ask", "criterion urgent:\n  accept probability at least 0.9\n  on uncertain discard\n", "requires ask"},
		{"missing accept", "criterion urgent:\n  ask \"Urgent?\"\n  on uncertain discard\n", "requires accept"},
		{"missing uncertainty", "criterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 0.9\n", "requires on uncertain"},
		{"threshold too low", "criterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 0.5\n  on uncertain discard\n", "greater than 0.5"},
		{"interpolated ask", "criterion urgent:\n  ask \"Is {name} urgent?\"\n  accept probability at least 0.9\n  on uncertain discard\n", "literal question"},
		{"wrong handler", "criterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 0.9\n  on uncertain discard\n  on failure stop\n", "only on uncertain"},
		{"undeclared adjective", "read \"t.json\" as json called tickets\nkeep critical tickets\n", "undeclared criterion"},
		{"duplicate declaration", "criterion urgent:\n  ask \"A?\"\n  accept probability at least 0.9\n  on uncertain discard\ncriterion urgent:\n  ask \"B?\"\n  accept probability at least 0.9\n  on uncertain discard\n", "duplicate criterion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Analyze(context.Background(), tc.source, AnalyzeOptions{Config: semanticConfig("semantic", "semantic")})
			requireFailure(t, out, err, tc.substr)
			if got := out.Usage.TotalAdmitted; got != 0 {
				t.Fatalf("criterion validation must not call the provider, got %d", got)
			}
		})
	}
	// Threshold 1 is the valid upper bound.
	ok := "criterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 1\n  on uncertain keep\nread \"t.json\" as json called tickets\nkeep urgent tickets\n"
	out, err := Analyze(context.Background(), ok, AnalyzeOptions{Config: semanticConfig("semantic", "semantic")})
	requireSuccess(t, out, err)
	if !strings.Contains(out.Canonical, "accept probability at least 1") || !strings.Contains(out.Canonical, "on uncertain keep") {
		t.Fatalf("threshold one lowering: %q", out.Canonical)
	}
}

func TestSemanticCheckerAuthorityRemapsToOriginalLines(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "read \"tickets.json\" as json called tickets\ncriterion urgent:\n  ask \"Urgent?\"\n  accept probability at least 0.9\n  on uncertain discard\nkeep the urgent ones\norder tick by created\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("semantic", "semantic")})
	requireFailure(t, out, err, "unknown name tick")
	// After the blanked declaration (lines 2-5) and the expanded application
	// (line 6 becomes four canonical lines), `order tick by created` sits at
	// canonical line 10; it must map back to original line 7.
	found := false
	for _, d := range out.Diagnostics {
		if strings.Contains(d.Message, "tick") {
			found = true
			if d.Line != 7 {
				t.Fatalf("remapped diagnostic line %d, want 7 (original order line)", d.Line)
			}
		}
	}
	if !found {
		t.Fatalf("want unknown-name diagnostic, got %+v", out.Diagnostics)
	}
}

func TestSemanticSavedReplayDeterministicWithoutProvider(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "read \"tickets.json\" as json called tickets\nretain tickets where status is \"open\"\nshow tickets\n"
	opts := AnalyzeOptions{Config: semanticConfig("assisted", "explicit")}
	saved, err := Analyze(context.Background(), source, opts)
	requireSuccess(t, saved, err)

	replayed, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Saved: saved})
	requireSuccess(t, replayed, err)
	if replayed.Canonical != saved.Canonical {
		t.Fatalf("replay canonical %q, want %q", replayed.Canonical, saved.Canonical)
	}
	if fmt.Sprint(replayed.Decisions) != fmt.Sprint(saved.Decisions) {
		t.Fatalf("replay decisions %+v, want %+v", replayed.Decisions, saved.Decisions)
	}
	if replayed.SourceHash != saved.SourceHash || replayed.PolicyHash != saved.PolicyHash {
		t.Fatal("replay must preserve identity hashes")
	}
	if got := replayed.Usage.TotalAdmitted; got != 0 {
		t.Fatalf("replay made %d provider requests", got)
	}
	if replayed.Model != saved.Model {
		t.Fatalf("replay model %q, want saved %q", replayed.Model, saved.Model)
	}
}

func TestSemanticSavedReplayJevDecisionWithoutProvider(t *testing.T) {
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		return "keep-where:events", 0.93
	})
	semanticTestEnv(t, srv.URL)
	source := "read \"tickets.json\" as json called tickets\nread \"events.json\" as json called events\nkeep them where status is \"open\"\n"
	opts := AnalyzeOptions{Config: semanticConfig("assisted", "explicit")}
	saved, err := Analyze(context.Background(), source, opts)
	requireSuccess(t, saved, err)
	if len(saved.Decisions) != 1 || saved.Decisions[0].Method != "jev" {
		t.Fatalf("saved decisions %+v", saved.Decisions)
	}

	// Any provider call now fails: the closed base URL proves replay is offline.
	semanticTestEnv(t, "http://127.0.0.1:1")
	replayed, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Saved: saved})
	requireSuccess(t, replayed, err)
	if !strings.Contains(replayed.Canonical, "keep events where status is \"open\"") {
		t.Fatalf("replayed canonical %q", replayed.Canonical)
	}
	if got := replayed.Usage.TotalAdmitted; got != 0 {
		t.Fatalf("replay made %d provider requests", got)
	}
	if replayed.Decisions[0].Candidate != "keep-where:events" {
		t.Fatalf("replayed decision %+v", replayed.Decisions[0])
	}
}

func TestSemanticSavedModelPreferenceAndBudgetChangeReuse(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "read \"tickets.json\" as json called tickets\nretain tickets where status is \"open\"\n"
	opts := AnalyzeOptions{Config: semanticConfig("assisted", "explicit")}
	saved, err := Analyze(context.Background(), source, opts)
	requireSuccess(t, saved, err)
	if saved.Model != "jev-test" {
		t.Fatalf("saved model %q", saved.Model)
	}

	// Ambient default drift must not invalidate or change a saved resolution.
	t.Setenv("TYPESAFE_DEFAULT_MODEL", "jev-env2")
	replayed, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Saved: saved})
	requireSuccess(t, replayed, err)
	if replayed.Model != "jev-test" {
		t.Fatalf("empty requested model must prefer saved model, got %q", replayed.Model)
	}

	// Budget changes are outside the saved identity and allow reuse.
	budget, budgetErr := NewRequestBudget(3, map[BudgetBucket]int{BudgetInterpretation: 1})
	if budgetErr != nil {
		t.Fatal(budgetErr)
	}
	replayed, err = Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Budget: budget, Saved: saved})
	requireSuccess(t, replayed, err)
	if budget.Snapshot().TotalAdmitted != 0 {
		t.Fatal("reuse must not charge the new budget")
	}
}

func TestSemanticSavedLockedRejectsMismatchesBeforeProvider(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "read \"tickets.json\" as json called tickets\nretain tickets where status is \"open\"\n"
	saved, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, saved, err)

	// Locked with no saved analysis at all.
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Locked: true})
	requireFailure(t, out, err, "locked analysis requires a saved resolution")

	// Changed source.
	changed := source + "show tickets\n"
	out, err = Analyze(context.Background(), changed, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Saved: saved, Locked: true})
	requireFailure(t, out, err, "source changed")
	if out.Usage.TotalAdmitted != 0 {
		t.Fatal("locked mismatch must reject before any provider request")
	}

	// Changed semantic policy (runtime mode participates in identity).
	out, err = Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "deny"), Saved: saved, Locked: true})
	requireFailure(t, out, err, "semantic policy changed")

	// Explicit different model.
	out, err = Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Model: "other-model", Saved: saved, Locked: true})
	requireFailure(t, out, err, "does not match saved model")

	// Unlocked mismatches re-resolve instead of failing.
	out, err = Analyze(context.Background(), changed, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Saved: saved})
	requireSuccess(t, out, err)
	if out.Canonical == saved.Canonical {
		t.Fatal("changed source must resolve to a different canonical program")
	}
}

func TestSemanticSavedTamperDetection(t *testing.T) {
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		return "keep-where:tickets", 0.95
	})
	semanticTestEnv(t, srv.URL)
	source := "read \"tickets.json\" as json called tickets\nfilter tickets where status is \"open\"\n"
	saved, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit")})
	requireSuccess(t, saved, err)
	if len(saved.Decisions) != 1 || saved.Decisions[0].Method != "jev" {
		t.Fatalf("saved decisions %+v", saved.Decisions)
	}
	// Any provider call now fails: the closed base URL proves rejection is offline.
	semanticTestEnv(t, "http://127.0.0.1:1")

	tamper := func(name string, mutate func(*Analysis)) {
		t.Run(name, func(t *testing.T) {
			tampered := *saved
			decisions := make([]Interpretation, len(saved.Decisions))
			copy(decisions, saved.Decisions)
			sourceMap := make([]int, len(saved.SourceMap))
			copy(sourceMap, saved.SourceMap)
			tampered.Decisions = decisions
			tampered.SourceMap = sourceMap
			mutate(&tampered)
			out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Saved: &tampered, Locked: true})
			requireFailure(t, out, err, "cannot reuse saved resolution")
			if out.Usage.TotalAdmitted != 0 {
				t.Fatal("tampered saved analysis must be rejected without provider requests")
			}
		})
	}
	tamper("edited canonical", func(a *Analysis) { a.Canonical += "show tickets\n" })
	tamper("unknown candidate id", func(a *Analysis) { a.Decisions[0].Candidate = "keep-where:elsewhere" })
	tamper("candidate canonical mismatch", func(a *Analysis) { a.Decisions[0].Canonical = "show \"injected\"" })
	tamper("duplicate decision", func(a *Analysis) { a.Decisions = append(a.Decisions, a.Decisions[0]) })
	tamper("unused decision", func(a *Analysis) {
		a.Decisions = append(a.Decisions, Interpretation{Line: 1, Source: "read", Candidate: "read-json", Method: "deterministic", Confidence: 1})
	})
	tamper("confidence above one", func(a *Analysis) { a.Decisions[0].Confidence = 1.5 })
	tamper("confidence below policy minimum", func(a *Analysis) { a.Decisions[0].Confidence = 0.5 })
	tamper("method swapped", func(a *Analysis) { a.Decisions[0].Method = "deterministic" })
	tamper("source map truncated", func(a *Analysis) { a.SourceMap = a.SourceMap[:1] })
	tamper("source map entry out of range", func(a *Analysis) { a.SourceMap[0] = 99 })
	tamper("wrong version", func(a *Analysis) { a.Version = semanticAnalysisVersion + 1 })
	tamper("saved diagnostics present", func(a *Analysis) { a.Diagnostics = []Diagnostic{{1, 1, "stale"}} })
	tamper("missing decision", func(a *Analysis) { a.Decisions = nil })
}

func TestSemanticBudgetExhaustionDiagnostic(t *testing.T) {
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		return "keep-where:tickets", 0.9
	})
	semanticTestEnv(t, srv.URL)
	budget, err := NewRequestBudget(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := "read \"tickets.json\" as json called tickets\nfilter tickets where status is \"open\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Budget: budget})
	requireFailure(t, out, err, "request budget is exhausted")
	var budgetErr *BudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error must wrap *BudgetError for transport classification, got %T: %v", err, err)
	}
}

func TestSemanticBucketSelectionChargesEditor(t *testing.T) {
	srv := semanticChoiceServer(t, func(req fixtureReq) (string, float64) {
		return "keep-where:tickets", 0.9
	})
	semanticTestEnv(t, srv.URL)
	budget, _ := NewRequestBudget(5, nil)
	source := "read \"tickets.json\" as json called tickets\nfilter tickets where status is \"open\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("assisted", "explicit"), Budget: budget, Bucket: BudgetEditor})
	requireSuccess(t, out, err)
	if got := out.Usage.Buckets[BudgetEditor].Requests; got != 1 {
		t.Fatalf("editor bucket requests %d, want 1", got)
	}
	if got := out.Usage.Buckets[BudgetInterpretation].Requests; got != 0 {
		t.Fatalf("interpretation bucket requests %d, want 0", got)
	}
}

func TestSemanticInvalidOptionsRejected(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	out, err := Analyze(context.Background(), "show a\n", AnalyzeOptions{Config: sosconfig.Effective{Interpretation: "wild"}})
	requireFailure(t, out, err, "not canonical, assisted, or semantic")

	out, err = Analyze(context.Background(), "show a\n", AnalyzeOptions{Config: sosconfig.Effective{Runtime: "loose"}})
	requireFailure(t, out, err, "not deny, explicit, or semantic")

	budget, _ := NewRequestBudget(5, nil)
	out, err = Analyze(context.Background(), "show a\n", AnalyzeOptions{Config: semanticConfig("canonical", "explicit"), Budget: budget, Bucket: "nope"})
	requireFailure(t, out, err, "unknown budget bucket")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err = Analyze(ctx, "read \"a.json\" as json called a\n", AnalyzeOptions{Config: semanticConfig("canonical", "explicit")})
	requireFailure(t, out, err, "canceled")

	out, err = Analyze(context.Background(), "+++\nversion = 1\n+++\nshow a\n???\n", AnalyzeOptions{Config: semanticConfig("canonical", "explicit")})
	requireFailure(t, out, err, "unknown construction")
}

func TestSemanticFrontmatterErrorDiagnostic(t *testing.T) {
	semanticTestEnv(t, "http://127.0.0.1:1")
	source := "+++\nversion = 1\nnope = 3\n+++\nshow a\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("canonical", "explicit")})
	requireFailure(t, out, err, "unknown configuration key")
}

func TestSemanticJSONFieldNames(t *testing.T) {
	blob, err := json.Marshal(Analysis{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "registry_version", "source_hash", "policy_hash", "model", "canonical", "source_map", "decisions", "diagnostics", "usage"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("Analysis JSON missing key %q in %s", key, blob)
		}
	}
	blob, err = json.Marshal(Interpretation{})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"line", "source", "canonical", "candidate", "confidence", "method", "explanation"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("Interpretation JSON missing key %q in %s", key, blob)
		}
	}
	blob, err = json.Marshal(AnalyzeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"config", "budget", "model", "bucket", "saved", "locked"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("AnalyzeOptions JSON missing key %q in %s", key, blob)
		}
	}
}
