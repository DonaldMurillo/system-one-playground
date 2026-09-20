package main

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 11: do the counting and date failures appear at larger sizes?
// Counting goes to 80 items; dates get 40 pairs with boundary crossings and
// ambiguous numeric formats, plus a "how far apart" bucket question.
func scalingFailures(ctx context.Context, c *typesafe.Client) error {
	if err := countingAtScale(ctx, c); err != nil {
		return err
	}
	return datesAtScale(ctx, c)
}

func countingAtScale(ctx context.Context, c *typesafe.Client) error {
	fruits := strings.Fields("apple banana cherry mango grape peach pear plum kiwi lemon lime fig papaya guava apricot nectarine pomegranate blueberry raspberry strawberry blackberry cranberry pineapple coconut watermelon cantaloupe tangerine clementine persimmon quince lychee durian jackfruit passionfruit starfruit gooseberry elderberry mulberry date olive")
	others := strings.Fields("carrot table python london hammer violin oxygen tuesday granite sparrow coffee marble sonnet tractor velvet ladder compass thunder pencil harbor saddle lantern copper meadow anchor whistle canvas turbine falcon glacier orbit pillow quartz ribbon tunnel walnut yogurt zephyr blanket cactus")
	r := rand.New(rand.NewPCG(21, 34))
	fmt.Println("  (a) counting fruits, 6 trials per list size")
	fmt.Printf("    %-6s %-30s %-30s %s\n", "items", "naive Choice over 0..n", "per-item Noul, summed", "tokens")
	for _, n := range []int{10, 20, 40, 80} {
		const trials = 6
		naiveExact, fixedExact, toks := 0, 0, 0
		var naiveErr, fixedErr float64
		countOpts := map[string]any{}
		for k := 0; k <= n; k++ {
			countOpts[strconv.Itoa(k)] = nil
		}
		for t := 0; t < trials; t++ {
			k := r.IntN(n + 1)
			items := make([]string, 0, n)
			for i := 0; i < k; i++ {
				items = append(items, fruits[r.IntN(len(fruits))])
			}
			for i := k; i < n; i++ {
				items = append(items, others[r.IntN(len(others))])
			}
			r.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
			qs := typesafe.Questions{"count": typesafe.Choice("Exactly how many entries of `items` are fruits?", countOpts)}
			for i := range items {
				qs["item_"+strconv.Itoa(i)] = typesafe.Noul(fmt.Sprintf("Is `items[%d]` the name of a fruit?", i))
			}
			res, err := c.SystemOne(ctx, typesafe.Request{State: map[string]any{"items": items}, Questions: qs})
			if err != nil {
				return err
			}
			toks += res.Usage.InputTokens
			naive, _ := strconv.Atoi(res.Answers.Choice("count").Choice)
			fixed := 0
			for i := range items {
				if res.Answers.Noul("item_"+strconv.Itoa(i)) > 0.5 {
					fixed++
				}
			}
			if naive == k {
				naiveExact++
			}
			if fixed == k {
				fixedExact++
			}
			naiveErr += math.Abs(float64(naive - k))
			fixedErr += math.Abs(float64(fixed - k))
		}
		fmt.Printf("    %-6d %-30s %-30s %d\n", n,
			fmt.Sprintf("%d/%d exact, MAE %.2f", naiveExact, trials, naiveErr/trials),
			fmt.Sprintf("%d/%d exact, MAE %.2f", fixedExact, trials, fixedErr/trials), toks/trials)
	}

	// Diagnostic: with 80 items, where do the per-item Nouls go wrong? Compare
	// array indexing (`items[37]`) with a keyed object (`w37`) and with inlining
	// the word into the question text, no state lookup at all.
	fmt.Println("  (a2) per-item Noul accuracy on 80 items, by position and by state layout (4 trials)")
	fmt.Printf("    %-28s %7s %7s %7s %7s %7s\n", "layout", "0-15", "16-31", "32-47", "48-63", "64-79")
	layouts := []struct {
		name  string
		state func(items []string) any
		q     func(i int, word string) string
	}{
		{"array `items[i]`", func(items []string) any { return map[string]any{"items": items} },
			func(i int, _ string) string { return fmt.Sprintf("Is `items[%d]` the name of a fruit?", i) }},
		{"object `w<i>` keys", func(items []string) any {
			m := map[string]any{}
			for i, w := range items {
				m["w"+strconv.Itoa(i)] = w
			}
			return m
		}, func(i int, _ string) string { return fmt.Sprintf("Is `w%d` the name of a fruit?", i) }},
		{"word inlined, state empty", func(items []string) any { return "" },
			func(_ int, word string) string { return fmt.Sprintf("Is %q the name of a fruit?", word) }},
	}
	for _, lay := range layouts {
		var right, total [5]int
		for t := 0; t < 4; t++ {
			items := make([]string, 80)
			isFruit := make([]bool, 80)
			for i := range items {
				if r.IntN(2) == 0 {
					items[i], isFruit[i] = fruits[r.IntN(len(fruits))], true
				} else {
					items[i] = others[r.IntN(len(others))]
				}
			}
			qs := typesafe.Questions{}
			for i, w := range items {
				qs["q"+strconv.Itoa(i)] = typesafe.Noul(lay.q(i, w))
			}
			res, err := c.SystemOne(ctx, typesafe.Request{State: lay.state(items), Questions: qs})
			if err != nil {
				return err
			}
			for i := range items {
				b := i / 16
				total[b]++
				if (res.Answers.Noul("q"+strconv.Itoa(i)) > 0.5) == isFruit[i] {
					right[b]++
				}
			}
		}
		fmt.Printf("    %-28s", lay.name)
		for b := range right {
			fmt.Printf(" %6.0f%%", float64(right[b])/float64(total[b])*100)
		}
		fmt.Println()
	}
	return nil
}

func datesAtScale(ctx context.Context, c *typesafe.Client) error {
	r := rand.New(rand.NewPCG(5, 8))
	// Mixed formats, including two ambiguous numeric ones that the instructions disambiguate.
	formats := []struct{ layout, note string }{
		{"January 2, 2006", ""},
		{"01/02/2006", "slash dates are month/day/year"},
		{"02.01.2006", "dot dates are day.month.year"},
		{"2006-01-02", ""},
		{"2 Jan 2006", ""},
		{"Jan 2nd, 2006", ""},
		{"20060102", "8-digit dates are yyyymmdd"},
		{"02-Jan-06", ""},
	}
	deltas := []int{1, -1, 2, -2, 7, -7, 30, -30, 31, -31, 365, -365, 366, -366, 1, -1, 1, -1, 28, -28}
	type pair struct {
		a, b   string
		ta, tb time.Time
		note   string
	}
	var pairs []pair
	for i := 0; i < 40; i++ {
		y := 2022 + r.IntN(4)
		m := time.Month(1 + r.IntN(12))
		var base time.Time
		switch r.IntN(3) {
		case 0: // last day of month, so ±1 crosses a boundary
			base = time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC)
		case 1: // first day
			base = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		default:
			base = time.Date(y, m, 1+r.IntN(28), 0, 0, 0, 0, time.UTC)
		}
		other := base.AddDate(0, 0, deltas[i%len(deltas)])
		fa, fb := formats[r.IntN(len(formats))], formats[r.IntN(len(formats))]
		var notes []string
		for _, n := range []string{fa.note, fb.note} {
			if n != "" && !strings.Contains(strings.Join(notes, " "), n) {
				notes = append(notes, n)
			}
		}
		note := ""
		if len(notes) > 0 {
			note = " Note: " + strings.Join(notes, "; ") + "."
		}
		pairs = append(pairs, pair{base.Format(fa.layout), other.Format(fb.layout), base, other, note})
	}

	monthOpts := typesafe.Options(months...)
	monthOpts["not stated"] = nil
	dayOpts := map[string]any{"not stated": nil}
	for d := 1; d <= 31; d++ {
		dayOpts[strconv.Itoa(d)] = nil
	}
	yearOpts := map[string]any{"not stated": nil}
	for y := 2020; y <= 2027; y++ {
		yearOpts[strconv.Itoa(y)] = nil
	}
	gapOpts := map[string]any{
		"same day":      nil,
		"1-2 days":      nil,
		"3-10 days":     nil,
		"11-45 days":    nil,
		"46-200 days":   nil,
		"over 200 days": nil,
	}
	gapBucket := func(days int) string {
		switch d := int(math.Abs(float64(days))); {
		case d == 0:
			return "same day"
		case d <= 2:
			return "1-2 days"
		case d <= 10:
			return "3-10 days"
		case d <= 45:
			return "11-45 days"
		case d <= 200:
			return "46-200 days"
		default:
			return "over 200 days"
		}
	}

	type out struct {
		naiveOK, fixedOK, partsOK, gapOK, boundary bool
		delta                                      int
	}
	res, errs := parallel(ctx, pairs, 8, func(ctx context.Context, _ int, p pair) (out, error) {
		rr, err := c.SystemOne(ctx, typesafe.Request{
			State: map[string]string{"a": p.a, "b": p.b},
			Questions: typesafe.Questions{
				"naive":   typesafe.Noul("Is the date `a` earlier than the date `b`?" + p.note),
				"gap":     typesafe.Choice("How far apart are the dates `a` and `b`?"+p.note, gapOpts),
				"a_month": typesafe.Choice("Which month is in the date `a`?"+p.note, monthOpts),
				"a_day":   typesafe.Choice("Which day of the month is in the date `a`?"+p.note, dayOpts),
				"a_year":  typesafe.Choice("Which year is in the date `a`? Two-digit years are 20xx."+p.note, yearOpts),
				"b_month": typesafe.Choice("Which month is in the date `b`?"+p.note, monthOpts),
				"b_day":   typesafe.Choice("Which day of the month is in the date `b`?"+p.note, dayOpts),
				"b_year":  typesafe.Choice("Which year is in the date `b`? Two-digit years are 20xx."+p.note, yearOpts),
			},
		})
		if err != nil {
			return out{}, err
		}
		gold := p.ta.Before(p.tb)
		days := int(p.tb.Sub(p.ta).Hours() / 24)
		o := out{delta: days, boundary: p.ta.Month() != p.tb.Month()}
		o.naiveOK = (rr.Answers.Noul("naive") > 0.5) == gold
		o.gapOK = rr.Answers.Choice("gap").Choice == gapBucket(days)
		ta, okA := assemble(rr.Answers, "a_")
		tb, okB := assemble(rr.Answers, "b_")
		o.partsOK = okA && okB && ta.Equal(p.ta) && tb.Equal(p.tb)
		o.fixedOK = okA && okB && ta.Before(tb) == gold
		return o, nil
	})
	if err := firstErr(errs); err != nil {
		return err
	}

	type tally struct{ n, naive, fixed, parts, gap int }
	var all, near, cross tally
	add := func(t *tally, o out) {
		t.n++
		if o.naiveOK {
			t.naive++
		}
		if o.fixedOK {
			t.fixed++
		}
		if o.partsOK {
			t.parts++
		}
		if o.gapOK {
			t.gap++
		}
	}
	for _, o := range res {
		add(&all, o)
		if math.Abs(float64(o.delta)) <= 2 {
			add(&near, o)
		}
		if o.boundary {
			add(&cross, o)
		}
	}
	fmt.Println("  (b) date ordering, 40 pairs in 8 formats including ambiguous numeric ones")
	fmt.Printf("    %-24s %5s %14s %14s %14s %14s\n", "subset", "n", "naive before?", "parts+code", "parts exact", "naive gap")
	for _, t := range []struct {
		name string
		t    tally
	}{{"all", all}, {"1-2 days apart", near}, {"crosses a month", cross}} {
		fmt.Printf("    %-24s %5d %14s %14s %14s %14s\n", t.name, t.t.n,
			fmt.Sprintf("%d/%d", t.t.naive, t.t.n), fmt.Sprintf("%d/%d", t.t.fixed, t.t.n),
			fmt.Sprintf("%d/%d", t.t.parts, t.t.n), fmt.Sprintf("%d/%d", t.t.gap, t.t.n))
	}
	return nil
}
