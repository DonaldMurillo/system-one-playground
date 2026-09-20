package main

import (
	"context"
	"fmt"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Scenario 4: speculative fan-out. Ask every question the code might need in one
// request, including branch-specific ones, then compare against one call per question.
func fanOut(ctx context.Context, c *typesafe.Client) error {
	ticket := map[string]string{
		"subject": "App crashes when exporting PDF",
		"body": "Every time I click Export > PDF on a report longer than 10 pages the app freezes and I have to force quit. " +
			"Started after yesterday's update (v4.2.1). MacBook Pro M3, macOS 15.3. Happens 100% of the time. " +
			"I have a board meeting Thursday and need these reports.",
	}

	questions := typesafe.Questions{
		"kind": typesafe.Choice("What kind of ticket is this?", map[string]any{
			"bug":      "Something is broken",
			"feature":  "A request for new behavior",
			"question": "How-to or account question",
			"other":    nil,
		}),
		// Speculative: only read when kind == bug.
		"bug_severity": typesafe.Score("Assuming this reports a bug, how severe is it?",
			"Cosmetic, no functional impact",
			"Functional issue with a workaround",
			"Core feature unusable, no workaround",
			"Data loss or security exposure",
		),
		"bug_reproducible": typesafe.Noul("Assuming this reports a bug, does the report describe steps that reproduce it?"),
		// Speculative: only read when kind == feature.
		"feature_scope": typesafe.Score("Assuming this is a feature request, how large is the change?",
			"Small tweak to existing behavior",
			"New capability within an existing area",
			"New product area",
		),
		// Always useful.
		"has_deadline":    typesafe.Noul("Does the customer mention a deadline or time pressure?"),
		"has_env_details": typesafe.Noul("Does the message include OS, device, or version details?"),
		"frustration": typesafe.Score("How frustrated is the customer?",
			"Calm, just stating facts", "Frustrated but civil", "Very angry, strong language"),
	}
	order := []string{"kind", "bug_severity", "bug_reproducible", "feature_scope", "has_deadline", "has_env_details", "frustration"}

	start := time.Now()
	batched, err := c.SystemOne(ctx, typesafe.Request{State: ticket, Questions: questions})
	if err != nil {
		return err
	}
	batchedMS := time.Since(start)
	fmt.Printf("  batched: %s, tokens in=%d\n", batchedMS.Round(time.Millisecond), batched.Usage.InputTokens)
	for _, id := range order {
		fmt.Printf("  %s: %s\n", id, batched.Answers[id])
	}

	switch batched.Answers.Choice("kind").Choice {
	case "bug":
		fmt.Printf("  consumed branch: bug severity=%.2f reproducible=%.2f (feature_scope ignored)\n",
			batched.Answers.Score("bug_severity").Score, batched.Answers.Noul("bug_reproducible"))
	case "feature":
		fmt.Printf("  consumed branch: feature scope=%.2f (bug_* ignored)\n", batched.Answers.Score("feature_scope").Score)
	}

	start = time.Now()
	sepTokens := 0
	for _, id := range order {
		r, err := c.SystemOne(ctx, typesafe.Request{State: ticket, Questions: typesafe.Questions{id: questions[id]}})
		if err != nil {
			return err
		}
		sepTokens += r.Usage.InputTokens
	}
	sepMS := time.Since(start)
	fmt.Printf("  separate (%d calls): %s, tokens in=%d\n", len(order), sepMS.Round(time.Millisecond), sepTokens)
	fmt.Printf("  batching saved %.1fx tokens and %.1fx wall time\n",
		float64(sepTokens)/float64(batched.Usage.InputTokens), float64(sepMS)/float64(batchedMS))
	return nil
}
