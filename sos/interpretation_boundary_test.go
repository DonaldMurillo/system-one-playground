package sos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

func boundaryPolicy(t *testing.T, mode, runtime string, calls int) sosconfig.Effective {
	t.Helper()
	c := sosconfig.Config{Version: 1}
	c.Interpretation.Mode = mode
	c.Runtime.Judgment = runtime
	c.Budget.Run.Requests = &calls
	p, err := sosconfig.Resolve(sosconfig.Layer{Name: "test", Config: c})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCanonicalPronounNamedBindingStaysLocal(t *testing.T) {
	source := "make them [{\"priority\": 1}]\nmake tickets [{\"priority\": 2}]\nkeep them where priority is 1 # ordinary binding\nshow them\n"
	a, err := Analyze(context.Background(), source, AnalyzeOptions{Config: boundaryPolicy(t, "assisted", "explicit", 0)})
	if err != nil {
		t.Fatal(err)
	}
	if a.Canonical != source || a.Usage.TotalAdmitted != 0 {
		t.Fatalf("ordinary named binding was reinterpreted: %+v", a)
	}
}

func TestAnalyzeCannotEscapeConfiguredSharedBudget(t *testing.T) {
	b, _ := NewRequestBudget(100, nil)
	source := "make tickets [{\"priority\": 1}]\nmake archive [{\"priority\": 2}]\ngroup them by priority called groups\n"
	_, err := Analyze(context.Background(), source, AnalyzeOptions{Config: boundaryPolicy(t, "assisted", "explicit", 0), Budget: b})
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget refusal, got %v", err)
	}
	var budgetErr *BudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("analysis lost typed budget cause: %v", err)
	}
	if b.Snapshot().TotalAdmitted != 0 {
		t.Fatal("configured zero budget admitted an interpretation request")
	}
}

func TestCriterionRejectsNonfiniteThresholdAndNestedStatements(t *testing.T) {
	for _, member := range []string{"  accept probability at least NaN\n", "  accept probability at least 0.85\n    show \"hidden\"\n"} {
		source := "criterion urgent:\n  ask \"Requires immediate action?\"\n" + member + "  on uncertain discard\nmake tickets []\nkeep urgent tickets\n"
		_, err := Analyze(context.Background(), source, AnalyzeOptions{Config: boundaryPolicy(t, "semantic", "semantic", 0)})
		if err == nil {
			t.Fatalf("malformed criterion accepted: %s", member)
		}
	}
}

func TestCanonicalSavedResolutionPreservesOriginalBytes(t *testing.T) {
	for _, source := range []string{
		"# comment\r\nmake total 2\r\nshow total\r\n",
		"\ufeffmake total 2\nshow total\n",
		"+++\r\nversion = 1\r\n+++\r\nmake total 2\r\nshow total\r\n",
	} {
		cfg := boundaryPolicy(t, "canonical", "explicit", 0)
		first, err := Analyze(context.Background(), source, AnalyzeOptions{Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := Analyze(context.Background(), source, AnalyzeOptions{Config: cfg, Saved: first, Locked: true})
		if err != nil {
			t.Fatalf("saved canonical source failed: %v", err)
		}
		if saved.Canonical != source || saved.Usage.TotalAdmitted != 0 {
			t.Fatalf("saved canonical bytes changed: %+v", saved)
		}
	}
}

func TestExistingCanonicalExamplesAnalyzeLocally(t *testing.T) {
	paths, err := filepath.Glob("../examples/sos/*.sos")
	if err != nil || len(paths) == 0 {
		t.Fatalf("example inventory: %v", err)
	}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// These examples intentionally use noncanonical sentence forms.
		if filepath.Base(path) == "team-report.sos" || filepath.Base(path) == "urgent-tickets.sos" || filepath.Base(path) == "jev-workflow.sos" {
			continue
		}
		// Import-using examples exercise LoadProgram (see tests/e2e); the
		// source-only analyzer has no module table by design.
		if strings.Contains(string(src), "import \"") {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg := boundaryPolicy(t, "assisted", "explicit", 0)
			a, err := Analyze(context.Background(), string(src), AnalyzeOptions{Config: cfg})
			if err != nil {
				t.Fatal(err)
			}
			if a.Canonical != string(src) || a.Usage.TotalAdmitted != 0 {
				t.Fatal("canonical example changed or admitted provider call")
			}
			if _, err := Analyze(context.Background(), string(src), AnalyzeOptions{Config: cfg, Saved: a, Locked: true}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSentenceVariantsResolveSingleVisibleCollection(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"order them by priority highest first", "sort tickets by priority descending"},
		{"retain them where priority is 1", "keep tickets where priority is 1"},
		{"collect them by priority called groups", "group tickets by priority called groups"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			source := "make tickets [{\"priority\": 1}]\n" + tc.source + "\n"
			a, err := Analyze(context.Background(), source, AnalyzeOptions{Config: boundaryPolicy(t, "assisted", "explicit", 0)})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(a.Canonical, tc.want) || a.Usage.TotalAdmitted != 0 {
				t.Fatalf("wrong lowering: %+v", a)
			}
		})
	}
}

func TestAnalyzeHonorsConfiguredDeadline(t *testing.T) {
	cfg := boundaryPolicy(t, "assisted", "explicit", 1)
	cfg.Timeout = time.Nanosecond
	a, err := Analyze(context.Background(), "show 1\n", AnalyzeOptions{Config: cfg})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("configured deadline ignored: %v", err)
	}
	if a.Usage.TotalAdmitted != 0 {
		t.Fatal("expired analysis admitted request")
	}
}

func TestUnlockedInvalidSavedResolutionStartsFresh(t *testing.T) {
	cfg := boundaryPolicy(t, "assisted", "explicit", 0)
	source := "make tickets [{\"priority\": 1}]\nretain tickets where priority is 1\n"
	saved, err := Analyze(context.Background(), source, AnalyzeOptions{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	expected := saved.Canonical
	saved.Decisions[0].Candidate = "invalid-candidate"
	fresh, err := Analyze(context.Background(), source, AnalyzeOptions{Config: cfg, Saved: saved})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Canonical != expected || len(fresh.Decisions) != 1 || fresh.Decisions[0].Candidate == "invalid-candidate" {
		t.Fatalf("stale replay state leaked into fresh analysis: %+v", fresh)
	}
}
