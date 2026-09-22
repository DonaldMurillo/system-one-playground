package soslsp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/internal/sossyntax"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

type lspDiagnostic struct {
	Range    lspRange `json:"range"`
	Severity int      `json:"severity"`
	Source   string   `json:"source"`
	Message  string   `json:"message"`
}

type publishParams struct {
	URI         string          `json:"uri"`
	Version     *int            `json:"version,omitempty"`
	Diagnostics []lspDiagnostic `json:"diagnostics"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type positionalParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     lspPosition            `json:"position"`
}

type didOpenParams struct {
	TextDocument struct {
		URI        string `json:"uri"`
		LanguageID string `json:"languageId"`
		Version    int    `json:"version"`
		Text       string `json:"text"`
	} `json:"textDocument"`
}

type contentChange struct {
	Range *lspRange `json:"range"`
	Text  string    `json:"text"`
}

type didChangeParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Version *int   `json:"version"`
	} `json:"textDocument"`
	ContentChanges []contentChange `json:"contentChanges"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type formattingParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

// bindPatterns extract the name bound by a sentence construction. The grammar
// is sentence led, so name binding is recognizable from statement text:
// declarations, make, loop singulars, and called result names.
var bindPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(?:command|expect)\s+(\w+)`),
	regexp.MustCompile(`^(?:argument|option|switch)\s+(\w+)`),
	regexp.MustCompile(`^make\s+(\w+)`),
	regexp.MustCompile(`^for each\s+(\w+)`),
	regexp.MustCompile(`\bcalled\s+(\w+)`),
}

func (s *server) didOpen(params json.RawMessage) {
	var p didOpenParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	version := p.TextDocument.Version
	s.docs[p.TextDocument.URI] = &document{text: p.TextDocument.Text, version: version, hasVersion: true}
	s.publishDiagnostics(p.TextDocument.URI, &version, p.TextDocument.Text)
}

func (s *server) didChange(params json.RawMessage) {
	var p didChangeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	uri := p.TextDocument.URI
	doc, ok := s.docs[uri]
	if !ok {
		doc = &document{}
		s.docs[uri] = doc
	}
	// A change older than the applied version is stale and must be ignored.
	if p.TextDocument.Version != nil && doc.hasVersion && *p.TextDocument.Version <= doc.version {
		return
	}
	for _, c := range p.ContentChanges {
		if c.Range == nil {
			doc.text = c.Text
		} else {
			doc.text = applyRange(doc.text, *c.Range, c.Text)
		}
	}
	version := new(int)
	if p.TextDocument.Version != nil {
		doc.version = *p.TextDocument.Version
		doc.hasVersion = true
	}
	*version = doc.version
	s.publishDiagnostics(uri, version, doc.text)
}

func (s *server) didClose(params json.RawMessage) {
	var p didCloseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	delete(s.docs, p.TextDocument.URI)
	delete(s.vocabCache, p.TextDocument.URI)
	s.notify("textDocument/publishDiagnostics", publishParams{
		URI:         p.TextDocument.URI,
		Diagnostics: []lspDiagnostic{},
	})
}

func (s *server) publishDiagnostics(uri string, version *int, text string) {
	diags := sos.Check(text)
	// Module-aware checking wherever a resolution root exists: sentence
	// calls resolve their vocabulary and collisions surface with origins.
	if filename := vocabFilename(uri, s.workspaceRoot); filename != "" {
		diags = sos.CheckFile(filename, text)
	}
	diagnostics := make([]lspDiagnostic, 0)
	for _, d := range sos.EditorDiagnostics(vocabFilename(uri, s.workspaceRoot), text, diags) {
		severity := 1
		if d.Severity == "information" {
			severity = 3
		}
		// Core diagnostics are one based line, one based byte column.
		line := d.Line - 1
		if line < 0 {
			line = 0
		}
		src := lineAt(text, line)
		startChar := byteToChar(src, d.Column-1)
		endChar := startChar + charWidthAt(src, d.Column-1)
		diagnostics = append(diagnostics, lspDiagnostic{
			Range: lspRange{
				Start: lspPosition{Line: line, Character: startChar},
				End:   lspPosition{Line: line, Character: endChar},
			},
			Severity: severity,
			Source:   "sos",
			Message:  d.Message,
		})
	}
	s.notify("textDocument/publishDiagnostics", publishParams{
		URI:         uri,
		Version:     version,
		Diagnostics: diagnostics,
	})
}

func (s *server) documentAt(params json.RawMessage) (uri, text string, pos lspPosition, ok bool) {
	var p positionalParams
	if err := json.Unmarshal(params, &p); err != nil {
		return "", "", lspPosition{}, false
	}
	doc, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return "", "", lspPosition{}, false
	}
	return p.TextDocument.URI, doc.text, p.Position, true
}

func (s *server) sentenceHover(params json.RawMessage) any {
	uri, text, pos, ok := s.documentAt(params)
	if !ok {
		return nil
	}
	line := lineAt(text, pos.Line)
	// Stream hover supplements the core statement meaning with ownership and
	// lifecycle guarantees that are essential at the call site.
	if reNewStreamLine.MatchString(strings.TrimSpace(stripLineComment(line))) {
		if value, is := s.newSyntaxHover(uri, text, line); is {
			return map[string]any{"contents": map[string]any{"kind": "markdown", "value": value}}
		}
	}
	if info, exists := sos.EditorMeanings(text)[pos.Line+1]; exists {
		return map[string]any{"contents": map[string]any{"kind": "markdown", "value": "```sos\n" + strings.TrimSpace(line) + "\n```\n\n**" + info.Kind + "**\n\n" + info.Description}}
	}
	// New module syntax the core does not classify yet is explained here so
	// stale or in-flight buffers still hover honestly.
	if value, is := s.newSyntaxHover(uri, text, line); is {
		return map[string]any{
			"contents": map[string]any{"kind": "markdown", "value": value},
		}
	}
	for _, st := range flattenStatements(statementsOf(text)) {
		if statementLine(st, lineCount(text)) != pos.Line {
			continue
		}
		value := "```sos\n" + st.Text + "\n```"
		if st.Kind != "" {
			value += "\n\nKind: `" + st.Kind + "`"
			if d, known := opDocs[st.Kind]; known {
				value += "\n\n**Effect:** " + d.effect
				if d.binds != "" {
					value += "\n\n**Binds:** " + d.binds
				}
			}
			if sos.UsesJev(st) {
				value += "\n\n**Provider:** calls the model; admitted through the request budget."
			}
		}
		return map[string]any{
			"contents": map[string]any{"kind": "markdown", "value": value},
		}
	}
	return nil
}

// completion returns the canonical phrases from the core, filtered by the
// identifier prefix being typed at the cursor. With a non-empty prefix it
// also offers names bound in scope and auto-import actions from the standard
// library and indexed local packages, each with detail, documentation, a
// textEdit for the typed word, and additionalTextEdits carrying the import.
func (s *server) completion(params json.RawMessage) any {
	uri, text, pos, ok := s.documentAt(params)
	prefix := ""
	currentLine := ""
	lineContext := ""
	var replace *lspRange
	if ok {
		line := lineAt(text, pos.Line)
		currentLine = line
		col := charToByte(line, pos.Character)
		start, end := col, col
		isPart := func(c byte) bool {
			return c == '.' || c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		}
		for start > 0 && isPart(line[start-1]) {
			start--
		}
		for end < len(line) && isPart(line[end]) {
			end++
		}
		prefix = strings.ToLower(line[start:col])
		lineContext = strings.ToLower(strings.TrimSpace(line[:col]))
		semanticContext := failureCompletionContext(lineContext)
		if prefix != "" || semanticContext {
			replace = &lspRange{Start: lspPosition{Line: pos.Line, Character: byteToChar(line, start)}, End: lspPosition{Line: pos.Line, Character: byteToChar(line, end)}}
		}

	}
	items := make([]map[string]any, 0)
	wordEdit := func(newText string) map[string]any {
		return map[string]any{"range": replace, "newText": newText}
	}
	for _, kw := range sos.Keywords() {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(kw), prefix) {
			continue
		}
		item := map[string]any{"label": kw, "kind": 14, "detail": "SysOneScript keyword"}
		if replace != nil {
			item["textEdit"] = wordEdit(kw)
		}
		items = append(items, item)
	}
	semanticContext := failureCompletionContext(lineContext)
	if !ok || replace == nil || (prefix == "" && !semanticContext) {
		// Empty prefix keeps the canonical keyword list exactly, matching
		// long-standing clients.
		return items
	}
	for _, b := range scopeBindings(text) {
		if !strings.HasPrefix(strings.ToLower(b.name), prefix) {
			continue
		}
		items = append(items, map[string]any{
			"label": b.name, "kind": 6, "detail": b.detail, "textEdit": wordEdit(b.name),
		})
	}
	if program := parseProgram(text); program != nil {
		for name, definition := range program.Definitions {
			if prefix != "" && !strings.HasPrefix(strings.ToLower(name), prefix) {
				continue
			}
			items = append(items, map[string]any{
				"label":    name,
				"kind":     7,
				"detail":   recordSignature(definition),
				"textEdit": wordEdit(name),
			})
		}
		for name, definition := range program.Failures {
			if prefix != "" && !strings.HasPrefix(strings.ToLower(name), prefix) {
				continue
			}
			items = append(items, map[string]any{
				"label": name, "kind": 7, "detail": failureSignature(definition),
				"textEdit": wordEdit(name),
			})
		}
		for _, field := range recordFieldCompletions(program, text, currentLine, prefix) {
			items = append(items, map[string]any{
				"label":    field,
				"kind":     10,
				"detail":   "record field",
				"textEdit": wordEdit(field),
			})
		}
	}
	catalog, _ := s.vocabularyFor(uri, vocabFilename(uri, s.workspaceRoot), text)
	seenTypes := map[string]bool{}
	for _, item := range items {
		if label, is := item["label"].(string); is {
			seenTypes[label] = true
		}
	}
	for i := range catalog.Definitions {
		definition := &catalog.Definitions[i]
		if seenTypes[definition.Name] || prefix != "" && !strings.HasPrefix(strings.ToLower(definition.Name), prefix) {
			continue
		}
		items = append(items, map[string]any{
			"label": definition.Name, "kind": 7, "detail": recordSignature(definition),
			"textEdit": wordEdit(definition.Name),
		})
		seenTypes[definition.Name] = true
	}
	// The vocabulary catalog includes exported failure types from resolved
	// imports. They are types, not callable word targets, so complete them
	// directly in failure positions (including every slot after a comma).
	if semanticContext {
		for i := range catalog.Failures {
			definition := &catalog.Failures[i]
			if seenTypes[definition.Name] || prefix != "" && !strings.HasPrefix(strings.ToLower(definition.Name), prefix) {
				continue
			}
			items = append(items, map[string]any{"label": definition.Name, "kind": 7, "detail": failureSignature(definition), "textEdit": wordEdit(definition.Name)})
			seenTypes[definition.Name] = true
		}
	}
	// Vocabulary words from the shared catalog: exactly the forms enabled by
	// this document's imports. Open imports offer the bare sentence; aliased
	// imports offer the qualifier form only — a bare hint under an alias
	// would be an invalid call. Available entries offer auto-import actions.
	for _, t := range s.wordTargets(uri, vocabFilename(uri, s.workspaceRoot), text) {
		stdLib := strings.HasPrefix(t.Origin, "standard library")
		forms, importAliasToUse := completionForms(t, stdLib, text)
		for _, form := range forms {
			if strings.HasPrefix(t.Result, "stream of ") && !t.Bare && t.Qualifier != "" {
				var offer bool
				form, offer = streamCompletionForm(t, form, lineContext)
				if !offer {
					continue
				}
			}
			if !completionAddresses(t, form.label, prefix) {
				continue
			}
			detail := entrySignature(t.Name, t.Params, t.Result, t.PossibleFailures)
			if !t.Enabled {
				detail = "auto-import · " + t.ImportPath
			}
			doc := t.Doc
			if doc == "" {
				doc = "Exported action of local package `" + defaultAlias(t.ImportPath) + "`."
			}
			item := map[string]any{
				"label":         form.label,
				"kind":          3,
				"detail":        detail,
				"documentation": map[string]any{"kind": "markdown", "value": doc},
				"sortText":      "~" + form.label,
				"filterText":    form.label,
				"textEdit":      wordEdit(form.insert),
			}
			if strings.Contains(form.insert, "${") {
				item["insertTextFormat"] = 2
			}
			if !t.Enabled && !hasImportPath(text, t.ImportPath) {
				item["additionalTextEdits"] = []any{importEdit(text, t.ImportPath, importAliasToUse)}
			}
			items = append(items, item)
		}
	}
	return items
}

func streamCompletionForm(t wordTarget, form completionForm, lineContext string) (completionForm, bool) {
	call := form.insert
	if len(t.Params) > 0 {
		call += " with "
		for i, param := range t.Params {
			if i > 0 {
				call += ", "
			}
			call += fmt.Sprintf("${%d:%s}", i+1, param.Name)
		}
	}
	call += " called items"
	label := "stream " + call
	if strings.HasPrefix(strings.TrimSpace(lineContext), "stream ") {
		return completionForm{label: label, insert: call}, true
	}
	return completionForm{label: label, insert: label}, true
}

func failureCompletionContext(line string) bool {
	line = strings.ToLower(strings.TrimSpace(line))
	if strings.Contains(line, "on failure") || strings.HasSuffix(line, "fail") {
		return true
	}
	i := strings.LastIndex(line, "may fail with")
	if i < 0 {
		return false
	}
	return !strings.Contains(line[i+len("may fail with"):], ":")
}

func recordSignature(definition *sos.RecordDef) string {
	if definition == nil {
		return "record"
	}
	parts := make([]string, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		parts = append(parts, field.Name+" as "+field.Type.String())
	}
	return "record " + definition.Name + " { " + strings.Join(parts, ", ") + " }"
}

func recordFieldCompletions(program *sos.Program, source, line, prefix string) []string {
	if program == nil || prefix == "" {
		return nil
	}
	typeName := ""
	if match := regexp.MustCompile(`\bas\s+([A-Z][A-Za-z0-9_]*)(?:\s+using|\s*[,\:])`).FindStringSubmatch(line); match != nil {
		typeName = match[1]
	}
	if match := regexp.MustCompile(`\bof\s+([a-z_][A-Za-z0-9_]*)`).FindStringSubmatch(line); match != nil {
		receiver := match[1]
		if declaration := regexp.MustCompile(`(?m)^\s*(?:make|assign|set)\s+` + regexp.QuoteMeta(receiver) + `\s+as\s+([A-Z][A-Za-z0-9_]*)`).FindStringSubmatch(source); declaration != nil {
			typeName = declaration[1]
		}
	}
	definition := program.Definitions[typeName]
	if definition == nil {
		return nil
	}
	fields := make([]string, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		if strings.HasPrefix(strings.ToLower(field.Name), prefix) {
			fields = append(fields, field.Name)
		}
	}
	return fields
}

// completionForm is one offerable label with the text inserted for it.
type completionForm struct{ label, insert string }

// completionForms lists the callable forms worth offering for one target and
// the alias its auto-import edit should bind. Open imports offer the bare
// sentence (canonical word and synonyms) plus the qualifier; aliased imports
// offer the qualifier form only. Available standard libraries suggest the
// open import — unless the default qualifier is already bound, in which case
// a collision-free alias keeps the qualified call executable. Available local
// packages suggest their aliased import: the prefix is mandatory.
func completionForms(t wordTarget, stdLib bool, source string) ([]completionForm, string) {
	switch {
	case t.Enabled && t.Bare:
		forms := []completionForm{{t.Name, t.Name}}
		for _, syn := range t.Synonyms {
			forms = append(forms, completionForm{syn, syn})
		}
		if t.Qualifier != "" {
			forms = append(forms, completionForm{t.Qualifier + "." + t.Name, t.Qualifier + "." + t.Name})
		}
		return forms, ""
	case t.Enabled && t.Qualifier != "":
		return []completionForm{{t.Qualifier + "." + t.Name, t.Qualifier + "." + t.Name}}, ""
	case !t.Enabled && stdLib:
		def := defaultAlias(t.ImportPath)
		if !hasImport(source, def) {
			return []completionForm{{t.Name, t.Name}, {def + "." + t.Name, def + "." + t.Name}}, ""
		}
		alias := importAlias(source, t.ImportPath, def)
		return []completionForm{{alias + "." + t.Name, alias + "." + t.Name}}, alias
	default:
		alias := importAlias(source, t.ImportPath, defaultAlias(t.ImportPath))
		return []completionForm{{alias + "." + t.Name, alias + "." + t.Name}}, alias
	}
}

// completionAddresses reports whether a typed prefix addresses a form: by
// the offered label, the canonical word, or the library's default qualifier
// (typing `text.tr` still finds the target when it is bound as `words`).
func completionAddresses(t wordTarget, label, prefix string) bool {
	def := defaultAlias(t.ImportPath)
	for _, hay := range []string{label, t.Name, def + "." + t.Name, def} {
		if hay != "" && strings.HasPrefix(strings.ToLower(hay), prefix) {
			return true
		}
	}
	return false
}

// scopeBindings lists the names bound before the cursor, earliest first, with
// the sentence construction that binds them.
func scopeBindings(text string) []scopeBinding {
	var out []scopeBinding
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		code := strings.TrimSpace(stripLineComment(line))
		if code == "" {
			continue
		}
		for j, pat := range bindPatterns {
			m := pat.FindStringSubmatch(code)
			if m == nil || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			out = append(out, scopeBinding{name: m[1], detail: bindDetails[j] + " (line " + strconv.Itoa(i+1) + ")"})
		}
	}
	return out
}

type scopeBinding struct {
	name   string
	detail string
}

var bindDetails = []string{
	"command or expectation",
	"parameter",
	"value bound by make",
	"loop singular",
	"call result",
}

// definition resolves a word at the cursor to the statement that binds it:
// command, expect, argument/option/switch, make, loop singulars, and called
// result names.
func (s *server) definition(params json.RawMessage) any {
	uri, text, pos, ok := s.documentAt(params)
	if !ok {
		return nil
	}
	line := lineAt(text, pos.Line)
	word, _, _ := wordAt(line, charToByte(line, pos.Character))
	if word == "" {
		return nil
	}
	if program := parseProgram(text); program != nil {
		if definition := program.Definitions[word]; definition != nil {
			defineLine := definition.Line - 1
			src := lineAt(text, defineLine)
			start := strings.Index(src, word)
			if start < 0 {
				start = 0
			}
			return map[string]any{"uri": uri, "range": lspRange{Start: lspPosition{Line: defineLine, Character: byteToChar(src, start)}, End: lspPosition{Line: defineLine, Character: byteToChar(src, start+len(word))}}}
		}
		for _, definition := range program.Definitions {
			for _, field := range definition.Fields {
				if field.Name != word {
					continue
				}
				fieldLine := field.Line - 1
				src := lineAt(text, fieldLine)
				start := strings.Index(src, word)
				if start < 0 {
					start = 0
				}
				return map[string]any{"uri": uri, "range": lspRange{Start: lspPosition{Line: fieldLine, Character: byteToChar(src, start)}, End: lspPosition{Line: fieldLine, Character: byteToChar(src, start+len(word))}}}
			}
		}
	}
	stmts := statementsOf(text)
	numLines := lineCount(text)
	bestLine := -1
	for _, st := range flattenStatements(stmts) {
		stLine := statementLine(st, numLines)
		for _, pat := range bindPatterns {
			loc := pat.FindStringSubmatchIndex(st.Text)
			if loc == nil || st.Text[loc[2]:loc[3]] != word {
				continue
			}
			if bestLine < 0 || stLine < bestLine {
				bestLine = stLine
			}
			break
		}
	}
	if bestLine < 0 {
		return nil
	}
	srcLine := lineAt(text, bestLine)
	idx := strings.Index(srcLine, word)
	if idx < 0 {
		idx = 0
	}
	return map[string]any{
		"uri": uri,
		"range": lspRange{
			Start: lspPosition{Line: bestLine, Character: byteToChar(srcLine, idx)},
			End:   lspPosition{Line: bestLine, Character: byteToChar(srcLine, idx+len(word))},
		},
	}
}

func (s *server) references(params json.RawMessage) any {
	uri, text, pos, ok := s.documentAt(params)
	if !ok {
		return nil
	}
	line := lineAt(text, pos.Line)
	word, _, _ := wordAt(line, charToByte(line, pos.Character))
	if word == "" {
		return []any{}
	}
	if scoped, ok := ownedStreamOccurrences(text, pos.Line, word); ok {
		locations := make([]map[string]any, 0, len(scoped))
		for _, occurrence := range scoped {
			locations = append(locations, map[string]any{"uri": uri, "range": occurrence})
		}
		return locations
	}
	locations := []map[string]any{}
	for lineNo, sourceLine := range strings.Split(text, "\n") {
		for _, token := range sossyntax.Parse(sourceLine).Lines[0].Tokens {
			if token.Kind != "identifier" || token.Text != word {
				continue
			}
			locations = append(locations, map[string]any{
				"uri":   uri,
				"range": lspRange{Start: lspPosition{Line: lineNo, Character: token.Start}, End: lspPosition{Line: lineNo, Character: token.End}},
			})
		}
	}
	return locations
}

func (s *server) rename(params json.RawMessage) any {
	var request struct {
		TextDocument textDocumentIdentifier `json:"textDocument"`
		Position     lspPosition            `json:"position"`
		NewName      string                 `json:"newName"`
	}
	if err := json.Unmarshal(params, &request); err != nil || !validRename(request.NewName) {
		return nil
	}
	doc, ok := s.docs[request.TextDocument.URI]
	if !ok {
		return nil
	}
	line := lineAt(doc.text, request.Position.Line)
	word, _, _ := wordAt(line, charToByte(line, request.Position.Character))
	if word == "" {
		return nil
	}
	if scoped, ok := ownedStreamOccurrences(doc.text, request.Position.Line, word); ok {
		edits := make([]map[string]any, 0, len(scoped))
		for _, occurrence := range scoped {
			edits = append(edits, map[string]any{"range": occurrence, "newText": request.NewName})
		}
		return map[string]any{"changes": map[string]any{request.TextDocument.URI: edits}}
	}
	edits := []map[string]any{}
	for lineNo, sourceLine := range strings.Split(doc.text, "\n") {
		for _, token := range sossyntax.Parse(sourceLine).Lines[0].Tokens {
			if token.Kind == "identifier" && token.Text == word {
				edits = append(edits, map[string]any{"range": lspRange{Start: lspPosition{Line: lineNo, Character: token.Start}, End: lspPosition{Line: lineNo, Character: token.End}}, "newText": request.NewName})
			}
		}
	}
	return map[string]any{"changes": map[string]any{request.TextDocument.URI: edits}}
}

type lexicalBinding struct {
	name  string
	line  int
	kind  string
	scope *lexicalScope
}

type lexicalScope struct {
	parent   *lexicalScope
	bindings []*lexicalBinding
}

// ownedHandleKinds are the core statement kinds that bind an owned,
// single-consumer stream handle: explicit stream opens plus the filesystem
// traversal (`stream ... under`) and watcher (`watch folder`) constructions
// once the parser core classifies them. The set is data so extending
// ownership-aware references and rename to a new handle kind is a one-line
// change.
var ownedHandleKinds = map[string]bool{
	"openStream":  true,
	"streamFiles": true,
	"watchFolder": true,
}

// ownedStreamOccurrences resolves a stream handle through lexical ownership
// scopes before returning edits. It deliberately declines non-stream symbols,
// which continue through the general reference path.
func ownedStreamOccurrences(text string, cursorLine int, word string) ([]lspRange, bool) {
	root := &lexicalScope{}
	lineScopes := map[int]*lexicalScope{}
	var walk func([]*sos.Statement, *lexicalScope)
	walk = func(statements []*sos.Statement, scope *lexicalScope) {
		for _, statement := range statements {
			line := statement.Line - 1
			lineScopes[line] = scope
			child := &lexicalScope{parent: scope}
			for index, pattern := range bindPatterns {
				match := pattern.FindStringSubmatch(statement.Text)
				if match == nil {
					continue
				}
				target := scope
				if index == 3 { // the singular introduced by "for each" belongs to its body
					target = child
				}
				binding := &lexicalBinding{name: match[1], line: line, kind: statement.Kind, scope: target}
				target.bindings = append(target.bindings, binding)
			}
			if len(statement.Body) > 0 {
				walk(statement.Body, child)
			}
		}
	}
	walk(statementsOf(text), root)
	resolve := func(scope *lexicalScope, line int, name string) *lexicalBinding {
		for current := scope; current != nil; current = current.parent {
			var found *lexicalBinding
			for _, binding := range current.bindings {
				if binding.name == name && binding.line <= line && (found == nil || binding.line >= found.line) {
					found = binding
				}
			}
			if found != nil {
				return found
			}
		}
		return nil
	}
	selected := resolve(lineScopes[cursorLine], cursorLine, word)
	if selected == nil || !ownedHandleKinds[selected.kind] {
		return nil, false
	}
	var occurrences []lspRange
	for lineNo, sourceLine := range strings.Split(text, "\n") {
		scope := lineScopes[lineNo]
		if scope == nil || resolve(scope, lineNo, word) != selected {
			continue
		}
		for _, token := range sossyntax.Parse(sourceLine).Lines[0].Tokens {
			if token.Kind == "identifier" && token.Text == word {
				occurrences = append(occurrences, lspRange{Start: lspPosition{Line: lineNo, Character: token.Start}, End: lspPosition{Line: lineNo, Character: token.End}})
			}
		}
	}
	return occurrences, true
}

func validRename(name string) bool {
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for _, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (s *server) formatting(params json.RawMessage) any {
	var p formattingParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	doc, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return nil
	}
	formatted, diagnostics := sos.Format(doc.text)
	if len(diagnostics) > 0 {
		return []map[string]any{}
	}
	if formatted == doc.text {
		return []map[string]any{}
	}
	return []map[string]any{
		{
			"range":   lspRange{Start: lspPosition{Line: 0, Character: 0}, End: endPosition(doc.text)},
			"newText": formatted,
		},
	}
}

// parseProgram tolerates incomplete source: the core returns partial
// statements alongside diagnostics.
func parseProgram(text string) *sos.Program {
	prog, _ := sos.Parse(text)
	return prog
}

// statementsOf returns the statements of a tolerant parse, or nil when the
// core yields no program.
func statementsOf(text string) []*sos.Statement {
	if prog := parseProgram(text); prog != nil {
		return prog.Statements
	}
	return nil
}

func flattenStatements(stmts []*sos.Statement) []*sos.Statement {
	var out []*sos.Statement
	var walk func(*sos.Statement)
	walk = func(st *sos.Statement) {
		if st == nil {
			return
		}
		out = append(out, st)
		for _, child := range st.Body {
			walk(child)
		}
	}
	for _, st := range stmts {
		walk(st)
	}
	return out
}

// statementLine maps a core statement line to a zero based index, clamped
// into the document.
func statementLine(st *sos.Statement, numLines int) int {
	l := st.Line
	if l >= 1 {
		l--
	}
	if l < 0 {
		l = 0
	}
	if numLines > 0 && l >= numLines {
		l = numLines - 1
	}
	return l
}
