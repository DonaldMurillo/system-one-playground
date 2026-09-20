package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 6: token budget edges. Where does a request tip over the documented
// 32k state limit and what does the error look like; and does accuracy on a
// needle-in-haystack Choice degrade before the hard limit.
func budget(ctx context.Context, _ *typesafe.Client) error {
	c, err := typesafe.New(typesafe.WithMaxRetries(0), typesafe.WithTimeout(120*time.Second))
	if err != nil {
		return err
	}

	fmt.Println("  (a) hard limit: one Noul over filler state of growing size")
	fmt.Printf("    %-9s %8s %10s %9s  %s\n", "est tok", "chars", "actual", "latency", "outcome")
	for _, est := range []int{8000, 16000, 24000, 30000, 33000, 40000, 60000, 70000} {
		state := filler(uint64(est), est)
		t0 := time.Now()
		r, err := c.SystemOne(ctx, typesafe.Request{
			State:     state,
			Questions: typesafe.Questions{"warehouse": typesafe.Noul("Is this text about warehouse operations?")},
		})
		lat := time.Since(t0).Round(time.Millisecond)
		switch {
		case err == nil:
			fmt.Printf("    %-9d %8d %10d %9s  ok, noul=%.2f\n", est, len(state), r.Usage.InputTokens, lat, r.Answers.Noul("warehouse"))
		default:
			var ae *typesafe.APIError
			if errors.As(err, &ae) {
				fmt.Printf("    %-9d %8d %10s %9s  http %d %s: %s\n", est, len(state), "-", lat, ae.Status, ae.Type, truncate(ae.Message, 90))
			} else {
				fmt.Printf("    %-9d %8d %10s %9s  %v\n", est, len(state), "-", lat, err)
			}
		}
	}

	fmt.Println("  (b) needle in a haystack: one sentence names a locker word; Choice over 6 candidates + 'not stated'")
	words := []string{"harbor", "violet", "quartz", "meadow", "falcon", "ember"}
	opts := typesafe.Options(words...)
	opts["not stated"] = nil
	fmt.Printf("    %-9s %9s %9s %9s  %s\n", "est tok", "accuracy", "mean p", "latency", "per trial (position%:ok)")
	r := rand.New(rand.NewPCG(9, 9))
	const trials = 6
	for _, est := range []int{1000, 4000, 12000, 24000, 30000} {
		type trial struct {
			pos  float64
			word string
		}
		ts := make([]trial, trials)
		for i := range ts {
			ts[i] = trial{pos: r.Float64(), word: words[r.IntN(len(words))]}
		}
		type out struct {
			ok   bool
			p    float64
			d    time.Duration
			note string
		}
		res, errs := parallel(ctx, ts, 3, func(ctx context.Context, i int, t trial) (out, error) {
			paras := strings.Split(strings.TrimSpace(filler(uint64(est*10+i), est)), "\n\n")
			at := int(t.pos * float64(len(paras)))
			needle := fmt.Sprintf("Reminder to staff: the locker combination word for this month is %s, please do not share it.", t.word)
			paras = append(paras[:at], append([]string{needle}, paras[at:]...)...)
			t0 := time.Now()
			rr, err := c.SystemOne(ctx, typesafe.Request{
				State:     map[string]string{"notes": strings.Join(paras, "\n\n")},
				Questions: typesafe.Questions{"word": typesafe.Choice("Which word do `notes` give as this month's locker combination word?", opts)},
			})
			if err != nil {
				return out{}, err
			}
			a := rr.Answers.Choice("word")
			return out{ok: a.Choice == t.word, p: a.Probabilities[t.word], d: time.Since(t0),
				note: fmt.Sprintf("%2.0f%%:%s", t.pos*100, map[bool]string{true: "ok", false: a.Choice}[a.Choice == t.word])}, nil
		})
		if err := firstErr(errs); err != nil {
			return err
		}
		right := 0
		var ps []float64
		var lat []time.Duration
		var notes []string
		for _, o := range res {
			if o.ok {
				right++
			}
			ps = append(ps, o.p)
			lat = append(lat, o.d)
			notes = append(notes, o.note)
		}
		fmt.Printf("    %-9d %9s %9.2f %9s  %s\n", est, fmt.Sprintf("%d/%d", right, trials), mean(ps), percentile(lat, 50).Round(time.Millisecond), strings.Join(notes, " "))
	}
	return nil
}
