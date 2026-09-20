package sos

import (
	"fmt"
	"regexp"
	"strings"
)

// analyze checks names and expression structure without filesystem or provider access.
// Collection fields and runtime data shapes remain checked during execution.
func analyze(p *Program) []Diagnostic {
	names := map[string]bool{"error": true, "failure": true}
	for _, a := range p.Parameters {
		names[a.Name] = true
	}
	funcs := map[string]bool{}
	schemas := map[string]bool{}
	types := map[string]TypeRef{}
	actions := map[string]*Statement{}
	var ds []Diagnostic
	localDefinitions, definitionDiagnostics := collectRecordDefinitions(p.Statements)
	p.Definitions = localDefinitions
	ds = append(ds, definitionDiagnostics...)
	visibleDefs, importedTypeProblems := visibleDefinitions(localDefinitions, func() map[string]*Module {
		if p.Modules == nil {
			return nil
		}
		return p.Modules.Aliases
	}())
	for _, problem := range importedTypeProblems {
		ds = append(ds, Diagnostic{1, 1, problem})
	}
	for _, statement := range p.Statements {
		if statement.Kind == "to" {
			actions[match("to", statement.Text)[1]] = statement
		}
	}
	add := func(s *Statement, msg string) { ds = append(ds, Diagnostic{s.Line, 1, msg}) }
	checkActionArgs := func(s *Statement, action string, args []string) {
		fn := actions[action]
		if fn == nil {
			return
		}
		decl, err := parseActionDecl(fn.Text)
		if err != nil {
			return
		}
		for i, arg := range args {
			if i >= len(decl.Params) {
				break
			}
			if message := staticArgumentProblem(arg, decl.Params[i].Type, types, visibleDefs, action, decl.Params[i].Name); message != "" {
				add(s, message)
			}
		}
	}
	var checkExpr func(*Statement, string, bool)
	checkExpr = func(s *Statement, expr string, fields bool) {
		tokens, e := lex(expr)
		if e != nil {
			add(s, e.Error())
			return
		}
		if len(tokens) == 0 {
			add(s, "expected expression")
			return
		}
		depth := []string{}
		for i, t := range tokens {
			if t.quoted {
				for _, m := range regexp.MustCompile(`\{([^{}]+)\}`).FindAllStringSubmatch(t.text, -1) {
					checkExpr(s, m[1], fields)
				}
				continue
			}
			switch t.text {
			case "(", "[", "{":
				depth = append(depth, t.text)
				continue
			case ")", "]", "}":
				if len(depth) == 0 {
					add(s, "unmatched "+t.text)
					return
				}
				open := depth[len(depth)-1]
				if (open == "(" && t.text != ")") || (open == "[" && t.text != "]") || (open == "{" && t.text != "}") {
					add(s, "mismatched brackets")
					return
				}
				depth = depth[:len(depth)-1]
				continue
			}
			if i+1 < len(tokens) && tokens[i+1].text == ":" {
				continue
			}
			if i+1 < len(tokens) && tokens[i+1].text == "of" {
				if i+2 < len(tokens) && !tokens[i+2].quoted {
					if message := knownFieldProblem(t.text, tokens[i+2].text, types, visibleDefs, fields); message != "" {
						add(s, message)
					}
				}
				continue
			} // a field name in "field of object"
			base := strings.Split(t.text, ".")[0]
			if !validName(base) || names[base] || fields {
				if !fields && names[base] && strings.Contains(t.text, ".") {
					if message := dottedFieldProblem(t.text, types, visibleDefs); message != "" {
						add(s, message)
					}
				}
				continue
			}
			switch base {
			case "true", "false", "on", "off", "null", "now", "empty", "list", "record", "not", "count", "length", "of", "words", "first", "last", "items", "and", "or", "is", "contains", "plus", "minus", "times":
				continue
			}
			add(s, fmt.Sprintf("unknown name %q", base))
		}
		if len(depth) > 0 {
			add(s, "unclosed bracket")
		}
	}
	var walk func([]*Statement)
	walk = func(sts []*Statement) {
		for _, s := range sts {
			m := match(s.Kind, s.Text)
			switch s.Kind {
			case "command":
				oldNames, oldFuncs, oldSchemas, oldTypes := names, funcs, schemas, types
				names, funcs, schemas = copyNames(names), copyNames(funcs), copyNames(schemas)
				types = copyTypes(types)
				for _, child := range s.Body {
					if child.Kind == "parameter" {
						names[match("parameter", child.Text)[2]] = true
					}
				}
				walk(s.Body)
				names, funcs, schemas, types = oldNames, oldFuncs, oldSchemas, oldTypes
				continue
			case "describe", "parameter", "field", "choice":
				continue
			case "schema":
				schemas[m[1]] = true
				continue
			case "define":
				continue
			case "to":
				funcs[m[1]] = true
				decl, err := parseActionDecl(s.Text)
				if err != nil {
					add(s, err.Error())
					continue
				}
				seenParams := map[string]bool{}
				for _, param := range decl.Params {
					if seenParams[param.Name] {
						add(s, "duplicate action parameter "+param.Name)
					}
					seenParams[param.Name] = true
					if err := validateTypeRefs(param.Type, visibleDefs, map[string]bool{}); err != nil && param.Type.Name != "any" {
						add(s, "parameter "+param.Name+": "+err.Error())
					}
				}
				if len(decl.Using) > 0 {
					if len(decl.Params) != 1 || decl.Params[0].Type.Name == "any" || decl.Params[0].Type.Element != nil {
						add(s, "using is allowed only for one named-record parameter")
					} else if def := visibleDefs[decl.Params[0].Type.Name]; def == nil {
						add(s, "using requires a named-record parameter")
					} else {
						fields := fieldsByName(def)
						usingSeen := map[string]bool{}
						for _, field := range decl.Using {
							if usingSeen[field] {
								add(s, "using field "+field+" is selected more than once")
							}
							usingSeen[field] = true
							if _, ok := fields[field]; !ok {
								add(s, fmt.Sprintf("%s has no field %s", def.Name, field))
							}
							if seenParams[field] {
								add(s, "using field "+field+" conflicts with parameter name")
							}
							if actionBodyBinds(s.Body, field) {
								add(s, "using field "+field+" conflicts with a local binding")
							}
						}
					}
				}
				old, oldTypes := names, types
				names = copyNames(names)
				types = copyTypes(types)
				for _, param := range decl.Params {
					names[param.Name] = true
					if param.Type.Name != "any" {
						types[param.Name] = param.Type.base()
					}
				}
				for _, field := range decl.Using {
					names[field] = true
					if def := visibleDefs[decl.Params[0].Type.Name]; def != nil {
						if selected, ok := fieldsByName(def)[field]; ok {
							types[field] = selected.Type.base()
						}
					}
				}
				walk(s.Body)
				names, types = old, oldTypes
				continue
			case "sent":
				m := matchSent(s.Text)
				if p.Modules == nil || p.Modules.vocab == nil {
					add(s, "sentence calls require resolved imports")
				} else {
					target, action := sentTargetResolve(p.Modules.vocab, m[1])
					if target == nil {
						add(s, "unknown vocabulary word "+m[1])
					} else if args, e := sentArguments(m); e != nil {
						add(s, e.Error())
					} else if want, known := sentArity(target, action); known && len(args) != want {
						add(s, fmt.Sprintf("%s expects %d argument(s)", m[1], want))
					}
				}
				args, e := sentArguments(m)
				if e != nil {
					add(s, e.Error())
				}
				for _, v := range args {
					checkExpr(s, v, false)
				}
				checkActionArgs(s, m[1], args)
				if m[4] != "" {
					names[m[4]] = true
				}
			case "word":
				add(s, "word declarations are allowed only in packages")
			case "call":
				if strings.Contains(m[1], ".") {
					alias, action, _ := strings.Cut(m[1], ".")
					mod := moduleAlias(p, alias)
					if mod == nil {
						add(s, "unknown module alias "+alias)
					} else if !mod.Exports[action] {
						if mod.Actions[action] != nil || mod.Native[action].Name != "" {
							add(s, action+" is not exported by module "+mod.Name)
						} else {
							add(s, "unknown action "+m[1])
						}
					} else if op := mod.Native[action]; op.Name != "" {
						if args, e := splitExpressions(m[2]); e != nil {
							add(s, e.Error())
						} else if len(args) != len(op.Params) {
							add(s, fmt.Sprintf("%s expects %d argument(s)", m[1], len(op.Params)))
						}
					}
				} else if !funcs[m[1]] {
					add(s, "unknown action "+m[1])
				}
				args, e := splitExpressions(m[2])
				if e != nil {
					add(s, e.Error())
				}
				for _, v := range args {
					checkExpr(s, v, false)
				}
				checkActionArgs(s, m[1], args)
				if m[3] != "" {
					names[m[3]] = true
					delete(types, m[3])
				}
			case "remember":
				checkExpr(s, m[1], false)
				names[m[2]] = true
			case "make":
				expr := strings.TrimPrefix(m[2], "as ")
				if !strings.HasPrefix(s.Text, "make ") && !names[m[1]] {
					add(s, "cannot assign unknown name "+m[1])
				}
				if expr == "with:" || strings.HasSuffix(expr, " with:") {
					var definition *RecordDef
					if strings.HasSuffix(strings.TrimPrefix(m[2], "as "), " with:") {
						typeName := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m[2], "as "), " with:"))
						definition = visibleDefs[typeName]
						if definition == nil {
							add(s, "unknown type "+typeName)
						}
					}
					seenFields := map[string]bool{}
					if strings.HasSuffix(expr, " with:") {
						expr = "with:"
					}
					for _, f := range s.Body {
						if f.Kind == "field" {
							key, v, _ := strings.Cut(f.Text, " from ")
							if definition != nil {
								if seenFields[key] {
									add(f, fmt.Sprintf("%s field %s appears more than once", definition.Name, key))
								}
								seenFields[key] = true
								field, known := fieldsByName(definition)[key]
								if !known {
									add(f, fmt.Sprintf("%s has no field %s", definition.Name, key))
								} else if message := staticArgumentProblem(v, field.Type, types, visibleDefs, definition.Name, key); message != "" {
									add(f, message)
								}
							}
							checkExpr(f, v, false)
						}
					}
					if definition != nil {
						for _, field := range definition.Fields {
							if !field.Type.Optional && !seenFields[field.Name] {
								add(s, fmt.Sprintf("%s requires field %s as %s", definition.Name, field.Name, field.Type.String()))
							}
						}
					}
				} else {
					checkExpr(s, expr, false)
				}
				names[m[1]] = true
				if strings.HasSuffix(strings.TrimPrefix(m[2], "as "), " with:") {
					typeName := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m[2], "as "), " with:"))
					if _, ok := visibleDefs[typeName]; ok {
						types[m[1]] = TypeRef{Name: typeName}
					}
				}
			case "read":
				checkExpr(s, m[1], false)
				names[m[3]] = true
				delete(types, m[3])
			case "readEach":
				checkExpr(s, m[2], false)
				names[m[1]] = true
				names[m[3]] = true
			case "find":
				checkExpr(s, m[1], false)
				checkExpr(s, m[2], false)
				names[m[3]] = true
			case "require":
				checkExpr(s, m[2], false)
				if !schemas[m[3]] {
					add(s, "unknown schema "+m[3])
				}
			case "keep":
				if !names[m[1]] {
					add(s, "unknown name "+m[1])
				}
				if jevPredicate(m[2]) {
					if m[2] != "jev:" {
						checkExpr(s, strings.TrimSpace(strings.TrimPrefix(m[2], "jev")), true)
					} else {
						ask := false
						for _, c := range s.Body {
							if c.Kind == "ask" {
								ask = true
							}
						}
						if !ask {
							add(s, "jev block requires ask")
						}
					}
				} else {
					checkExpr(s, m[2], true)
				}
			case "sort", "group":
				if !names[m[1]] {
					add(s, "unknown name "+m[1])
				}
				checkExpr(s, m[2], true)
				if s.Kind == "group" {
					names[m[3]] = true
				}
			case "map":
				checkExpr(s, m[2], false)
				checkExpr(s, m[3], false)
				old := names
				names = copyNames(names)
				names[m[1]] = true
				names["number"] = true
				walk(s.Body)
				names = old
				names[m[4]] = true
				continue
			case "for":
				checkExpr(s, m[2], false)
				old := names
				names = copyNames(names)
				names[m[1]] = true
				names["number"] = true
				walk(s.Body)
				for k := range names {
					if k != m[1] && k != "number" {
						old[k] = true
					}
				}
				names = old
				continue
			case "when", "while":
				checkExpr(s, m[1], false)
				if v, e := evaluate(m[1], nil, nil); e == nil {
					if _, ok := v.(bool); !ok {
						add(s, s.Kind+" requires boolean")
					}
				}
			case "repeat", "return", "folder", "append":
				checkExpr(s, m[1], false)
				if s.Kind == "append" && !names[m[2]] {
					add(s, "unknown list "+m[2])
				}
			case "take":
				checkExpr(s, m[2], false)
				checkExpr(s, m[3], false)
				names[m[4]] = true
			case "evaluate":
				checkExpr(s, m[1], false)
				checkExpr(s, m[2], false)
				names[m[3]] = true
			case "classify", "score", "judge":
				checkExpr(s, m[1], false)
				if !strings.HasPrefix(m[2], "jev ") {
					add(s, "semantic judgments require by jev followed by a question")
				}
				checkExpr(s, strings.TrimPrefix(m[2], "jev "), false)
				names[m[3]] = true
				n := 0
				for _, c := range s.Body {
					if c.Kind == "choice" {
						n++
					}
				}
				if n < 2 && s.Kind != "judge" {
					add(s, "classify requires at least two choices")
				}
			case "show":
				expr, _, _ := splitOutside(m[1], " as table")
				checkExpr(s, expr, false)
			case "save":
				checkExpr(s, m[1], false)
				for _, v := range m[3:] {
					if v != "" {
						checkExpr(s, v, false)
					}
				}
			case "ask", "model", "accept":
				checkExpr(s, m[1], s.Kind == "ask")
			case "using": // names here refer to fields of the explicitly selected item
			case "stop":
				if m[1] != "" {
					checkExpr(s, m[1], false)
				}
			case "handler":
				if m[1] == "uncertain" && m[2] == "" {
					add(s, "on uncertain expects inline keep, discard, or stop")
				}
				if m[2] != "" {
					switch m[1] {
					case "uncertain":
						if m[2] != "keep" && m[2] != "discard" && m[2] != "stop" {
							add(s, "on uncertain expects keep, discard, or stop")
						}
					case "existing":
						if m[2] != "stop" && m[2] != "replace" {
							add(s, "on existing expects stop or replace")
						}
					default:
						kind := classifyLine(m[2])
						if kind == "" {
							add(s, "unknown handler action")
						} else {
							walk([]*Statement{{Kind: kind, Text: m[2], Line: s.Line}})
						}
					}
				}
			}
			walk(s.Body)
		}
	}
	// Shared declarations are visible regardless of source order.
	for _, s := range p.Statements {
		if s.Kind == "to" {
			funcs[match("to", s.Text)[1]] = true
		}
		if s.Kind == "schema" {
			schemas[match("schema", s.Text)[1]] = true
		}
	}
	exported := map[string]bool{}
	for _, s := range p.Statements {
		if s.Kind != "export" {
			continue
		}
		name := match("export", s.Text)[1]
		if !funcs[name] && !schemas[name] && p.Definitions[name] == nil {
			add(s, "export "+name+" has no matching declaration")
			continue
		}
		if exported[name] {
			add(s, "duplicate export "+name)
			continue
		}
		exported[name] = true
	}
	walk(p.Statements)
	return ds
}

// sentTargetResolve resolves a sentence-call name for checking. Qualified
// names use module aliases; bare names use the file's word table.
func sentTargetResolve(v *fileVocab, name string) (*Module, string) {
	mod, action, ok := v.resolveName(name)
	if !ok {
		return nil, ""
	}
	return mod, action
}

// moduleAlias resolves an import alias in the entry file's scope.
func moduleAlias(p *Program, alias string) *Module {
	if p == nil || p.Modules == nil {
		return nil
	}
	return p.Modules.Aliases[alias]
}

// checkSource parses and analyzes with an optional import graph so qualified
// calls and sentence calls validate against resolved modules.
func checkSource(source string, modules *ModuleTable) []Diagnostic {
	p, ds := ParseWithVocabulary(source, modules)
	if len(ds) == 0 {
		ds = append(ds, analyze(p)...)
	}
	return ds
}
func copyNames(m map[string]bool) map[string]bool {
	n := map[string]bool{}
	for k, v := range m {
		n[k] = v
	}
	return n
}

func copyTypes(m map[string]TypeRef) map[string]TypeRef {
	result := map[string]TypeRef{}
	for name, typ := range m {
		result[name] = typ
	}
	return result
}

func staticArgumentProblem(expression string, wanted TypeRef, known map[string]TypeRef, defs map[string]*RecordDef, action, parameter string) string {
	if wanted.Name == "any" {
		return ""
	}
	tokens, err := lex(strings.TrimSpace(expression))
	if err != nil || len(tokens) != 1 {
		return ""
	}
	if tokens[0].quoted {
		if wanted.Name != "text" && wanted.Name != "any" {
			return fmt.Sprintf("%s.%s must be %s; received text", action, parameter, wanted.String())
		}
		return ""
	}
	if actual, ok := known[tokens[0].text]; ok {
		if actual.Name == wanted.Name && actual.Element == nil && wanted.Element == nil {
			return ""
		}
		return fmt.Sprintf("%s.%s must be %s; received %s", action, parameter, wanted.String(), actual.String())
	}
	if tokens[0].text == "null" {
		if !wanted.Optional {
			return fmt.Sprintf("%s.%s must be %s; received null", action, parameter, wanted.String())
		}
		return ""
	}
	value, evalErr := evaluate(expression, nil, nil)
	if evalErr != nil {
		return ""
	}
	if !typeMatchesRef(value, wanted, defs) {
		return fmt.Sprintf("%s.%s must be %s; received %s", action, parameter, wanted.String(), valueTypeName(value))
	}
	return ""
}

func knownFieldProblem(field, receiver string, types map[string]TypeRef, defs map[string]*RecordDef, inFields bool) string {
	if inFields {
		return ""
	}
	typ := types[receiver]
	if typ.Name == "" || typ.Name == "any" || typ.Element != nil {
		return ""
	}
	if def := defs[typ.Name]; def != nil {
		if _, ok := fieldsByName(def)[field]; !ok {
			return fmt.Sprintf("%s has no field %s", typ.Name, field)
		}
	}
	return ""
}

func dottedFieldProblem(expression string, types map[string]TypeRef, defs map[string]*RecordDef) string {
	parts := strings.Split(expression, ".")
	if len(parts) < 2 {
		return ""
	}
	typ := types[parts[0]]
	for _, field := range parts[1:] {
		if typ.Name == "" || typ.Name == "any" || typ.Element != nil {
			return ""
		}
		def := defs[typ.Name]
		if def == nil {
			return ""
		}
		declared, ok := fieldsByName(def)[field]
		if !ok {
			return fmt.Sprintf("%s has no field %s", typ.Name, field)
		}
		typ = declared.Type.base()
	}
	return ""
}

func actionBodyBinds(stmts []*Statement, wanted string) bool {
	for _, statement := range stmts {
		switch statement.Kind {
		case "make":
			m := match("make", statement.Text)
			if len(m) > 1 && m[1] == wanted {
				return true
			}
		case "remember":
			m := match("remember", statement.Text)
			if len(m) > 2 && m[2] == wanted {
				return true
			}
		case "call":
			if m := match("call", statement.Text); len(m) > 3 && m[3] == wanted {
				return true
			}
		case "sent":
			if m := matchSent(statement.Text); len(m) > 4 && m[4] == wanted {
				return true
			}
		}
		if actionBodyBinds(statement.Body, wanted) {
			return true
		}
	}
	return false
}
