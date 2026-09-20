package typesafe

import (
	"fmt"
	"sort"
	"strings"
)

// Answer is one typed answer. Type is "noul", "choice" or "score" and decides
// which of the other fields are populated.
type Answer struct {
	Type string `json:"type"`
	// Noul: probability that the answer is yes.
	Noul float64 `json:"noul"`
	// Choice: the selected label.
	Choice string `json:"choice"`
	// Score: expected position along the levels, which may fall between two of them.
	Score float64 `json:"score"`
	// Choice and Score: how peaked the distribution is, 0 to 1.
	Confidence float64 `json:"confidence"`
	// Choice: keyed by label. Score: keyed by level index as a string ("0", "1", ...).
	Probabilities map[string]float64 `json:"probabilities"`
	// Score: level index as a string to the level description you supplied.
	Legend map[string]any `json:"legend"`
}

// Answers are keyed by question id.
type Answers map[string]Answer

// Get returns the answer for id and whether it exists.
func (a Answers) Get(id string) (Answer, bool) {
	ans, ok := a[id]
	return ans, ok
}

// Noul returns the yes-probability for id. Panics if id is missing or not a noul.
func (a Answers) Noul(id string) float64 { return a.must(id, "noul").Noul }

// Choice returns the choice answer for id. Panics if id is missing or not a choice.
func (a Answers) Choice(id string) Answer { return a.must(id, "choice") }

// Score returns the score answer for id. Panics if id is missing or not a score.
func (a Answers) Score(id string) Answer { return a.must(id, "score") }

func (a Answers) must(id, typ string) Answer {
	ans, ok := a[id]
	if !ok {
		panic(fmt.Sprintf("typesafe: no answer for question %q", id))
	}
	if ans.Type != typ {
		panic(fmt.Sprintf("typesafe: answer %q is a %s, not a %s", id, ans.Type, typ))
	}
	return ans
}

// Levels is the number of Score levels, taken from the legend.
func (a Answer) Levels() int { return len(a.Legend) }

// Normalized maps a Score onto 0..1 so scores with different level counts compare.
func (a Answer) Normalized() float64 {
	n := a.Levels()
	if n <= 1 {
		return 0
	}
	return a.Score / float64(n-1)
}

// Ranked is the probability distribution sorted from most to least likely.
// Score answers sort by level index instead so the order matches the rubric.
func (a Answer) Ranked() []Prob {
	out := make([]Prob, 0, len(a.Probabilities))
	for k, v := range a.Probabilities {
		out = append(out, Prob{Label: k, P: v})
	}
	if a.Type == "score" {
		sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].P > out[j].P })
	}
	return out
}

// Prob is one entry of a probability distribution.
type Prob struct {
	Label string
	P     float64
}

// String renders the answer compactly for logs.
func (a Answer) String() string {
	switch a.Type {
	case "noul":
		return fmt.Sprintf("noul=%.3f", a.Noul)
	case "choice":
		return fmt.Sprintf("choice=%s conf=%.3f  [%s]", a.Choice, a.Confidence, a.dist())
	case "score":
		return fmt.Sprintf("score=%.3f conf=%.3f  [%s]", a.Score, a.Confidence, a.dist())
	}
	return "type=" + a.Type
}

func (a Answer) dist() string {
	parts := make([]string, 0, len(a.Probabilities))
	for _, p := range a.Ranked() {
		parts = append(parts, fmt.Sprintf("%s=%.1f%%", p.Label, p.P*100))
	}
	return strings.Join(parts, " ")
}
