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
	addCharacter := func(lineNo, character int, label string) {
		k := 1
		hints = append(hints, inlayHintItem{Position: lspPosition{Line: lineNo, Character: character}, Label: label, Kind: &k})
	}
	imported := map[string]string{}
	bindingTypes := map[string]string{}
	targets := s.wordTargets(p.TextDocument.URI, vocabFilename(p.TextDocument.URI, s.workspaceRoot), doc.text)
	for _, line := range strings.Split(doc.text, "\n") {
		code := stripLineComment(line)
		if m := reImportStmt.FindStringSubmatch(strings.TrimSpace(code)); m != nil {
			alias := m[2]
			if alias == "" {
				alias = defaultAlias(m[1])
			}
			imported[alias] = m[1]
		}
		if m := reMakeValue.FindStringSubmatchIndex(code); m != nil {
			if inferred := confidentValueType(strings.TrimSpace(code[m[4]:m[5]])); inferred != "" {
				bindingTypes[code[m[2]:m[3]]] = inferred
			}
		} else if m := reReadCall.FindStringSubmatchIndex(code); m != nil {
			inferred := code[m[2]:m[3]]
			if inferred == "lines of json" {
				inferred = "list"
			}
			bindingTypes[code[m[4]:m[5]]] = inferred
		} else if m := reReadEach.FindStringSubmatchIndex(code); m != nil {
			bindingTypes[code[m[2]:m[3]]] = "list"
		} else if m := reTakeItems.FindStringSubmatchIndex(code); m != nil {
			bindingTypes[code[m[2]:m[3]]] = "list"
		}
	}
	for i, line := range strings.Split(doc.text, "\n") {
		if p.Range != nil && (i < p.Range.Start.Line || i > p.Range.End.Line) {
			continue
		}
		code := stripLineComment(line)
		for _, token := range sossyntax.Parse(code).Lines[0].Tokens {
			if token.Kind != "string" && token.Kind != "incompleteString" {
				continue
			}
			for _, inner := range interpolationSemanticTokens(i, token) {
				if inner.kind == tokVariable && bindingTypes[inner.text] != "" {
					addCharacter(i, inner.start+inner.len, ": "+bindingTypes[inner.text])
				}
			}
		}
		if m := reMakeValue.FindStringSubmatchIndex(code); m != nil {
			value := strings.TrimSpace(code[m[4]:m[5]])
			if inferred := confidentValueType(value); inferred != "" {
				add(i, line, m[3], ": "+inferred)
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

func confidentValueType(value string) string {
	if value == "as empty list" || value == "empty list" {
		return "list"
	}
	if reNumberLit.MatchString(value) {
		return "number"
	}
	if value == "true" || value == "false" {
		return "boolean"
	}
	if _, err := strconv.Unquote(value); err == nil && strings.HasPrefix(value, "\"") {
		return "text"
	}
	return ""
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
	// A std/streams technical alias call offers its deterministic canonical
	// rewrite. Aliases never change behavior; this only changes wording.
	for lineNo, line := range strings.Split(doc.text, "\n") {
		if lineNo < p.Range.Start.Line || lineNo > p.Range.End.Line {
			continue
		}
		m := reStreamAliasCall.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rewrite, ok := streamAliasRewrite(m[1], m[2], m[3])
		if !ok {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		replacement := indent + strings.ReplaceAll(rewrite, "\n", "\n"+indent)
		actions = append(actions, map[string]any{
			"title": "Rewrite as canonical stream handling",
			"kind":  "quickfix",
			"edit": map[string]any{"changes": map[string]any{p.TextDocument.URI: []any{map[string]any{
				"range":   lspRange{Start: lspPosition{Line: lineNo, Character: 0}, End: lspPosition{Line: lineNo, Character: byteToChar(line, len(line))}},
				"newText": replacement,
			}}}},
		})
	}
	// Dotted data access remains valid, but this opt-in action gives users the
	// idiomatic `field of value` spelling without rewriting source silently.
	dotted := regexp.MustCompile(`\b([a-z_][A-Za-z0-9_]*)\.([a-z_][A-Za-z0-9_]*)\b`)
	for lineNo, line := range strings.Split(doc.text, "\n") {
		trimmed := strings.TrimSpace(line)
		if lineNo < p.Range.Start.Line || lineNo > p.Range.End.Line || strings.HasPrefix(trimmed, "call ") || strings.HasPrefix(trimmed, "import ") {
			continue
		}
		match := dotted.FindStringSubmatchIndex(stripLineComment(line))
		if match == nil {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if match[0] == indent && !strings.HasPrefix(trimmed, "show ") && !strings.HasPrefix(trimmed, "return ") {
			continue
		}
		receiver, field := line[match[2]:match[3]], line[match[4]:match[5]]
		edit := map[string]any{
			"range":   lspRange{Start: lspPosition{Line: lineNo, Character: byteToChar(line, match[0])}, End: lspPosition{Line: lineNo, Character: byteToChar(line, match[1])}},
			"newText": field + " of " + receiver,
		}
		actions = append(actions, map[string]any{
			"title": "Use field of value access",
			"kind":  "quickfix",
			"edit":  map[string]any{"changes": map[string]any{p.TextDocument.URI: []any{edit}}},
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
	"command":       {"defines a runnable command block", "the command name"},
	"parameter":     {"declares a command line parameter", "the parameter name"},
	"describe":      {"documentation prose; no runtime effect", ""},
	"schema":        {"names a validation schema for values", "the schema name"},
	"remember":      {"keeps a value in memory", "the called name"},
	"find":          {"scans the filesystem and binds the matches", "a list of matches"},
	"readEach":      {"reads a file line by line as JSON", "a list of parsed lines"},
	"read":          {"reads a file into memory", "the parsed file content"},
	"require":       {"validates every item against a schema", ""},
	"keep":          {"filters a list", "the kept items"},
	"sort":          {"sorts a list or table", "the ordered result"},
	"group":         {"groups a table by a column", "the grouped table"},
	"folder":        {"creates a directory if missing (filesystem write)", ""},
	"make":          {"binds a value to a name", "the value"},
	"for":           {"control flow: iterates a list", "the loop singular"},
	"map":           {"control flow: maps isolated iterations with a bounded number of workers; collects returned results in input order", "the called list, or outcome records when collecting failures"},
	"while":         {"control flow: repeats while a condition holds", ""},
	"repeat":        {"control flow: repeats a fixed number of times", ""},
	"when":          {"control flow: conditional block", ""},
	"otherwise":     {"control flow: fallback block", ""},
	"take":          {"selects bounded items; taking first items from a stream consumes it and cancels after the limit", "the selected list"},
	"classify":      {"organizes items into named buckets", "the classified table"},
	"evaluate":      {"evaluates a named question batch with Jev in one request", "the named answers"},
	"judge":         {"judges values against a criterion", "the verdict"},
	"score":         {"scores values against a criterion", "the scored table"},
	"append":        {"appends a value to a list (mutation)", ""},
	"save":          {"writes a file (filesystem write)", ""},
	"show":          {"prints output", ""},
	"stop":          {"stops the run", ""},
	"to":            {"defines a handler block", ""},
	"call":          {"calls an imported action", "the called result"},
	"stream":        {"opens a bounded producer and takes ownership of its stream", "an owned, single-consumer stream handle"},
	"streamFor":     {"consumes a stream sequentially and blocks until its terminal outcome", "the current stream item"},
	"closeStream":   {"cancels an owned stream and waits for bounded shutdown", ""},
	"stopReading":   {"cancels the source and exits the nearest stream loop successfully", ""},
	"collectStream": {"consumes a stream into a list with an explicit overflow limit", "the bounded list"},
	"sent":          {"calls vocabulary as a sentence: bare word or qualifier.word", "the called result"},
	"handler":       {"registers an outcome handler", ""},
	"ask":           {"configures the prompt of a provider call", ""},
	"using":         {"selects the model for provider calls", ""},
	"model":         {"overrides the model for this scope", ""},
	"accept":        {"sets the acceptance probability threshold", ""},

	// Stream-handling constructions: canonical surface of std/streams.
	"quietStream":       {"debounce: each item restarts the quiet timer; the latest item is emitted only after the complete duration with no newer item. Consumes the source; the derived stream becomes the single owner", "the derived stream"},
	"limitStream":       {"throttle: bounded emission rate; the keeping policy is mandatory because there is no unambiguous default", "the derived stream"},
	"throttlePolicy":    {"declares which item survives a rate window: the first or the latest", ""},
	"rejectExcess":      {"completes dropped owned items with a typed rejection status", ""},
	"handleEachStream":  {"merge: bounded concurrent handling; items start in source order, completion order is unconstrained", ""},
	"handleOneStream":   {"concat: ordered sequential handling; the next item is requested only after the current handler finishes", ""},
	"newestStream":      {"switch_latest: a newer item cancels the previous handler; only the newest surviving result becomes visible", ""},
	"newestCancel":      {"declares that a newer arrival cancels the previous handler's work", ""},
	"effectAck":         {"acknowledges that cancellation does not reverse completed effects; the risk becomes visible and auditable", ""},
	"cancelPolicy":      {"completes canceled owned items with a typed rejection status", ""},
	"streamBound":       {"bounds the number of concurrent keyed handlers", ""},
	"streamKnownBound":  {"bounds the number of known keys", ""},
	"conflateStream":    {"conflate: the active handler is never canceled; at most one waiting item is retained and replaced by newer arrivals", ""},
	"conflatePolicy":    {"retains only the latest waiting item while the active handler runs", ""},
	"exhaustStream":     {"exhaust: new arrivals are ignored or explicitly rejected while a handler is busy", ""},
	"exhaustPolicy":     {"declares the busy policy: ignoring (unowned items only) or typed rejection", ""},
	"batchStream":       {"batch: bounded batches emitted on the count or time limit, whichever happens first", "the derived stream of bounded lists"},
	"batchWindow":       {"the time limit after which a nonempty partial batch is emitted", ""},
	"distinctStream":    {"distinct_consecutive: only the immediately previous item is retained; consecutive repeats are dropped", "the derived stream"},
	"distinctKeyStream": {"distinct_consecutive over one declared scalar key", "the derived stream"},
	"idleStream":        {"idle_timeout: no item during the duration fails with StreamIdleTimeout and cancels upstream", "the derived stream"},
	"takeForStream":     {"take_for: bounded observation; upstream is canceled at the deadline and the stream completes normally", "the derived stream"},
	"deadlineStream":    {"fails with StreamDeadlineExceeded if the source has not finished within the duration", "the derived stream"},
	"filterStream":      {"block-based filter: the block must keep the item at most once per path", "the derived stream"},
	"keepItem":          {"keeps the current item in a filter block", ""},
	"projectStream":     {"block-based projection: the block must use exactly one value on every successful path", "the derived stream"},
	"useValue":          {"provides the projected value", ""},
	"calledName":        {"names the derived stream of a transformation", "the derived stream"},
}

var (
	reNewPackage    = regexp.MustCompile(`^\s*package\s+([A-Za-z_]\w*)`)
	reNewExport     = regexp.MustCompile(`^\s*export\s+([A-Za-z_]\w*)`)
	reNewImport     = regexp.MustCompile(`^\s*import\s+"([^"]*)"(?:\s+as\s+([A-Za-z_]\w*))?`)
	reNewCallLine   = regexp.MustCompile(`^\s*call\s+([A-Za-z_]\w*)\.([A-Za-z_]\w*)(?:\s+with\s+.*?)?(?:\s+called\s+([A-Za-z_]\w*))?\s*$`)
	reNewStreamLine = regexp.MustCompile(`^\s*stream\s+([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)(?:\s+with\s+.*?)?\s+called\s+([A-Za-z_]\w*)\s*$`)
	reNewWatchLine  = regexp.MustCompile(`^\s*watch\s+folder\s+".*?"\s+recursively\s+called\s+([A-Za-z_]\w*)`)
	reNewWalkLine   = regexp.MustCompile(`^\s*walk\s+through\s+folder\s+".*?"\s+at\s+most\s+\d+\s+entries\s+called\s+([A-Za-z_]\w*)`)
)

// filesHoverDocs explains the canonical filesystem constructions while the
// parser core does not yet classify them. Entries are ordered: the owned
// traversal and watcher forms first (they extend the stream lifecycle hover),
// then the remaining read/write/destructive forms. Each entry states the
// effect label, parameter names, reserved typed failures, and target
// availability the tooling contract requires. When the core classifies these
// lines, EditorMeanings takes precedence and these rows stop firing.
var filesHoverDocs = []struct {
	re  *regexp.Regexp
	doc func(code string, m []string) string
}{
	{regexp.MustCompile(`^stream\s+(files|folders|entries)\s+under\s+folder\s+".*?"\s+called\s+([A-Za-z_]\w*)`), func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Opens traversal stream** `%s` — a bounded, owned producer of `FileEntry` items in depth-first lexical order, with backpressure so traversal cannot outrun the consumer.\n\n**Effect:** filesystem read (`filesystem-read` capability; native and capable WASI hosts).\n\n**Defaults:** files and folders, no patterns, no exclusions, symbolic links not followed, stop on inaccessible entries. `stop reading`, `close stream`, cancellation, or scope cleanup stops the walk promptly.\n\n**May fail while opening or consuming:** `FileNotFound`, `FilePermissionDenied`, `InvalidFilePath`, `FileTraversalLimitExceeded`, `FileSystemUnavailable`.", code, m[2])
	}},
	{reNewWatchLine, func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Opens watcher** `%s` — an independently owned stream of `FileChange` items that stays active and emits future filesystem changes; it never emits an implicit snapshot.\n\n**Effect:** filesystem read (`filesystem-read` capability; native-only unless the host supplies a watcher adapter).\n\n**Change kinds:** `created`, `modified`, `removed`, `moved`, `overflowed`. An `overflowed` change means the consumer must rescan; the runtime never claims it observed dropped changes. Ownership, credit, counters, cancellation, and terminal failures are separate for every invocation: closing this watcher does not close another watcher from the same action.\n\n**May fail while opening or consuming:** `FileNotFound`, `FilePermissionDenied`, `InvalidFilePath`, `FileWatchOverflow`, `FileSystemUnavailable`.", code, m[1])
	}},
	{regexp.MustCompile(`^write\s+.+?\s+(atomically\s+)?to\s+file\s+".*?"\s*$`), func(code string, m []string) string {
		plan := "The next line must state the policy: `only if it does not exist` (create) or `replacing an existing file` (replace). There is no implicit overwrite."
		if strings.Contains(code, "atomically") {
			plan = "Atomic write creates a temporary regular file in the destination folder, flushes and closes it, then atomically replaces the destination where the host supports it; a failure before replacement preserves the old file."
		}
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem write (`filesystem-write` capability; native and capable WASI hosts).\n\n%s\n\n**Parameters:** `contents`, `path`, `policy` (`create` or `replace`). Fallback: `call files.write_text%s with path, contents, policy called written`.\n\n**May fail with:** `FileNotFound`, `FileAlreadyExists`, `FilePermissionDenied`, `FileTooLarge`, `InvalidFileType`, `InvalidFilePath`, `FileSystemUnavailable`.", code, plan, map[bool]string{true: "_atomically"}[strings.Contains(code, "atomically")])
	}},
	{regexp.MustCompile(`^(only if it does not exist|replacing an existing file)\s*$`), func(code string, m []string) string {
		policy := "create"
		if m[1] == "replacing an existing file" {
			policy = "replace"
		}
		return fmt.Sprintf("```sos\n%s\n```\n\n**Write policy modifier:** passes `policy` `%s` to the write above. Write policy is always explicit; there is no implicit overwrite.", code, policy)
	}},
	{regexp.MustCompile(`^append\s+line\s+to\s+file\s+".*?"\s*$`), func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem write — appends to the end of one regular file (`filesystem-write`; native and capable WASI hosts). Fallback: `call files.append_text with path, contents called written`.\n\n**May fail with:** `FileNotFound`, `FilePermissionDenied`, `InvalidFileType`, `InvalidFilePath`, `FileSystemUnavailable`.", code)
	}},
	{regexp.MustCompile(`^check\s+whether\s+(file|folder)\s+".*?"\s+exists\s+called\s+([A-Za-z_]\w*)`), func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem read — binds `%s` to true only when the named entry is absent; permission, malformed-path, and I/O errors fail instead of returning false (`filesystem-read`; native and capable WASI hosts).\n\n**May fail with:** `FilePermissionDenied`, `InvalidFilePath`, `FileSystemUnavailable`.", code, m[2])
	}},
	{regexp.MustCompile(`^inspect\s+entry\s+".*?"\s+called\s+([A-Za-z_]\w*)`), func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem read — binds `%s` to the typed `FileEntry` for the path itself, without following a final symbolic link unless explicitly requested (`filesystem-read`; native and capable WASI hosts).\n\n**May fail with:** `FileNotFound`, `FilePermissionDenied`, `InvalidFilePath`, `FileSystemUnavailable`.", code, m[1])
	}},
	{regexp.MustCompile(`^list\s+(entries|files|folders)\s+in\s+folder\s+".*?"\s+called\s+([A-Za-z_]\w*)`), func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem read — binds `%s` to the immediate children only (kind: %s), sorted by portable relative path; it does not recurse and fails rather than silently truncating (`filesystem-read`; native and capable WASI hosts).\n\n**May fail with:** `FileNotFound`, `FilePermissionDenied`, `InvalidFilePath`, `FileTraversalLimitExceeded`, `FileSystemUnavailable`.", code, m[2], m[1])
	}},
	{reNewWalkLine, func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem read — binds `%s` to a materialized, explicitly bounded list of `FileEntry` values for the tree that exists now; it never waits for future changes (`filesystem-read`; native and capable WASI hosts).\n\n**Defaults:** files and folders, no patterns, no exclusions, symbolic links not followed, stop on inaccessible entries. Modifier lines may add `including`, `matching`, `excluding`, `at most N folders deep`, and `without following symbolic links`.\n\n**May fail with:** `FileNotFound`, `FilePermissionDenied`, `InvalidFilePath`, `FileTraversalLimitExceeded`, `FileSystemUnavailable`.", code, m[1])
	}},
	{regexp.MustCompile(`^copy\s+(file|folder)\s+".*?"\s+to\s+".*?"\s*$`), func(code string, m []string) string {
		scope := "It does not follow a symbolic-link source by default."
		if m[1] == "folder" {
			scope = "Folder copy is recursive and observes the same link, exclusion, cancellation, and entry-limit rules as traversal."
		}
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem write — duplicates the source while preserving it (capabilities: read and write; native and capable WASI hosts). %s The next line may require `only if the destination does not exist`.\n\n**May fail with:** `FileNotFound`, `FileAlreadyExists`, `FilePermissionDenied`, `InvalidFileType`, `InvalidFilePath`, `FileTraversalLimitExceeded`, `FileSystemUnavailable`.", code, scope)
	}},
	{regexp.MustCompile(`^move\s+(file|folder)\s+".*?"\s+to\s+".*?"\s*$`), func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem write — relocates the entry (capabilities: read and write; native and capable WASI hosts). Cross-device move may fall back to copy followed by removal only when explicitly permitted, because that fallback is not atomic.\n\n**May fail with:** `FileNotFound`, `FileAlreadyExists`, `FilePermissionDenied`, `InvalidFileType`, `InvalidFilePath`, `FileSystemUnavailable`.", code)
	}},
	{regexp.MustCompile(`^remove\s+(file\s+".*?"|empty\s+folder\s+".*?"|folder\s+".*?"\s+including\s+its\s+contents)\s*$`), func(code string, m []string) string {
		kind := "one exact file"
		if strings.HasPrefix(m[1], "empty") {
			kind = "one empty folder"
		} else if strings.HasPrefix(m[1], "folder") {
			kind = "one folder and its contents — the phrase `including its contents` is required, because `remove entry` would conceal whether recursive deletion is possible"
		}
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** destructive filesystem write — deletes %s. Recursive removal does not follow symbolic links and applies an entry limit before deletion begins where the host can enumerate safely. There is no force option that converts permission or I/O failures into success (`filesystem-write`; native and capable WASI hosts).\n\n**May fail with:** `FileNotFound`, `FilePermissionDenied`, `InvalidFileType`, `InvalidFilePath`, `FileTraversalLimitExceeded`, `FileSystemUnavailable`.", code, kind)
	}},
	{regexp.MustCompile(`^create\s+folders\s+through\s+".*?"\s*$`), func(code string, m []string) string {
		return fmt.Sprintf("```sos\n%s\n```\n\n**Effect:** filesystem write — ensures every folder along the path exists, creating missing parents (`filesystem-write`; native and capable WASI hosts). Compare `create folder ... if missing` for one folder.\n\n**May fail with:** `FileAlreadyExists` (when a non-folder entry blocks the path), `FilePermissionDenied`, `InvalidFilePath`, `FileSystemUnavailable`.", code)
	}},
}

// newSyntaxHover explains package/export/import/call lines the core does not
// classify yet, plus sentence calls that resolve in this document's
// vocabulary. ok is false for lines that are none of these.
func (s *server) newSyntaxHover(uri, text, line string) (string, bool) {
	code := strings.TrimSpace(stripLineComment(line))
	if m := reNewStreamLine.FindStringSubmatch(code); m != nil {
		detail := ""
		if !strings.Contains(m[1], ".") {
			if item, failures, ok := localStreamMetadata(text, m[1]); ok {
				detail = fmt.Sprintf("\n\n**Item type:** `%s`", item)
				if len(failures) > 0 {
					detail += "\n\n**May fail while opening or consuming:** `" + strings.Join(failures, "`, `") + "`"
				}
			}
		} else {
			alias, action, _ := strings.Cut(m[1], ".")
			for _, target := range s.wordTargets(uri, vocabFilename(uri, s.workspaceRoot), text) {
				if !target.Enabled || target.Qualifier != alias || target.Name != action {
					continue
				}
				if item := strings.TrimSpace(strings.TrimPrefix(target.Result, "stream of ")); item != "" && item != target.Result {
					detail += fmt.Sprintf("\n\n**Item type:** `%s`", item)
				}
				if len(target.PossibleFailures) > 0 {
					detail += "\n\n**May fail while opening or consuming:** `" + strings.Join(target.PossibleFailures, "`, `") + "`"
				}
				if len(target.Effects) > 0 {
					detail += "\n\n**Effects:** `" + strings.Join(target.Effects, "`, `") + "`"
				}
				if len(target.Targets) > 0 {
					detail += "\n\n**Targets:** `" + strings.Join(target.Targets, "`, `") + "`"
				}
				break
			}
		}
		return fmt.Sprintf("```sos\n%s\n```\n\n**Opens stream** `%s` from `%s`.%s The handle owns one bounded producer, may be consumed once, and must be consumed or closed before its scope exits. Opening waits only for producer confirmation; terminal failures occur while consuming.", code, m[2], m[1], detail), true
	}
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
	for _, row := range filesHoverDocs {
		if m := row.re.FindStringSubmatch(code); m != nil {
			return row.doc(code, m), true
		}
	}
	if head := sentHead(code); head != "" {
		if t := resolveSent(head, s.wordTargets(uri, vocabFilename(uri, s.workspaceRoot), text)); t != nil {
			return sentHover(code, head, t), true
		}
	}
	return "", false
}

func localStreamMetadata(source, name string) (string, []string, bool) {
	program, _ := sos.Parse(source)
	if program == nil {
		return "", nil, false
	}
	for _, action := range program.ActionMetadata() {
		if action.Name != name || action.StreamItem == "" {
			continue
		}
		return action.StreamItem, append([]string(nil), action.PossibleFailures...), true
	}
	return "", nil, false
}

// sentHover documents one resolved sentence call.
func sentHover(code, head string, t *wordTarget) string {
	form := "bare word"
	if strings.Contains(head, ".") {
		form = "qualified"
	}
	value := fmt.Sprintf("```sos\n%s\n```\n\n**Calls** `%s` (%s) from `%s`", code, head, form, t.ImportPath)
	if sig := entrySignature(t.Name, t.Params, t.Result, t.PossibleFailures); sig != t.Name+"()" {
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
