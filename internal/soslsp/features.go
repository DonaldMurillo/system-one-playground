package soslsp

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

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
			continue
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
	var replace *lspRange
	if ok {
		line := lineAt(text, pos.Line)
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
		if prefix != "" {
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
	if !ok || prefix == "" || replace == nil {
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
	// Vocabulary words from the shared catalog: exactly the forms enabled by
	// this document's imports. Open imports offer the bare sentence; aliased
	// imports offer the qualifier form only — a bare hint under an alias
	// would be an invalid call. Available entries offer auto-import actions.
	for _, t := range s.wordTargets(uri, vocabFilename(uri, s.workspaceRoot), text) {
		stdLib := strings.HasPrefix(t.Origin, "standard library")
		forms, importAliasToUse := completionForms(t, stdLib, text)
		for _, form := range forms {
			if !completionAddresses(t, form.label, prefix) {
				continue
			}
			detail := entrySignature(t.Name, t.Params, t.Result)
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
			if !t.Enabled && !hasImportPath(text, t.ImportPath) {
				item["additionalTextEdits"] = []any{importEdit(text, t.ImportPath, importAliasToUse)}
			}
			items = append(items, item)
		}
	}
	return items
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
