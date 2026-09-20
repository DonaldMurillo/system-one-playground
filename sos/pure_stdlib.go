package sos

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

func registerPure(module, name string, params []NativeParam, result, description string, fn func([]any) (any, error)) {
	if stdRegistry[module] == nil {
		stdRegistry[module] = map[string]NativeOp{}
	}
	stdRegistry[module][name] = NativeOp{Name: name, Params: params, Fn: fn}
	stdDocs[module+"."+name] = stdDoc{result, description, []string{"pure"}}
}
func stringList(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
func listIndex(value any, length int, allowEnd bool) (int, error) {
	n, ok := number(value)
	limit := length
	if allowEnd {
		limit++
	}
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || math.Trunc(n) != n || n >= float64(limit) {
		return 0, fmt.Errorf("index must be an integer within the collection bounds")
	}
	return int(n), nil
}
func init() {
	registerPure("std/list", "percentile", []NativeParam{{"numbers", "list"}, {"percent", "number"}}, "number", "Returns the sorted sample at floor(percent/100 * (count-1)); percent is 0..100 and the sample must be nonempty and finite.", func(args []any) (any, error) {
		input := args[0].([]any)
		if len(input) == 0 {
			return nil, fmt.Errorf("percentile requires a nonempty sample")
		}
		percent, _ := number(args[1])
		if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
			return nil, fmt.Errorf("percent must be from 0 through 100")
		}
		values := make([]float64, len(input))
		for i, v := range input {
			n, ok := number(v)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, fmt.Errorf("sample must contain finite numbers")
			}
			values[i] = n
		}
		sort.Float64s(values)
		return values[int(math.Floor(percent/100*float64(len(values)-1)))], nil
	})
	registerPure("std/text", "split", []NativeParam{{"text", "text"}, {"separator", "text"}}, "list", "Splits text at each separator; an empty separator splits Unicode code points.", func(args []any) (any, error) {
		return stringList(strings.Split(args[0].(string), args[1].(string))), nil
	})
	registerPure("std/text", "lines", []NativeParam{{"text", "text"}}, "list", "Splits LF or CRLF lines, preserving a final empty line.", func(args []any) (any, error) {
		return stringList(strings.Split(strings.ReplaceAll(args[0].(string), "\r\n", "\n"), "\n")), nil
	})
	registerPure("std/text", "slice", []NativeParam{{"text", "text"}, {"start", "number"}, {"end", "number"}}, "text", "Returns the half-open zero-based Unicode code-point range [start,end). Out-of-range indices fail.", func(args []any) (any, error) {
		r := []rune(args[0].(string))
		start, err := listIndex(args[1], len(r), true)
		if err != nil {
			return nil, err
		}
		end, err := listIndex(args[2], len(r), true)
		if err != nil {
			return nil, err
		}
		if end < start {
			return nil, fmt.Errorf("end must be at least start")
		}
		return string(r[start:end]), nil
	})
	registerPure("std/text", "replace", []NativeParam{{"text", "text"}, {"old", "text"}, {"new", "text"}}, "text", "Replaces every literal occurrence of old with new.", func(args []any) (any, error) {
		return strings.ReplaceAll(args[0].(string), args[1].(string), args[2].(string)), nil
	})
	registerPure("std/text", "matches", []NativeParam{{"text", "text"}, {"pattern", "text"}}, "list", "Finds Go/RE2 regex matches; each record contains text, byteStart/byteEnd (zero-based), line/column (one-based code points), and groups.", regexMatches)
	registerPure("std/list", "at", []NativeParam{{"list", "list"}, {"index", "number"}}, "any", "Reads a zero-based list element; out-of-range indices fail.", func(args []any) (any, error) {
		values := args[0].([]any)
		i, err := listIndex(args[1], len(values), false)
		if err != nil {
			return nil, err
		}
		return values[i], nil
	})
	registerPure("std/list", "flatten", []NativeParam{{"list", "list"}}, "list", "Flattens one list level into a new list; every input element must be a list.", func(args []any) (any, error) {
		out := []any{}
		for _, v := range args[0].([]any) {
			list, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("flatten requires a list of lists")
			}
			out = append(out, list...)
		}
		return out, nil
	})
	registerPure("std/record", "get", []NativeParam{{"record", "record"}, {"key", "text"}}, "any", "Reads a dynamic record field; a missing field is an error.", func(args []any) (any, error) {
		v, ok := args[0].(map[string]any)[args[1].(string)]
		if !ok {
			return nil, fmt.Errorf("record has no field %q", args[1])
		}
		return v, nil
	})
	registerPure("std/record", "set", []NativeParam{{"record", "record"}, {"key", "text"}, {"value", "any"}}, "record", "Returns a new record with the named field set; does not mutate the original.", func(args []any) (any, error) {
		out := map[string]any{}
		for k, v := range args[0].(map[string]any) {
			out[k] = v
		}
		out[args[1].(string)] = args[2]
		return out, nil
	})
	registerPure("std/record", "keys", []NativeParam{{"record", "record"}}, "list", "Returns record keys sorted lexicographically.", func(args []any) (any, error) {
		keys := []string{}
		for k := range args[0].(map[string]any) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return stringList(keys), nil
	})
}
func regexMatches(args []any) (any, error) {
	text := args[0].(string)
	re, err := regexp.Compile(args[1].(string))
	if err != nil {
		return nil, err
	}
	indices := re.FindAllStringSubmatchIndex(text, 100001)
	if len(indices) > 100000 {
		return nil, fmt.Errorf("regex exceeds 100000 matches")
	}
	out := []any{}
	line, column, position := 1, 1, 0
	for _, m := range indices {
		for position < m[0] {
			r, size := utf8.DecodeRuneInString(text[position:])
			position += size
			if r == '\n' {
				line++
				column = 1
			} else {
				column++
			}
		}
		groups := []any{}
		for i := 2; i < len(m); i += 2 {
			if m[i] < 0 {
				groups = append(groups, nil)
			} else {
				groups = append(groups, text[m[i]:m[i+1]])
			}
		}
		out = append(out, map[string]any{"text": text[m[0]:m[1]], "byteStart": float64(m[0]), "byteEnd": float64(m[1]), "line": float64(line), "column": float64(column), "groups": groups})
	}
	return out, nil
}
