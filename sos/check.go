package sos

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
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
	for _, problem := range validateRecordDefinitionSet(localDefinitions, visibleDefs) {
		ds = append(ds, Diagnostic{1, 1, problem})
	}
	_, importedFailureProblems := visibleFailureDefinitionsWithProblems(p)
	for _, problem := range importedFailureProblems {
		ds = append(ds, Diagnostic{1, 1, problem})
	}
	for _, failure := range p.Failures {
		for _, field := range failure.Fields {
			if err := validateTypeRefs(field.Type, visibleDefs, map[string]bool{}); err != nil {
				ds = append(ds, Diagnostic{field.Line, 1, fmt.Sprintf("%s.%s: %v", failure.Name, field.Name, err)})
			}
		}
	}
	for _, statement := range p.Statements {
		if statement.Kind == "to" {
			actions[match("to", statement.Text)[1]] = statement
		}
	}
	add := func(s *Statement, msg string) { ds = append(ds, Diagnostic{s.Line, 1, msg}) }
	checkActionArgs := func(s *Statement, action string, args []string, checkArity bool) {
		fn := actions[action]
		targetDefs := visibleDefs
		if fn == nil && strings.Contains(action, ".") {
			alias, name, _ := strings.Cut(action, ".")
			if mod := moduleAlias(p, alias); mod != nil && mod.Exports[name] {
				fn = mod.Actions[name]
				targetDefs, _ = mod.definitionScope(mod.scope(name))
			}
		}
		if fn == nil && p.Modules != nil && p.Modules.vocab != nil {
			if mod, name := sentTargetResolve(p.Modules.vocab, action); mod != nil && mod.Exports[name] {
				fn = mod.Actions[name]
				targetDefs, _ = mod.definitionScope(mod.scope(name))
			}
		}
		if fn == nil {
			return
		}
		decl, err := parseActionDecl(fn.Text)
		if err != nil {
			return
		}
		if checkArity && len(args) != len(decl.Params) {
			add(s, fmt.Sprintf("%s expects %d argument(s)", action, len(decl.Params)))
		}
		for i, arg := range args {
			if i >= len(decl.Params) {
				break
			}
			if message := staticArgumentProblem(arg, decl.Params[i].Type, types, targetDefs, action, decl.Params[i].Name); message != "" {
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
			case "true", "false", "on", "off", "null", "now", "empty", "list", "record", "not", "count", "length", "of", "words", "first", "last", "items", "and", "or", "is", "contains", "plus", "minus", "times", "millisecond", "milliseconds", "second", "seconds", "minute", "minutes", "hour", "hours", "day", "days":
				continue
			}
			if i > 0 && isDurationUnit(base) {
				if _, err := strconv.ParseFloat(tokens[i-1].text, 64); err == nil {
					continue
				}
			}
			add(s, fmt.Sprintf("unknown name %q", base))
		}
		if len(depth) > 0 {
			add(s, "unclosed bracket")
		}
	}
	var walk func([]*Statement)
	walk = func(sts []*Statement) {
		for i := 0; i < len(sts); i++ {
			s := sts[i]
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
				if decl.Streaming {
					if err := validateTypeRefs(decl.StreamItem, visibleDefs, map[string]bool{}); err != nil {
						add(s, "stream item type: "+err.Error())
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
					if target != nil {
						if _, streaming, _ := streamTarget(p, actions, action, target); streaming && !isStreamAliasCall(p, m[1]) {
							add(s, "streaming action "+m[1]+" must be opened with stream")
						}
					}
				}
				args, e := sentArguments(m)
				if e != nil {
					add(s, e.Error())
				}
				for _, v := range args {
					checkExpr(s, v, false)
				}
				checkActionArgs(s, m[1], args, false)
				checkRecoveryShape(s, operationStaticResult(p, actions, s), func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
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
				checkActionArgs(s, m[1], args, true)
				checkRecoveryShape(s, operationStaticResult(p, actions, s), func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
				if fn := actions[m[1]]; fn != nil {
					if decl, err := parseActionDecl(fn.Text); err == nil && decl.Streaming {
						add(s, "streaming action "+m[1]+" must be opened with stream")
					}
				} else if strings.Contains(m[1], ".") {
					alias, action, _ := strings.Cut(m[1], ".")
					if mod := moduleAlias(p, alias); mod != nil {
						if op, ok := mod.Native[action]; ok && strings.HasPrefix(op.Result, "stream of ") && !isStreamAliasCall(p, m[1]) {
							add(s, "streaming action "+m[1]+" must be opened with stream")
						}
					}
				}
				if m[3] != "" {
					names[m[3]] = true
					delete(types, m[3])
				}
			case "openStream":
				args, e := splitExpressions(m[2])
				if e != nil {
					add(s, e.Error())
				}
				for _, v := range args {
					checkExpr(s, v, false)
				}
				checkActionArgs(s, m[1], args, true)
				checkRecoveryShape(s, false, func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
				if known, streaming, hosted := streamTarget(p, actions, m[1], nil); known && streaming && !hosted {
					add(s, "streaming action "+m[1]+" has no host implementation and cannot be opened")
				}
				if strings.Contains(m[1], ".") {
					alias, action, _ := strings.Cut(m[1], ".")
					if mod := moduleAlias(p, alias); mod != nil {
						if op, ok := mod.Native[action]; ok && len(args) != len(op.Params) {
							add(s, fmt.Sprintf("%s expects %d argument(s)", m[1], len(op.Params)))
						}
					}
				}
				names[m[3]] = true
			case "httpGet", "httpPost", "httpRequest":
				for _, expr := range httpCanonicalExprs(s, m) {
					checkExpr(s, expr, false)
				}
				names[httpCanonicalBinding(s, m)] = true
				delete(types, httpCanonicalBinding(s, m))
				checkRecoveryShape(s, true, func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
			case "httpListen":
				names[m[3]] = true
				delete(types, m[3])
				checkRecoveryShape(s, false, func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
			case "httpReadBody":
				if !names[m[2]] {
					add(s, "unknown name "+m[2])
				}
				if m[3] != "" && visibleDefs[m[3]] == nil {
					add(s, "unknown type "+m[3])
				}
				names[m[4]] = true
				if m[3] != "" {
					types[m[4]] = TypeRef{Name: m[3]}
				} else {
					delete(types, m[4])
				}
				checkRecoveryShape(s, true, func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
			case "httpRespond":
				if !names[m[1]] {
					add(s, "unknown name "+m[1])
				}
				if m[4] != "" {
					checkExpr(s, m[4], false)
				}
			case "httpRespondComplete":
				if !names[m[1]] {
					add(s, "unknown name "+m[1])
				}
				for _, expr := range httpCanonicalExprs(s, m) {
					checkExpr(s, expr, false)
				}
			case "deadline":
				checkTimeStatement(s, checkExpr, add)
				walk(s.Body)
			case "timerOneShot", "timerEvery", "scheduleStream", "findCalendar", "advanceTime", "stopStream":
				if binding := checkTimeStatement(s, checkExpr, add); binding != "" {
					names[binding] = true
					delete(types, binding)
				}
			case "closeStream":
				if !names[m[1]] {
					add(s, "unknown stream "+m[1])
				}
			case "collectStream":
				checkExpr(s, m[1], false)
				checkExpr(s, m[2], false)
				names[m[3]] = true
				checkRecoveryShape(s, true, func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
			case "streamFor":
				checkExpr(s, m[2], false)
				old := names
				names = copyNames(names)
				names[m[1]] = true
				names["number"] = true
				checkRecoveryShape(s, false, func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
				walk(s.Body)
				names = old
				continue
			case "stopReading":
				// Stream-loop placement is validated by the ownership pass.
			case "quietStream", "limitStream", "batchStream", "distinctStream", "distinctKeyStream", "idleStream", "takeForStream", "deadlineStream", "filterStream", "projectStream", "handleEachStream", "handleOneStream", "newestStream", "conflateStream", "exhaustStream":
				var inline string
				switch s.Kind {
				case "quietStream", "batchStream", "idleStream":
					inline = m[5]
				case "limitStream":
					inline = m[7]
				case "takeForStream", "deadlineStream":
					inline = m[3]
				}
				if sink := streamSink(s, inline); sink != "" {
					names[sink] = true
				}
				if s.Kind == "distinctStream" {
					names[m[2]] = true
				}
				if s.Kind == "distinctKeyStream" || s.Kind == "filterStream" || s.Kind == "projectStream" {
					names[m[3]] = true
				}
				old := names
				names = copyNames(names)
				switch s.Kind {
				case "filterStream", "projectStream", "handleEachStream", "handleOneStream", "newestStream":
					names[m[1]] = true
				case "exhaustStream":
					names[strings.ReplaceAll(m[1], " ", "_")] = true
				case "conflateStream":
					if policy := streamChild(s, "conflatePolicy"); policy != nil {
						names[match("conflatePolicy", policy.Text)[1]] = true
					}
				}
				walk(s.Body)
				names = old
				continue
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
				if s.Kind == "when" {
					if outcome, ok := resultNarrowing(m[1]); ok {
						oldTypes := types
						types = copyTypes(types)
						types[outcome] = TypeRef{Name: "ResultSuccess"}
						walk(s.Body)
						if i+1 < len(sts) && sts[i+1].Kind == "otherwise" {
							types = copyTypes(oldTypes)
							types[outcome] = TypeRef{Name: "ResultFailure"}
							walk(sts[i+1].Body)
							i++
						}
						types = oldTypes
						continue
					}
				}
			case "repeat", "return", "folder", "append":
				checkExpr(s, m[1], false)
				if s.Kind == "append" && !names[m[2]] {
					add(s, "unknown list "+m[2])
				}
			case "finish", "recover":
				if m[1] != "" {
					checkExpr(s, m[1], false)
				}
			case "fail":
				if m[2] != "" {
					checkExpr(s, m[2], false)
				}
				for _, field := range s.Body {
					if field.Kind == "field" {
						if _, expression, ok := strings.Cut(field.Text, " from "); ok {
							checkExpr(field, expression, false)
						}
					}
				}
			case "capture":
				if !captureTargetKnown(p, actions, m[1]) {
					add(s, "unknown capture target "+m[1])
				}
				if m[2] != "" {
					checkExpr(s, m[2], false)
				}
				names[m[3]] = true
				types[m[3]] = TypeRef{Name: "Result"}
				if _, streaming, _ := streamTarget(p, actions, m[1], nil); streaming {
					add(s, "cannot capture streaming action "+m[1]+"; handle opening and terminal failures separately")
				}
			case "take":
				checkExpr(s, m[2], false)
				checkExpr(s, m[3], false)
				names[m[4]] = true
				checkRecoveryShape(s, true, func(line int, format string, args ...any) {
					ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
				})
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
			case "readFile":
				checkExpr(s, m[1], false)
				names[m[2]] = true
				types[m[2]] = TypeRef{Name: "text"}
			case "writeFile":
				checkExpr(s, m[1], false)
				checkExpr(s, m[3], false)
				checkFileFormModifiers(s, add, checkExpr)
			case "appendFile":
				checkExpr(s, m[1], false)
				checkExpr(s, m[2], false)
			case "checkExists":
				checkExpr(s, m[2], false)
				names[m[3]] = true
				types[m[3]] = TypeRef{Name: "boolean"}
			case "inspectEntry":
				checkExpr(s, m[2], false)
				names[m[3]] = true
				types[m[3]] = TypeRef{Name: "FileEntry"}
			case "listEntries":
				checkExpr(s, m[2], false)
				names[m[3]] = true
				types[m[3]] = TypeRef{Element: &TypeRef{Name: "FileEntry"}}
			case "walkThrough":
				checkExpr(s, m[1], false)
				if limit, err := strconv.ParseFloat(m[2], 64); err == nil && limit <= 0 {
					add(s, "walk through requires a positive entry bound")
				}
				names[m[3]] = true
				types[m[3]] = TypeRef{Element: &TypeRef{Name: "FileEntry"}}
				checkFileFormModifiers(s, add, checkExpr)
			case "streamFiles", "watchFolder":
				root := m[2]
				if s.Kind == "watchFolder" {
					root = m[1]
				}
				checkExpr(s, root, false)
				names[m[3]] = true
				delete(types, m[3])
				checkFileFormModifiers(s, add, checkExpr)
			case "copyEntry", "moveEntry":
				checkExpr(s, m[2], false)
				checkExpr(s, m[3], false)
				checkFileFormModifiers(s, add, checkExpr)
			case "createFoldersThrough", "removeFile", "removeEmptyFolder", "removeFolder":
				checkExpr(s, m[1], false)
			case "fsMatch", "fsExclude":
				checkExpr(s, m[1], false)
			case "handler":
				if m[1] == "failure" {
					_, binding, fields, typed := failureHandlerHeader(s.Text)
					if typed {
						oldNames, oldTypes := names, types
						names, types = copyNames(names), copyTypes(types)
						if binding != "" {
							if names[binding] {
								add(s, "failure handler binding "+binding+" collides with an existing name")
							}
							names[binding] = true
						} else {
							seenFields := map[string]bool{}
							for _, field := range fields {
								if names[field] || seenFields[field] {
									add(s, "failure handler binding "+field+" collides with an existing name")
								}
								seenFields[field] = true
								names[field] = true
							}
						}
						walk(s.Body)
						names, types = oldNames, oldTypes
						continue
					}
				}
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
						if _, _, _, typed := failureHandlerHeader(s.Text); typed {
							break
						}
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
		if !funcs[name] && !schemas[name] && p.Definitions[name] == nil && p.Failures[name] == nil {
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
	ds = append(ds, checkActionContracts(p, actions, visibleDefs)...)
	ds = append(ds, checkStreamOwnership(p, actions)...)
	ds = append(ds, checkStreamAliasCalls(p)...)
	ds = append(ds, checkStreamSendPlacement(p.Statements)...)
	return ds
}

func checkStreamSendPlacement(statements []*Statement) []Diagnostic {
	var diagnostics []Diagnostic
	var walk func([]*Statement, bool)
	walk = func(items []*Statement, streaming bool) {
		for _, statement := range items {
			if statement.Kind == "send" && !streaming {
				diagnostics = append(diagnostics, Diagnostic{statement.Line, 1, "send is only valid inside a streaming action"})
			}
			childStreaming := streaming
			if statement.Kind == "to" {
				decl, err := parseActionDecl(statement.Text)
				childStreaming = err == nil && decl.Streaming
			}
			walk(statement.Body, childStreaming)
		}
	}
	walk(statements, false)
	return diagnostics
}

type streamOwnershipState struct {
	line        int
	consumed    bool
	derived     bool
	obligations []string
}

func streamTarget(p *Program, actions map[string]*Statement, name string, explicit *Module) (known, streaming, hosted bool) {
	if explicit != nil {
		action := name
		if _, suffix, ok := strings.Cut(name, "."); ok {
			action = suffix
		}
		if op, ok := explicit.Native[action]; ok {
			return true, strings.HasPrefix(op.Result, "stream of "), true
		}
		if fn := explicit.Actions[action]; fn != nil {
			decl, err := parseActionDecl(fn.Text)
			return err == nil, err == nil && decl.Streaming, err == nil && decl.Streaming
		}
		return false, false, false
	}
	if !strings.Contains(name, ".") {
		fn := actions[name]
		if fn == nil {
			return false, false, false
		}
		decl, err := parseActionDecl(fn.Text)
		return err == nil, err == nil && decl.Streaming, err == nil && decl.Streaming
	}
	alias, action, _ := strings.Cut(name, ".")
	if mod := moduleAlias(p, alias); mod != nil {
		return streamTarget(p, actions, action, mod)
	}
	return false, false, false
}

func checkStreamOwnership(p *Program, actions map[string]*Statement) []Diagnostic {
	var ds []Diagnostic
	clone := func(in map[string]*streamOwnershipState) map[string]*streamOwnershipState {
		out := make(map[string]*streamOwnershipState, len(in))
		for name, state := range in {
			copy := *state
			out[name] = &copy
		}
		return out
	}
	streaming := func(name string) (known, isStream bool) {
		if !strings.Contains(name, ".") {
			fn := actions[name]
			if fn == nil {
				return false, false
			}
			decl, err := parseActionDecl(fn.Text)
			return err == nil, err == nil && decl.Streaming
		}
		alias, action, _ := strings.Cut(name, ".")
		if mod := moduleAlias(p, alias); mod != nil {
			if op, ok := mod.Native[action]; ok {
				return true, strings.HasPrefix(op.Result, "stream of ")
			}
			if fn := mod.Actions[action]; fn != nil {
				decl, err := parseActionDecl(fn.Text)
				return err == nil, err == nil && decl.Streaming
			}
		}
		return false, false
	}
	var walk func([]*Statement, map[string]*streamOwnershipState, bool, bool)
	// loopStream names the stream whose for-each loop is currently being
	// walked so a handler may stop that stream from inside the loop.
	loopStream := ""
	walk = func(sts []*Statement, owned map[string]*streamOwnershipState, inStreamLoop, closeScope bool) {
		initial := map[string]bool{}
		for name := range owned {
			initial[name] = true
		}
		for i := 0; i < len(sts); i++ {
			s := sts[i]
			m := match(s.Kind, s.Text)
			if binding := statementBindingName(s); binding != "" && s.Kind != "openStream" && s.Kind != "httpListen" {
				if state := owned[binding]; state != nil && !state.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, "cannot overwrite active stream " + binding + "; consume or close it first"})
				}
			}
			switch s.Kind {
			case "openStream":
				known, isStream := streaming(m[1])
				if !known {
					ds = append(ds, Diagnostic{s.Line, 1, "unknown streaming action " + m[1]})
					continue
				}
				if !isStream {
					ds = append(ds, Diagnostic{s.Line, 1, m[1] + " is not a streaming action"})
					continue
				}
				if openFailureHandlerRecovers(s) {
					ds = append(ds, Diagnostic{s.Line, 1, "an opening failure handler cannot recover and continue because no stream handle exists; finish, fail, stop, or pass the failure on"})
					continue
				}
				if existing := owned[m[3]]; existing != nil && !existing.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, "stream " + m[3] + " is already active; consume or close it before reopening"})
					continue
				}
				owned[m[3]] = &streamOwnershipState{line: s.Line, obligations: streamActionObligations(p, m[1])}
			case "httpListen":
				if openFailureHandlerRecovers(s) {
					ds = append(ds, Diagnostic{s.Line, 1, "an opening failure handler cannot recover and continue because no stream handle exists; finish, fail, stop, or pass the failure on"})
					continue
				}
				if existing := owned[m[3]]; existing != nil && !existing.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, "stream " + m[3] + " is already active; consume or close it before reopening"})
					continue
				}
				owned[m[3]] = &streamOwnershipState{line: s.Line, obligations: []string{"http-response"}}
			case "streamFiles", "watchFolder":
				if openFailureHandlerRecovers(s) {
					ds = append(ds, Diagnostic{s.Line, 1, "an opening failure handler cannot recover and continue because no stream handle exists; finish, fail, stop, or pass the failure on"})
					continue
				}
				if existing := owned[m[3]]; existing != nil && !existing.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, "stream " + m[3] + " is already active; consume or close it before reopening"})
					continue
				}
				owned[m[3]] = &streamOwnershipState{line: s.Line}
			case "timerOneShot", "timerEvery":
				name := m[2]
				if s.Kind == "timerEvery" {
					name = m[3]
				}
				if existing := owned[name]; existing != nil && !existing.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, "stream " + name + " is already active; consume or close it before reopening"})
					continue
				}
				owned[name] = &streamOwnershipState{line: s.Line}
			case "scheduleStream":
				called := ""
				for _, child := range s.Body {
					if child.Kind == "scheduleCalled" {
						called = match("scheduleCalled", child.Text)[1]
					}
				}
				if called == "" {
					continue
				}
				if existing := owned[called]; existing != nil && !existing.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, "stream " + called + " is already active; consume or close it before reopening"})
					continue
				}
				owned[called] = &streamOwnershipState{line: s.Line}
			case "stopStream":
				state := owned[m[1]]
				if state == nil {
					ds = append(ds, Diagnostic{s.Line, 1, m[1] + " is not an owned stream"})
				} else if state.consumed && loopStream != m[1] {
					ds = append(ds, Diagnostic{s.Line, 1, m[1] + " was already consumed"})
				}
			case "streamFor":
				name := strings.TrimSpace(m[2])
				state := owned[name]
				if state == nil {
					ds = append(ds, Diagnostic{s.Line, 1, name + " is not an owned stream"})
				} else if state.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, name + " was already consumed"})
				} else {
					state.consumed = true
					if ownsHTTPResponse(state.obligations) && !streamHandlerCompletesObligations(p, [][]*Statement{s.Body}, m[1], state.obligations) {
						ds = append(ds, Diagnostic{s.Line, 1, "an owned source item must be completed, transferred, or explicitly rejected on every reachable handler path; the handler never uses " + m[1]})
					}
				}
				previous := loopStream
				loopStream = name
				walk(s.Body, owned, true, true)
				loopStream = previous
			case "closeStream":
				state := owned[m[1]]
				if state == nil {
					ds = append(ds, Diagnostic{s.Line, 1, m[1] + " is not an owned stream"})
				} else if state.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, m[1] + " was already consumed"})
				} else {
					state.consumed = true
				}
			case "collectStream":
				if limit, err := strconv.ParseFloat(strings.TrimSpace(m[1]), 64); err == nil && (limit <= 0 || limit != math.Trunc(limit) || limit > maxStreamMaterializationItems) {
					ds = append(ds, Diagnostic{s.Line, 1, fmt.Sprintf("collect at most requires a positive bounded integer (at most %d)", maxStreamMaterializationItems)})
				}
				name := strings.TrimSpace(m[2])
				state := owned[name]
				if state == nil {
					ds = append(ds, Diagnostic{s.Line, 1, name + " is not an owned stream"})
				} else if state.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, name + " was already consumed"})
				} else {
					state.consumed = true
				}
			case "take":
				name := strings.TrimSpace(m[3])
				if state := owned[name]; state != nil {
					if m[1] == "last" {
						ds = append(ds, Diagnostic{s.Line, 1, "take last requires a collection, but " + name + " is a stream"})
					} else if state.consumed {
						ds = append(ds, Diagnostic{s.Line, 1, name + " was already consumed"})
					} else {
						state.consumed = true
					}
				}
			case "call", "sent":
				var target, argsText, sink string
				if s.Kind == "call" {
					target, argsText, sink = m[1], m[2], m[3]
				} else if sent := matchSent(s.Text); sent != nil {
					target, argsText, sink = sent[1], sent[2], sent[4]
					if argsText == "" {
						argsText = sent[3]
					}
				}
				if isStreamAliasCall(p, target) {
					args := splitStreamAliasArguments(argsText)
					if len(args) > 0 {
						source := strings.TrimSpace(args[0])
						if state := owned[source]; state == nil {
							ds = append(ds, Diagnostic{s.Line, 1, source + " is not an owned stream"})
						} else if state.consumed {
							ds = append(ds, Diagnostic{s.Line, 1, source + " was already consumed"})
						} else {
							state.consumed = true
							if sink != "" {
								owned[sink] = &streamOwnershipState{line: s.Line, obligations: state.obligations}
							}
						}
					}
				}
				walk(s.Body, owned, inStreamLoop, false)
			case "quietStream", "limitStream", "batchStream", "distinctStream", "distinctKeyStream", "idleStream", "takeForStream", "deadlineStream", "filterStream", "projectStream", "handleEachStream", "handleOneStream", "newestStream", "conflateStream", "exhaustStream":
				ds = append(ds, checkStreamConstruction(p, s, m, owned)...)
				for _, body := range streamHandlerBodies(s) {
					walk(body, owned, true, true)
				}
			case "keep", "sort", "group", "map", "for", "require":
				index := 1
				if s.Kind == "map" || s.Kind == "for" || s.Kind == "require" {
					index = 2
				}
				name := strings.TrimSpace(m[index])
				if owned[name] != nil {
					ds = append(ds, Diagnostic{s.Line, 1, s.Kind + " requires a collection, but " + name + " is a stream; collect it with an explicit limit first"})
				}
			case "make":
				expr := strings.TrimSpace(strings.TrimPrefix(m[2], "as "))
				if name := expressionOwnedStream(expr, owned); name != "" {
					ds = append(ds, Diagnostic{s.Line, 1, "cannot copy stream " + name + " with make"})
				}
				for _, field := range s.Body {
					if _, expression, ok := strings.Cut(field.Text, " from "); ok {
						expression = strings.TrimSpace(expression)
						if name := expressionOwnedStream(expression, owned); name != "" {
							ds = append(ds, Diagnostic{field.Line, 1, "cannot copy stream " + name + " with make"})
						}
					}
				}
			case "remember":
				expr := strings.TrimSpace(m[1])
				if name := expressionOwnedStream(expr, owned); name != "" {
					ds = append(ds, Diagnostic{s.Line, 1, "cannot copy stream " + name + " with remember"})
				}
			case "append":
				expr := strings.TrimSpace(m[1])
				if name := expressionOwnedStream(expr, owned); name != "" {
					ds = append(ds, Diagnostic{s.Line, 1, "cannot copy stream " + name + " with append"})
				}
			case "readEach":
				if state := owned[m[1]]; state != nil && !state.consumed {
					ds = append(ds, Diagnostic{s.Line, 1, "cannot overwrite active stream " + m[1] + "; consume or close it first"})
				}
			case "stopReading":
				if !inStreamLoop {
					ds = append(ds, Diagnostic{s.Line, 1, "stop reading requires an active stream loop"})
				}
			case "to":
				actionOwned := map[string]*streamOwnershipState{}
				walk(s.Body, actionOwned, false, true)
			case "when":
				before := clone(owned)
				thenState := clone(owned)
				walk(s.Body, thenState, inStreamLoop, true)
				elseState := clone(before)
				if i+1 < len(sts) && sts[i+1].Kind == "otherwise" {
					walk(sts[i+1].Body, elseState, inStreamLoop, true)
					i++
				}
				for name, original := range before {
					owned[name].consumed = thenState[name].consumed && elseState[name].consumed
					owned[name].line = original.line
				}
			case "otherwise":
				// Paired otherwise blocks are consumed with their when above.
			default:
				if len(s.Body) > 0 {
					walk(s.Body, owned, inStreamLoop, true)
				}
			}
		}
		if closeScope {
			for name, state := range owned {
				if !initial[name] && !state.consumed {
					ds = append(ds, Diagnostic{state.line, 1, "stream " + name + " remains active; consume it, close it, or transfer ownership"})
				}
			}
		}
	}
	owned := map[string]*streamOwnershipState{}
	walk(p.Statements, owned, false, false)
	for name, state := range owned {
		if !state.consumed && !state.derived {
			ds = append(ds, Diagnostic{state.line, 1, "stream " + name + " remains active; consume it, close it, or transfer ownership"})
		}
	}
	return ds
}

func expressionOwnedStream(expr string, owned map[string]*streamOwnershipState) string {
	identifiers := expressionIdentifiers(expr)
	for name, state := range owned {
		if state != nil && !state.consumed && identifiers[name] {
			return name
		}
	}
	return ""
}

func expressionIdentifiers(expr string) map[string]bool {
	result := map[string]bool{}
	for i := 0; i < len(expr); {
		if expr[i] == '"' {
			i++
			for i < len(expr) {
				if expr[i] == '\\' && i+1 < len(expr) {
					i += 2
					continue
				}
				if expr[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}
		if expr[i] == '_' || expr[i] >= 'A' && expr[i] <= 'Z' || expr[i] >= 'a' && expr[i] <= 'z' {
			start := i
			for i < len(expr) && (expr[i] == '_' || expr[i] >= 'A' && expr[i] <= 'Z' || expr[i] >= 'a' && expr[i] <= 'z' || expr[i] >= '0' && expr[i] <= '9') {
				i++
			}
			name := expr[start:i]
			j := i
			for j < len(expr) && (expr[j] == ' ' || expr[j] == '\t') {
				j++
			}
			if j >= len(expr) || expr[j] != ':' {
				result[name] = true
			}
			continue
		}
		i++
	}
	return result
}

func openFailureHandlerRecovers(operation *Statement) bool {
	var containsRecover func([]*Statement) bool
	containsRecover = func(stmts []*Statement) bool {
		for _, statement := range stmts {
			if statement.Kind == "call" || statement.Kind == "openStream" {
				continue
			}
			if statement.Kind == "recover" || containsRecover(statement.Body) {
				return true
			}
		}
		return false
	}
	for _, handler := range operation.Body {
		if handler.Kind != "handler" || match("handler", handler.Text)[1] != "failure" {
			continue
		}
		inline := strings.TrimSpace(match("handler", handler.Text)[2])
		if strings.HasPrefix(inline, "recover") || containsRecover(handler.Body) {
			return true
		}
		if inline == "finish" || inline == "fail" || inline == "stop" || inline == "rethrow" || inline == "pass failure on" || strings.HasPrefix(inline, "finish with ") || strings.HasPrefix(inline, "stop with ") || strings.HasPrefix(inline, "fail ") {
			continue
		}
		if !statementsTerminate(handler.Body) {
			return true
		}
	}
	return false
}

func statementsTerminate(stmts []*Statement) bool {
	return handlerTerminates(stmts)
}

// checkActionContracts performs the deterministic part of typed failure
// checking. Untyped legacy actions remain dynamic; once an action declares a
// result or failure set, direct calls and explicit terminal forms participate
// in the closed contract.
func checkActionContracts(p *Program, actions map[string]*Statement, defs map[string]*RecordDef) []Diagnostic {
	var ds []Diagnostic
	visibleFailures, _ := visibleFailureDefinitionsWithProblems(p)
	add := func(line int, format string, args ...any) {
		ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
	}
	for name, fn := range actions {
		decl, err := parseActionDecl(fn.Text)
		if err != nil {
			continue
		}
		typed := decl.HasResult || decl.Streaming || len(decl.Failures) > 0
		if !typed {
			continue
		}
		declared := map[string]bool{}
		knownTypes := map[string]TypeRef{}
		for _, param := range decl.Params {
			knownTypes[param.Name] = param.Type.base()
		}
		for _, failure := range decl.Failures {
			declared[failure] = true
			if visibleFailures[failure] == nil {
				add(fn.Line, "%s declares unknown failure %s", name, failure)
			}
		}
		streamFailures := map[string][]string{}
		var visit func([]*Statement, bool)
		visit = func(stmts []*Statement, inHandler bool) {
			for _, s := range stmts {
				m := match(s.Kind, s.Text)
				switch s.Kind {
				case "send":
					if !decl.Streaming {
						add(s.Line, "send is only valid inside a streaming action")
					} else if message := staticArgumentProblem(m[1], decl.StreamItem, knownTypes, defs, name, "stream item"); message != "" {
						add(s.Line, "%s", message)
					}
				case "finish":
					if decl.HasResult && m[1] == "" {
						add(s.Line, "%s must finish with %s", name, decl.Result.String())
					}
					if !decl.HasResult && m[1] != "" {
						add(s.Line, "%s cannot finish with a value", name)
					}
					if decl.HasResult && m[1] != "" {
						if message := staticArgumentProblem(m[1], decl.Result, knownTypes, defs, name, "result"); message != "" {
							add(s.Line, "%s", message)
						}
					}
				case "return":
					if !decl.HasResult {
						// Legacy return remains valid for untyped actions only.
						add(s.Line, "%s cannot return a value; use finish with only for a returning action", name)
					} else if message := staticArgumentProblem(m[1], decl.Result, knownTypes, defs, name, "result"); message != "" {
						add(s.Line, "%s", message)
					}
				case "recover":
					if decl.HasResult && m[1] != "" {
						if message := staticArgumentProblem(m[1], decl.Result, knownTypes, defs, name, "recovery"); message != "" {
							add(s.Line, "%s", message)
						}
					}
				case "fail":
					failure := m[1]
					if visibleFailures[failure] == nil {
						add(s.Line, "unknown failure %s", failure)
					} else if !declared[failure] {
						add(s.Line, "%s may pass %s on; handle it or add it to \"may fail with\"", name, failure)
					}
					if m[2] != "" {
						checkStaticActionExpr(s, m[2], defs, add)
					}
					checkFailurePayload(s, visibleFailures[failure], defs, add)
				case "call":
					possible := possibleFailuresForCall(p, actions, s)
					checkFailureHandlers(s, possible, add)
					visit(s.Body, true)
					for _, failure := range possible {
						if declared[failure] || callHasFailureHandler(s, failure) {
							continue
						}
						add(s.Line, "%s may pass %s on; handle it or add it to \"may fail with\"", name, failure)
					}
				case "httpGet", "httpPost", "httpRequest", "httpReadBody", "httpRespond", "httpRespondComplete":
					possible := possibleFailuresForCall(p, actions, s)
					checkFailureHandlers(s, possible, add)
					visit(s.Body, true)
					for _, failure := range possible {
						if declared[failure] || callHasFailureHandler(s, failure) {
							continue
						}
						add(s.Line, "%s may pass %s on; handle it or add it to \"may fail with\"", name, failure)
					}
				case "httpListen":
					possible := possibleFailuresForCall(p, actions, s)
					checkFailureHandlers(s, possible, add)
					streamFailures[m[3]] = possible
					visit(s.Body, true)
					for _, failure := range possible {
						if !declared[failure] && !callHasFailureHandler(s, failure) {
							add(s.Line, "%s may pass %s on while opening a stream; handle it or add it to \"may fail with\"", name, failure)
						}
					}
				case "openStream":
					possible := possibleFailuresForCall(p, actions, s)
					checkFailureHandlers(s, possible, add)
					streamFailures[m[3]] = possible
					visit(s.Body, true)
					for _, failure := range possible {
						if !declared[failure] && !callHasFailureHandler(s, failure) {
							add(s.Line, "%s may pass %s on while opening a stream; handle it or add it to \"may fail with\"", name, failure)
						}
					}
				case "streamFor", "collectStream", "take":
					streamName := ""
					switch s.Kind {
					case "streamFor":
						streamName = strings.TrimSpace(m[2])
					case "collectStream":
						streamName = strings.TrimSpace(m[2])
					case "take":
						streamName = strings.TrimSpace(m[3])
					}
					possible := streamFailures[streamName]
					if s.Kind == "collectStream" {
						possible = append(append([]string(nil), possible...), "StreamLimitExceeded")
					}
					checkFailureHandlers(s, possible, add)
					visit(s.Body, true)
					for _, failure := range possible {
						if !declared[failure] && !callHasFailureHandler(s, failure) {
							add(s.Line, "%s may pass terminal stream failure %s on; handle it or add it to \"may fail with\"", name, failure)
						}
					}
				case "sent":
					possible := possibleFailuresForCall(p, actions, s)
					checkFailureHandlers(s, possible, add)
					for _, failure := range possible {
						if declared[failure] || callHasFailureHandler(s, failure) {
							continue
						}
						add(s.Line, "%s may pass %s on; handle it or add it to \"may fail with\"", name, failure)
					}
				case "readFile", "writeFile", "appendFile", "checkExists", "inspectEntry", "listEntries", "walkThrough", "copyEntry", "moveEntry", "createFoldersThrough", "removeFile", "removeEmptyFolder", "removeFolder":
					possible := fileFormFailures(s.Kind)
					checkFailureHandlers(s, possible, add)
					visit(s.Body, true)
					for _, failure := range possible {
						if !declared[failure] && !callHasFailureHandler(s, failure) {
							add(s.Line, "%s may pass %s on; handle it or add it to \"may fail with\"", fileFormLabel(s.Kind), failure)
						}
					}
				case "streamFiles", "watchFolder":
					possible := fileFormFailures(s.Kind)
					checkFailureHandlers(s, possible, add)
					streamFailures[m[3]] = possible
					visit(s.Body, true)
					for _, failure := range possible {
						if !declared[failure] && !callHasFailureHandler(s, failure) {
							add(s.Line, "%s may pass %s on while opening a stream; handle it or add it to \"may fail with\"", fileFormLabel(s.Kind), failure)
						}
					}
				case "handler":
					if match(s.Kind, s.Text)[1] != "failure" {
						break
					}
					if m[2] == "" && len(s.Body) > 0 && !handlerTerminates(s.Body) {
						add(s.Line, "failure handler must recover, finish, fail, stop, or pass failure on")
					}
					kind, binding, fields, isTyped := failureHandlerHeader(s.Text)
					if isTyped && visibleFailures[kind] == nil {
						add(s.Line, "unknown failure handler %s", kind)
					}
					if isTyped {
						if len(s.Body) == 0 {
							add(s.Line, "failure handler must recover, finish, fail, stop, or pass failure on")
						} else if !handlerTerminates(s.Body) {
							add(s.Line, "failure handler must recover, finish, fail, stop, or pass failure on")
						}
						if binding != "" && len(fields) > 0 {
							add(s.Line, "called and using are mutually exclusive in a failure handler")
						}
						if visibleFailures[kind] != nil {
							valid := map[string]bool{"kind": true, "message": true, "retryable": true, "status": true, "code": true, "frames": true}
							for _, field := range visibleFailures[kind].Fields {
								valid[field.Name] = true
							}
							for _, field := range fields {
								if !valid[field] {
									add(s.Line, "failure %s has no field %s", kind, field)
								}
							}
						}
					}
					visit(s.Body, true)
				default:
					visit(s.Body, inHandler)
				}
			}
		}
		visit(fn.Body, false)
		flow := actionFlow(fn.Body)
		if decl.HasResult && flow.fallsThrough {
			add(fn.Line, "%s does not have a successful path ending in finish with a value", name)
		}
	}
	return ds
}

func checkStaticActionExpr(s *Statement, expression string, defs map[string]*RecordDef, add func(int, string, ...any)) {
	v, err := evaluate(expression, nil, nil)
	if err == nil && !typeMatchesRef(v, TypeRef{Name: "text"}, defs) {
		add(s.Line, "failure message must be text")
	}
}

func checkFailurePayload(s *Statement, def *FailureDef, defs map[string]*RecordDef, add func(int, string, ...any)) {
	if def == nil {
		return
	}
	seen := map[string]bool{}
	for _, child := range s.Body {
		if child.Kind != "field" {
			continue
		}
		name, expression, ok := strings.Cut(child.Text, " from ")
		if !ok {
			continue
		}
		if seen[name] {
			add(child.Line, "%s field %s appears more than once", def.Name, name)
		}
		seen[name] = true
		field, ok := failureFieldsByName(def)[name]
		if !ok {
			add(child.Line, "%s has no field %s", def.Name, name)
			continue
		}
		if message := staticArgumentProblem(expression, field.Type, map[string]TypeRef{}, defs, def.Name, name); message != "" {
			add(child.Line, "%s", message)
		}
	}
	for _, field := range def.Fields {
		if !field.Type.Optional && !seen[field.Name] {
			add(s.Line, "%s requires field %s as %s", def.Name, field.Name, field.Type.String())
		}
	}
}

func possibleFailuresForCall(p *Program, actions map[string]*Statement, s *Statement) []string {
	if failures, ok := httpFormPossibleFailures(s); ok {
		return failures
	}
	if p == nil || s == nil {
		return nil
	}
	if s.Kind == "call" || s.Kind == "openStream" {
		m := match(s.Kind, s.Text)
		if strings.Contains(m[1], ".") {
			alias, action, _ := strings.Cut(m[1], ".")
			if p.Modules != nil {
				if mod := p.Modules.Aliases[alias]; mod != nil {
					return modulePossibleFailures(mod, action, map[string]bool{})
				}
			}
			return nil
		}
		if p.actionDeclaration(m[1]) != nil {
			return p.actionPossibleFailures(m[1], map[string]bool{})
		}
		return nil
	}
	if s.Kind == "sent" && p.Modules != nil && p.Modules.vocab != nil {
		m := matchSent(s.Text)
		mod, action, ok := p.Modules.vocab.resolveName(m[1])
		if ok {
			return modulePossibleFailures(mod, action, map[string]bool{})
		}
	}
	return nil
}

func modulePossibleFailures(m *Module, action string, visiting map[string]bool) []string {
	if m == nil {
		return nil
	}
	visitKey := m.Key + "\x00" + action
	if visiting[visitKey] {
		return nil
	}
	fn := m.Actions[action]
	if fn == nil {
		if op, ok := m.Native[action]; ok {
			return append([]string(nil), op.PossibleFailures...)
		}
		return nil
	}
	visiting[visitKey] = true
	defer delete(visiting, visitKey)
	decl, _ := parseActionDecl(fn.Text)
	result := append([]string(nil), decl.Failures...)
	streamFailures := map[string][]string{}
	var walk func([]*Statement)
	walk = func(stmts []*Statement) {
		for _, statement := range stmts {
			switch statement.Kind {
			case "fail":
				result = append(result, match("fail", statement.Text)[1])
			case "call", "openStream":
				callMatch := match(statement.Kind, statement.Text)
				callee := callMatch[1]
				calleeModule := callMatch
				var failures []string
				if strings.Contains(callee, ".") {
					alias, name, _ := strings.Cut(callee, ".")
					calleeModule = nil
					if imported := m.scope(action)[alias]; imported != nil {
						failures = modulePossibleFailures(imported, name, visiting)
					}
				}
				if calleeModule != nil {
					failures = modulePossibleFailures(m, callee, visiting)
				}
				result = append(result, unhandledFailures(failures, statement)...)
				if statement.Kind == "openStream" {
					streamFailures[callMatch[3]] = failures
				}
				walkFailureHandlerBodies(statement.Body, walk)
				continue
			case "httpGet", "httpPost", "httpRequest", "httpReadBody", "httpRespond", "httpRespondComplete":
				failures, _ := httpFormPossibleFailures(statement)
				result = append(result, unhandledFailures(failures, statement)...)
				walkFailureHandlerBodies(statement.Body, walk)
				continue
			case "httpListen":
				failures, _ := httpFormPossibleFailures(statement)
				result = append(result, unhandledFailures(failures, statement)...)
				streamFailures[match("httpListen", statement.Text)[3]] = failures
				walkFailureHandlerBodies(statement.Body, walk)
				continue
			case "streamFor", "collectStream", "take":
				m := match(statement.Kind, statement.Text)
				streamName := m[2]
				if statement.Kind == "take" {
					streamName = m[3]
				}
				result = append(result, unhandledFailures(streamFailures[strings.TrimSpace(streamName)], statement)...)
				walk(statement.Body)
				continue
			case "sent":
				var failures []string
				if vocab := m.vocabulary(action); vocab != nil {
					if sent := matchSent(statement.Text); sent != nil {
						if imported, name, ok := vocab.resolveName(sent[1]); ok {
							failures = modulePossibleFailures(imported, name, visiting)
						}
					}
				}
				result = append(result, unhandledFailures(failures, statement)...)
				walkFailureHandlerBodies(statement.Body, walk)
				continue
			}
			walk(statement.Body)
		}
	}
	walk(fn.Body)
	return uniqueSorted(result)
}

func callHasFailureHandler(s *Statement, kind string) bool {
	for _, child := range s.Body {
		if child.Kind != "handler" || match("handler", child.Text)[1] != "failure" {
			continue
		}
		handled, _, _, typed := failureHandlerHeader(child.Text)
		if (!typed || handled == kind) && !handlerPassesFailure(child) {
			return true
		}
	}
	return false
}

func handlerPassesFailure(h *Statement) bool {
	if h == nil {
		return false
	}
	if strings.Contains(h.Text, "pass failure on") || strings.Contains(h.Text, "rethrow") {
		return true
	}
	for _, child := range h.Body {
		if handlerPassesFailure(child) {
			return true
		}
	}
	return false
}

func checkFailureHandlers(operation *Statement, possible []string, add func(int, string, ...any)) {
	possibleSet := map[string]bool{}
	for _, kind := range possible {
		possibleSet[kind] = true
	}
	seen := map[string]bool{}
	general := -1
	for i, child := range operation.Body {
		if child.Kind != "handler" || match("handler", child.Text)[1] != "failure" {
			continue
		}
		kind, _, _, typed := failureHandlerHeader(child.Text)
		if !typed && match("handler", child.Text)[2] != "" {
			continue
		}
		if !typed {
			kind = ""
		} else if !possibleSet[kind] {
			add(child.Line, "failure handler %s is impossible for this operation", kind)
		}
		if seen[kind] {
			add(child.Line, "failure handler %s is declared more than once", kind)
		}
		seen[kind] = true
		if kind == "" {
			general = i
		}
		if general >= 0 && i > general {
			add(child.Line, "general failure handler must be last")
		}
	}
}

func operationStaticResult(p *Program, actions map[string]*Statement, s *Statement) bool {
	if s == nil {
		return false
	}
	if s.Kind == "collectStream" || s.Kind == "take" {
		return true
	}
	var mod *Module
	var name string
	if s.Kind == "call" {
		m := match("call", s.Text)
		name = m[1]
		if !strings.Contains(name, ".") {
			if fn := actions[name]; fn != nil {
				decl, _ := parseActionDecl(fn.Text)
				return decl.HasResult
			}
			return false
		}
		alias, action, _ := strings.Cut(name, ".")
		name = action
		mod = moduleAlias(p, alias)
	} else if s.Kind == "sent" && p != nil && p.Modules != nil && p.Modules.vocab != nil {
		m := matchSent(s.Text)
		mod, name, _ = p.Modules.vocab.resolveName(m[1])
	} else {
		return false
	}
	if mod == nil {
		return false
	}
	if op, ok := mod.Native[name]; ok {
		return op.Result != "" && op.Result != "none" && !strings.HasPrefix(op.Result, "stream of ")
	}
	if fn := mod.Actions[name]; fn != nil {
		decl, _ := parseActionDecl(fn.Text)
		return decl.HasResult
	}
	return false
}

func checkRecoveryShape(operation *Statement, hasResult bool, add func(int, string, ...any)) {
	var walk func([]*Statement)
	walk = func(stmts []*Statement) {
		for _, child := range stmts {
			if child.Kind == "call" || child.Kind == "openStream" {
				continue
			}
			if child.Kind == "recover" {
				hasValue := match("recover", child.Text)[1] != ""
				if hasResult && !hasValue {
					add(child.Line, "recover requires a value for this operation")
				}
				if !hasResult && hasValue {
					add(child.Line, "recover with is only valid for a value-returning operation")
				}
			}
			walk(child.Body)
		}
	}
	for _, child := range operation.Body {
		if child.Kind != "handler" || match("handler", child.Text)[1] != "failure" {
			continue
		}
		h := match("handler", child.Text)
		if strings.HasPrefix(h[2], "recover") {
			hasValue := strings.HasPrefix(h[2], "recover with ")
			if hasResult && !hasValue {
				add(child.Line, "recover requires a value for this operation")
			}
			if !hasResult && hasValue {
				add(child.Line, "recover with is only valid for a value-returning operation")
			}
		}
		walk(child.Body)
	}
}

func visibleFailureDefinitions(p *Program) map[string]*FailureDef {
	result, _ := visibleFailureDefinitionsWithProblems(p)
	return result
}

func visibleFailureDefinitionsWithProblems(p *Program) (map[string]*FailureDef, []string) {
	if p == nil {
		return map[string]*FailureDef{}, nil
	}
	modules := map[string]*Module(nil)
	if p.Modules != nil {
		modules = p.Modules.Aliases
	}
	return visibleFailuresFrom(p.Failures, modules, "the current file")
}

func visibleFailuresFrom(local map[string]*FailureDef, modules map[string]*Module, localOwner string) (map[string]*FailureDef, []string) {
	result := builtInFailures()
	for name, definition := range reservedStreamFailureDefs() {
		result[name] = definition
	}
	for name, definition := range reservedTimeFailures() {
		result[name] = definition
	}
	owners := map[string]string{}
	for name := range result {
		owners[name] = "the SysOneScript runtime"
	}
	var problems []string
	for name, definition := range local {
		if owner, exists := owners[name]; exists {
			problems = append(problems, fmt.Sprintf("failure %s is reserved by %s and cannot be redefined in %s", name, owner, localOwner))
			continue
		}
		result[name] = definition
		owners[name] = localOwner
	}
	if modules != nil {
		aliases := make([]string, 0, len(modules))
		for alias := range modules {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			module := modules[alias]
			for name, definition := range module.Failures {
				if module.Exports[name] {
					identity := fmt.Sprintf("module %s (imported as %s)", module.Name, alias)
					if owner, exists := owners[name]; exists && result[name] != definition {
						problems = append(problems, fmt.Sprintf("failure %s is ambiguous between %s and %s", name, owner, identity))
						continue
					}
					result[name], owners[name] = definition, identity
				}
			}
		}
	}
	return result, uniqueSorted(problems)
}

type flowSummary struct {
	fallsThrough bool
	hasValue     bool
	hasFailure   bool
}

// actionFlow is conservative: every branch must terminate for a typed action
// to be considered complete. Loops may execute zero times, so a terminal
// inside a loop cannot prove the outer action path complete.
func actionFlow(stmts []*Statement) flowSummary {
	result := flowSummary{fallsThrough: true}
	for i := 0; i < len(stmts) && result.fallsThrough; i++ {
		s := stmts[i]
		switch s.Kind {
		case "finish", "return":
			result.fallsThrough = false
			result.hasValue = match(s.Kind, s.Text)[1] != ""
		case "fail", "stop", "passFailure", "rethrow":
			result.fallsThrough = false
			result.hasFailure = true
		case "when":
			then := actionFlow(s.Body)
			otherwise := flowSummary{fallsThrough: true}
			if i+1 < len(stmts) && stmts[i+1].Kind == "otherwise" {
				otherwise = actionFlow(stmts[i+1].Body)
				i++
			}
			result.fallsThrough = then.fallsThrough || otherwise.fallsThrough
			result.hasValue = then.hasValue || otherwise.hasValue
			result.hasFailure = then.hasFailure || otherwise.hasFailure
		case "for", "while", "repeat", "map":
			result.fallsThrough = true
		}
	}
	return result
}

// handlerTerminates verifies that every handler branch produces an outcome.
// recover is terminal for the handler because control returns to the wrapped
// operation after recovery; finish/fail/pass/rethrow/stop terminate the action
// or propagate the failure.
func handlerTerminates(stmts []*Statement) bool {
	var fallsThrough func([]*Statement) bool
	fallsThrough = func(body []*Statement) bool {
		canFallThrough := true
		for i := 0; i < len(body) && canFallThrough; i++ {
			s := body[i]
			switch s.Kind {
			case "recover", "finish", "fail", "stop", "passFailure", "rethrow", "return":
				canFallThrough = false
			case "when":
				then := fallsThrough(s.Body)
				otherwise := true
				if i+1 < len(body) && body[i+1].Kind == "otherwise" {
					otherwise = fallsThrough(body[i+1].Body)
					i++
				}
				canFallThrough = then || otherwise
			case "for", "while", "repeat", "map":
				canFallThrough = true
			}
		}
		return canFallThrough
	}
	return !fallsThrough(stmts)
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

func moduleAlias(p *Program, alias string) *Module {
	if p == nil {
		return nil
	}
	if p.Modules != nil {
		if mod := p.Modules.Aliases[alias]; mod != nil {
			return mod
		}
	}
	// std/time is core language surface: it stays reachable under its default
	// alias even without an explicit import, so canonical English time
	// statements and technical fallbacks share one implementation.
	if alias == "time" {
		mod, _ := stdModule("std/time")
		return mod
	}
	return nil
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
		if sameTypeRef(actual, wanted.base()) {
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
	if typ.Name == "Result" {
		if field != "succeeded" && field != "value" && field != "failure" {
			return fmt.Sprintf("Result has no field %s", field)
		}
		return ""
	}
	if typ.Name == "ResultSuccess" {
		if field == "failure" {
			return "Result.failure is unavailable in a successful branch"
		}
		if field != "succeeded" && field != "value" {
			return fmt.Sprintf("Result has no field %s", field)
		}
		return ""
	}
	if typ.Name == "ResultFailure" {
		if field == "value" {
			return "Result.value is unavailable in a failed branch"
		}
		if field != "succeeded" && field != "failure" {
			return fmt.Sprintf("Result has no field %s", field)
		}
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
		if typ.Name == "Result" {
			if field != "succeeded" && field != "value" && field != "failure" {
				return fmt.Sprintf("Result has no field %s", field)
			}
			typ = TypeRef{}
			continue
		}
		if typ.Name == "ResultSuccess" {
			if field == "failure" {
				return "Result.failure is unavailable in a successful branch"
			}
			if field != "succeeded" && field != "value" {
				return fmt.Sprintf("Result has no field %s", field)
			}
		}
		if typ.Name == "ResultFailure" {
			if field == "value" {
				return "Result.value is unavailable in a failed branch"
			}
			if field != "succeeded" && field != "failure" {
				return fmt.Sprintf("Result has no field %s", field)
			}
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

func resultNarrowing(expression string) (string, bool) {
	parts := strings.SplitN(strings.TrimSpace(expression), " of ", 2)
	if len(parts) == 2 && strings.TrimSpace(parts[0]) == "succeeded" {
		name := strings.TrimSpace(parts[1])
		if validName(name) {
			return name, true
		}
	}
	return "", false
}

func captureTargetKnown(p *Program, actions map[string]*Statement, target string) bool {
	if actions[target] != nil {
		return true
	}
	if strings.Contains(target, ".") && p != nil && p.Modules != nil {
		alias, action, _ := strings.Cut(target, ".")
		if module := p.Modules.Aliases[alias]; module != nil {
			return module.Actions[action] != nil || module.Native[action].Name != ""
		}
	}
	return false
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

// ---------------------------------------------------------------------------
// Stream handling: static ownership, bounds, policy, effect, and obligation
// checks for the canonical stream-handling grammar (docs/sysonescript-
// stream-handling-spec.md).

var streamDurationRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s+(?:milliseconds?|seconds?|minutes?|hours?|days?)$`)

// positiveDurationLiteral reports whether text is a positive duration literal
// such as "500 milliseconds". Zero is invalid unless a construction explicitly
// permits it, and none of the current constructions do.
func positiveDurationLiteral(text string) bool {
	m := streamDurationRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return err == nil && v > 0
}

func validHTTPStatus(text string) bool {
	code, err := strconv.Atoi(strings.TrimSpace(text))
	return err == nil && code >= 400 && code <= 599
}

// isStreamAliasCall reports whether a qualified call targets a std/streams
// technical alias: the one streaming surface that is valid as an ordinary
// call because it names a derived stream.
func isStreamAliasCall(p *Program, target string) bool {
	alias, action, qualified := strings.Cut(target, ".")
	if !qualified {
		return false
	}
	mod := moduleAlias(p, alias)
	if mod == nil || mod.Key != "std/streams" {
		return false
	}
	_, known := streamAlias(action)
	return known
}

func streamActionObligations(p *Program, target string) []string {
	_, obligations := streamActionMetadata(p, target)
	return obligations
}

func ownsHTTPResponse(obligations []string) bool {
	for _, obligation := range obligations {
		if obligation == "http-response" {
			return true
		}
	}
	return false
}

func streamChild(s *Statement, kind string) *Statement {
	for _, c := range s.Body {
		if c.Kind == kind {
			return c
		}
	}
	return nil
}

func streamChildren(s *Statement, kind string) []*Statement {
	var out []*Statement
	for _, c := range s.Body {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// streamSink resolves the derived binding name: either the header's inline
// "called" name or a "called <name>" continuation line.
func streamSink(s *Statement, inline string) string {
	if inline != "" {
		return inline
	}
	if c := streamChild(s, "calledName"); c != nil {
		return match("calledName", c.Text)[1]
	}
	return ""
}

// streamHandlerBodies returns exactly the statements the runtime executes for
// an item. Policy continuation lines are metadata, not alternate code paths.
func streamHandlerBodies(s *Statement) [][]*Statement {
	switch s.Kind {
	case "handleEachStream", "handleOneStream", "filterStream", "projectStream", "newestStream", "conflateStream", "exhaustStream":
		return [][]*Statement{streamHandlerStatements(s)}
	}
	return nil
}

// streamCallTargets lists every module or local action invoked by a handler.
func streamCallTargets(bodies [][]*Statement) []string {
	var targets []string
	var walk func([]*Statement)
	walk = func(sts []*Statement) {
		for _, st := range sts {
			switch st.Kind {
			case "call":
				if m := match("call", st.Text); m != nil {
					targets = append(targets, m[1])
				}
			case "sent":
				if m := matchSent(st.Text); m != nil {
					targets = append(targets, m[1])
				}
			}
			walk(st.Body)
		}
	}
	for _, body := range bodies {
		walk(body)
	}
	return targets
}

// streamActionMetadata resolves the declared effect and ownership metadata of
// a module action. External module actions are consulted first because they
// carry owned obligations beyond their native effect summary.
func streamActionMetadata(p *Program, target string) (effects, obligations []string) {
	if p == nil || !strings.Contains(target, ".") {
		return nil, nil
	}
	alias, action, _ := strings.Cut(target, ".")
	mod := moduleAlias(p, alias)
	if mod == nil {
		return nil, nil
	}
	if mod.Key == "std/http" && action == "listen" {
		return []string{"network-listen"}, []string{"http-response"}
	}
	if mod.external != nil {
		for _, a := range mod.external.Actions {
			if a.Name == action {
				return a.Effects, a.Owns
			}
		}
	}
	if op, ok := mod.Native[action]; ok {
		return op.Effects, nil
	}
	return nil, nil
}

// streamCancellingEffects are effect names that cancellation cannot undo:
// the explicit stream vocabulary entry plus the mutating capability effects
// external module definitions declare.
var streamCancellingEffects = map[string]bool{
	"non-idempotent": true, "write": true, "process": true, "network": true,
}

// streamHandlerNonIdempotent reports whether any invoked action declares an
// effect that cancellation cannot undo.
func streamHandlerNonIdempotent(p *Program, targets []string) bool {
	for _, target := range targets {
		effects, _ := streamActionMetadata(p, target)
		for _, effect := range effects {
			if streamCancellingEffects[effect] {
				return true
			}
		}
	}
	return false
}

// streamHandlerOwnsObligations reports whether any invoked action owns
// response, acknowledgment, or similar completion obligations.
func streamHandlerOwnsObligations(p *Program, targets []string) bool {
	for _, target := range targets {
		if _, obligations := streamActionMetadata(p, target); len(obligations) > 0 {
			return true
		}
	}
	return false
}

// statementMentionsBinding reports whether text references the item binding
// as a whole word outside double-quoted strings. Quoted data and substrings
// of longer identifiers ("deadline" for "line") are not uses of the binding.
func statementMentionsBinding(text, item string) bool {
	unquoted := string(maskQuoted(text))
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(item) + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(unquoted)
}

// statementCompletesObligation reports whether one call or send statement
// completes the item's obligations: it must use the item binding and either
// invoke an action that owns obligations or name one of the inherited
// obligation names.
func statementCompletesObligation(p *Program, s *Statement, item string, obligations []string) bool {
	if s.Kind == "httpRespond" || s.Kind == "httpRespondComplete" {
		if m := match(s.Kind, s.Text); m != nil {
			return m[1] == item
		}
		return false
	}
	if s.Kind != "call" && s.Kind != "sent" {
		return false
	}
	if !statementMentionsBinding(s.Text, item) {
		return false
	}
	var target, argsText string
	if s.Kind == "call" {
		if m := match("call", s.Text); m != nil {
			target, argsText = m[1], m[2]
		}
	} else if m := matchSent(s.Text); m != nil {
		target, argsText = m[1], m[2]
		if argsText == "" {
			argsText = m[3]
		}
	}
	if ownsHTTPResponse(obligations) {
		alias, action, qualified := strings.Cut(target, ".")
		if qualified {
			if mod := moduleAlias(p, alias); mod != nil && mod.Key == "std/http" {
				switch action {
				case "respond_status", "respond_text", "respond_json", "respond":
					args, err := splitExpressions(argsText)
					return err == nil && len(args) > 0 && strings.TrimSpace(args[0]) == item
				}
			}
		}
	}
	if len(streamActionObligations(p, target)) > 0 {
		return true
	}
	for _, obligation := range obligations {
		if statementMentionsBinding(s.Text, obligation) {
			return true
		}
	}
	return false
}

// pathsCompleteObligation reports whether every linearly reachable path
// through the statement list completes the item's obligation. A statement
// that completes it satisfies the path; when/otherwise chains require every
// branch (plus the implicit else of a one-sided when) to complete.
func pathsCompleteObligation(p *Program, sts []*Statement, item string, obligations []string) bool {
	for i := 0; i < len(sts); i++ {
		s := sts[i]
		if statementCompletesObligation(p, s, item, obligations) {
			return true
		}
		switch s.Kind {
		case "when", "otherwise":
			j := i
			hasOtherwise := false
			for j < len(sts) && (sts[j].Kind == "when" || sts[j].Kind == "otherwise") {
				if sts[j].Kind == "otherwise" {
					hasOtherwise = true
				}
				j++
			}
			rest := sts[j:]
			for k := i; k < j; k++ {
				branch := append(append([]*Statement{}, sts[k].Body...), rest...)
				if !pathsCompleteObligation(p, branch, item, obligations) {
					return false
				}
			}
			if !hasOtherwise && !pathsCompleteObligation(p, rest, item, obligations) {
				return false
			}
			i = j - 1
		default:
			if len(s.Body) > 0 && pathsCompleteObligation(p, s.Body, item, obligations) {
				return true
			}
		}
	}
	return false
}

// streamHandlerCompletesObligations reports whether every reachable handler
// path completes the owned item's obligations.
func streamHandlerCompletesObligations(p *Program, bodies [][]*Statement, item string, obligations []string) bool {
	for _, body := range bodies {
		if !pathsCompleteObligation(p, body, item, obligations) {
			return false
		}
	}
	return true
}

// checkStreamConstruction validates one canonical stream-handling statement
// and updates the ownership map: the source binding is consumed, and derived
// bindings become the new single owners, inheriting source obligations.
func checkStreamConstruction(p *Program, s *Statement, m []string, owned map[string]*streamOwnershipState) []Diagnostic {
	var ds []Diagnostic
	diag := func(line int, format string, args ...any) {
		ds = append(ds, Diagnostic{line, 1, fmt.Sprintf(format, args...)})
	}
	consume := func(name string, line int) []string {
		state := owned[name]
		if state == nil {
			diag(line, "%s is not an owned stream", name)
			return nil
		}
		if state.consumed {
			diag(line, "%s was already consumed", name)
			return nil
		}
		state.consumed = true
		return state.obligations
	}
	// consumeOrNoun consumes name when it names an owned stream. Forms whose
	// first noun names the item type rather than a binding ("limit metrics
	// ...", "require an update ...") use this: an unknown name is an item
	// noun, not an ownership error.
	consumeOrNoun := func(name string, line int) []string {
		if owned[name] == nil {
			return nil
		}
		return consume(name, line)
	}
	// childNumber reads a numeric bound from a continuation line.
	childNumber := func(kind string) string {
		c := streamChild(s, kind)
		if c == nil {
			return ""
		}
		return match(kind, c.Text)[1]
	}
	bind := func(name string, line int, obligations []string) {
		if name == "" {
			return
		}
		if existing := owned[name]; existing != nil && !existing.consumed {
			diag(line, "stream %s is already active; consume or close it before reusing the name", name)
			return
		}
		owned[name] = &streamOwnershipState{line: line, derived: true, obligations: obligations}
	}
	switch s.Kind {
	case "quietStream":
		if !positiveDurationLiteral(m[3]) {
			diag(s.Line, "the quiet duration must be a positive duration such as \"500 milliseconds\"")
		}
		bound := m[4]
		if bound == "" {
			bound = childNumber("streamPendingBound")
		}
		if m[1] != "" && bound == "" {
			diag(s.Line, "keyed quiet waiting requires a bounded key count: add \"with at most N pending %ss\"", m[1])
		} else if bound != "" {
			if n, err := strconv.Atoi(bound); err != nil || n <= 0 {
				diag(s.Line, "the pending-key bound must be a positive integer")
			}
		}
		obligations := consume(m[2], s.Line)
		sink := streamSink(s, m[5])
		if sink == "" {
			diag(s.Line, "quiet waiting must name its derived stream: add \"called <name>\"")
		}
		bind(sink, s.Line, obligations)
	case "limitStream":
		allowanceText := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[3]), "at most "))
		if allowanceText != "one" {
			if allowance, err := strconv.Atoi(allowanceText); err != nil || allowance <= 0 {
				diag(s.Line, "the rate allowance must be \"one\" or a positive integer")
			}
		}
		policy := m[5]
		policies := streamChildren(s, "throttlePolicy")
		if policy == "" && len(policies) == 0 {
			diag(s.Line, "the rate policy is mandatory: add a line \"keeping the first\" or \"keeping the latest\"")
		} else if len(policies) > 1 || (policy != "" && len(policies) > 0) {
			diag(s.Line, "the rate policy must be declared exactly once")
		} else if policy == "" {
			policy = match("throttlePolicy", policies[0].Text)[1]
		}
		bound := m[6]
		if bound == "" {
			bound = childNumber("streamKeyBound")
		}
		if m[2] != "" && bound == "" {
			diag(s.Line, "keyed rate limiting requires a bounded key count: add \"with at most N %ss\"", m[2])
		} else if bound != "" {
			if n, err := strconv.Atoi(bound); err != nil || n <= 0 {
				diag(s.Line, "the rate-limit key bound must be a positive integer")
			}
		}
		obligations := consumeOrNoun(m[1], s.Line)
		if rejection := streamChild(s, "rejectExcess"); rejection != nil && !validHTTPStatus(match("rejectExcess", rejection.Text)[1]) {
			diag(rejection.Line, "the rejection status must be an integer from 400 through 599")
		}
		if len(obligations) > 0 {
			if rejection := streamChild(s, "rejectExcess"); rejection == nil {
				diag(s.Line, "rate limiting may drop items that own obligations; add \"rejecting excess %s with status <code>\" to complete them", m[1])
			}
		}
		sink := streamSink(s, m[7])
		if sink == "" {
			diag(s.Line, "rate limiting must name its derived stream: add \"called <name>\"")
		}
		bind(sink, s.Line, obligations)
	case "handleEachStream":
		bound := m[3]
		if bound == "" {
			bound = childNumber("streamBound")
		}
		if bound == "" {
			diag(s.Line, "the handling policy is ambiguous: add \"with at most N at once\" for concurrent handling or \"one at a time\" for sequential handling")
		} else if n, err := strconv.Atoi(bound); err != nil || n <= 0 {
			diag(s.Line, "the concurrency bound must be a positive integer")
		}
		obligations := consume(m[2], s.Line)
		bodies := streamHandlerBodies(s)
		if len(obligations) > 0 && !streamHandlerCompletesObligations(p, bodies, m[1], obligations) {
			diag(s.Line, "an owned source item must be completed, transferred, or explicitly rejected on every reachable handler path; the handler never uses %s", m[1])
		}
	case "handleOneStream":
		obligations := consume(m[2], s.Line)
		bodies := streamHandlerBodies(s)
		if len(obligations) > 0 && !streamHandlerCompletesObligations(p, bodies, m[1], obligations) {
			diag(s.Line, "an owned source item must be completed, transferred, or explicitly rejected on every reachable handler path; the handler never uses %s", m[1])
		}
	case "newestStream":
		sourceObligations := []string(nil)
		if source := owned[m[3]]; source != nil {
			sourceObligations = source.obligations
		}
		if streamChild(s, "newestCancel") == nil {
			diag(s.Line, "latest-only handling must declare its cancellation line: \"when a newer %s arrives cancel the previous work\"", m[1])
		}
		if m[2] != "" {
			if streamChild(s, "streamBound") == nil || streamChild(s, "streamKnownBound") == nil {
				diag(s.Line, "keyed latest-only handling requires both a concurrency bound (\"with at most N %ss at once\") and a known-key bound (\"and at most M known %ss\")", m[2], m[2])
			}
		} else if streamChild(s, "streamBound") != nil || streamChild(s, "streamKnownBound") != nil {
			diag(s.Line, "non-keyed latest-only handling does not accept keyed concurrency or known-key bounds")
			for _, kind := range []string{"streamBound", "streamKnownBound"} {
				if value := childNumber(kind); value != "" {
					if n, err := strconv.Atoi(value); err != nil || n <= 0 {
						diag(s.Line, "keyed latest-only bounds must be positive integers")
					}
				}
			}
		}
		bodies := streamHandlerBodies(s)
		targets := streamCallTargets(bodies)
		if streamHandlerNonIdempotent(p, targets) && streamChild(s, "effectAck") == nil {
			diag(s.Line, "the handler performs non-idempotent effects; cancellation does not reverse completed effects; add \"acknowledging completed effects are not reversed\"")
		}
		if (len(sourceObligations) > 0 || streamHandlerOwnsObligations(p, targets)) && streamChild(s, "cancelPolicy") == nil {
			diag(s.Line, "the handler owns completion obligations; add \"canceling an older %s with status <code>\" so canceled items are completed", m[1])
		}
		if ownsHTTPResponse(sourceObligations) && !streamHandlerCompletesObligations(p, bodies, m[1], sourceObligations) {
			diag(s.Line, "an owned source item must be completed, transferred, or explicitly rejected on every reachable handler path; the handler never uses %s", m[1])
		}
		if policy := streamChild(s, "cancelPolicy"); policy != nil {
			if status := match("cancelPolicy", policy.Text)[2]; !validHTTPStatus(status) {
				diag(policy.Line, "the cancellation status must be an integer from 400 through 599")
			}
		}
		consume(m[3], s.Line)
	case "conflateStream":
		if streamChild(s, "conflatePolicy") == nil {
			diag(s.Line, "conflation must declare its retention line: \"keeping only the latest waiting update:\"")
		} else if len(streamChildren(s, "conflatePolicy")) > 1 {
			diag(s.Line, "conflation must declare its retention line exactly once")
		}
		if obligations := consume(m[1], s.Line); len(obligations) > 0 {
			diag(s.Line, "conflation drops waiting items; it is invalid while items own unresolved obligations")
		}
	case "exhaustStream":
		policies := streamChildren(s, "exhaustPolicy")
		if len(policies) != 1 {
			diag(s.Line, "busy handling must declare exactly one policy: \"ignoring new %ss while busy:\" or \"rejecting new %ss with status <code> while busy:\"", m[1], m[1])
		}
		source := strings.ReplaceAll(m[1], " ", "_")
		obligations := consume(source, s.Line)
		if ownsHTTPResponse(obligations) && !streamHandlerCompletesObligations(p, streamHandlerBodies(s), source, obligations) {
			diag(s.Line, "an owned source item must be completed, transferred, or explicitly rejected on every reachable handler path; the handler never uses %s", source)
		}
		if len(policies) == 1 {
			pm := match("exhaustPolicy", policies[0].Text)
			if pm[1] == "ignoring" && len(obligations) > 0 {
				diag(policies[0].Line, "ignoring is valid only for values without completion obligations; use \"rejecting new %ss with status <code> while busy:\"", m[1])
			}
			if pm[1] == "rejecting" {
				if pm[2] == "" {
					diag(policies[0].Line, "rejection must complete the item with an explicit status: \"rejecting new %ss with status <code> while busy:\"", m[1])
				} else if !validHTTPStatus(pm[2]) {
					diag(policies[0].Line, "the rejection status must be an integer from 400 through 599")
				}
			}
		}
	case "batchStream":
		if count, err := strconv.Atoi(m[3]); err != nil || count <= 0 {
			diag(s.Line, "the batch size bound must be a positive integer")
		}
		window := streamChild(s, "batchWindow")
		if window == nil {
			diag(s.Line, "batching requires a time window: add a line \"or after <duration>\"")
		} else if !positiveDurationLiteral(match("batchWindow", window.Text)[1]) {
			diag(window.Line, "the batch time window must be a positive duration such as \"5 seconds\"")
		}
		bound := m[4]
		if bound == "" {
			bound = childNumber("streamKeyBound")
		}
		if m[2] != "" && bound == "" {
			diag(s.Line, "keyed batching requires a bounded key count: add \"with at most N %ss\"", m[2])
		} else if bound != "" {
			if n, err := strconv.Atoi(bound); err != nil || n <= 0 {
				diag(s.Line, "the batch key bound must be a positive integer")
			}
		}
		obligations := consumeOrNoun(m[1], s.Line)
		sink := streamSink(s, m[5])
		if sink == "" {
			diag(s.Line, "batching must name its derived stream: add \"called <name>\"")
		}
		bind(sink, s.Line, obligations)
	case "distinctStream":
		obligations := consume(m[1], s.Line)
		bind(m[2], s.Line, obligations)
	case "distinctKeyStream":
		obligations := consume(m[2], s.Line)
		bind(m[3], s.Line, obligations)
	case "idleStream":
		if !positiveDurationLiteral(m[3]) {
			diag(s.Line, "the idle deadline must be a positive duration such as \"30 seconds\"")
		}
		source := m[1]
		if source == "" {
			source = m[4]
		}
		if source == "" {
			source = m[2]
		}
		if m[1] == "" && m[4] == "" && owned[source] == nil && owned[source+"s"] != nil {
			source += "s"
		}
		var obligations []string
		if source == "" || owned[source] == nil {
			diag(s.Line, "idle deadlines require an active source stream")
		} else {
			obligations = consume(source, s.Line)
		}
		sink := streamSink(s, m[5])
		if sink == "" {
			diag(s.Line, "idle deadlines must name their derived stream: add \"called <name>\"")
		}
		bind(sink, s.Line, obligations)
	case "takeForStream":
		if !positiveDurationLiteral(m[2]) {
			diag(s.Line, "the listening lifetime must be a positive duration such as \"10 minutes\"")
		}
		obligations := consume(m[1], s.Line)
		sink := streamSink(s, m[3])
		if sink == "" {
			diag(s.Line, "bounded listening must name its derived stream: add \"called <name>\"")
		}
		bind(sink, s.Line, obligations)
	case "deadlineStream":
		if !positiveDurationLiteral(m[2]) {
			diag(s.Line, "the deadline must be a positive duration such as \"10 minutes\"")
		}
		obligations := consume(m[1], s.Line)
		sink := streamSink(s, m[3])
		if sink == "" {
			diag(s.Line, "required deadlines must name their derived stream: add \"called <name>\"")
		}
		bind(sink, s.Line, obligations)
	case "filterStream":
		keeps := 0
		for _, block := range s.Body {
			if block.Kind == "when" {
				count := 0
				for _, c := range block.Body {
					if c.Kind == "keepItem" {
						count++
						if name := match("keepItem", c.Text)[1]; name != m[1] {
							diag(c.Line, "the filter block must keep %s, not %s", m[1], name)
						}
					}
				}
				if count > 1 {
					diag(block.Line, "the filter block must keep %s at most once per item", m[1])
				}
				keeps += count
			}
		}
		if keeps == 0 {
			diag(s.Line, "the filter block must keep %s on at least one path", m[1])
		}
		obligations := consume(m[2], s.Line)
		bind(m[3], s.Line, obligations)
	case "projectStream":
		if uses := streamChildren(s, "useValue"); len(uses) != 1 {
			diag(s.Line, "the projection block must use exactly one value: add a single \"use <value>\" line")
		}
		obligations := consume(m[2], s.Line)
		bind(m[3], s.Line, obligations)
	}
	return ds
}

// checkStreamAliasCalls gives every std/streams technical alias one
// deterministic meaning: the argument list must match the alias signature
// exactly, and derived-stream aliases must name their sink. Aliases are
// canonical calls, so they never reach the paid semantic engine.
func checkStreamAliasCalls(p *Program) []Diagnostic {
	if p == nil {
		return nil
	}
	var ds []Diagnostic
	var walk func([]*Statement)
	walk = func(sts []*Statement) {
		for _, s := range sts {
			var target, argsText, sink string
			switch s.Kind {
			case "call":
				m := match("call", s.Text)
				target, argsText, sink = m[1], m[2], m[3]
			case "sent":
				m := matchSent(s.Text)
				if m == nil {
					continue
				}
				target, argsText, sink = m[1], m[2], m[4]
				if argsText == "" {
					argsText = m[3]
				}
			default:
				walk(s.Body)
				continue
			}
			alias, action, qualified := strings.Cut(target, ".")
			if !qualified {
				walk(s.Body)
				continue
			}
			mod := moduleAlias(p, alias)
			if mod == nil || mod.Key != "std/streams" {
				walk(s.Body)
				continue
			}
			info, known := streamAlias(action)
			if !known {
				ds = append(ds, Diagnostic{s.Line, 1, action + " is not a std/streams operation"})
				continue
			}
			var args []string
			if strings.TrimSpace(argsText) != "" {
				args = splitStreamAliasArguments(argsText)
			}
			if len(args) != len(info.Params) {
				ds = append(ds, Diagnostic{s.Line, 1, fmt.Sprintf("streams.%s expects %d argument(s) (%s); got %d. It means: %s", action, len(info.Params), strings.Join(info.Params, ", "), len(args), info.Canonical)})
				continue
			}
			ds = append(ds, validateStreamAliasArguments(s.Line, action, args)...)
			if action == "merge" || action == "concat" || action == "switch_latest" || action == "exhaust" || action == "conflate" {
				ds = append(ds, Diagnostic{s.Line, 1, fmt.Sprintf("streams.%s requires a canonical handler block so effects and obligations can be checked; use: %s", action, info.Canonical)})
				continue
			}
			if info.RequiresSink && sink == "" {
				ds = append(ds, Diagnostic{s.Line, 1, fmt.Sprintf("streams.%s must name its derived stream; add \"called <name>\". It means: %s", action, info.Canonical)})
			}
			walk(s.Body)
		}
	}
	walk(p.Statements)
	return ds
}

var streamAliasStatusRe = regexp.MustCompile(`^reject with status (\d+)$`)

// validateStreamAliasArguments checks every technical alias argument against
// its canonical meaning: durations must be positive, counts positive
// integers, throttle policies one of first/latest, and exhaust policies an
// explicit rejection.
func validateStreamAliasArguments(line int, action string, args []string) []Diagnostic {
	var ds []Diagnostic
	positiveDuration := func(i int) {
		if !positiveDurationLiteral(args[i]) {
			ds = append(ds, Diagnostic{line, 1, fmt.Sprintf("streams.%s requires a positive duration such as \"500 milliseconds\"; got %q", action, args[i])})
		}
	}
	positiveInteger := func(i int) {
		if n, err := strconv.Atoi(strings.TrimSpace(args[i])); err != nil || n <= 0 {
			ds = append(ds, Diagnostic{line, 1, fmt.Sprintf("streams.%s requires a positive integer; got %q", action, args[i])})
		}
	}
	switch action {
	case "debounce", "idle_timeout", "take_for":
		positiveDuration(1)
	case "throttle":
		positiveInteger(1)
		if _, err := streamUnitDuration(args[2]); err != nil {
			ds = append(ds, Diagnostic{line, 1, fmt.Sprintf("streams.throttle requires a positive window unit such as \"second\" or \"minute\"; got %q", args[2])})
		}
		if policy := strings.TrimSpace(args[3]); policy != "first" && policy != "latest" {
			ds = append(ds, Diagnostic{line, 1, fmt.Sprintf("streams.throttle requires a keeping policy of \"first\" or \"latest\"; got %q", args[3])})
		}
	case "merge":
		positiveInteger(1)
	case "batch":
		positiveInteger(1)
		positiveDuration(2)
	case "exhaust":
		policy := strings.TrimSpace(args[1])
		if policy != "ignore" {
			status := streamAliasStatusRe.FindStringSubmatch(policy)
			if status == nil || !validHTTPStatus(status[1]) {
				ds = append(ds, Diagnostic{line, 1, fmt.Sprintf("streams.exhaust requires \"ignore\" or \"reject with status <code>\" (400 through 599); got %q", args[1])})
			}
		}
	}
	return ds
}

// splitStreamAliasArguments splits a comma-separated argument list, honoring
// double-quoted text.
func splitStreamAliasArguments(text string) []string {
	masked := string(maskQuoted(text))
	var args []string
	start := 0
	for i := 0; i <= len(masked); i++ {
		if i == len(masked) || masked[i] == ',' {
			if field := strings.TrimSpace(text[start:i]); field != "" {
				args = append(args, field)
			}
			start = i + 1
		}
	}
	return args
}

// reservedStreamFailureDefs are the reserved typed failures of the stream
// handling model. They exist before the runtime registers them so failure
// handlers and redefinitions are checked consistently.
func reservedStreamFailureDefs() map[string]*FailureDef {
	integer := func() TypeRef { return TypeRef{Name: "integer"} }
	text := func() TypeRef { return TypeRef{Name: "text"} }
	duration := func() TypeRef { return TypeRef{Name: "duration"} }
	optionalText := func() TypeRef { return TypeRef{Name: "text", Optional: true} }
	return map[string]*FailureDef{
		"StreamKeyLimitExceeded":         {Name: "StreamKeyLimitExceeded", Fields: []RecordField{{Name: "limit", Type: integer()}, {Name: "operation", Type: text()}}},
		"StreamConcurrencyLimitExceeded": {Name: "StreamConcurrencyLimitExceeded", Fields: []RecordField{{Name: "limit", Type: integer()}}},
		"StreamIdleTimeout":              {Name: "StreamIdleTimeout", Fields: []RecordField{{Name: "idle_for", Type: duration()}}},
		"StreamDeadlineExceeded":         {Name: "StreamDeadlineExceeded", Fields: []RecordField{{Name: "deadline", Type: duration()}}},
		"StreamHandlerCleanupFailed":     {Name: "StreamHandlerCleanupFailed", Fields: []RecordField{{Name: "operation", Type: text()}, {Name: "reason", Type: text()}}},
		"StreamObligationAbandoned":      {Name: "StreamObligationAbandoned", Fields: []RecordField{{Name: "operation", Type: text()}, {Name: "item", Type: optionalText()}}},
	}
}

// checkTimeStatement validates one canonical timing statement: duration
// literals, timer and schedule structure, required named zones, DST and
// catch-up policy placement, and test-harness virtual-time use. It returns
// the stream or value name the statement binds, if any.
func checkTimeStatement(s *Statement, checkExpr func(*Statement, string, bool), add func(*Statement, string)) string {
	m := match(s.Kind, s.Text)
	durationExpr := func(stmt *Statement, expr string) {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			add(stmt, "a duration is required")
			return
		}
		// A bare literal must parse strictly; names and composed expressions
		// are validated by the ordinary expression checker.
		if expr[0] >= '0' && expr[0] <= '9' {
			if _, err := ParseDurationValue(expr); err != nil {
				add(stmt, "invalid duration "+expr)
			}
			return
		}
		checkExpr(stmt, expr, false)
	}
	switch s.Kind {
	case "timerOneShot":
		durationExpr(s, m[1])
		if len(s.Body) != 0 {
			add(s, "a one-shot timer declaration takes no nested lines")
		}
		return m[2]
	case "timerEvery":
		durationExpr(s, m[2])
		policies := 0
		for _, child := range s.Body {
			if child.Kind != "timerPolicy" {
				add(child, strings.Fields(child.Text)[0]+" is not valid in a repeating timer declaration")
				continue
			}
			policies++
		}
		if policies > 1 {
			add(s, "declare at most one missed-tick policy")
		}
		return m[3]
	case "scheduleStream":
		rules, zones, called := 0, 0, ""
		remembering := false
		missedStartup := 0
		dstMissing, dstTwice := false, false
		for _, child := range s.Body {
			switch child.Kind {
			case "scheduleRuleHour", "scheduleRuleAt", "scheduleRuleMonth":
				rules++
				if cm := match(child.Kind, child.Text); len(cm) >= 3 {
					for _, part := range cm[2:] {
						if part == "" {
							continue
						}
						if n, err := strconv.Atoi(part); err != nil || n < 0 {
							add(child, "invalid clock time "+child.Text)
						}
					}
				}
			case "scheduleZone":
				zones++
				checkExpr(child, strings.TrimSpace(match("scheduleZone", child.Text)[1]), false)
			case "scheduleRemember":
				remembering = true
			case "scheduleCatchup":
				if strings.Contains(child.Text, "scheduled time was missed") || strings.Contains(child.Text, "missed scheduled times") {
					missedStartup++
				}
			case "scheduleNotExist":
				dstMissing = true
				if len(child.Body) == 0 {
					add(child, "declare whether a nonexistent local time is skipped or moved to the next valid time")
				}
				for _, grandchild := range child.Body {
					if grandchild.Kind != "scheduleDSTChoice" {
						add(grandchild, strings.Fields(grandchild.Text)[0]+" is not valid here")
					}
				}
			case "scheduleOccursTwice":
				dstTwice = true
				if len(child.Body) == 0 {
					add(child, "declare which occurrence of a repeated local time runs")
				}
				for _, grandchild := range child.Body {
					if grandchild.Kind != "scheduleDSTChoice" {
						add(grandchild, strings.Fields(grandchild.Text)[0]+" is not valid here")
					}
				}
			case "scheduleCalled":
				if called != "" {
					add(child, "declare the schedule name once")
				}
				called = match("scheduleCalled", child.Text)[1]
			default:
				add(child, strings.Fields(child.Text)[0]+" is not valid in a scheduled times declaration")
			}
		}
		if rules != 1 {
			add(s, "a calendar schedule declares exactly one timing rule")
		}
		if zones != 1 {
			add(s, "a calendar schedule requires exactly one named time zone")
		}
		if called == "" {
			add(s, "a calendar schedule requires a called name")
		}
		if missedStartup > 0 && !remembering {
			add(s, "startup catch-up requires remembering progress as a checkpoint identity")
		}
		if missedStartup > 1 {
			add(s, "declare at most one missed-startup policy")
		}
		_ = dstMissing
		_ = dstTwice
		return called
	case "findCalendar":
		checkExpr(s, m[3], false)
		policies, zones, called := 0, 0, ""
		for _, child := range s.Body {
			switch child.Kind {
			case "dayNotExist":
				policies++
				if len(child.Body) == 0 {
					add(child, "declare an invalid-day policy")
				}
				for _, grandchild := range child.Body {
					if grandchild.Kind != "dayPolicy" {
						add(grandchild, strings.Fields(grandchild.Text)[0]+" is not valid here")
					}
				}
			case "scheduleZone":
				zones++
			case "scheduleCalled":
				called = match("scheduleCalled", child.Text)[1]
			default:
				add(child, strings.Fields(child.Text)[0]+" is not valid in a calendar arithmetic declaration")
			}
		}
		if zones > 1 {
			add(s, "declare at most one time zone")
		}
		if policies > 1 {
			add(s, "declare at most one invalid-day policy")
		}
		if called == "" {
			add(s, "calendar arithmetic requires a called name")
		}
		return called
	case "deadline":
		durationExpr(s, m[1])
		return ""
	case "advanceTime":
		durationExpr(s, m[1])
		if len(s.Body) != 0 {
			add(s, "advance test time takes no nested lines")
		}
		return ""
	case "stopStream":
		return ""
	}
	return ""
}
