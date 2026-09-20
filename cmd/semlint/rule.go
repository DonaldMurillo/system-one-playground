package main

import (
	"sort"
	"github.com/DonaldMurillo/system-one-playground/internal/semcore"
	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

type Rule = semcore.Rule
type Selector = semcore.Selector

var LoadBuiltin = semcore.LoadBuiltin
var builtinSets = []string{"default", "browser-storage"}

func noulFires(threshold float64) func(typesafe.Answer) (bool, float64) {
	return func(a typesafe.Answer) (bool, float64) { return a.Noul >= threshold, a.Noul }
}

// ruleRegistry is the compiled battery for the current run.
type ruleRegistry struct {
	rules []Rule
	byID  map[string][]Rule
}

func newRegistry(rs []Rule) *ruleRegistry {
	reg := &ruleRegistry{rules: rs, byID: map[string][]Rule{}}
	for _, r := range rs {
		reg.byID[r.ID] = append(reg.byID[r.ID], r)
	}
	return reg
}

// get returns any compiled variant of a rule id. Variants differ only in which
// file type they select, and everything the caller needs is shared.
func (reg *ruleRegistry) get(id string) *Rule {
	if rs := reg.byID[id]; len(rs) > 0 {
		return &rs[0]
	}
	return nil
}

// ids lists every rule id, sorted.
func (reg *ruleRegistry) ids() []string {
	var out []string
	for id := range reg.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
