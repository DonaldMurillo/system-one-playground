package semcore

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

// RuleSpec is a rule as written in a rules file. Everything a rule needs is
// data, so adding one is editing JSON rather than editing this program.
//
//	{
//	  "id": "error-swallowed",
//	  "severity": "warning",
//	  "why": "what goes wrong, in one or two sentences",
//	  "where": { "ext": [".go", ".js"], "kind": "catch" },
//	  "ask": {
//	    "instructions": "the one judgment a regex cannot make",
//	    "yes": "what a yes means",
//	    "no":  "what a no means",
//	    "threshold": 0.75
//	  },
//	  "message": "the finding text"
//	}
type RuleSpec struct {
	ID       string    `json:"id"`
	Severity string    `json:"severity"`
	Why      string    `json:"why"`
	Where    WhereSpec `json:"where"`
	// Check makes the rule deterministic. When present, no question is sent.
	Check *CheckSpec `json:"check,omitempty"`
	// Ask is the semantic half, for anything Check cannot decide.
	Ask     *AskSpec `json:"ask,omitempty"`
	Message string   `json:"message"`
	// Disabled keeps a rule in the file without running it.
	Disabled bool `json:"disabled,omitempty"`
}

// WhereSpec selects the sites a rule applies to. Give either a pattern or a
// kind; kind resolves per language so one rule can cover several of them.
type WhereSpec struct {
	Ext     []string `json:"ext,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Kind    string   `json:"kind,omitempty"` // function, catch, comment, return-error
}

// CheckSpec is a deterministic verdict built from regexes. A rule whose answer
// is decided by which tokens appear belongs here, not in Ask: it costs nothing
// and cannot be wrong in the confident way a model can.
//
// All the conditions that are present must hold for the rule to fire.
type CheckSpec struct {
	// LineHas fires when every pattern appears on the matched line.
	LineHas []string `json:"line_has,omitempty"`
	// LineLacks fires when any pattern is absent from the matched line.
	LineLacks []string `json:"line_lacks,omitempty"`
	// UnitLacks fires when any pattern is absent from the enclosing unit.
	UnitLacks []string `json:"unit_lacks,omitempty"`
	// Always fires on every matched site; use when the pattern is the whole rule.
	Always bool `json:"always,omitempty"`
}

// AskSpec is one question. Omit Levels for a yes/no judgment, or give at least
// two for a graded one.
type AskSpec struct {
	Instructions string   `json:"instructions"`
	Yes          string   `json:"yes,omitempty"`
	No           string   `json:"no,omitempty"`
	Levels       []string `json:"levels,omitempty"`
	// Threshold is the probability at or above which a yes/no rule fires.
	// Calibrate it: run with 0.01, read both distributions, and put it in the
	// gap between them rather than on the edge of one.
	Threshold float64 `json:"threshold,omitempty"`
	// AtLeast and MinConfidence apply to a graded judgment.
	AtLeast       float64 `json:"at_least,omitempty"`
	MinConfidence float64 `json:"min_confidence,omitempty"`
}

// RuleFile is the top level of a rules file.
type RuleFile struct {
	// Name and Description are for humans reading a rules file.
	Name        string     `json:"name,omitempty"`
	Description string     `json:"description,omitempty"`
	Rules       []RuleSpec `json:"rules"`
}

// languageProfile maps a `kind` to the pattern that finds it in one language.
// This is what lets a rule about swallowed errors apply to Go and JavaScript
// from the same definition.
var LanguageProfiles = map[string]map[string]string{
	".go": {
		"function":     `^\s*func\s+(\([^)]*\)\s*)?[A-Z_a-z]`,
		"catch":        `^\s*if\s+err\s*!=\s*nil\s*\{|\brecover\s*\(\s*\)`,
		"comment":      `^\s*//`,
		"return-error": `\breturn\b[^\n]*\berr\b|\bfmt\.Errorf\s*\(|\berrors\.New\s*\(`,
	},
	".js": {
		// Named definitions only. A bare `(function () {` wrapper would
		// otherwise match and make the whole file one unit, which is both
		// expensive and too coarse to point at anything.
		"function":     `\bfunction\s+[A-Za-z_$][\w$]*\s*\(|\b(?:const|let|var)\s+[A-Za-z_$][\w$]*\s*=\s*(?:async\s*)?(?:function\b|\([^)]*\)\s*=>)|^\s*(?:async\s+)?[A-Za-z_$][\w$]*\s*\([^)]*\)\s*\{`,
		"catch":        `\bcatch\s*\(|\.catch\s*\(`,
		"comment":      `^\s*(//|/\*|\*)`,
		"return-error": `\bthrow\s+new\b|\bthrow\b|\breject\s*\(`,
	},
	".ts": {
		"function":     `\bfunction\s+[A-Za-z_$][\w$]*\s*\(|\b(?:const|let|var)\s+[A-Za-z_$][\w$]*\s*=\s*(?:async\s*)?(?:function\b|\([^)]*\)\s*=>)|^\s*(?:async\s+)?[A-Za-z_$][\w$]*\s*\([^)]*\)\s*[:{]`,
		"catch":        `\bcatch\s*[({]|\.catch\s*\(`,
		"comment":      `^\s*(//|/\*|\*)`,
		"return-error": `\bthrow\s+new\b|\bthrow\b|\breject\s*\(`,
	},
	// Python is deliberately absent. The unit extractor finds block boundaries
	// by counting braces, which says nothing about an indentation-delimited
	// language: every Python unit would fall back to a line window that
	// straddles function boundaries, and findings would point at unrelated
	// code. Supporting it means an indentation-aware extractor, not another
	// entry in this table.
}

// extensionsFor returns the file types a rule covers, defaulting to every
// language that knows the kind it asks for.
func (w WhereSpec) extensionsFor() []string {
	if len(w.Ext) > 0 {
		return w.Ext
	}
	if w.Kind == "" {
		return nil
	}
	var out []string
	for ext, profile := range LanguageProfiles {
		if _, ok := profile[w.Kind]; ok {
			out = append(out, ext)
		}
	}
	return out
}

// Compile turns specs into runnable rules, one per (rule, extension) pair so a
// single definition can cover several languages.
func Compile(specs []RuleSpec) ([]Rule, error) {
	var out []Rule
	seen := map[string]bool{}
	for _, sp := range specs {
		if sp.Disabled {
			continue
		}
		if sp.ID == "" {
			return nil, fmt.Errorf("rule with no id")
		}
		if sp.Check == nil && sp.Ask == nil {
			return nil, fmt.Errorf("rule %q has neither check nor ask", sp.ID)
		}
		if sp.Check != nil && sp.Ask != nil {
			return nil, fmt.Errorf("rule %q has both check and ask; a decidable rule needs only check", sp.ID)
		}
		if sp.Severity == "" {
			sp.Severity = "warning"
		}
		if _, ok := severityRank[sp.Severity]; !ok {
			return nil, fmt.Errorf("rule %q: unknown severity %q", sp.ID, sp.Severity)
		}

		exts := sp.Where.extensionsFor()
		if len(exts) == 0 {
			return nil, fmt.Errorf("rule %q: `where` needs ext, kind, or both", sp.ID)
		}
		for _, ext := range exts {
			pattern := sp.Where.Pattern
			if pattern == "" {
				profile, ok := LanguageProfiles[ext]
				if !ok {
					return nil, fmt.Errorf("rule %q: no language profile for %q", sp.ID, ext)
				}
				p, ok := profile[sp.Where.Kind]
				if !ok {
					continue // this language has no such construct; skip it
				}
				pattern = p
			}
			site, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("rule %q: site pattern: %w", sp.ID, err)
			}

			r := Rule{
				ID:       sp.ID,
				Severity: sp.Severity,
				Why:      sp.Why,
				Message:  sp.Message,
				Selector: Selector{Ext: ext, Site: site},
			}
			if sp.Check != nil {
				check, err := compileCheck(sp.ID, *sp.Check)
				if err != nil {
					return nil, err
				}
				r.Check = check
			} else {
				q, fires, err := compileAsk(sp.ID, *sp.Ask)
				if err != nil {
					return nil, err
				}
				r.Question, r.Fires = q, fires
			}

			key := sp.ID + "|" + ext
			if seen[key] {
				return nil, fmt.Errorf("duplicate rule %q for %s", sp.ID, ext)
			}
			seen[key] = true
			out = append(out, r)
		}
	}
	return out, nil
}

func compileCheck(id string, c CheckSpec) (func(Site) bool, error) {
	compile := func(ps []string) ([]*regexp.Regexp, error) {
		var out []*regexp.Regexp
		for _, p := range ps {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("rule %q: check pattern %q: %w", id, p, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	has, err := compile(c.LineHas)
	if err != nil {
		return nil, err
	}
	lacks, err := compile(c.LineLacks)
	if err != nil {
		return nil, err
	}
	unitLacks, err := compile(c.UnitLacks)
	if err != nil {
		return nil, err
	}
	if !c.Always && len(has)+len(lacks)+len(unitLacks) == 0 {
		return nil, fmt.Errorf("rule %q: check has no conditions", id)
	}
	return func(s Site) bool {
		for _, re := range has {
			if !re.MatchString(s.Text) {
				return false
			}
		}
		for _, re := range lacks {
			if re.MatchString(s.Text) {
				return false
			}
		}
		for _, re := range unitLacks {
			if re.MatchString(s.Unit) {
				return false
			}
		}
		return true
	}, nil
}

func compileAsk(id string, a AskSpec) (typesafe.Question, func(typesafe.Answer) (bool, float64), error) {
	if strings.TrimSpace(a.Instructions) == "" {
		return typesafe.Question{}, nil, fmt.Errorf("rule %q: ask needs instructions", id)
	}
	if len(a.Levels) > 0 {
		if len(a.Levels) < 2 {
			return typesafe.Question{}, nil, fmt.Errorf("rule %q: a graded question needs at least two levels", id)
		}
		levels := make([]any, len(a.Levels))
		for i, l := range a.Levels {
			levels[i] = l
		}
		at := a.AtLeast
		if at == 0 {
			at = float64(len(a.Levels)-1) / 2
		}
		minc := a.MinConfidence
		if minc == 0 {
			minc = 0.5
		}
		return typesafe.Score(a.Instructions+" "+abstentionClause, levels...), scoreFires(at, minc), nil
	}
	th := a.Threshold
	if th == 0 {
		th = 0.75
	}
	if th <= 0 || th >= 1 {
		return typesafe.Question{}, nil, fmt.Errorf("rule %q: threshold must be between 0 and 1", id)
	}
	instructions := a.Instructions + " " + abstentionClause
	var q typesafe.Question
	if a.Yes != "" || a.No != "" {
		q = typesafe.NoulWith(instructions, a.Yes, a.No)
	} else {
		q = typesafe.Noul(instructions)
	}
	return q, noulFires(th), nil
}

// abstentionClause is appended to every question. Reporting a defect from code
// that was never shown is the worst failure this tool has, so the instruction
// to abstain is attached by the compiler rather than left to each rule author.
const abstentionClause = "Finally, read `context_completeness`: if it says the context is incomplete and the evidence you would need falls in the part that was cut, answer no."

// LoadRuleFile reads and compiles one rules file.
func LoadRuleFile(path string) ([]Rule, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseRuleBytes(b, path)
}

func parseRuleBytes(b []byte, source string) ([]Rule, error) {
	var f RuleFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if len(f.Rules) == 0 {
		return nil, fmt.Errorf("%s: no rules", source)
	}
	compiled, err := Compile(f.Rules)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return compiled, nil
}

var severityRank = map[string]int{"hint": 0, "warning": 1, "error": 2}
