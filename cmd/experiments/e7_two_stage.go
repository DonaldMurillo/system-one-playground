package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Experiment 7: a two-request workflow, the skill-suggestion pattern. Stage 1
// ranks every docs page by its one-line description and asks whether any page
// answers at all. Stage 2 fetches the full text of the top three and judges
// again against real evidence, with a 'none' option. The second request is
// justified because its state (the page bodies) cannot be built until the
// first answer picks which pages to fetch.
func twoStage(ctx context.Context, c *typesafe.Client) error {
	pages, err := loadDocsIndex()
	if err != nil {
		return err
	}
	labels := byLabel(pages)
	all := optionsFor(pages)

	queries := append([]query(nil), docQueries...)
	queries = append(queries,
		query{"how do I fine-tune jev on my own labelled data", []string{"none"}},
		query{"pricing for the on-premises enterprise appliance", []string{"none"}},
	)

	fmt.Printf("  %-44s %-26s %-26s %s\n", "query", "stage 1 top-1", "stage 2 pick", "")
	s1Right, s2Right, s1Toks, s2Toks := 0, 0, 0, 0
	var s1Lat, s2Lat []time.Duration
	for _, q := range queries {
		t0 := time.Now()
		s1, err := c.SystemOne(ctx, typesafe.Request{
			State: map[string]string{"query": q.text},
			Questions: typesafe.Questions{
				"page":       typesafe.Choice("Which documentation page best answers `query`?", all),
				"answerable": typesafe.Noul("Does the documentation, judging by these page titles and descriptions, contain a page that answers `query`?"),
			},
		})
		if err != nil {
			return err
		}
		s1Lat = append(s1Lat, time.Since(t0))
		s1Toks += s1.Usage.InputTokens
		pick1 := labels[s1.Answers.Choice("page").Choice].Title
		if hit(q.gold, pick1) {
			s1Right++
		}

		// Stage 2: fetch the top three pages and judge with their text.
		top := topK(s1.Answers.Choice("page"), 3)
		cands := make([]map[string]string, 0, len(top))
		opts := map[string]any{"none": "None of these pages answers the query"}
		for _, l := range top {
			p := labels[l]
			body, err := fetchPage(p.URL)
			if err != nil {
				return err
			}
			cands = append(cands, map[string]string{"title": p.Label, "excerpt": body})
			opts[p.Label] = nil
		}
		t0 = time.Now()
		s2, err := c.SystemOne(ctx, typesafe.Request{
			State:     map[string]any{"query": q.text, "candidates": cands},
			Questions: typesafe.Questions{"page": typesafe.Choice("Based on the `candidates` excerpts, which page actually answers `query`? Choose none if no excerpt does.", opts)},
		})
		if err != nil {
			return err
		}
		s2Lat = append(s2Lat, time.Since(t0))
		s2Toks += s2.Usage.InputTokens
		a2 := s2.Answers.Choice("page")
		pick2 := a2.Choice
		if pick2 != "none" {
			pick2 = labels[pick2].Title
		}
		if hit(q.gold, pick2) {
			s2Right++
		}
		mark := func(ok bool) string {
			if ok {
				return "ok"
			}
			return "MISS"
		}
		fmt.Printf("  %-44s %-26s %-26s s1 %s (answerable %.2f) s2 %s (conf %.2f)\n",
			truncate(q.text, 42), truncate(pick1, 24), truncate(pick2, 24),
			mark(hit(q.gold, pick1)), s1.Answers.Noul("answerable"), mark(hit(q.gold, pick2)), a2.Confidence)
	}
	n := len(queries)
	fmt.Printf("\n  stage 1 (descriptions only): %d/%d, %d tokens, p50 %s\n", s1Right, n, s1Toks/n, percentile(s1Lat, 50).Round(time.Millisecond))
	fmt.Printf("  stage 2 (full text of top 3): %d/%d, %d tokens, p50 %s\n", s2Right, n, s2Toks/n, percentile(s2Lat, 50).Round(time.Millisecond))
	return nil
}

var jsxLine = regexp.MustCompile(`^\s*(<|/>|export |import |\}|\{|\)|\(|function |return |var |const |for |if |else)`)

// fetchPage returns a docs page's Markdown with embedded component code stripped,
// truncated to a few thousand characters, cached under data/pages.
func fetchPage(url string) (string, error) {
	slug := strings.NewReplacer("https://docs.typesafe.ai/", "", "/", "_").Replace(url)
	cache := filepath.Join("data", "pages", slug)
	if b, err := os.ReadFile(cache); err == nil {
		return string(b), nil
	}
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch %s: http %d", url, resp.StatusCode)
	}
	var sb strings.Builder
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 2<<20))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	inCode := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
			continue
		}
		if inCode || jsxLine.MatchString(line) || strings.TrimSpace(line) == "" {
			continue
		}
		sb.WriteString(line + "\n")
		if sb.Len() > 5000 {
			break
		}
	}
	text := sb.String()
	_ = os.MkdirAll(filepath.Dir(cache), 0o755)
	_ = os.WriteFile(cache, []byte(text), 0o644)
	return text, nil
}
