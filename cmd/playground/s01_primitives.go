package main

import (
	"context"
	"fmt"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Scenario 1: the three primitives on a plain-text state, mirroring the quickstart.
func primitives(ctx context.Context, c *typesafe.Client) error {
	ticket := "Hi, I've been trying to connect my Stripe account for 3 days and it keeps failing. I'm losing sales. Please help ASAP."

	res, err := c.SystemOne(ctx, typesafe.Request{
		State: ticket,
		Questions: typesafe.Questions{
			"department": typesafe.Choice("Which team should handle this", map[string]any{
				"billing":   "Payment or subscription issues",
				"technical": "Bugs or integration problems",
				"sales":     "Pricing or account questions",
			}),
			"frustration": typesafe.Score("How frustrated the customer appears",
				"Calm, just stating facts",
				"Frustrated but civil",
				"Very angry, strong language",
			),
			"is_urgent":    typesafe.Noul("The message conveys urgency or time-sensitivity"),
			"wants_refund": typesafe.Noul("The customer is asking for money back"),
		},
	})
	if err != nil {
		return err
	}

	fmt.Printf("  model=%s request=%s tokens in/out=%d/%d\n", res.Model, res.RequestID, res.Usage.InputTokens, res.Usage.OutputTokens)
	for _, id := range []string{"department", "frustration", "is_urgent", "wants_refund"} {
		fmt.Printf("  %s: %s\n", id, res.Answers[id])
	}

	// Typed accessors: code branches on the primitive directly.
	dept := res.Answers.Choice("department")
	fmt.Printf("  dispatch -> %s queue (p=%s)\n", dept.Choice, pct(dept.Probabilities[dept.Choice]))
	if res.Answers.Noul("is_urgent") > 0.8 {
		fmt.Println("  flag: urgent")
	}
	return nil
}
