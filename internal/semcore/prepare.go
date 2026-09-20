package semcore

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BuiltinSpecs returns editable rule data for a bundled ruleset.
func BuiltinSpecs(name string) ([]RuleSpec, error) {
	b, err := builtinRules.ReadFile("rules/" + name + ".json")
	if err != nil {
		return nil, err
	}
	return ParseSpecs(b)
}

// ParseSpecs validates a JSON rule file without evaluating any question.
func ParseSpecs(b []byte) ([]RuleSpec, error) {
	var f RuleFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if len(f.Rules) == 0 {
		return nil, fmt.Errorf("no rules")
	}
	if _, err := Compile(f.Rules); err != nil {
		return nil, err
	}
	return f.Rules, nil
}

// Supported reports whether semlint has a language profile for the path.
func Supported(path string) bool {
	for ext := range LanguageProfiles {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return strings.HasSuffix(path, ".mjs") || strings.HasSuffix(path, ".cjs")
}

// Prepare constructs batched questions and bounded context, leaving judgment and
// threshold/report policy to the caller. Units must be the complete scan if
// cross-file context is desired.
func Prepare(units []Unit, specs []RuleSpec) ([]map[string]any, error) {
	compiled, err := Compile(specs)
	if err != nil {
		return nil, err
	}
	byID := map[string]Rule{}
	spByID := map[string]RuleSpec{}
	for _, r := range compiled {
		if _, ok := byID[r.ID]; !ok {
			byID[r.ID] = r
		}
	}
	for _, r := range specs {
		if _, exists := spByID[r.ID]; !exists {
			spByID[r.ID] = r
		}
	}
	ix := BuildCrossIndex(units)
	out := make([]map[string]any, 0, len(units))
	for _, u := range units {
		qs := []any{}
		rules := []any{}
		det := []any{}
		for _, id := range u.RuleIDs() {
			r, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("unknown unit rule %q", id)
			}
			var first *Site
			for i := range u.Sites {
				s := &u.Sites[i]
				if s.RuleID != id {
					continue
				}
				if first == nil {
					first = s
				}
				if r.Check != nil && r.Check(*s) {
					det = append(det, finding(u, *s, r))
				}
			}
			if r.Check != nil || first == nil {
				continue
			}
			q := map[string]any{"type": r.Question.Type, "instructions": r.Question.Instructions}
			if r.Question.Criteria != nil {
				q["criteria"] = r.Question.Criteria
			}
			qs = append(qs, map[string]any{"id": id, "question": q})
			a := spByID[id].Ask
			threshold := a.Threshold
			if threshold == 0 {
				threshold = .75
			}
			minc := a.MinConfidence
			if minc == 0 {
				minc = .5
			}
			at := a.AtLeast
			if at == 0 {
				at = float64(len(a.Levels)-1) / 2
			}
			meta := finding(u, *first, r)
			delete(meta, "confidence")
			meta["id"] = id
			meta["type"] = r.Question.Type
			meta["kind"] = r.Question.Type
			meta["threshold"] = threshold
			meta["at_least"] = at
			meta["min_confidence"] = minc
			rules = append(rules, meta)
		}
		state := map[string]any{"file": u.File, "code": u.Code, "file_header_comment": u.Header, "module_scope_declarations": u.Module, "enclosing_scope": u.Enclosing, "other_matching_lines_in_file": u.Peers, "related_lines_in_other_files": ix.Related(u), "line_numbers_are_real": "Every line in `code` is prefixed with its real line number in the file."}
		for _, id := range u.RuleIDs() {
			state["site_line_"+strings.ReplaceAll(id, "-", "_")] = u.SiteLines(id)
		}
		lines := []int{}
		for _, s := range u.Sites {
			lines = append(lines, s.Line)
		}
		state["site_line"] = lines
		cut := append([]string(nil), u.Cut...)
		trimmed := false
		for _, drop := range []struct{ field, note string }{
			{"related_lines_in_other_files", "`related_lines_in_other_files` was omitted because the request was too large, so the other side of anything spanning files is not shown."},
			{"other_matching_lines_in_file", "`other_matching_lines_in_file` was omitted because the request was too large."},
			{"enclosing_scope", "`enclosing_scope` was omitted because the request was too large, so a guard installed by the calling function is not shown."},
			{"module_scope_declarations", "`module_scope_declarations` was omitted because the request was too large, so helpers and constants this code calls are not shown."},
			{"file_header_comment", "The file header comment was omitted because the request was too large."},
		} {
			n := 0
			for k, v := range state {
				n += len(k)
				if s, ok := v.(string); ok {
					n += len(s)
				}
			}
			if n/3 <= 26000 {
				break
			}
			if state[drop.field] == "" {
				continue
			}
			state[drop.field] = ""
			cut = append(cut, drop.note)
			trimmed = true
		}
		state["context_completeness"] = "complete"
		if len(cut) > 0 {
			state["context_completeness"] = "INCOMPLETE. " + strings.Join(cut, " ")
		}
		out = append(out, map[string]any{"file": u.File, "start_line": u.StartLine, "end_line": u.EndLine, "state": state, "questions": qs, "rules": rules, "deterministic_findings": det, "trimmed": trimmed, "partial_context": len(cut) > 0})
	}
	return out, nil
}
func finding(u Unit, s Site, r Rule) map[string]any {
	return map[string]any{"file": u.File, "line": s.Line, "unit_start": u.StartLine, "unit_end": u.EndLine, "rule": r.ID, "severity": r.Severity, "message": r.Message, "why": r.Why, "confidence": 1, "snippet": s.Text}
}
