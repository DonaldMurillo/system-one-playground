package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// parallel runs fn over items with at most `workers` in flight, preserving order.
func parallel[T, R any](ctx context.Context, items []T, workers int, fn func(context.Context, int, T) (R, error)) ([]R, []error) {
	out := make([]R, len(items))
	errs := make([]error, len(items))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, it T) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i], errs[i] = fn(ctx, i, it)
		}(i, it)
	}
	wg.Wait()
	return out, errs
}

func firstErr(errs []error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// --- datasets ---------------------------------------------------------------

// hfRows fetches the first n rows of a Hugging Face dataset split via the public
// rows API and caches them under data/.
func hfRows(dataset, config, split string, n int) ([]map[string]any, error) {
	return hfRowsAt(dataset, config, split, 0, n)
}

// hfRowsAt fetches n rows starting at offset.
func hfRowsAt(dataset, config, split string, offset, n int) ([]map[string]any, error) {
	cache := filepath.Join("data", fmt.Sprintf("%s_%s_%s_%d_%d.json", strings.ReplaceAll(dataset, "/", "_"), config, split, offset, n))
	if b, err := os.ReadFile(cache); err == nil {
		var rows []map[string]any
		if json.Unmarshal(b, &rows) == nil && len(rows) == n {
			return rows, nil
		}
	}
	var rows []map[string]any
	for off := offset; off < offset+n; off += 100 {
		length := min(100, offset+n-off)
		u := fmt.Sprintf("https://datasets-server.huggingface.co/rows?dataset=%s&config=%s&split=%s&offset=%d&length=%d",
			url.QueryEscape(dataset), config, split, off, length)
		resp, err := http.Get(u)
		if err != nil {
			return nil, err
		}
		var body struct {
			Rows []struct {
				Row map[string]any `json:"row"`
			} `json:"rows"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", u, err)
		}
		if resp.StatusCode != 200 || len(body.Rows) == 0 {
			return nil, fmt.Errorf("hf rows %s: http %d, %d rows", dataset, resp.StatusCode, len(body.Rows))
		}
		for _, r := range body.Rows {
			rows = append(rows, r.Row)
		}
	}
	if err := os.MkdirAll("data", 0o755); err == nil {
		if b, err := json.Marshal(rows); err == nil {
			_ = os.WriteFile(cache, b, 0o644)
		}
	}
	return rows, nil
}

type labelled struct {
	Text  string
	Label int
}

func sst2(n int) ([]labelled, error) {
	rows, err := hfRows("stanfordnlp/sst2", "default", "validation", n)
	if err != nil {
		return nil, err
	}
	out := make([]labelled, len(rows))
	for i, r := range rows {
		out[i] = labelled{Text: strings.TrimSpace(r["sentence"].(string)), Label: int(r["label"].(float64))}
	}
	return out, nil
}

// --- metrics ----------------------------------------------------------------

// calib accumulates reliability-diagram bins and expected calibration error.
type calib struct {
	n, correct   [10]int
	conf         [10]float64
	total, right int
}

func (c *calib) add(conf float64, ok bool) {
	b := min(9, max(0, int(conf*10)))
	c.n[b]++
	c.conf[b] += conf
	c.total++
	if ok {
		c.correct[b]++
		c.right++
	}
}

func (c *calib) accuracy() float64 { return float64(c.right) / float64(max(1, c.total)) }

func (c *calib) ece() float64 {
	var e float64
	for b := range c.n {
		if c.n[b] == 0 {
			continue
		}
		acc := float64(c.correct[b]) / float64(c.n[b])
		avg := c.conf[b] / float64(c.n[b])
		e += float64(c.n[b]) / float64(c.total) * math.Abs(acc-avg)
	}
	return e
}

func (c *calib) print(name string) {
	fmt.Printf("  %s: accuracy=%.1f%% ECE=%.3f (n=%d)\n", name, c.accuracy()*100, c.ece(), c.total)
	fmt.Printf("    %-9s %5s %9s %9s\n", "bin", "n", "avg conf", "accuracy")
	for b := range c.n {
		if c.n[b] == 0 {
			continue
		}
		fmt.Printf("    %.1f-%.1f   %5d %8.1f%% %8.1f%%\n", float64(b)/10, float64(b+1)/10, c.n[b],
			c.conf[b]/float64(c.n[b])*100, float64(c.correct[b])/float64(c.n[b])*100)
	}
}

type confusion struct {
	labels   []string
	idx      map[string]int
	m        [][]int
	n, right int
}

func newConfusion(labels []string) *confusion {
	c := &confusion{labels: labels, idx: map[string]int{}}
	for i, l := range labels {
		c.idx[l] = i
		c.m = append(c.m, make([]int, len(labels)))
	}
	return c
}

func (c *confusion) add(gold, pred string) {
	c.n++
	if gold == pred {
		c.right++
	}
	if gi, ok := c.idx[gold]; ok {
		if pi, ok := c.idx[pred]; ok {
			c.m[gi][pi]++
		}
	}
}

func (c *confusion) accuracy() float64 { return float64(c.right) / float64(max(1, c.n)) }

func (c *confusion) print() {
	fmt.Printf("    %-10s", "gold\\pred")
	for _, l := range c.labels {
		fmt.Printf(" %6s", abbrev(l, 6))
	}
	fmt.Println()
	for i, l := range c.labels {
		fmt.Printf("    %-10s", abbrev(l, 10))
		for j := range c.labels {
			fmt.Printf(" %6d", c.m[i][j])
		}
		fmt.Println()
	}
}

func abbrev(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[min(len(s)-1, max(0, i))]
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// estTokens is a rough English token estimate; the API reports the real count.
func estTokens(s string) int { return len(s) / 4 }

// --- filler text --------------------------------------------------------------

var fillerWords = strings.Fields(`the quarterly maintenance window for the regional warehouse begins after the
last outbound truck departs and the facilities team inspects conveyor belts pallet wrappers and loading dock seals
before recording findings in the shared log each finding receives a severity a location code and an owner who
must acknowledge it within two business days the procurement office reviews vendor contracts annually comparing
unit prices delivery reliability and warranty terms while the training coordinator schedules refresher sessions
for forklift certification first aid and hazardous material handling attendance is tracked and reported to
insurance auditors the cafeteria menu rotates weekly and suggestions may be submitted through the intranet form
parking permits are renewed in january visitors sign in at reception and wear a badge at all times`)

// filler generates deterministic, varied, irrelevant prose of roughly n tokens.
func filler(seed uint64, approxTokens int) string {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var b strings.Builder
	for estTokens(b.String()) < approxTokens {
		sentences := 4 + r.IntN(4)
		for s := 0; s < sentences; s++ {
			words := 8 + r.IntN(9)
			for w := 0; w < words; w++ {
				word := fillerWords[r.IntN(len(fillerWords))]
				if w == 0 {
					word = strings.ToUpper(word[:1]) + word[1:]
				}
				b.WriteString(word)
				if w < words-1 {
					b.WriteByte(' ')
				}
			}
			b.WriteString(". ")
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func pctf(x float64) string { return fmt.Sprintf("%.1f%%", x*100) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
