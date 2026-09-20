package sos

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// criterionDecl is a reusable semantic criterion declared as
//
//	criterion urgent:
//	  ask "The customer is blocked from doing their work"
//	  using message, status
//	  accept probability at least 0.85
//	  on uncertain discard
//
// ask, the threshold, and the uncertainty policy are required; using and
// model are optional. Declarations record a runtime judgment; they lower to
// blank lines so the canonical program contains only their applications.
type criterionDecl struct {
	name      string
	ask       string
	using     []string
	model     string
	threshold float64
	uncertain string
	lineNums  []int
	problems  []string
}

var criterionFieldRe = regexp.MustCompile(`^[A-Za-z_]\w*$`)

// parseCriterionDecl validates a declaration and its members. Every problem
// is source-located on the declaration line; the canonical lowering exists
// only for valid declarations.
func parseCriterionDecl(n *semNode) *criterionDecl {
	d := &criterionDecl{name: n.m[1], lineNums: []int{n.line.num}}
	asks, usings, accepts, uncertains, models := 0, 0, 0, 0, 0
	for _, c := range n.children {
		d.lineNums = append(d.lineNums, c.line.num)
		if len(c.children) > 0 {
			d.problems = append(d.problems, "statements cannot be nested inside a criterion")
		}
		switch c.form {
		case "ask":
			asks++
			value := match("ask", c.text)[1]
			if q, ok := quotedLiteral(value); ok && !strings.ContainsAny(q, "{}") {
				if d.ask == "" {
					d.ask = q
				}
			} else {
				d.problems = append(d.problems, "criterion ask must be a literal question without {expression} interpolation")
			}
		case "using":
			usings++
			fields := strings.Split(match("using", c.text)[1], ",")
			seen := map[string]bool{}
			for _, f := range fields {
				f = strings.TrimSpace(f)
				if !criterionFieldRe.MatchString(f) {
					d.problems = append(d.problems, "criterion using fields must be plain field names")
					continue
				}
				if seen[f] {
					d.problems = append(d.problems, "duplicate criterion using field "+f)
					continue
				}
				seen[f] = true
				d.using = append(d.using, f)
			}
		case "accept":
			accepts++
			value := match("accept", c.text)[1]
			t, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || math.IsNaN(t) || math.IsInf(t, 0) || t <= 0.5 || t > 1 {
				d.problems = append(d.problems, "criterion threshold must be greater than 0.5 and at most 1")
			} else {
				d.threshold = t
			}
		case "model":
			models++
			if m, ok := quotedLiteral(match("model", c.text)[1]); ok && d.model == "" {
				d.model = m
			} else if !ok {
				d.problems = append(d.problems, "criterion model must be a quoted model name")
			}
		case "handler":
			hm := match("handler", c.text)
			if hm[1] != "uncertain" {
				d.problems = append(d.problems, "only on uncertain is allowed inside a criterion")
				continue
			}
			if hm[2] != "keep" && hm[2] != "discard" && hm[2] != "stop" {
				d.problems = append(d.problems, "criterion on uncertain expects keep, discard, or stop")
				continue
			}
			uncertains++
			d.uncertain = hm[2]
		default:
			d.problems = append(d.problems, fmt.Sprintf("unsupported criterion member on line %d: %s", c.line.num, c.text))
		}
	}
	if asks == 0 {
		d.problems = append(d.problems, "criterion requires ask with a literal question")
	} else if asks > 1 {
		d.problems = append(d.problems, "criterion allows exactly one ask")
	}
	if usings > 1 {
		d.problems = append(d.problems, "criterion allows at most one using")
	}
	if accepts == 0 {
		d.problems = append(d.problems, "criterion requires accept probability at least with a threshold")
	} else if accepts > 1 {
		d.problems = append(d.problems, "criterion allows exactly one accept")
	}
	if uncertains == 0 {
		d.problems = append(d.problems, "criterion requires on uncertain keep, discard, or stop")
	} else if uncertains > 1 {
		d.problems = append(d.problems, "criterion allows exactly one on uncertain")
	}
	if models > 1 {
		d.problems = append(d.problems, "criterion allows at most one model")
	}
	return d
}

// quotedLiteral returns the value of a single double-quoted literal.
func quotedLiteral(s string) (string, bool) {
	tokens, err := lex(strings.TrimSpace(s))
	if err != nil || len(tokens) != 1 || !tokens[0].quoted {
		return "", false
	}
	return tokens[0].text, true
}

func (d *criterionDecl) valid() bool { return len(d.problems) == 0 }

// lowering emits the canonical keep block that applies this criterion to a
// collection. Lines carry no indentation; the resolver indents them.
func (d *criterionDecl) lowering(referent string) []string {
	question, _ := json.Marshal(d.ask)
	lines := []string{"keep " + referent + " where jev:", "ask " + string(question)}
	if len(d.using) > 0 {
		lines = append(lines, "using "+strings.Join(d.using, ", "))
	}
	if d.model != "" {
		name, _ := json.Marshal(d.model)
		lines = append(lines, "model "+string(name))
	}
	lines = append(lines, "accept probability at least "+strconv.FormatFloat(d.threshold, 'g', -1, 64))
	lines = append(lines, "on uncertain "+d.uncertain)
	return lines
}
