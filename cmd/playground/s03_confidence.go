package main

import (
	"context"
	"fmt"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Scenario 3: confidence as a second axis. Same question over clear, ambiguous
// and garbage inputs, then confidence-gated routing with risk-scaled thresholds.
func confidence(ctx context.Context, c *typesafe.Client) error {
	action := typesafe.Choice("What is the user trying to do?", map[string]any{
		"check_balance":    "View account balance",
		"approve_transfer": "Approve the pending withdrawal request",
		"support":          "Get help with an issue",
	})

	inputs := []string{
		"How much money do I have in checking?",
		"Yes go ahead and approve the $2,400 withdrawal to my landlord.",
		"ok do it",
		"The transfer thing... can you check on that? Something looks off with my balance too.",
		"asdf qwerty",
	}

	for _, msg := range inputs {
		res, err := c.SystemOne(ctx, typesafe.Request{State: msg, Questions: typesafe.Questions{"action": action}})
		if err != nil {
			return err
		}
		a := res.Answers.Choice("action")
		fmt.Printf("\n  %q\n  action: %s\n  -> %s\n", msg, a, route(a))
	}
	return nil
}

func route(a typesafe.Answer) string {
	switch {
	case a.Confidence < 0.5:
		return "route_to_human (low confidence)"
	case a.Choice == "check_balance":
		return "show_balance (low stakes)"
	case a.Choice == "approve_transfer" && a.Confidence > 0.9:
		return "confirm_then_execute"
	case a.Choice == "approve_transfer":
		return "ask_user_to_confirm (high stakes, moderate confidence)"
	default:
		return "open_support_ticket"
	}
}
