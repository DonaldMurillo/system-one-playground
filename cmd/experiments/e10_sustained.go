package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 10: sustained load past the documented limits (1,200 requests/min,
// 250k tokens/s). Phase A hammers small requests for a minute to probe the
// request limit; phase B sends ~2k-token states to probe the token limit.
// Reports per-10-second buckets, 429s, retries and the largest Retry-After.
func sustained(ctx context.Context, _ *typesafe.Client) error {
	c, err := typesafe.New(typesafe.WithMaxRetries(3), typesafe.WithTimeout(30*time.Second))
	if err != nil {
		return err
	}
	qs := typesafe.Questions{
		"refund":    typesafe.Noul("Does the customer likely want a refund?"),
		"sentiment": typesafe.Choice("Overall sentiment?", typesafe.Options("positive", "neutral", "negative")),
	}
	bigStates := make([]string, 16)
	for i := range bigStates {
		bigStates[i] = filler(uint64(100+i), 3000) // ~2.1k real tokens (estimate runs ~40% high)
	}
	phases := []struct {
		name  string
		dur   time.Duration
		conc  int
		state func(i int) any
	}{
		{"A: small requests, 48 workers, 60s", 60 * time.Second, 48, func(i int) any {
			return fmt.Sprintf("Order %d: the parcel arrived %s and the customer sounds %s.", i,
				[]string{"late", "damaged", "on time", "opened"}[i%4], []string{"angry", "fine", "puzzled"}[i%3])
		}},
		{"B: ~2k-token states, 64 workers, 30s", 30 * time.Second, 64, func(i int) any {
			return map[string]string{"notes": bigStates[i%len(bigStates)], "message": fmt.Sprintf("Order %d arrived damaged.", i)}
		}},
	}

	type sample struct {
		at   time.Duration
		d    time.Duration
		toks int
		err  error
	}
	for _, ph := range phases {
		fmt.Printf("  phase %s\n", ph.name)
		before := c.Stats()
		var mu sync.Mutex
		var samples []sample
		var counter atomic.Int64
		pctx, cancel := context.WithTimeout(ctx, ph.dur)
		start := time.Now()
		var wg sync.WaitGroup
		for w := 0; w < ph.conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for pctx.Err() == nil {
					i := int(counter.Add(1))
					t0 := time.Now()
					r, err := c.SystemOne(pctx, typesafe.Request{State: ph.state(i), Questions: qs})
					if err != nil && pctx.Err() != nil {
						return // cut off by the phase deadline, not a real failure
					}
					s := sample{at: t0.Sub(start), d: time.Since(t0), err: err}
					if r != nil {
						s.toks = r.Usage.InputTokens
					}
					mu.Lock()
					samples = append(samples, s)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		cancel()
		after := c.Stats()

		sort.Slice(samples, func(i, j int) bool { return samples[i].at < samples[j].at })
		fmt.Printf("    %-7s %6s %6s %8s %8s %9s %9s  %s\n", "window", "ok", "errs", "req/s", "p50", "p99", "tok/s", "error kinds")
		bucket := 10 * time.Second
		for b := time.Duration(0); b < ph.dur; b += bucket {
			var lat []time.Duration
			ok, nerr, toks := 0, 0, 0
			kinds := map[string]int{}
			for _, s := range samples {
				if s.at < b || s.at >= b+bucket {
					continue
				}
				lat = append(lat, s.d)
				if s.err == nil {
					ok++
					toks += s.toks
				} else {
					nerr++
					var ae *typesafe.APIError
					if errors.As(s.err, &ae) {
						kinds[fmt.Sprintf("http %d", ae.Status)]++
					} else {
						kinds[fmt.Sprintf("%T", s.err)]++
					}
				}
			}
			if ok+nerr == 0 {
				continue
			}
			kindStr := ""
			if len(kinds) > 0 {
				kindStr = fmt.Sprint(kinds)
			}
			fmt.Printf("    %3.0f-%-3.0fs %6d %6d %8.1f %8s %9s %9.0f  %s\n", b.Seconds(), (b + bucket).Seconds(), ok, nerr,
				float64(ok+nerr)/bucket.Seconds(), percentile(lat, 50).Round(time.Millisecond), percentile(lat, 99).Round(time.Millisecond),
				float64(toks)/bucket.Seconds(), kindStr)
		}
		codes := map[int]uint64{}
		for k, v := range after.Statuses {
			if d := v - before.Statuses[k]; d > 0 {
				codes[k] = d
			}
		}
		fmt.Printf("    totals: %d requests, %d http attempts, %d retries, statuses %v, max Retry-After %s\n",
			len(samples), after.Attempts-before.Attempts, after.Retries-before.Retries, codes, after.RetryAfterMax)
	}
	return nil
}
