package sos

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// NativeParam is one typed parameter of a native standard-library operation.
// Type reuses the schema vocabulary plus "any" for unconstrained values.
type NativeParam struct {
	Name string
	Type string
}

// NativeOp is a typed standard-library operation. Fn is pure; ContextFn
// supplies bounded host I/O using the run context and options. Provider calls
// remain canonical runtime operations with explicit budget admission.
// Synonyms are alternate words for the same operation
// (upper/uppercase); the name always works too.
type NativeOp struct {
	ContextFn func(context.Context, Options, []any) (any, error)
	Targets   []string
	Name      string
	Params    []NativeParam
	Synonyms  []string
	Fn        func(args []any) (any, error)
}

// check validates argument values against the typed signature.
func (op NativeOp) check(vals []any) error {
	if len(vals) != len(op.Params) {
		return fmt.Errorf("%s expects %d argument(s), got %d", op.Name, len(op.Params), len(vals))
	}
	for i, p := range op.Params {
		if p.Type == "any" || typeMatches(vals[i], p.Type) {
			continue
		}
		return fmt.Errorf("argument %s of %s must be %s", p.Name, op.Name, p.Type)
	}
	return nil
}

func textOp(name string, synonyms []string, fn func(string) string) NativeOp {
	return NativeOp{
		Name:     name,
		Params:   []NativeParam{{Name: "value", Type: "text"}},
		Synonyms: synonyms,
		Fn:       func(args []any) (any, error) { return fn(args[0].(string)), nil },
	}
}

// stdDoc is the offline documentation record for one standard operation:
// result type, human description, and effect summary. It is the single source
// for StandardOperations and the vocabulary catalog.
type stdDoc struct {
	result      string
	description string
	effects     []string
}

var stdDocs = map[string]stdDoc{
	"std/text.trim":   {"text", "Returns text with leading and trailing Unicode whitespace removed.", []string{"pure"}},
	"std/text.upper":  {"text", "Returns text converted to uppercase.", []string{"pure"}},
	"std/text.lower":  {"text", "Returns text converted to lowercase.", []string{"pure"}},
	"std/json.encode": {"text", "Encodes a value as JSON text.", []string{"pure"}},
	"std/json.decode": {"any", "Decodes JSON text into a value; invalid JSON is an error.", []string{"pure"}},
}

// stdRegistry is the typed operation registry for standard packages. It is
var stdRegistry = map[string]map[string]NativeOp{
	"std/text": {
		"trim":  textOp("trim", nil, strings.TrimSpace),
		"upper": textOp("upper", []string{"uppercase"}, strings.ToUpper),
		"lower": textOp("lower", []string{"lowercase"}, strings.ToLower),
	},
	"std/json": {
		"encode": {
			Name:   "encode",
			Params: []NativeParam{{Name: "value", Type: "any"}},
			Fn: func(args []any) (any, error) {
				data, err := json.Marshal(args[0])
				if err != nil {
					return nil, fmt.Errorf("json encode: %w", err)
				}
				return string(data), nil
			},
		},
		"decode": {
			Name:   "decode",
			Params: []NativeParam{{Name: "text", Type: "text"}},
			Fn: func(args []any) (any, error) {
				var v any
				if err := json.Unmarshal([]byte(args[0].(string)), &v); err != nil {
					return nil, fmt.Errorf("json decode: %w", err)
				}
				return v, nil
			},
		},
	},
}

// stdModule materializes one standard package as a Module. All registered
// operations are exported, and Words maps every usable word (names and
// synonyms) to its canonical operation name.
func stdModule(key string) (*Module, bool) {
	ops, ok := stdRegistry[key]
	if !ok {
		return nil, false
	}
	name := key[strings.LastIndex(key, "/")+1:]
	m := &Module{
		Key:     key,
		Name:    name,
		Actions: map[string]*Statement{},
		Schemas: map[string]*Statement{},
		Native:  ops,
		Exports: map[string]bool{},
		Words:   map[string]string{},
	}
	for n, op := range ops {
		m.Exports[n] = true
		m.Words[n] = n
		for _, s := range op.Synonyms {
			m.Words[s] = n
		}
	}
	return m, true
}
