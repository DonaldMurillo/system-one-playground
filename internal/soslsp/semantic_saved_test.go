package soslsp

import (
	"context"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestInvalidSavedAnalysisDoesNotFallBackToFreshWork(t *testing.T) {
	source := "show \"ok\"\n"
	saved := &sos.Analysis{Version: 1, Canonical: "show \"stale\"\n"}
	result, err := Analyze(context.Background(), AnalyzeRequest{Source: source, Dir: t.TempDir(), Saved: saved})
	if err == nil {
		t.Fatalf("invalid saved analysis unexpectedly succeeded: %+v", result)
	}
	if result == nil || result.Reused {
		t.Fatalf("result = %+v", result)
	}
}
