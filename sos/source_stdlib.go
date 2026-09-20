package sos

import (
	"encoding/json"
	"fmt"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/internal/semcore"
	"unicode/utf8"
)

func sourceDecode(v any, dst any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, dst)
}
func sourceValue(v any) (any, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var out any
	e = json.Unmarshal(b, &out)
	return out, e
}
func init() {
	ops := map[string]NativeOp{}
	add := func(name, desc string, params []NativeParam, fn func([]any) (any, error)) {
		ops[name] = NativeOp{Name: name, Params: params, Fn: fn}
		stdDocs["std/source."+name] = stdDoc{"any", desc, []string{"pure"}}
	}
	add("supported", "Reports whether a path has a supported source language.", []NativeParam{{"path", "text"}}, func(a []any) (any, error) { return semcore.Supported(a[0].(string)), nil })
	add("builtin", "Returns validated bundled lint rule records.", []NativeParam{{"name", "text"}}, func(a []any) (any, error) {
		names := []string{a[0].(string)}
		if names[0] == "all" {
			names = []string{"default", "browser-storage"}
		}
		specs := []semcore.RuleSpec{}
		for _, n := range names {
			r, e := semcore.BuiltinSpecs(n)
			if e != nil {
				return nil, e
			}
			specs = append(specs, r...)
		}
		return sourceValue(specs)
	})
	add("rules", "Parses and validates a semlint JSON rule file.", []NativeParam{{"json", "text"}}, func(a []any) (any, error) {
		r, e := semcore.ParseSpecs([]byte(a[0].(string)))
		if e != nil {
			return nil, e
		}
		return sourceValue(r)
	})
	add("extract", "Extracts source units and explicit context cuts; path is a label, not a file read.", []NativeParam{{"path", "text"}, {"text", "text"}, {"rules", "any"}}, func(a []any) (any, error) {
		var specs []semcore.RuleSpec
		if e := sourceDecode(a[2], &specs); e != nil {
			return nil, e
		}
		rules, e := semcore.Compile(specs)
		if e != nil {
			return nil, e
		}
		if !semcore.Supported(a[0].(string)) {
			return []any{}, nil
		}
		units := semcore.ExtractText(a[0].(string), a[1].(string), rules)
		if units == nil {
			units = []semcore.Unit{}
		}
		return sourceValue(units)
	})
	add("prepare", "Builds named question batches, cross-file context and deterministic findings; never calls Jev.", []NativeParam{{"units", "any"}, {"rules", "any"}}, func(a []any) (any, error) {
		var units []semcore.Unit
		var specs []semcore.RuleSpec
		if e := sourceDecode(a[0], &units); e != nil {
			return nil, e
		}
		if e := sourceDecode(a[1], &specs); e != nil {
			return nil, e
		}
		p, e := semcore.Prepare(units, specs)
		if e != nil {
			return nil, e
		}
		return sourceValue(p)
	})
	add("diff", "Parses added/new-side unified diff ranges relative to root.", []NativeParam{{"text", "text"}, {"root", "text"}}, func(a []any) (any, error) {
		v, e := semcore.ParseUnifiedDiff(strings.NewReader(a[0].(string)), a[1].(string))
		if e != nil {
			return nil, e
		}
		return sourceValue(v)
	})
	add("touches", "Tests whether a unit overlaps parsed changed lines.", []NativeParam{{"changes", "any"}, {"path", "text"}, {"start", "number"}, {"end", "number"}}, func(a []any) (any, error) {
		var c semcore.ChangedLines
		if e := sourceDecode(a[0], &c); e != nil {
			return nil, e
		}
		var start, end int
		if e := sourceDecode(a[2], &start); e != nil {
			return nil, fmt.Errorf("start must be an integer")
		}
		if e := sourceDecode(a[3], &end); e != nil {
			return nil, fmt.Errorf("end must be an integer")
		}
		return c.Touches(a[1].(string), start, end), nil
	})
	add("reduce", "Drops surrounding context and caps code at 40000 bytes for an explicit oversized-input retry, preserving disclosure.", []NativeParam{{"state", "any"}}, func(a []any) (any, error) {
		var state map[string]any
		if e := sourceDecode(a[0], &state); e != nil {
			return nil, e
		}
		if state == nil {
			return nil, fmt.Errorf("state must be a record")
		}
		for _, field := range []string{"other_matching_lines_in_file", "enclosing_scope", "module_scope_declarations", "file_header_comment", "related_lines_in_other_files"} {
			state[field] = ""
		}
		note := "INCOMPLETE. "
		if previous, ok := state["context_completeness"].(string); ok && previous != "complete" {
			note += previous + " "
		}
		if code, ok := state["code"].(string); ok && len(code) > 40000 {
			end := 40000
			for end > 0 && !utf8.RuneStart(code[end]) {
				end--
			}
			state["code"] = code[:end]
			note += "Code was cut to its first 40000 bytes because the unit was too large; later code is not shown. "
		}
		state["context_completeness"] = note + "All surrounding context was omitted because the unit was too large to send."
		return state, nil
	})
	stdRegistry["std/source"] = ops
}
