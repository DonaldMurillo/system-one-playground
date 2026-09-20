package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// Separation is the property that decides whether a rule can be trusted, and
// it is not the threshold. A rule whose defective and correct populations sit
// far apart survives run-to-run drift, a change of batch size, and a change of
// which other rules are active. A rule whose populations nearly touch cannot be
// rescued by any threshold, because those effects are the same size as its
// signal.
//
// This mode measures both populations directly, using labelled fixtures, and
// reports the gap.
type Separation struct {
	Rule string
	// True holds probabilities for sites a fixture labels as defective.
	True []float64
	// False holds probabilities for every other site, across all fixtures.
	False []float64
}

// Gap is the strict distance: the lowest true reading minus the highest false
// one. Negative means at least one pair overlaps. It is the honest worst case
// but a single outlier dominates it, so read it alongside RobustGap.
func (s Separation) Gap() float64 {
	if len(s.True) == 0 || len(s.False) == 0 {
		return 0
	}
	return minOf(s.True) - maxOf(s.False)
}

// RobustGap ignores the worst tenth of each population. A rule that separates
// nine cases in ten and fails on one is a different thing from a rule whose
// populations genuinely sit on top of each other, and the strict gap calls both
// of them the same.
func (s Separation) RobustGap() float64 {
	if len(s.True) == 0 || len(s.False) == 0 {
		return 0
	}
	return pct(s.True, 10) - pct(s.False, 90)
}

// pct returns the p-th percentile of a sample.
func pct(xs []float64, p float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(p / 100 * float64(len(s)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

// Suggested is the midpoint of the gap, which is where a threshold belongs.
// Suggested puts the threshold in the middle of the robust gap, which is the
// choice that survives the worst case in each population being an outlier.
func (s Separation) Suggested() float64 {
	if len(s.True) == 0 {
		return 0
	}
	if len(s.False) == 0 {
		return round2(minOf(s.True) - 0.10)
	}
	return round2((pct(s.True, 10) + pct(s.False, 90)) / 2)
}

// Verdict grades a rule by how much room its threshold has.
func (s Separation) Verdict() string {
	switch {
	case len(s.True) == 0:
		return "UNTESTED   no labelled defect exercises this rule"
	case len(s.False) == 0:
		return "UNTESTED   no correct counterpart exercises this rule"
	case s.RobustGap() <= 0:
		return "UNUSABLE   populations sit on top of each other; tuning cannot fix this"
	case s.RobustGap() < 0.15:
		return "FRAGILE    gap is under the batching effect; report as a lead only"
	case s.RobustGap() < 0.30:
		return "WORKABLE   usable, but recheck whenever the rule set changes"
	case s.Gap() <= 0:
		return "MOSTLY     separates the bulk cleanly; expect occasional errors at the edges"
	default:
		return "ROBUST     clean separation with room for drift"
	}
}

// truthFile is the sidecar that says which sites are genuinely defective.
type truthFile struct {
	File   string `json:"file"`
	Expect []struct {
		Rule string `json:"rule"`
		Line int    `json:"line"`
	} `json:"expect"`
}

// loadTruth finds the sidecar next to a fixture: defects.js -> defects.expected.json.
func loadTruth(path string) (map[string][]int, error) {
	ext := filepath.Ext(path)
	sidecar := strings.TrimSuffix(path, ext) + ".expected.json"
	b, err := os.ReadFile(sidecar)
	if err != nil {
		return nil, nil // no sidecar: every site in this file counts as correct
	}
	var t truthFile
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", sidecar, err)
	}
	out := map[string][]int{}
	for _, e := range t.Expect {
		out[e.Rule] = append(out[e.Rule], e.Line)
	}
	return out, nil
}

// RunSeparation measures every rule against labelled fixtures and prints the
// distributions. It runs with the whole active set, because a question's answer
// shifts with the company it keeps: measured -0.12 for a mid-confidence rule.
func RunSeparation(ctx context.Context, client *typesafe.Client, paths []string, reg *ruleRegistry, workers, runs int, model string) error {
	if runs < 1 {
		runs = 3
	}
	sep := map[string]*Separation{}
	get := func(rule string) *Separation {
		if s, ok := sep[rule]; ok {
			return s
		}
		s := &Separation{Rule: rule}
		sep[rule] = s
		return s
	}

	for _, p := range paths {
		truth, err := loadTruth(p)
		if err != nil {
			return err
		}
		// A finding is the labelled defect when the unit it was judged from
		// contains the expected line. Matching on a line window instead counts
		// a correct neighbouring function as the defect, which made five sound
		// rules look unusable.
		isTrue := func(f Finding) bool {
			for _, l := range truth[f.Rule] {
				if l >= f.UnitStart && l <= f.UnitEnd {
					return true
				}
			}
			return false
		}
		for i := 0; i < runs; i++ {
			findings, _, err := Lint(ctx, client, []string{p}, reg, workers, model, nil)
			if err != nil {
				return err
			}
			for _, f := range findings {
				// Deterministic rules always answer 1.0; they have no
				// distribution to separate and cannot be miscalibrated.
				if r := reg.get(f.Rule); r != nil && r.Check != nil {
					continue
				}
				s := get(f.Rule)
				if isTrue(f) {
					s.True = append(s.True, f.Confidence)
				} else {
					s.False = append(s.False, f.Confidence)
				}
			}
		}
	}

	names := make([]string, 0, len(sep))
	for n := range sep {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return sep[names[i]].RobustGap() > sep[names[j]].RobustGap() })

	fmt.Printf("Separation over %d runs of %d fixture file(s), with the whole rule set active.\n", runs, len(paths))
	fmt.Println("A threshold belongs in the gap. A rule without one cannot be fixed by tuning.")
	fmt.Printf("\n%-30s %-18s %-18s %7s %6s %6s  %s\n", "rule", "defective", "correct", "gap p10", "strict", "put at", "verdict")
	for _, n := range names {
		s := sep[n]
		tr, fa := "-", "-"
		if len(s.True) > 0 {
			tr = fmt.Sprintf("%.2f-%.2f n=%d", minOf(s.True), maxOf(s.True), len(s.True))
		}
		if len(s.False) > 0 {
			fa = fmt.Sprintf("%.2f-%.2f n=%d", minOf(s.False), maxOf(s.False), len(s.False))
		}
		gap, strict, put := "-", "-", "-"
		if len(s.True) > 0 && len(s.False) > 0 {
			gap = fmt.Sprintf("%+.2f", s.RobustGap())
			strict = fmt.Sprintf("%+.2f", s.Gap())
			put = fmt.Sprintf("%.2f", s.Suggested())
		}
		fmt.Printf("%-30s %-18s %-18s %7s %6s %6s  %s\n", n, tr, fa, gap, strict, put, s.Verdict())
	}

	var untested []string
	for _, id := range reg.ids() {
		if r := reg.get(id); r != nil && r.Check != nil {
			continue
		}
		if _, ok := sep[id]; !ok {
			untested = append(untested, id)
		}
	}
	if len(untested) > 0 {
		fmt.Printf("\nnever fired on these fixtures, so nothing is known about them: %s\n", strings.Join(untested, ", "))
	}
	return nil
}

func minOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}

func maxOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
