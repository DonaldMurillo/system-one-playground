package sos

import (
	"context"
	"github.com/DonaldMurillo/system-one-playground/typesafe"
	"strings"
	"testing"
)

func TestGatewayRejectsOversizedPayloadBeforeAdmission(t *testing.T) {
	b, err := NewRequestBudget(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := typesafe.Choice("choose", map[string]any{"ok": "valid", "reject": "invalid"})
	_, err = Evaluate(context.Background(), EvaluationRequest{State: map[string]any{"data": strings.Repeat("x", providerMaxPayloadBytes)}, Question: q, Budget: b, Bucket: BudgetRuntime})
	if err == nil || !strings.Contains(err.Error(), "payload exceeds") {
		t.Fatalf("oversized payload error = %v", err)
	}
	if b.Snapshot().TotalAdmitted != 0 {
		t.Fatal("oversized payload consumed budget")
	}
}

func TestGatewayRejectsMalformedCriteriaBeforeAdmission(t *testing.T) {
	for _, q := range []typesafe.Question{
		{Type: "choice"}, {Type: "choice", Criteria: []any{"a"}},
		{Type: "score"}, {Type: "score", Criteria: map[string]any{"a": "b"}},
	} {
		b, err := NewRequestBudget(1, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Evaluate(context.Background(), EvaluationRequest{Question: q, Budget: b, Bucket: BudgetInterpretation})
		if err == nil {
			t.Fatalf("accepted malformed question %+v", q)
		}
		if b.Snapshot().TotalAdmitted != 0 {
			t.Fatal("malformed question consumed budget")
		}
	}
}
