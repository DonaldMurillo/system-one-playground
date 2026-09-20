package semcore

import (
	"embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// A Rule is one compiled lint check. It has two halves, and the split is the
// whole idea:
//
//   - Selector is deterministic. A regex finds the sites where the rule could
//     possibly apply. This costs nothing and is exact.
//   - Check or Question decides. Check is a regex verdict; Question is the one
//     thing a regex cannot decide.
//
// Rules are written as data in rules/*.json and compiled into this by
// ruleset.go, so adding one never means editing this program.
type Rule struct {
	ID       string
	Severity string // error, warning, hint
	// Why explains the consequence. It is written for whoever reads the
	// finding, including a model being handed it as context.
	Why string
	// Selector decides which sites this rule applies to.
	Selector Selector
	// Check makes the rule fully deterministic. When it is set no question is
	// sent and Question is ignored.
	//
	// The discipline this enforces is the point of the whole framework: if the
	// answer is decided by the presence of literal tokens, write the check.
	// Handing a decidable question to a model buys nothing and can be wrong,
	// confidently. The cookie-attribute rule was a model question until it
	// claimed SameSite was missing from a line containing "SameSite=Lax".
	Check func(site Site) bool
	// Question is the judgment, for everything Check cannot decide.
	Question typesafe.Question
	// Fires turns the answer into a verdict. Thresholds live here, in code.
	Fires func(typesafe.Answer) (bool, float64)
	// Message is the finding text.
	Message string
	// Source names the rule set this came from, for -list.
	Source string
}

// Selector is the deterministic half: which files, and which lines inside them.
type Selector struct {
	// Ext limits the rule to a file extension, such as ".js".
	Ext string
	// Site matches the construct the rule is about. Every match becomes a
	// candidate site, and the enclosing function becomes the unit judged.
	Site *regexp.Regexp
}

// noulFires builds the common policy: the defect is reported when the
// probability clears a threshold. Confidence comes back as the probability
// itself, so the report can rank and the caller can raise the bar.
func noulFires(threshold float64) func(typesafe.Answer) (bool, float64) {
	return func(a typesafe.Answer) (bool, float64) {
		return a.Noul >= threshold, a.Noul
	}
}

// scoreFires reports when a graded judgment reaches a level, and only when the
// distribution is concentrated enough to mean something.
func scoreFires(level, minConfidence float64) func(typesafe.Answer) (bool, float64) {
	return func(a typesafe.Answer) (bool, float64) {
		return a.Score >= level && a.Confidence >= minConfidence, a.Confidence
	}
}

//go:embed rules/*.json
var builtinRules embed.FS

// builtinSets are the rule files shipped with the linter. "default" is general
// purpose and runs unless told otherwise; the rest are opt-in by name.
var builtinSets = []string{"default", "browser-storage"}

// LoadBuiltin compiles one shipped rule set by name.
func LoadBuiltin(name string) ([]Rule, error) {
	b, err := builtinRules.ReadFile("rules/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("no built-in rule set %q (have: %s)", name, strings.Join(builtinSets, ", "))
	}
	rs, err := parseRuleBytes(b, name)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		rs[i].Source = name
	}
	return rs, nil
}

func (r Rule) String() string { return fmt.Sprintf("%s (%s)", r.ID, r.Severity) }
