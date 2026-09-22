package sos

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TypeRef is the language type used by named records and typed action
// parameters. Named records deliberately remain structural at runtime; Name
// is only used to find the closed shape used for validation and tooling.
type TypeRef struct {
	Name     string   `json:"name,omitempty"`
	Optional bool     `json:"optional,omitempty"`
	Element  *TypeRef `json:"element,omitempty"`
}

func (t TypeRef) String() string {
	if t.Element != nil {
		prefix := "list of " + t.Element.String()
		if t.Optional {
			return "optional " + prefix
		}
		return prefix
	}
	if t.Optional {
		return "optional " + t.Name
	}
	return t.Name
}

func (t TypeRef) base() TypeRef {
	t.Optional = false
	return t
}

func sameTypeRef(left, right TypeRef) bool {
	if left.Name != right.Name || left.Optional != right.Optional || (left.Element == nil) != (right.Element == nil) {
		return false
	}
	return left.Element == nil || sameTypeRef(*left.Element, *right.Element)
}

type RecordField struct {
	Name string  `json:"name"`
	Type TypeRef `json:"type"`
	Line int     `json:"line,omitempty"`
}

type RecordDef struct {
	Name   string        `json:"name"`
	Fields []RecordField `json:"fields"`
	Line   int           `json:"line,omitempty"`
}

// FailureDef describes a declared catchable domain failure. Failure payloads
// use the same closed named-record rules as ordinary records, while the
// runtime adds host-owned common metadata.
type FailureDef struct {
	Name   string        `json:"name"`
	Fields []RecordField `json:"fields"`
	Line   int           `json:"line,omitempty"`
}

type ActionParam struct {
	Name string  `json:"name"`
	Type TypeRef `json:"type"`
}

type ActionDecl struct {
	Name       string
	Params     []ActionParam
	Using      []string
	Result     TypeRef
	HasResult  bool
	StreamItem TypeRef
	Streaming  bool
	Failures   []string
}

var failureCommonFields = map[string]bool{
	"kind": true, "message": true, "retryable": true, "status": true,
	"code": true, "frames": true,
}

func builtInFailures() map[string]*FailureDef {
	failures := map[string]*FailureDef{
		"StreamLimitExceeded": {
			Name: "StreamLimitExceeded",
			Fields: []RecordField{
				{Name: "limit", Type: TypeRef{Name: "integer"}},
				{Name: "received", Type: TypeRef{Name: "integer"}},
			},
		},
		"StreamKeyLimitExceeded": {
			Name: "StreamKeyLimitExceeded",
			Fields: []RecordField{
				{Name: "limit", Type: TypeRef{Name: "integer"}},
				{Name: "operation", Type: TypeRef{Name: "text"}},
			},
		},
		"StreamConcurrencyLimitExceeded": {
			Name: "StreamConcurrencyLimitExceeded",
			Fields: []RecordField{
				{Name: "limit", Type: TypeRef{Name: "integer"}},
			},
		},
		"StreamIdleTimeout": {
			Name: "StreamIdleTimeout",
			Fields: []RecordField{
				{Name: "idle_for", Type: TypeRef{Name: "duration"}},
			},
		},
		"StreamDeadlineExceeded": {
			Name: "StreamDeadlineExceeded",
			Fields: []RecordField{
				{Name: "deadline", Type: TypeRef{Name: "duration"}},
			},
		},
		"StreamHandlerCleanupFailed": {
			Name: "StreamHandlerCleanupFailed",
			Fields: []RecordField{
				{Name: "operation", Type: TypeRef{Name: "text"}},
				{Name: "reason", Type: TypeRef{Name: "text"}},
			},
		},
		"StreamObligationAbandoned": {
			Name: "StreamObligationAbandoned",
			Fields: []RecordField{
				{Name: "operation", Type: TypeRef{Name: "text"}},
				{Name: "item", Type: TypeRef{Name: "text", Optional: true}},
			},
		},
	}
	for kind, definition := range fileFailures() {
		failures[kind] = definition
	}
	return failures
}

func parseFailureDefinition(statement *Statement) (*FailureDef, []Diagnostic) {
	m := match("failure", statement.Text)
	definition := &FailureDef{Name: m[1], Line: statement.Line}
	var diagnostics []Diagnostic
	seen := map[string]bool{}
	for _, child := range statement.Body {
		if child.Kind != "field" {
			diagnostics = append(diagnostics, Diagnostic{child.Line, 1, "define failure " + m[1] + " expects fields"})
			continue
		}
		field, err := parseFieldDecl(child.Text)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{child.Line, 1, fmt.Sprintf("%s.%s: %v", m[1], strings.Fields(child.Text)[0], err)})
			continue
		}
		if failureCommonFields[field.Name] {
			diagnostics = append(diagnostics, Diagnostic{child.Line, 1, "failure field " + field.Name + " is reserved"})
			continue
		}
		if seen[field.Name] {
			diagnostics = append(diagnostics, Diagnostic{child.Line, 1, "duplicate field " + m[1] + "." + field.Name})
			continue
		}
		seen[field.Name] = true
		field.Line = child.Line
		definition.Fields = append(definition.Fields, field)
	}
	return definition, diagnostics
}

func failureFieldsByName(def *FailureDef) map[string]RecordField {
	result := make(map[string]RecordField, len(def.Fields))
	for _, field := range def.Fields {
		result[field.Name] = field
	}
	return result
}

func collectRecordDefinitions(stmts []*Statement) (map[string]*RecordDef, []Diagnostic) {
	defs := map[string]*RecordDef{}
	var diagnostics []Diagnostic
	for _, statement := range stmts {
		if statement.Kind != "define" {
			continue
		}
		name := match("define", statement.Text)[1]
		if builtInFileDefinitions()[name] != nil {
			diagnostics = append(diagnostics, Diagnostic{statement.Line, 1, "record " + name + " is reserved by the SysOneScript runtime"})
			continue
		}
		if _, exists := defs[name]; exists {
			diagnostics = append(diagnostics, Diagnostic{statement.Line, 1, "duplicate definition " + name})
			continue
		}
		definition, definitionDiagnostics := parseRecordDefinition(statement)
		diagnostics = append(diagnostics, definitionDiagnostics...)
		defs[name] = definition
	}
	return defs, diagnostics
}

func collectFailureDefinitions(stmts []*Statement) (map[string]*FailureDef, []Diagnostic) {
	defs := map[string]*FailureDef{}
	var diagnostics []Diagnostic
	for _, statement := range stmts {
		if statement.Kind != "failure" {
			continue
		}
		name := match("failure", statement.Text)[1]
		if _, exists := defs[name]; exists {
			diagnostics = append(diagnostics, Diagnostic{statement.Line, 1, "duplicate failure " + name})
			continue
		}
		definition, problems := parseFailureDefinition(statement)
		diagnostics = append(diagnostics, problems...)
		defs[name] = definition
	}
	return defs, diagnostics
}

func parseRecordDefinition(statement *Statement) (*RecordDef, []Diagnostic) {
	name := match("define", statement.Text)[1]
	definition := &RecordDef{Name: name, Line: statement.Line}
	var diagnostics []Diagnostic
	for _, child := range statement.Body {
		if child.Kind != "field" {
			diagnostics = append(diagnostics, Diagnostic{child.Line, 1, "define " + name + " expects fields"})
			continue
		}
		field, err := parseFieldDecl(child.Text)
		if err != nil {
			fieldName := "field"
			if words := strings.Fields(child.Text); len(words) > 0 {
				fieldName = words[0]
			}
			diagnostics = append(diagnostics, Diagnostic{child.Line, 1, fmt.Sprintf("%s.%s: %v", name, fieldName, err)})
			continue
		}
		field.Line = child.Line
		definition.Fields = append(definition.Fields, field)
	}
	return definition, diagnostics
}

func visibleDefinitions(local map[string]*RecordDef, modules map[string]*Module) (map[string]*RecordDef, []string) {
	result := map[string]*RecordDef{}
	for name, definition := range builtInFileDefinitions() {
		result[name] = definition
	}
	localNames := map[string]bool{}
	for name, definition := range local {
		result[name] = definition
		localNames[name] = true
	}
	seenImported := map[string]string{}
	localCollisions := map[string][]string{}
	aliases := make([]string, 0, len(modules))
	for alias := range modules {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		module := modules[alias]
		for name, definition := range module.Definitions {
			if !module.Exports[name] {
				continue
			}
			if localNames[name] {
				localCollisions[name] = append(localCollisions[name], alias)
				continue
			}
			if existing, ok := result[name]; ok && existing != definition {
				if previous := seenImported[name]; previous != "" {
					seenImported[name] = previous + ", " + alias
				} else {
					seenImported[name] = alias
				}
				continue
			}
			if previous, ok := seenImported[name]; ok && previous != alias {
				continue
			}
			result[name] = definition
			seenImported[name] = alias
		}
	}
	var collisions []string
	localCollisionNames := make([]string, 0, len(localCollisions))
	for name := range localCollisions {
		localCollisionNames = append(localCollisionNames, name)
	}
	sort.Strings(localCollisionNames)
	for _, name := range localCollisionNames {
		collisions = append(collisions, fmt.Sprintf("type name %s is declared locally and exported by import(s) %s", name, strings.Join(localCollisions[name], ", ")))
	}
	importedNames := make([]string, 0, len(seenImported))
	for name := range seenImported {
		importedNames = append(importedNames, name)
	}
	sort.Strings(importedNames)
	for _, name := range importedNames {
		aliases := seenImported[name]
		if strings.Contains(aliases, ", ") {
			collisions = append(collisions, fmt.Sprintf("type name %s is exported by multiple imports (%s)", name, aliases))
		}
	}
	return result, collisions
}

func parseType(text string, allowAny bool) (TypeRef, error) {
	text = strings.TrimSpace(text)
	optional := false
	if strings.HasPrefix(text, "optional ") {
		optional = true
		text = strings.TrimSpace(strings.TrimPrefix(text, "optional "))
	}
	if strings.HasPrefix(text, "list of ") {
		element, err := parseType(strings.TrimSpace(strings.TrimPrefix(text, "list of ")), allowAny)
		if err != nil {
			return TypeRef{}, err
		}
		// Keep optional on the outer list, not on the element.
		element.Optional = false
		return TypeRef{Optional: optional, Element: &element}, nil
	}
	if allowAny && text == "any" {
		return TypeRef{Name: "any", Optional: optional}, nil
	}
	switch text {
	case "text", "timestamp", "number", "integer", "boolean", "duration", "file", "folder":
		return TypeRef{Name: text, Optional: optional}, nil
	}
	if !validTypeName(text) {
		return TypeRef{}, fmt.Errorf("unknown type %q", text)
	}
	return TypeRef{Name: text, Optional: optional}, nil
}

func validTypeName(name string) bool {
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func parseFieldDecl(text string) (RecordField, error) {
	parts := strings.SplitN(strings.TrimSpace(text), " as ", 2)
	if len(parts) != 2 || !validBindingName(parts[0]) {
		return RecordField{}, fmt.Errorf("field must be NAME as TYPE")
	}
	t, err := parseType(parts[1], false)
	if err != nil {
		return RecordField{}, err
	}
	return RecordField{Name: parts[0], Type: t}, nil
}

func parseActionDecl(text string) (ActionDecl, error) {
	m := actionHeaderRE.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) == 0 {
		return ActionDecl{}, fmt.Errorf("invalid action declaration")
	}
	d := ActionDecl{Name: m[1]}
	params := strings.TrimSpace(m[2])
	resultText := strings.TrimSpace(m[3])
	streamText := strings.TrimSpace(m[4])
	failureText := strings.TrimSpace(m[5])
	if resultText != "" {
		t, err := parseType(resultText, true)
		if err != nil {
			return ActionDecl{}, fmt.Errorf("return type: %w", err)
		}
		d.Result, d.HasResult = t, true
	}
	if streamText != "" {
		t, err := parseType(streamText, false)
		if err != nil {
			return ActionDecl{}, fmt.Errorf("stream item type: %w", err)
		}
		d.StreamItem, d.Streaming = t, true
	}
	if failureText != "" {
		for _, name := range strings.Split(failureText, ",") {
			name = strings.TrimSpace(name)
			if !validTypeName(name) {
				return ActionDecl{}, fmt.Errorf("invalid failure name %q", name)
			}
			d.Failures = append(d.Failures, name)
		}
	}
	if params == "" {
		return d, nil
	}
	if before, using, ok := strings.Cut(params, " using "); ok {
		params = strings.TrimSpace(before)
		for _, field := range strings.Split(using, ",") {
			field = strings.TrimSpace(field)
			if !validBindingName(field) {
				return ActionDecl{}, fmt.Errorf("using expects field names")
			}
			d.Using = append(d.Using, field)
		}
	}
	for _, part := range strings.Split(params, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return ActionDecl{}, fmt.Errorf("empty action parameter")
		}
		bits := strings.SplitN(part, " as ", 2)
		if !validBindingName(strings.TrimSpace(bits[0])) {
			return ActionDecl{}, fmt.Errorf("invalid action parameter %q", strings.TrimSpace(bits[0]))
		}
		param := ActionParam{Name: strings.TrimSpace(bits[0]), Type: TypeRef{Name: "any"}}
		if len(bits) == 2 {
			t, err := parseType(bits[1], true)
			if err != nil {
				return ActionDecl{}, fmt.Errorf("parameter %s: %w", param.Name, err)
			}
			if t.Element != nil && (t.Element.Name == "any" || t.Element.Element != nil) {
				return ActionDecl{}, fmt.Errorf("parameter %s: list parameters require one concrete element type", param.Name)
			}
			param.Type = t
		}
		d.Params = append(d.Params, param)
	}
	return d, nil
}

var actionHeaderRE = regexp.MustCompile(`^to ([A-Za-z_]\w*)(?: with (.*?))?(?:(?: returning ((?:optional )?(?:list of )?(?:any|text|file|folder|timestamp|number|integer|boolean|duration|[A-Z][A-Za-z0-9_]*)))|(?: streaming (text|file|folder|timestamp|number|integer|boolean|duration|[A-Z][A-Za-z0-9_]*)))?(?: may fail with (.+?))?:$`)

func validBindingName(name string) bool {
	return validName(name) && (name[0] == '_' || name[0] >= 'a' && name[0] <= 'z')
}

func fieldsByName(def *RecordDef) map[string]RecordField {
	result := make(map[string]RecordField, len(def.Fields))
	for _, field := range def.Fields {
		result[field.Name] = field
	}
	return result
}

func validateRecordDefinitions(defs map[string]*RecordDef) []string {
	return validateRecordDefinitionSet(defs, defs)
}

func validateRecordDefinitionSet(selected, visible map[string]*RecordDef) []string {
	var problems []string
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		def := selected[name]
		seen := map[string]bool{}
		for _, field := range def.Fields {
			if seen[field.Name] {
				problems = append(problems, fmt.Sprintf("duplicate field %s.%s", name, field.Name))
			}
			seen[field.Name] = true
			if err := validateTypeRefs(field.Type, visible, map[string]bool{name: true}); err != nil {
				problems = append(problems, fmt.Sprintf("%s.%s: %v", name, field.Name, err))
			}
		}
	}
	return problems
}

func validateTypeRefs(t TypeRef, defs map[string]*RecordDef, path map[string]bool) error {
	if t.Element != nil {
		return validateTypeRefs(*t.Element, defs, path)
	}
	if t.Name == "any" || isScalarType(t.Name) {
		return nil
	}
	if defs[t.Name] == nil {
		return fmt.Errorf("unknown type %q", t.Name)
	}
	if path[t.Name] {
		return fmt.Errorf("recursive definition %s is not supported", t.Name)
	}
	next := make(map[string]bool, len(path)+1)
	for key, value := range path {
		next[key] = value
	}
	next[t.Name] = true
	for _, field := range defs[t.Name].Fields {
		if err := validateTypeRefs(field.Type, defs, next); err != nil {
			return err
		}
	}
	return nil
}

func isScalarType(name string) bool {
	switch name {
	case "text", "file", "folder", "timestamp", "number", "integer", "boolean", "duration":
		return true
	}
	return false
}

func typeMatchesRef(value any, typ TypeRef, defs map[string]*RecordDef) bool {
	if value == nil {
		return typ.Optional
	}
	if typ.Element != nil {
		items, ok := value.([]any)
		if !ok {
			return false
		}
		for _, item := range items {
			if !typeMatchesRef(item, *typ.Element, defs) {
				return false
			}
		}
		return true
	}
	if typ.Name == "any" {
		return true
	}
	switch typ.Name {
	case "text":
		_, ok := value.(string)
		return ok
	case "file", "folder":
		_, ok := value.(string)
		return ok
	case "timestamp":
		switch v := value.(type) {
		case time.Time:
			return true
		case string:
			_, err := time.Parse(time.RFC3339Nano, v)
			return err == nil
		default:
			return false
		}
	case "number":
		_, ok := number(value)
		return ok
	case "integer":
		n, ok := number(value)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n == math.Trunc(n) && n >= -(1<<53-1) && n <= 1<<53-1
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "duration":
		_, ok := value.(time.Duration)
		return ok
	}
	def := defs[typ.Name]
	if def == nil {
		return false
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	fields := fieldsByName(def)
	for key := range obj {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	for _, field := range def.Fields {
		got, present := obj[field.Name]
		if !present {
			if field.Type.Optional {
				continue
			}
			return false
		}
		if !typeMatchesRef(got, field.Type, defs) {
			return false
		}
	}
	return true
}

func propertyTyped(value any, field string, receiver TypeRef, defs map[string]*RecordDef) (any, TypeRef, error) {
	if value == nil {
		return nil, TypeRef{}, fmt.Errorf("cannot read %s of null", field)
	}
	if receiver.Name != "" && receiver.Element == nil && receiver.Name != "any" {
		if def := defs[receiver.Name]; def != nil {
			declared, ok := fieldsByName(def)[field]
			if !ok {
				return nil, TypeRef{}, fmt.Errorf("%s has no field %s", receiver.Name, field)
			}
			obj, ok := value.(map[string]any)
			if !ok {
				return nil, TypeRef{}, fmt.Errorf("cannot read %s of non-record", field)
			}
			got, present := obj[field]
			if !present {
				if declared.Type.Optional {
					return nil, declared.Type.base(), nil
				}
				return nil, TypeRef{}, fmt.Errorf("%s requires field %s as %s", receiver.Name, field, declared.Type.String())
			}
			return got, declared.Type.base(), nil
		}
	}
	got, err := property(value, field)
	return got, TypeRef{}, err
}

func valueTypeName(value any) string {
	if value == nil {
		return "null"
	}
	switch value.(type) {
	case string:
		return "text"
	case bool:
		return "boolean"
	case time.Duration:
		return "duration"
	case time.Time:
		return "timestamp"
	case []any:
		return "list"
	case map[string]any:
		return "record"
	}
	return fmt.Sprintf("%T", value)
}
