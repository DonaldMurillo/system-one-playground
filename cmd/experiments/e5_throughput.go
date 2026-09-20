package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 5: throughput from Go. A worker pool at rising concurrency,
// latency percentiles, tokens per second, and whether the retry path fires.
// Published limits: 250k tokens/s and 1,200 requests/min.
func throughput(ctx context.Context, _ *typesafe.Client) error {
	c, err := typesafe.New(typesafe.WithMaxRetries(3), typesafe.WithTimeout(30*time.Second))
	if err != nil {
		return err
	}
	items := []string{"headphones", "jacket", "blender", "desk lamp", "sneakers", "monitor"}
	conditions := []string{"damaged", "on time", "two weeks late", "in the wrong color", "perfect", "missing a part"}
	moods := []string{"furious", "happy", "confused", "disappointed", "grateful", "indifferent"}
	qs := typesafe.Questions{
		"refund":     typesafe.Noul("Does the customer likely want a refund?"),
		"sentiment":  typesafe.Choice("Overall sentiment?", typesafe.Options("positive", "neutral", "negative")),
		"escalation": typesafe.Score("How urgently should a human look at this?", "No rush", "This week", "Today", "Right now"),
	}

	fmt.Printf("  %-5s %-5s %8s %8s %8s %8s %7s %8s %6s %8s\n", "conc", "reqs", "p50", "p95", "p99", "max", "req/s", "tok/s", "errs", "retries")
	for _, lvl := range []struct{ conc, reqs int }{{8, 200}, {32, 200}, {96, 200}, {128, 400}} {
		reqs := make([]int, lvl.reqs)
		for i := range reqs {
			reqs[i] = i
		}
		before := c.Stats()
		start := time.Now()
		type out struct {
			d    time.Duration
			toks int
		}
		res, errs := parallel(ctx, reqs, lvl.conc, func(ctx context.Context, i int, _ int) (out, error) {
			state := fmt.Sprintf("Order %d: customer says the %s arrived %s and they sound %s.", i, items[i%6], conditions[(i/6)%6], moods[(i/36)%6])
			t0 := time.Now()
			r, err := c.SystemOne(ctx, typesafe.Request{State: state, Questions: qs})
			if err != nil {
				return out{d: time.Since(t0)}, err
			}
			return out{d: time.Since(t0), toks: r.Usage.InputTokens}, nil
		})
		wall := time.Since(start)
		after := c.Stats()
		var lat []time.Duration
		toks, nerr := 0, 0
		statusSeen := map[string]int{}
		for i, o := range res {
			lat = append(lat, o.d)
			toks += o.toks
			if errs[i] != nil {
				nerr++
				var ae *typesafe.APIError
				if errors.As(errs[i], &ae) {
					statusSeen[fmt.Sprintf("http %d", ae.Status)]++
				} else {
					statusSeen[fmt.Sprintf("%T", errs[i])]++
				}
			}
		}
		fmt.Printf("  %-5d %-5d %8s %8s %8s %8s %7.1f %8.0f %6d %8d\n", lvl.conc, lvl.reqs,
			percentile(lat, 50).Round(time.Millisecond), percentile(lat, 95).Round(time.Millisecond),
			percentile(lat, 99).Round(time.Millisecond), percentile(lat, 100).Round(time.Millisecond),
			float64(lvl.reqs)/wall.Seconds(), float64(toks)/wall.Seconds(), nerr, after.Retries-before.Retries)
		if len(statusSeen) > 0 {
			fmt.Printf("        errors: %v\n", statusSeen)
		}
		codes := map[int]uint64{}
		for k, v := range after.Statuses {
			if d := v - before.Statuses[k]; d > 0 && k != 200 {
				codes[k] = d
			}
		}
		if len(codes) > 0 {
			fmt.Printf("        non-200 statuses this level: %v\n", codes)
		}
	}
	st := c.Stats()
	fmt.Printf("  total attempts %d, retries %d, conn errors %d, statuses %v\n", st.Attempts, st.Retries, st.ConnErrs, st.Statuses)
	return nil
}
