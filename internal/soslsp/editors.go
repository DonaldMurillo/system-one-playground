package soslsp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// This file hosts the editor features beyond diagnostics/definition:
// semanticTokens/full, inlayHint, codeAction quick fixes, and the hover /
// completion enrichment shared with them.

type textDocumentOnly struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type codeActionContext struct {
	Diagnostics []lspDiagnostic `json:"diagnostics"`
}

type codeActionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Range        lspRange               `json:"range"`
	Context      codeActionContext      `json:"context"`
}

// semanticTokens handles textDocument/semanticTokens/full. Data is the
// standard relative encoding over the legend in tokenLegend.
func (s *server) semanticTokens(params json.RawMessage) any {
	var p textDocumentOnly
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	doc, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return nil
	}
	toks := syntaxTokens(doc.syntaxTree(), s.sentHeads(p.TextDocument.URI, doc.text))
	data := make([]uint32, 0, len(toks)*5)
	prevLine, prevStart := 0, 0
	for _, t := range toks {
		deltaLine := t.line - prevLine
		deltaStart := t.start
		if deltaLine == 0 {
			deltaStart = t.start - prevStart
		}
		data = append(data, uint32(deltaLine), uint32(deltaStart), uint32(t.len), uint32(t.kind), 0)
		prevLine, prevStart = t.line, t.start
	}
	return map[string]any{"data": data}
}

// sentHeads maps each line whose head resolves in this document's enabled
// vocabulary to that head, for function-token highlighting.
func (s *server) sentHeads(uri, text string) map[int]string {
	targets := s.wordTargets(uri, vocabFilename(uri, s.workspaceRoot), text)
	heads := map[int]string{}
	for i, line := range strings.Split(text, "\n") {
		code := strings.TrimSpace(stripLineComment(line))
		head := sentHead(code)
		if head == "" {
			continue
		}
		if resolveSent(head, targets) != nil {
			heads[i] = head
		}
	}
	return heads
}

// Inlay hints: only confident inferences. A hint is emitted when the bound
// value is a literal, an explicit empty list, or a read with a known shape.

type inlayHintItem struct {
	Position lspPosition `json:"position"`
	Label    string      `json:"label"`
	Kind     *int        `json:"kind,omitempty"`
}

var (
	reMakeValue = regexp.MustCompile(`^\s*(?:make|assign|set)\s+([A-Za-z_]\w*)\s+(.+)$`)
	reReadCall  = regexp.MustCompile(`^\s*read\s+.+\s+as\s+(json|text|lines of json)\s+called\s+([A-Za-z_]\w*)`)
	reReadEach  = regexp.MustCompile(`^\s*read\s+each\s+\w+\s+in\s+.+\s+as\s+lines\s+of\s+json\s+into\s+([A-Za-z_]\w*)`)
	reTakeItems = regexp.MustCompile(`^\s*take\s+(?:first|last)\s+\w+\s+items\s+from\s+\w+\s+called\s+([A-Za-z_]\w*)`)
	reNumberLit = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)
	reStringLit = regexp.MustCompile(`^"[^"]*"$`)
)

// inlayHints handles textDocument/inlayHint. The range, when present, bounds
// the lines considered.
func (s *server) inlayHints(params json.RawMessage) any {
	var p struct {
		TextDocument textDocumentIdentifier `json:"textDocument"`
		Range        *lspRange              `json:"range"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	doc, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return nil
	}
	hints := []inlayHintItem{}
	add := func(lineNo int, line string, end int, label string) {
		k := 1
		hints = append(hints, inlayHintItem{Position: lspPosition{Line: lineNo, Character: byteToChar(line, end)}, Label: label, Kind: &k})
	}
	imported := map[string]string{}
	targets := s.wordTargets(p.TextDocument.URI, vocabFilename(p.TextDocument.URI, s.workspaceRoot), doc.text)
	for _, line := range strings.Split(doc.text, "\n") {
		if m := reImportStmt.FindStringSubmatch(strings.TrimSpace(stripLineComment(line))); m != nil {
			alias := m[2]
			if alias == "" {
				alias = defaultAlias(m[1])
			}
			imported[alias] = m[1]
		}
	}
	for i, line := range strings.Split(doc.text, "\n") {
		if p.Range != nil && (i < p.Range.Start.Line || i > p.Range.End.Line) {
			continue
		}
		code := stripLineComment(line)
		if m := reMakeValue.FindStringSubmatchIndex(code); m != nil {
			value := strings.TrimSpace(code[m[4]:m[5]])
			label := ""
			if value == "as empty list" || value == "empty list" {
				label = ": list"
			} else if reNumberLit.MatchString(value) {
				label = ": number"
			} else if value == "true" || value == "false" {
				label = ": boolean"
			} else if _, err := strconv.Unquote(value); err == nil && strings.HasPrefix(value, "\"") {
				label = ": text"
			}
			if label != "" {
				add(i, line, m[3], label)
			}
			continue
		}
		if m := reReadCall.FindStringSubmatchIndex(code); m != nil {
			label := code[m[2]:m[3]]
			if label == "lines of json" {
				label = "list"
			}
			add(i, line, m[5], ": "+label)
			continue
		}
		if m := reReadEach.FindStringSubmatchIndex(code); m != nil {
			add(i, line, m[3], ": list")
			continue
		}
		if m := reTakeItems.FindStringSubmatchIndex(code); m != nil {
			add(i, line, m[3], ": list")
			continue
		}
		if m := reNewCallLine.FindStringSubmatchIndex(code); m != nil && m[6] >= 0 {
			alias, action := code[m[2]:m[3]], code[m[4]:m[5]]
			for _, op := range sos.StandardOperations() {
				if imported[alias] == op.ImportPath && action == op.Name && op.Result != "any" {
					add(i, line, m[7], ": "+op.Result)
					break
				}
			}
			continue
		}
		// Sentence calls: hint the result type after `called NAME` when the
		// head resolves in this document's enabled vocabulary.
		if head := sentHead(strings.TrimSpace(code)); head != "" {
			if t := resolveSent(head, targets); t != nil && t.Result != "" && t.Result != "any" {
				if cm := reCalledName.FindStringSubmatchIndex(code); cm != nil {
					add(i, line, cm[1], ": "+t.Result)
				}
			}
		}
	}

	return hints
}

// reCalledName captures a trailing `called NAME` sink.
var reCalledName = regexp.MustCompile(`called\s+([A-Za-z_]\w*)\s*$`)

// codeActions handles textDocument/codeAction: auto-import quick fixes for
// qualified calls whose alias resolves to the standard library or an indexed
// local package. Actions whose target action does not exist are not offered.
func (s *server) codeActions(params json.RawMessage) any {
	var p codeActionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil
	}
	doc, ok := s.docs[p.TextDocument.URI]
	if !ok {
		return nil
	}
	idx := s.pkgIndex()
	seen := map[string]bool{}
	actions := []map[string]any{}
	for _, call := range qualifiedCalls(doc.text) {
		if call.Line < p.Range.Start.Line || call.Line > p.Range.End.Line {
			continue
		}
		if hasImport(doc.text, call.Alias) || seen[call.Alias] {
			continue
		}
		seen[call.Alias] = true
		var importPath string
		stdImport := false
		if std, isStd := stdImportForAlias(call.Alias); isStd {
			if !stdHasAction(call.Alias, call.Action) {
				continue // never claim an unimplemented operation exists
			}
			importPath = std.ImportPath
			stdImport = true
		} else if pkg := idx.packages[call.Alias]; pkg != nil && pkg.hasAction(call.Action) {
			importPath = pkg.ImportPath
		} else {
			continue
		}
		// Standard library quick fixes write the open import: bare sentences
		// plus the default qualifier, so existing qualified calls keep working.
		// Local packages keep the explicit alias the call already uses.
		title := fmt.Sprintf("Import %q as %s", importPath, call.Alias)
		editAlias := call.Alias
		if stdImport {
			title = fmt.Sprintf("Import %q", importPath)
			editAlias = ""
		}
		actions = append(actions, map[string]any{
			"title": title,
			"kind":  "quickfix",
			"edit": map[string]any{
				"changes": map[string]any{
					p.TextDocument.URI: []any{importEdit(doc.text, importPath, editAlias)},
				},
			},
		})
	}
	return actions
}

func (a stdAction) matches(name string) bool { return a.Name == name }

func stdHasAction(alias, action string) bool {
	for _, a := range stdActionsForAlias(alias) {
		if a.matches(action) {
			return true
		}
	}
	return false
}

func (p *indexedPackage) hasAction(name string) bool {
	if p == nil {
		return false
	}
	for _, a := range p.Actions {
		if a.Name == name {
			return true
		}
	}
	return false
}

// opDocs describes the effect of every core statement kind. Effects are the
// honest taxonomy: provider call, filesystem, output, control flow, memory.
var opDocs = map[string]struct{ effect, binds string }{
	"command":   {"defines a runnable command block", "the command name"},
	"parameter": {"declares a command line parameter", "the parameter name"},
	"describe":  {"documentation prose; no runtime effect", ""},
	"schema":    {"names a validation schema for values", "the schema name"},
	"remember":  {"keeps a value in memory", "the called name"},
	"find":      {"scans the filesystem and binds the matches", "a list of matches"},
	"readEach":  {"reads a file line by line as JSON", "a list of parsed lines"},
	"read":      {"reads a file into memory", "the parsed file content"},
	"require":   {"validates every item against a schema", ""},
	"keep":      {"filters a list", "the kept items"},
	"sort":      {"sorts a list or table", "the ordered result"},
	"group":     {"groups a table by a column", "the grouped table"},
	"folder":    {"creates a directory if missing (filesystem write)", ""},
	"make":      {"binds a value to a name", "the value"},
	"for":       {"control flow: iterates a list", "the loop singular"},
	"map":       {"control flow: maps isolated iterations with a bounded number of workers; collects returned results in input order", "the called list, or outcome records when collecting failures"},
	"while":     {"control flow: repeats while a condition holds", ""},
	"repeat":    {"control flow: repeats a fixed number of times", ""},
	"when":      {"control flow: conditional block", ""},
	"otherwise": {"control flow: fallback block", ""},
	"take":      {"selects the first or last items", "the selected list"},
	"classify":  {"organizes items into named buckets", "the classified table"},
	"evaluate":  {"evaluates a named question batch with Jev in one request", "the named answers"},
	"judge":     {"judges values against a criterion", "the verdict"},
	"score":     {"scores values against a criterion", "the scored table"},
	"append":    {"appends a value to a list (mutation)", ""},
	"save":      {"writes a file (filesystem write)", ""},
	"show":      {"prints output", ""},
	"stop":      {"stops the run", ""},
	"to":        {"defines a handler block", ""},
	"call":      {"calls an imported action", "the called result"},
	"sent":      {"calls vocabulary as a sentence: bare word or qualifier.word", "the called result"},
	"handler":   {"registers an outcome handler", ""},
	"ask":       {"configures the prompt of a provider call", ""},
	"using":     {"selects the model for provider calls", ""},
	"model":     {"overrides the model for this scope", ""},
	"accept":    {"sets the acceptance probability threshold", ""},
}

var (
	reNewPackage  = regexp.MustCompile(`^\s*package\s+([A-Za-z_]\w*)`)
	reNewExport   = regexp.MustCompile(`^\s*export\s+([A-Za-z_]\w*)`)
	reNewImport   = regexp.MustCompile(`^\s*import\s+"([^"]*)"(?:\s+as\s+([A-Za-z_]\w*))?`)
	reNewCallLine = regexp.MustCompile(`^\s*call\s+([A-Za-z_]\w*)\.([A-Za-z_]\w*)(?:\s+with\s+.*?)?(?:\s+called\s+([A-Za-z_]\w*))?\s*$`)
)

// newSyntaxHover explains package/export/import/call lines the core does not
// classify yet, plus sentence calls that resolve in this document's
// vocabulary. ok is false for lines that are none of these.
func (s *server) newSyntaxHover(uri, text, line string) (string, bool) {
	code := strings.TrimSpace(stripLineComment(line))
	if m := reNewPackage.FindStringSubmatch(code); m != nil {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Declares package** `%s` — the actions exported below belong to it.", code, m[1]), true
	}
	if m := reNewExport.FindStringSubmatch(code); m != nil {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Exports action** `%s` from this package; importers may call it.", code, m[1]), true
	}
	if m := reNewImport.FindStringSubmatch(code); m != nil {
		return s.importHover(code, m[1], m[2]), true
	}
	if m := reNewCallLine.FindStringSubmatch(code); m != nil {
		return s.qualifiedCallHover(code, m[1], m[2]), true
	}
	if head := sentHead(code); head != "" {
		if t := resolveSent(head, s.wordTargets(uri, vocabFilename(uri, s.workspaceRoot), text)); t != nil {
			return sentHover(code, head, t), true
		}
	}
	return "", false
}

// sentHover documents one resolved sentence call.
func sentHover(code, head string, t *wordTarget) string {
	form := "bare word"
	if strings.Contains(head, ".") {
		form = "qualified"
	}
	value := fmt.Sprintf("```sos\n%s\n```\n\n**Calls** `%s` (%s) from `%s`", code, head, form, t.ImportPath)
	if sig := entrySignature(t.Name, t.Params, t.Result); sig != t.Name+"()" {
		value += "\n\n" + sig
	}
	if t.Doc != "" {
		value += "\n\n" + t.Doc
	}
	return value
}

// importHover documents an import line from the catalog or the index,
// explaining the open-vs-aliased vocabulary semantics.
func (s *server) importHover(code, path, alias string) string {
	var body string
	if entries := stdCatalogForPath(path); len(entries) > 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = "`" + e.Name + "` — " + e.Doc
		}
		body = "Standard library package. Operations:\n\n" + strings.Join(names, "\n\n")
	} else if pkg := s.pkgIndex().packages[pathToPkgKey(path)]; pkg != nil {
		body = pkgHover(pkg)
	} else if pkg := s.pkgIndex().packageByImportPath(path); pkg != nil {
		body = pkgHover(pkg)
	} else {
		body = "Not found in this workspace's module index. Standard library and indexed local packages are available."
	}
	if alias == "" {
		body += "\n\n**Open import:** exposes the bare sentence vocabulary (`" + strings.Join(stdNamesFor(path), "`, `") + "` …) and keeps the `" + defaultAlias(path) + ".` qualifier valid."
		return fmt.Sprintf("```sos\n%s\n```\n\n%s", code, body)
	}
	body += "\n\n**Aliased import:** every call requires the `" + alias + ".` prefix; bare words are not enabled."
	return fmt.Sprintf("```sos\n%s\n```\n\n%s", code, body)
}

// stdNamesFor lists the operation names of one standard library path.
func stdNamesFor(path string) []string {
	var out []string
	for _, e := range stdCatalogForPath(path) {
		out = append(out, e.Name)
	}
	if len(out) == 0 {
		return []string{"…"}
	}
	return out
}

func pkgHover(pkg *indexedPackage) string {
	names := make([]string, len(pkg.Actions))
	for i, a := range pkg.Actions {
		names[i] = "`" + a.Name + "`"
	}
	out := fmt.Sprintf("Local package `%s` (import path `%s`). Exported actions: %s.", pkg.Name, pkg.ImportPath, strings.Join(names, ", "))
	var docs []string
	for _, a := range pkg.Actions {
		if a.Doc != "" {
			docs = append(docs, fmt.Sprintf("- `%s` — %s", a.Name, a.Doc))
		}
	}
	if len(docs) > 0 {
		out += "\n\n" + strings.Join(docs, "\n")
	}
	return out
}

func pathToPkgKey(path string) string {
	// Import paths for local packages are relative module paths; the last
	// segment is the conventional package key when files declare names.
	seg := path
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	return seg
}

func (idx *packageIndex) packageByImportPath(path string) *indexedPackage {
	if idx == nil {
		return nil
	}
	if p := idx.packages[path]; p != nil {
		return p
	}
	for _, p := range idx.packages {
		if p.ImportPath == path {
			return p
		}
	}
	return nil
}

func stdCatalogForPath(path string) []stdAction {
	var out []stdAction
	for _, a := range stdCatalog {
		if a.ImportPath == path {
			out = append(out, a)
		}
	}
	return out
}

// qualifiedCallHover documents `call alias.action` from the catalog or index.
func (s *server) qualifiedCallHover(code, alias, action string) string {
	for _, a := range stdActionsForAlias(alias) {
		if a.matches(action) {
			return fmt.Sprintf("```sos\n%s\n```\n\n**Calls** `%s.%s`\n\n%s\n\nStandard library; requires `import %q as %s`.", code, alias, action, a.Doc, a.ImportPath, alias)
		}
	}
	if pkg := s.pkgIndex().packages[alias]; pkg != nil && pkg.hasAction(action) {
		doc := "Local package action."
		for _, a := range pkg.Actions {
			if a.Name == action && a.Doc != "" {
				doc = a.Doc
			}
		}
		return fmt.Sprintf("```sos\n%s\n```\n\n**Calls** `%s.%s`\n\n%s\n\nFrom local package `%s`; requires `import %q as %s`.", code, alias, action, doc, pkg.Name, pkg.ImportPath, alias)
	}
	return fmt.Sprintf("```sos\n%s\n```\n\n**Calls** `%s.%s`\n\nUnknown action: `%s` is neither a standard library namespace nor an indexed local package. Add an import, or check the package name.", code, alias, action, alias)
}
