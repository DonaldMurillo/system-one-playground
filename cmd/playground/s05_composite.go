package main

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

type scored struct {
	text                                 string
	severity, frustration, actionability float64 // normalized 0..1
}

type weights struct{ severity, frustration, actionability float64 }

func (s scored) priority(w weights) float64 {
	return s.severity*w.severity + s.frustration*w.frustration + s.actionability*w.actionability
}

// Scenario 5: composite scoring. Split "priority" into atomic Scores, combine with
// weights in code, and rerank under a second policy without another inference call.
func composite(ctx context.Context, c *typesafe.Client) error {
	tickets := []string{
		"Login button does nothing on Safari. Tried clearing cache. Not urgent, I use Chrome mostly.",
		"PAYMENTS ARE DOWN. Every checkout returns 500 since 9am. We are losing thousands per hour. Fix it NOW.",
		"Would love a dark mode option someday :)",
		"Export to CSV drops the last row when the table has exactly 1000 rows. Repro: create 1000 rows, export, count. Happens on 3.1.0 and 3.1.1.",
	}

	questions := typesafe.Questions{
		"severity": typesafe.Score("How severe is the problem described?",
			"No problem, or purely cosmetic",
			"Inconvenience with a workaround",
			"Core functionality broken for this user",
			"Outage or data loss affecting many users",
		),
		"frustration": typesafe.Score("How frustrated is the writer?",
			"Calm or positive", "Frustrated but civil", "Very angry, strong language"),
		"actionability": typesafe.Score("How much does the report give an engineer to work with?",
			"Vague, no details",
			"Some context, missing key details",
			"Clear steps or environment details, reproducible",
		),
	}

	// Independent tickets: score them concurrently.
	results := make([]scored, len(tickets))
	errs := make([]error, len(tickets))
	var wg sync.WaitGroup
	for i, t := range tickets {
		wg.Add(1)
		go func(i int, t string) {
			defer wg.Done()
			res, err := c.SystemOne(ctx, typesafe.Request{State: t, Questions: questions})
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = scored{
				text:          t,
				severity:      res.Answers.Score("severity").Normalized(),
				frustration:   res.Answers.Score("frustration").Normalized(),
				actionability: res.Answers.Score("actionability").Normalized(),
			}
		}(i, t)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	for _, s := range results {
		fmt.Printf("  sev=%.2f fru=%.2f act=%.2f  %q\n", s.severity, s.frustration, s.actionability, truncate(s.text, 60))
	}

	policies := []struct {
		name string
		w    weights
	}{
		{"ops on-call (severity first)", weights{0.7, 0.2, 0.1}},
		{"eng triage (actionable first)", weights{0.4, 0.1, 0.5}},
	}
	for _, p := range policies {
		ranked := append([]scored(nil), results...)
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].priority(p.w) > ranked[j].priority(p.w) })
		fmt.Printf("\n  ranking under %q:\n", p.name)
		for i, s := range ranked {
			fmt.Printf("    %d. p=%.2f  %q\n", i+1, s.priority(p.w), truncate(s.text, 50))
		}
	}
	fmt.Println("\n  (second ranking reused the same answers; no extra inference)")
	return nil
}
