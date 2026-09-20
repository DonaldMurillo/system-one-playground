package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Finding is one reported defect, in the shape a linter emits and an agent can
// consume: a location, a rule, what is wrong, and why it matters.
type Finding struct {
	File string `json:"file"`
	Line int    `json:"line"`
	// UnitStart and UnitEnd bound the code this finding was judged from.
	// Labelled measurement matches on the unit, not a line window: functions
	// sit closer together than any sensible window, so a window silently
	// counts a correct neighbour as the defect it was looking for.
	UnitStart  int     `json:"unit_start"`
	UnitEnd    int     `json:"unit_end"`
	Rule       string  `json:"rule"`
	Severity   string  `json:"severity"`
	Message    string  `json:"message"`
	Why        string  `json:"why"`
	Confidence float64 `json:"confidence"`
	Snippet    string  `json:"snippet,omitempty"`
}

// Stats records what the run cost, which is the claim being tested: a battery
// of semantic rules should cost one request per unit, not one per rule.
type Stats struct {
	Files     int
	Units     int
	Sites     int
	Requests  int
	Questions int
	Tokens    int
	Wall      time.Duration
	Latencies []time.Duration
	// Skipped records units that produced no answers, with the reason. One
	// oversized generated file used to abort the whole run; now it costs its
	// own unit and nothing else.
	Skipped []Skip
	// Trimmed counts units that fitted only after optional context was dropped.
	Trimmed int
	// PartialContext counts units judged without their full surroundings for
	// any reason, including an oversized function reduced to a line window.
	// These findings rest on less evidence than the rest.
	PartialContext int
	// UnitsFiltered counts units excluded because a diff did not touch them.
	UnitsFiltered int
}

// Skip is one unit the linter could not judge.
type Skip struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// stateTokenBudget is the ceiling for one request's state. The documented
// limit is 32k for state plus the longest question; this leaves headroom for
// the questions themselves and for the estimate being approximate.
const stateTokenBudget = 26000

// estimateTokens is a rough guide, not a guarantee. Measured on this API,
// English prose runs about 5.7 characters per token and densely punctuated
// code about 2.5, so no single divisor is right for both. The estimate trims
// optional context early; the hard case is caught by retrying, below.
func estimateTokens(s string) int { return len(s) / 3 }

// codeByteCap bounds the code field on a retry after the server has told us
// the request was too large. It is deliberately well under the documented
// limit, because by then the estimate has already been proven wrong.
const codeByteCap = 40000

// Lint runs the battery over a set of files.
func Lint(ctx context.Context, client *typesafe.Client, paths []string, reg *ruleRegistry, workers int, model string, changed ChangedLines) ([]Finding, Stats, error) {
	start := time.Now()
	var st Stats

	// Deterministic pass first. Nothing here costs anything, so it can run
	// over an entire repository before a single question is asked.
	var units []Unit
	for _, p := range paths {
		us, err := ExtractUnits(p, reg.rules)
		if err != nil {
			st.Skipped = append(st.Skipped, Skip{File: p, Reason: "could not read: " + err.Error()})
			continue
		}
		kept := us
		if changed != nil {
			kept = kept[:0]
			for _, u := range us {
				if changed.Touches(u.File, u.StartLine, u.EndLine) {
					kept = append(kept, u)
				} else {
					st.UnitsFiltered++
				}
			}
		}
		if len(kept) > 0 {
			st.Files++
		}
		units = append(units, kept...)
	}
	// One index over every unit in the run. A key written in one module and
	// read in another is invisible to both halves without this.
	ix := buildCrossIndex(units)

	st.Units = len(units)
	for _, u := range units {
		st.Sites += len(u.Sites)
	}
	if len(units) == 0 {
		st.Wall = time.Since(start)
		return nil, st, nil
	}

	type result struct {
		findings  []Finding
		questions int
		tokens    int
		latency   time.Duration
		trimmed   bool
		err       error
	}
	results := make([]result, len(units))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, u := range units {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, u Unit) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = judgeUnit(ctx, client, u, reg, model, ix)
		}(i, u)
	}
	wg.Wait()

	var findings []Finding
	for i, r := range results {
		if r.err != nil {
			// One unit failing must never end the run. A single generated file
			// with a 35KB line used to abort everything.
			st.Skipped = append(st.Skipped, Skip{
				File: units[i].File, Line: units[i].StartLine, Reason: r.err.Error(),
			})
			continue
		}
		if r.trimmed {
			st.Trimmed++
		}
		if len(units[i].Cut) > 0 || r.trimmed {
			st.PartialContext++
		}
		findings = append(findings, r.findings...)
		st.Questions += r.questions
		st.Tokens += r.tokens
		if r.latency > 0 {
			st.Requests++
			st.Latencies = append(st.Latencies, r.latency)
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	st.Wall = time.Since(start)
	return findings, st, nil
}

// setCompleteness tells the model exactly what it is not being shown.
func setCompleteness(state map[string]any, cut []string) {
	if len(cut) == 0 {
		state["context_completeness"] = "complete"
		return
	}
	state["context_completeness"] = "INCOMPLETE. " + strings.Join(cut, " ")
}

// isTooLarge reports whether the server refused the request for its size.
func isTooLarge(err error) bool {
	var ae *typesafe.APIError
	return errors.As(err, &ae) && ae.Type == "max_tokens_exceeded"
}

// renderState sizes a state map the way the request will carry it.
func renderState(state map[string]any) string {
	var b strings.Builder
	for k, v := range state {
		b.WriteString(k)
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

// judgeUnit asks every applicable rule about one unit in a single request.
// This is the efficiency claim: ten rules over a function cost ten questions'
// worth of tokens and one round trip, not ten round trips.
func judgeUnit(ctx context.Context, client *typesafe.Client, u Unit, reg *ruleRegistry, model string, ix *crossIndex) (res struct {
	findings  []Finding
	questions int
	tokens    int
	latency   time.Duration
	trimmed   bool
	err       error
}) {
	ids := u.RuleIDs()

	// Deterministic rules resolve here, with no request and no uncertainty.
	qs := typesafe.Questions{}
	for _, id := range ids {
		r := reg.get(id)
		if r == nil {
			continue
		}
		if r.Check != nil {
			for _, s := range u.Sites {
				if s.RuleID == id && r.Check(s) {
					res.findings = append(res.findings, Finding{
						File: u.File, Line: s.Line, UnitStart: u.StartLine, UnitEnd: u.EndLine,
						Rule: r.ID, Severity: r.Severity,
						Message: r.Message, Why: r.Why, Confidence: 1, Snippet: s.Text,
					})
				}
			}
			continue
		}
		qs[id] = r.Question
	}
	if len(qs) == 0 {
		return
	}

	// Keyed fields, never an array: values referenced through array indices
	// lose accuracy as the index grows, measured on this API on 2026-09-17.
	state := map[string]any{
		"file":                         u.File,
		"code":                         u.Code,
		"file_header_comment":          u.Header,
		"module_scope_declarations":    u.Module,
		"enclosing_scope":              u.Enclosing,
		"other_matching_lines_in_file": u.Peers,
		"related_lines_in_other_files": ix.Related(u),
		"line_numbers_are_real":        "Every line in `code` is prefixed with its real line number in the file.",
	}
	for _, id := range ids {
		state["site_line_"+strings.ReplaceAll(id, "-", "_")] = u.SiteLines(id)
	}
	// `site_line` is named in the questions; give it the lines this unit cares
	// about so a rule with several sites still points somewhere real.
	var allLines []int
	for _, s := range u.Sites {
		allLines = append(allLines, s.Line)
	}
	state["site_line"] = allLines

	// Fit the request to the budget by dropping the optional context in order
	// of how much a rule usually needs it, recording every drop so the model is
	// told what it is missing rather than left to guess.
	cut := append([]string(nil), u.Cut...)
	for _, drop := range []struct{ field, note string }{
		{"related_lines_in_other_files", "`related_lines_in_other_files` was omitted because the request was too large, so the other side of anything spanning files is not shown."},
		{"other_matching_lines_in_file", "`other_matching_lines_in_file` was omitted because the request was too large."},
		{"enclosing_scope", "`enclosing_scope` was omitted because the request was too large, so a guard installed by the calling function is not shown."},
		{"module_scope_declarations", "`module_scope_declarations` was omitted because the request was too large, so helpers and constants this code calls are not shown."},
		{"file_header_comment", "The file header comment was omitted because the request was too large."},
	} {
		if estimateTokens(renderState(state)) <= stateTokenBudget {
			break
		}
		if v, _ := state[drop.field].(string); v == "" {
			continue
		}
		state[drop.field] = ""
		cut = append(cut, drop.note)
		res.trimmed = true
	}
	setCompleteness(state, cut)

	t0 := time.Now()
	resp, err := client.SystemOne(ctx, typesafe.Request{State: state, Questions: qs, Model: model})

	// The estimate can be wrong in the unsafe direction on very dense code.
	// When the server says so, strip to the code alone and cut it to a size
	// that is certain to fit, telling the model exactly what it lost.
	if isTooLarge(err) {
		for _, f := range []string{"other_matching_lines_in_file", "enclosing_scope", "module_scope_declarations", "file_header_comment"} {
			state[f] = ""
		}
		if code, _ := state["code"].(string); len(code) > codeByteCap {
			state["code"] = code[:codeByteCap]
			cut = append(cut, fmt.Sprintf("`code` was cut to its first %d bytes because the unit was too large to send; the rest of it is not shown.", codeByteCap))
		}
		cut = append(cut, "All surrounding context was omitted because the unit was too large to send.")
		setCompleteness(state, cut)
		res.trimmed = true
		resp, err = client.SystemOne(ctx, typesafe.Request{State: state, Questions: qs, Model: model})
	}

	res.latency = time.Since(t0)
	if err != nil {
		res.err = fmt.Errorf("%s:%d: %w", u.File, u.StartLine, err)
		return
	}
	res.questions = len(qs)
	res.tokens = resp.Usage.InputTokens

	for _, id := range ids {
		r := reg.get(id)
		if r == nil || r.Check != nil {
			continue
		}
		ans, ok := resp.Answers[id]
		if !ok {
			continue
		}
		fires, conf := r.Fires(ans)
		if !fires {
			continue
		}
		site := u.Sites[0]
		for _, s := range u.Sites {
			if s.RuleID == id {
				site = s
				break
			}
		}
		res.findings = append(res.findings, Finding{
			File:       u.File,
			Line:       site.Line,
			UnitStart:  u.StartLine,
			UnitEnd:    u.EndLine,
			Rule:       r.ID,
			Severity:   r.Severity,
			Message:    r.Message,
			Why:        r.Why,
			Confidence: conf,
			Snippet:    site.Text,
		})
	}
	return
}
