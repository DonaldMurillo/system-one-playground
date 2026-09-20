package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// The TypeSafe docs index is the corpus for the retrieval experiments: ~120
// pages, each with a title and a one-line description.
type page struct {
	Title, URL, Desc, Group string
	Label                   string // unique option label
}

var indexLine = regexp.MustCompile(`^- \[(.+?)\]\((\S+?)\)(?::\s*(.*))?$`)

func loadDocsIndex() ([]page, error) {
	cache := filepath.Join("data", "llms.txt")
	b, err := os.ReadFile(cache)
	if err != nil {
		resp, err := http.Get("https://docs.typesafe.ai/llms.txt")
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var sb strings.Builder
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			sb.WriteString(sc.Text() + "\n")
		}
		b = []byte(sb.String())
		_ = os.MkdirAll("data", 0o755)
		_ = os.WriteFile(cache, b, 0o644)
	}
	var pages []page
	seen := map[string]int{}
	for _, line := range strings.Split(string(b), "\n") {
		m := indexLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		p := page{Title: m[1], URL: m[2], Desc: m[3]}
		path := strings.TrimPrefix(p.URL, "https://docs.typesafe.ai/")
		parts := strings.Split(path, "/")
		switch {
		case len(parts) >= 2 && parts[0] == "sdk":
			p.Group = "sdk/" + parts[1]
		case len(parts) >= 2:
			p.Group = parts[0]
		default:
			p.Group = "general"
		}
		p.Label = p.Title
		if seen[p.Title] > 0 {
			p.Label = fmt.Sprintf("%s (%s)", p.Title, p.Group)
		}
		seen[p.Title]++
		pages = append(pages, p)
	}
	return pages, nil
}

type query struct {
	text string
	gold []string // acceptable titles
}

var docQueries = []query{
	{"how is the confidence number computed and how should I threshold on it", []string{"Confidence"}},
	{"install the javascript sdk and make a first call", []string{"JavaScript SDK"}},
	{"what are the rate limits and the price per token", []string{"Models"}},
	{"check whether a quoted citation actually supports the claim it is attached to", []string{"Double-checking citations"}},
	{"rebuild markdown headings and lists from text that lost its formatting", []string{"Structure recovery"}},
	{"async python client for asking questions", []string{"Asynchronous client"}},
	{"is it cheaper to send many questions in one request than one at a time", []string{"Parallel questions", "Speculative fan-out"}},
	{"decide whether two product catalog entries describe the same product", []string{"Knowledge graph entity alignment"}},
	{"known failure modes and weaknesses of the current model", []string{"Jev 1.13 jaggedness"}},
	{"configure retry backoff for 429 errors in the python sdk", []string{"Retries"}},
	{"what fields does a yes/no answer return", []string{"Noul"}},
	{"rate the input against several ordered levels", []string{"Score"}},
}

func hit(gold []string, title string) bool {
	for _, g := range gold {
		if g == title {
			return true
		}
	}
	return false
}

func optionsFor(pages []page) map[string]any {
	opts := make(map[string]any, len(pages))
	for _, p := range pages {
		if p.Desc != "" {
			opts[p.Label] = p.Desc
		} else {
			opts[p.Label] = nil
		}
	}
	return opts
}

func byLabel(pages []page) map[string]page {
	m := map[string]page{}
	for _, p := range pages {
		m[p.Label] = p
	}
	return m
}

// topK returns option labels sorted by probability.
func topK(a typesafe.Answer, k int) []string {
	ranked := a.Ranked()
	var out []string
	for i := 0; i < len(ranked) && i < k; i++ {
		out = append(out, ranked[i].Label)
	}
	return out
}

// Experiment 4: large option sets. Flat Choice over 20, 60 and all pages;
// BM25 shortlist reranked with one Noul per candidate; and a two-level
// hierarchical beam search over page groups.
func scale(ctx context.Context, c *typesafe.Client) error {
	pages, err := loadDocsIndex()
	if err != nil {
		return err
	}
	labels := byLabel(pages)
	fmt.Printf("  corpus: %d docs pages in %d groups; %d queries\n", len(pages), len(groupsOf(pages)), len(docQueries))

	// --- (a) flat Choice, growing option count -------------------------------
	fmt.Println("  (a) flat Choice over N pages (label = title, description = one-liner)")
	fmt.Printf("    %5s %6s %6s %8s %9s\n", "N", "top-1", "top-3", "tokens", "latency")
	r := rand.New(rand.NewPCG(42, 42))
	for _, n := range []int{20, 60, len(pages)} {
		top1, top3, toks := 0, 0, 0
		var lat []time.Duration
		for _, q := range docQueries {
			subset := sample(r, pages, n, q.gold)
			t0 := time.Now()
			res, err := c.SystemOne(ctx, typesafe.Request{
				State:     map[string]string{"query": q.text},
				Questions: typesafe.Questions{"page": typesafe.Choice("Which documentation page best answers `query`?", optionsFor(subset))},
			})
			if err != nil {
				return err
			}
			lat = append(lat, time.Since(t0))
			toks += res.Usage.InputTokens
			a := res.Answers.Choice("page")
			if hit(q.gold, labels[a.Choice].Title) {
				top1++
			}
			for _, l := range topK(a, 3) {
				if hit(q.gold, labels[l].Title) {
					top3++
					break
				}
			}
		}
		nq := len(docQueries)
		fmt.Printf("    %5d %5d/%d %5d/%d %8d %9s\n", n, top1, nq, top3, nq, toks/nq, percentile(lat, 50).Round(time.Millisecond))
	}

	// --- (b) BM25 shortlist, Noul rerank ----------------------------------------
	fmt.Println("  (b) BM25 over title+description, then rerank the top 15 with one Noul per candidate")
	bm := newBM25(pages)
	b1, b3, b10, r1, r3 := 0, 0, 0, 0, 0
	rerankToks := 0
	for _, q := range docQueries {
		ranked := bm.search(q.text, 15)
		for i, p := range ranked {
			if hit(q.gold, p.Title) {
				if i < 1 {
					b1++
				}
				if i < 3 {
					b3++
				}
				b10++
				break
			}
		}
		cands := make([]map[string]string, len(ranked))
		qs := typesafe.Questions{}
		for i, p := range ranked {
			cands[i] = map[string]string{"title": p.Title, "description": p.Desc}
			qs["rel_"+strconv.Itoa(i)] = typesafe.Noul(fmt.Sprintf("Is `candidates[%d]` the documentation page that answers `query`?", i))
		}
		res, err := c.SystemOne(ctx, typesafe.Request{State: map[string]any{"query": q.text, "candidates": cands}, Questions: qs})
		if err != nil {
			return err
		}
		rerankToks += res.Usage.InputTokens
		idx := make([]int, len(ranked))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			return res.Answers.Noul("rel_"+strconv.Itoa(idx[a])) > res.Answers.Noul("rel_"+strconv.Itoa(idx[b]))
		})
		for rank, i := range idx {
			if hit(q.gold, ranked[i].Title) {
				if rank < 1 {
					r1++
				}
				if rank < 3 {
					r3++
				}
				break
			}
		}
	}
	nq := len(docQueries)
	fmt.Printf("    BM25 alone:     top-1 %d/%d  top-3 %d/%d  top-15 %d/%d\n", b1, nq, b3, nq, b10, nq)
	fmt.Printf("    BM25 + rerank:  top-1 %d/%d  top-3 %d/%d  (%d tokens/query)\n", r1, nq, r3, nq, rerankToks/nq)

	// --- (c) hierarchical beam search -------------------------------------------
	fmt.Println("  (c) hierarchical: Choice over groups, then Choice within the top-2 groups, score = p(group)*p(page)")
	groups := groupsOf(pages)
	groupOpts := map[string]any{}
	for g, ps := range groups {
		var ex []string
		for i, p := range ps {
			if i == 4 {
				break
			}
			ex = append(ex, p.Title)
		}
		groupOpts[g] = fmt.Sprintf("%d pages, e.g. %s", len(ps), strings.Join(ex, "; "))
	}
	h1, hToks := 0, 0
	for _, q := range docQueries {
		s1, err := c.SystemOne(ctx, typesafe.Request{
			State:     map[string]string{"query": q.text},
			Questions: typesafe.Questions{"group": typesafe.Choice("Which section of the documentation would answer `query`?", groupOpts)},
		})
		if err != nil {
			return err
		}
		hToks += s1.Usage.InputTokens
		beam := topK(s1.Answers.Choice("group"), 2)
		qs := typesafe.Questions{}
		for _, g := range beam {
			opts := optionsFor(groups[g])
			opts["none of these"] = "No page in this list answers the query"
			qs[g] = typesafe.Choice("Which page in this list best answers `query`?", opts)
		}
		s2, err := c.SystemOne(ctx, typesafe.Request{State: map[string]string{"query": q.text}, Questions: qs})
		if err != nil {
			return err
		}
		hToks += s2.Usage.InputTokens
		best, bestP := "", -1.0
		for _, g := range beam {
			pg := s1.Answers.Choice("group").Probabilities[g]
			for label, pp := range s2.Answers.Choice(g).Probabilities {
				if label == "none of these" {
					continue
				}
				if p := pg * pp; p > bestP {
					best, bestP = label, p
				}
			}
		}
		if hit(q.gold, labels[best].Title) {
			h1++
		} else {
			fmt.Printf("      miss: %q -> %s (via %v)\n", truncate(q.text, 40), best, beam)
		}
	}
	fmt.Printf("    hierarchical top-1 %d/%d, %d tokens/query over 2 requests\n", h1, nq, hToks/nq)
	return nil
}

func groupsOf(pages []page) map[string][]page {
	g := map[string][]page{}
	for _, p := range pages {
		g[p.Group] = append(g[p.Group], p)
	}
	return g
}

// sample picks n pages at random, always including every gold page.
func sample(r *rand.Rand, pages []page, n int, gold []string) []page {
	if n >= len(pages) {
		return pages
	}
	var out, rest []page
	for _, p := range pages {
		if hit(gold, p.Title) {
			out = append(out, p)
		} else {
			rest = append(rest, p)
		}
	}
	r.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	return append(out, rest[:n-len(out)]...)
}

// --- minimal BM25 --------------------------------------------------------------

type bm25 struct {
	pages  []page
	docs   [][]string
	df     map[string]int
	avgLen float64
}

var tokenRe = regexp.MustCompile(`[a-z0-9]+`)

func tokenize(s string) []string { return tokenRe.FindAllString(strings.ToLower(s), -1) }

func newBM25(pages []page) *bm25 {
	b := &bm25{pages: pages, df: map[string]int{}}
	total := 0
	for _, p := range pages {
		toks := tokenize(p.Title + " " + p.Desc)
		b.docs = append(b.docs, toks)
		total += len(toks)
		seen := map[string]bool{}
		for _, t := range toks {
			if !seen[t] {
				seen[t] = true
				b.df[t]++
			}
		}
	}
	b.avgLen = float64(total) / float64(len(pages))
	return b
}

func (b *bm25) search(q string, k int) []page {
	const k1, bb = 1.5, 0.75
	n := float64(len(b.docs))
	scores := make([]float64, len(b.docs))
	for _, t := range tokenize(q) {
		df, ok := b.df[t]
		if !ok {
			continue
		}
		idf := math.Log(1 + (n-float64(df)+0.5)/(float64(df)+0.5))
		for i, doc := range b.docs {
			tf := 0
			for _, w := range doc {
				if w == t {
					tf++
				}
			}
			if tf == 0 {
				continue
			}
			den := float64(tf) + k1*(1-bb+bb*float64(len(doc))/b.avgLen)
			scores[i] += idf * float64(tf) * (k1 + 1) / den
		}
	}
	idx := make([]int, len(b.docs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return scores[idx[i]] > scores[idx[j]] })
	var out []page
	for i := 0; i < k && i < len(idx); i++ {
		out = append(out, b.pages[idx[i]])
	}
	return out
}
