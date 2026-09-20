package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

type charge struct {
	ID        string `json:"id"`
	AmountUSD int    `json:"amount_usd"`
	DaysAgo   int    `json:"days_ago"`
}

// Scenario 2: JSON state with several parts; questions reference fields by backticked path.
// The model judges eligibility per charge; code owns the refund decision.
func structuredState(ctx context.Context, c *typesafe.Client) error {
	charges := []charge{{"ch_1", 49, 3}, {"ch_2", 49, 33}}
	state := map[string]any{
		"customer": map[string]any{"id": "c_812", "plan": "pro", "tenure_months": 14},
		"policy": map[string]any{
			"refund_window_days": 30,
			"text":               "Refunds are available within 30 days of a charge. Charges older than 30 days are not refundable.",
		},
		"charges": charges,
		"messages": []map[string]string{
			{"role": "customer", "text": "You charged me twice this month and I only use one seat. I want both charges reversed today."},
		},
	}

	levels := []any{"Calm, just stating facts", "Frustrated but civil", "Very angry, strong language"}
	res, err := c.SystemOne(ctx, typesafe.Request{
		State: state,
		Questions: typesafe.Questions{
			"refund_requested": typesafe.Noul("Does `messages[0].text` ask for money back?"),
			"request_type": typesafe.Choice("What does the customer in `messages[0].text` want?", map[string]any{
				"refund":      "Money returned for a past charge",
				"cancel":      "Stop future billing",
				"information": "An explanation, no money movement",
				"other":       "None of the above",
			}),
			"ch1_eligible": typesafe.Noul("Under `policy.text`, is charge `charges[0]` eligible for a refund?"),
			"ch2_eligible": typesafe.Noul("Under `policy.text`, is charge `charges[1]` eligible for a refund?"),
			"frustration":  typesafe.Score("How frustrated is the customer in `messages[0].text`?", levels...),
		},
	})
	if err != nil {
		return err
	}

	fmt.Printf("  tokens in/out=%d/%d\n", res.Usage.InputTokens, res.Usage.OutputTokens)
	for _, id := range []string{"refund_requested", "request_type", "ch1_eligible", "ch2_eligible", "frustration"} {
		fmt.Printf("  %s: %s\n", id, res.Answers[id])
	}

	if res.Answers.Noul("refund_requested") > 0.7 {
		var refund []string
		for i, ch := range charges {
			if res.Answers.Noul(fmt.Sprintf("ch%d_eligible", i+1)) > 0.7 {
				refund = append(refund, ch.ID)
			}
		}
		if len(refund) == 0 {
			fmt.Println("  decision: refund nothing")
		} else {
			fmt.Printf("  decision: refund %s\n", strings.Join(refund, ", "))
		}
	}
	return nil
}
