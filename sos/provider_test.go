package sos

import (
	"context"
	"testing"
	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

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
