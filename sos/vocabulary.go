package sos

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The sentence-call form: NAME [ARG | "with" ARGS] ["called" RESULT], where
// NAME is a bare vocabulary word or an alias-qualified word. It is recognized
// only when NAME resolves in the file's enabled vocabulary; otherwise the line
// stays an unknown construction and flows to assisted interpretation exactly
// as before. matchSent extracts the trailing "called NAME" sink against
// quote-masked text, so quoted argument text containing " called " stays
// argument text, and zero-argument calls carry no argument groups.
var (
	sentNameRe = regexp.MustCompile(`^([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)`)
	sentSinkRe = regexp.MustCompile(` called ([A-Za-z_]\w*)$`)
)

func matchSent(text string) []string {
	masked := string(maskQuoted(text))
	loc := sentNameRe.FindStringIndex(masked)
	if loc == nil {
		return nil
	}
	if loc[1] == len(masked) {
		return []string{text, text, "", "", ""}
	}
	if masked[loc[1]] != ' ' {
		return nil // "name:" or "name," is not a sentence call
	}
	name := text[:loc[1]]
	rest := masked[loc[1]:]
	argsText := text[loc[1]:]
	sink := ""
	if sinkLoc := sentSinkRe.FindStringSubmatchIndex(rest); sinkLoc != nil {
		sink = text[loc[1]+sinkLoc[2] : loc[1]+sinkLoc[3]]
		argsText = text[loc[1] : loc[1]+sinkLoc[0]]
	}
	argsText = strings.TrimPrefix(argsText, " ")
	out := []string{text, name, "", "", sink}
	if argsText == "" {
		return out
	}
	if with, ok := strings.CutPrefix(argsText, "with "); ok {
		out[2] = with
	} else {
		out[3] = argsText
	}
	return out
}

// reservedWords are sentence words a bare vocabulary word can never displace:
// canonical keywords plus the noncanonical registry's leading words. Exposing
// one bare is a deterministic construction error, never a silent shadowing.
var reservedWords = func() map[string]bool {
	set := map[string]bool{}
	for _, k := range Keywords() {
		set[strings.Fields(k)[0]] = true
	}
	for _, w := range []string{"print", "emit", "set", "stop", "order", "load", "write", "store", "collect", "retain", "remove", "filter", "criterion"} {
		set[w] = true
	}
	return set
}()

// importBinding is one resolved vocabulary source for a file: an import
// statement or a configured library.
type importBinding struct {
	ref      string // reference as written
	alias    string // explicit alias ("" when bare)
	explicit bool
	line     int
	origin   string // "import" or "config <layer name>"
	module   *Module
}

// vocabTarget is one resolvable bare word.
type vocabTarget struct {
	module *Module
	action string // canonical exported operation name
}

// vocabLib records one enabled library binding for catalogs and graphs.
type vocabLib struct {
	path   string
	key    string
	alias  string
	bare   bool
	origin string
	module *Module
}

// fileVocab is one file's resolved vocabulary: definition-site only. The
// entry file's vocabulary merges its imports with resolved configuration
// libraries; a package file's vocabulary is exactly that file's imports.
type fileVocab struct {
	words   map[string]vocabTarget
	aliases map[string]*Module
	libs    []vocabLib
}

// resolveName maps a sentence-call name to its module and canonical action.
func (v *fileVocab) resolveName(name string) (mod *Module, action string, ok bool) {
	if v == nil {
		return nil, "", false
	}
	if alias, word, found := strings.Cut(name, "."); found {
		mod = v.aliases[alias]
		if mod == nil {
			return nil, "", false
		}
		action = mod.Words[word]
		if action == "" || !mod.Exports[action] {
			return nil, "", false
		}
		return mod, action, true
	}
	t, found := v.words[name]
	if !found {
		return nil, "", false
	}
	return t.module, t.action, true
}

// buildFileVocab resolves one file's bindings into a vocabulary, reporting
// deterministic conflicts: duplicate aliases, bare-word collisions across
// libraries, words shadowing file-local actions, and reserved sentence words.
// Collisions are errors, never silent precedence.
func buildFileVocab(bindings []importBinding, localActions map[string]bool, diag func(line int, format string, args ...any)) *fileVocab {
	v := &fileVocab{words: map[string]vocabTarget{}, aliases: map[string]*Module{}}
	seenAlias := map[string]string{}
	for _, b := range bindings {
		alias := b.alias
		if alias == "" {
			alias = b.module.Name
		}
		if alias != "" {
			if prev, dup := seenAlias[alias]; dup {
				diag(b.line, "duplicate import alias %q (already bound by %s)", alias, prev)
				continue
			}
			seenAlias[alias] = b.ref
			v.aliases[alias] = b.module
		}
		v.libs = append(v.libs, vocabLib{path: b.ref, key: b.module.Key, alias: alias, bare: !b.explicit, origin: b.origin, module: b.module})
		if b.explicit {
			continue
		}
		words := sortedModuleWords(b.module)
		for _, w := range words {
			canon := b.module.Words[w]
			if prev, dup := v.words[w]; dup {
				if prev.module.Key == b.module.Key && prev.action == canon {
					continue // same library reachable twice; one meaning
				}
				diag(b.line, "vocabulary word %q from %s collides with the same word from %s; import one with an as alias (for example as = %q) to require its prefix", w, b.ref, prev.module.Name, b.module.Name)
				continue
			}
			if reservedWords[w] {
				diag(b.line, "vocabulary word %q from %s cannot be exposed bare: it collides with a built-in sentence word; import it with an as alias instead", w, b.ref)
				continue
			}
			if localActions[w] {
				diag(b.line, "vocabulary word %q from %s collides with action %q in this file; import the library with an as alias", w, b.ref, w)
				continue
			}
			v.words[w] = vocabTarget{module: b.module, action: canon}
		}
	}
	return v
}

func sortedModuleWords(m *Module) []string {
	out := make([]string, 0, len(m.Words))
	for word := range m.Words {
		out = append(out, word)
	}
	sort.Strings(out)
	return out
}

// sentArguments splits argument expressions; zero-argument actions need none.
func sentArguments(m []string) ([]string, error) {
	if m[2] != "" {
		return splitExpressions(m[2])
	}
	if strings.TrimSpace(m[3]) == "" {
		return nil, nil
	}
	return []string{m[3]}, nil
}

// sentArity reports the expected argument count for a target, when statically
// known: native signatures are typed; actions declare their parameter list.
func sentArity(mod *Module, action string) (int, bool) {
	if op, ok := mod.Native[action]; ok {
		return len(op.Params), true
	}
	if s := mod.Actions[action]; s != nil {
		fm := match("to", s.Text)
		if fm[2] == "" {
			return 0, true
		}
		n := 0
		for _, part := range strings.Split(fm[2], ",") {
			if strings.TrimSpace(part) != "" {
				n++
			}
		}
		return n, true
	}
	return 0, false
}

// reclassifySent rewrites resolvable sentence calls from invalid statements to
// the canonical "sent" kind, recording their lines so the matching unknown-
// construction diagnostics can be dropped. Statements with non-handler
// children stay invalid: a sentence call owns only handlers.
func reclassifySent(sts []*Statement, v *fileVocab, reclassified map[int]string) {
	for _, s := range sts {
		if s.Kind == "invalid" && handlersOnly(s.Body) {
			if m := matchSent(s.Text); m != nil {
				if mod, action, ok := v.resolveName(m[1]); ok {
					if want, known := sentArity(mod, action); known {
						if args, e := sentArguments(m); e == nil && len(args) == want {
							s.Kind = "sent"
							reclassified[s.Line] = s.Text
						}
					}
				}
			}
		}
		reclassifySent(s.Body, v, reclassified)
	}
}

func handlersOnly(sts []*Statement) bool {
	for _, s := range sts {
		if s.Kind != "handler" {
			return false
		}
	}
	return true
}

// dropReclassifiedDiagnostics removes the unknown-construction diagnostics of
// lines promoted to sentence calls, keeping every other diagnostic.
func dropReclassifiedDiagnostics(ds []Diagnostic, reclassified map[int]string) []Diagnostic {
	if len(reclassified) == 0 {
		return ds
	}
	out := ds[:0]
	for _, d := range ds {
		if text, ok := reclassified[d.Line]; ok && d.Message == "unknown construction: "+text {
			continue
		}
		out = append(out, d)
	}
	return out
}

// reclassifyProgram applies a resolved vocabulary to a parsed program.
func reclassifyProgram(p *Program, modules *ModuleTable) {
	if p == nil || modules == nil || modules.vocab == nil {
		return
	}
	fixed := map[int]string{}
	reclassifySent(p.Statements, modules.vocab, fixed)
	return
}

// ParseWithVocabulary parses source and applies the module table's resolved
// vocabulary: sentence calls that resolve become canonical "sent" statements
// and their unknown-construction diagnostics are dropped. It is the parse to
// use wherever a program is rebuilt from source with imports already resolved.
func ParseWithVocabulary(source string, modules *ModuleTable) (*Program, []Diagnostic) {
	p, ds := Parse(source)
	if modules == nil || modules.vocab == nil {
		return p, ds
	}
	fixed := map[int]string{}
	reclassifySent(p.Statements, modules.vocab, fixed)
	ds = dropReclassifiedDiagnostics(ds, fixed)
	p.Modules = modules
	return p, ds
}

// VocabularyCatalog is the offline searchable vocabulary surface for one
// source file: its enabled word forms plus discoverable library entries.
type VocabularyCatalog struct {
	Entries   []VocabularyEntry   `json:"entries"`
	Libraries []VocabularyLibrary `json:"libraries"`
	Failures  []FailureDef        `json:"failures,omitempty"`
}

// VocabularyParam is one typed parameter of a vocabulary entry.
type VocabularyParam struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// VocabularyEntry is one catalog row: a callable operation with its usable
// sentence forms, signature, effects, origin, and whether it is enabled here.
type VocabularyEntry struct {
	ID               string            `json:"id"`
	Library          string            `json:"library"`
	Alias            string            `json:"alias,omitempty"`
	Name             string            `json:"name"`
	Kind             string            `json:"kind"`
	Patterns         []string          `json:"patterns"`
	Synonyms         []string          `json:"synonyms,omitempty"`
	Description      string            `json:"description,omitempty"`
	Params           []VocabularyParam `json:"params,omitempty"`
	Result           string            `json:"result,omitempty"`
	PossibleFailures []string          `json:"possibleFailures,omitempty"`
	Effects          []string          `json:"effects,omitempty"`
	Origin           string            `json:"origin"`
	Enabled          bool              `json:"enabled"`
	Import           string            `json:"import,omitempty"`
}

// VocabularyLibrary is one library binding: how it is reached and from where.
type VocabularyLibrary struct {
	Path   string `json:"path"`
	Alias  string `json:"alias"`
	Bare   bool   `json:"bare"`
	Origin string `json:"origin"`
	Key    string `json:"key,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Vocabulary resolves the offline vocabulary catalog for one source file. It
// reports the enabled forms from the file's imports and resolved project and
// global configuration, plus discoverable standard and local library entries
// for preview (Enabled false). It reads only local sources and configuration;
// it never contacts a provider or consumes request budget.
func Vocabulary(filename, source string) (*VocabularyCatalog, []Diagnostic) {
	p, ds, loader := resolveModulesLoaded(filename, source)
	catalog := &VocabularyCatalog{Entries: []VocabularyEntry{}, Libraries: []VocabularyLibrary{}}
	if p != nil {
		for _, name := range sortedFailureNames(p.Failures) {
			catalog.Failures = append(catalog.Failures, *p.Failures[name])
		}
		if p.Modules != nil {
			seen := map[string]bool{}
			for _, definition := range catalog.Failures {
				seen[definition.Name] = true
			}
			for _, module := range p.Modules.Aliases {
				for name, definition := range module.Failures {
					if module.Exports[name] && !seen[name] {
						catalog.Failures = append(catalog.Failures, *definition)
						seen[name] = true
					}
				}
			}
			sort.Slice(catalog.Failures, func(i, j int) bool { return catalog.Failures[i].Name < catalog.Failures[j].Name })
		}
	}
	enabledKeys := map[string]bool{}
	if p != nil && p.Modules != nil && p.Modules.vocab != nil {
		for _, lib := range p.Modules.vocab.libs {
			enabledKeys[lib.key] = true
			catalog.Libraries = append(catalog.Libraries, VocabularyLibrary{Path: lib.path, Alias: lib.alias, Bare: lib.bare, Origin: lib.origin, Key: lib.key})
			catalog.Entries = append(catalog.Entries, moduleEntries(lib, true)...)
		}
	}
	catalog.Entries = append(catalog.Entries, stdPreviewEntries(enabledKeys)...)
	catalog.Entries = append(catalog.Entries, localPreviewEntries(loader, enabledKeys)...)
	sortEntries(catalog.Entries)
	sort.Slice(catalog.Libraries, func(i, j int) bool { return catalog.Libraries[i].Path < catalog.Libraries[j].Path })
	return catalog, ds
}

func sortedFailureNames(defs map[string]*FailureDef) []string {
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortEntries(entries []VocabularyEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Library != entries[j].Library {
			return entries[i].Library < entries[j].Library
		}
		return entries[i].Name < entries[j].Name
	})
}

// moduleEntries builds catalog rows for one resolved library binding.
func moduleEntries(lib vocabLib, enabled bool) []VocabularyEntry {
	mod := lib.module
	names := make([]string, 0, len(mod.Exports))
	for name := range mod.Exports {
		names = append(names, name)
	}
	out := make([]VocabularyEntry, 0, len(names))
	for _, name := range names {
		e := VocabularyEntry{
			ID:      lib.path + "." + name,
			Library: lib.path,
			Name:    name,
			Origin:  lib.origin,
			Enabled: enabled,
		}
		if enabled {
			e.Alias = lib.alias
		}
		for w, canon := range mod.Words {
			if canon == name && w != name {
				e.Synonyms = append(e.Synonyms, w)
			}
		}
		sort.Strings(e.Synonyms)
		words := append([]string{name}, e.Synonyms...)
		if op, ok := mod.Native[name]; ok {
			e.Kind = "native"
			for _, p := range op.Params {
				e.Params = append(e.Params, VocabularyParam{Name: p.Name, Type: p.Type})
			}
			if doc, ok := stdDocs[lib.key+"."+name]; ok {
				e.Result = doc.result
				e.Description = doc.description
				e.Effects = append([]string(nil), doc.effects...)
			}
		} else if s := mod.Actions[name]; s != nil {
			e.Kind = "action"
			if decl, err := parseActionDecl(s.Text); err == nil {
				if decl.HasResult {
					e.Result = decl.Result.String()
				}
				e.PossibleFailures = append(e.PossibleFailures, decl.Failures...)
			}
			e.PossibleFailures = append(e.PossibleFailures, modulePossibleFailures(mod, name, map[string]bool{})...)
			e.PossibleFailures = uniqueSorted(e.PossibleFailures)
			if fm := match("to", s.Text); fm[2] != "" {
				for _, part := range strings.Split(fm[2], ",") {
					if part = strings.TrimSpace(part); part != "" {
						e.Params = append(e.Params, VocabularyParam{Name: part, Type: "value"})
					}
				}
			}
			e.Effects = actionEffects(mod, name, map[string]bool{})
		} else {
			continue // schemas are not callable vocabulary
		}
		if enabled {
			e.Alias = lib.alias
			e.Patterns = sentencePatterns(e.Alias, words, len(e.Params), !lib.bare)
		} else {
			// Preview rows show the forms an unaliased import would enable.
			e.Alias = mod.Name
			e.Import = `import "` + lib.path + `"`
			e.Patterns = sentencePatterns(e.Alias, words, len(e.Params), false)
		}
		out = append(out, e)
	}
	return out
}

// sentencePatterns lists the concrete usable forms: bare word forms when the
// binding exposes them, and the qualified alias form otherwise.
func sentencePatterns(alias string, words []string, params int, qualifiedOnly bool) []string {
	var out []string
	suffix := ""
	if params == 1 {
		suffix = " VALUE"
	}
	if params > 1 {
		parts := make([]string, params)
		for i := range parts {
			parts[i] = "VALUE"
		}
		suffix = " with " + strings.Join(parts, ", ")
	}
	for _, word := range words {
		if !qualifiedOnly {
			out = append(out, word+suffix)
		}
		if alias != "" {
			out = append(out, alias+"."+word+suffix)
		}
	}
	return out
}

// stdPreviewEntries lists compiled-in standard operations that are not enabled
// by this file's bindings.
func stdPreviewEntries(enabledKeys map[string]bool) []VocabularyEntry {
	var out []VocabularyEntry
	ops := StandardOperations()
	for _, op := range ops {
		if enabledKeys["std/"+op.Package] {
			continue
		}
		e := VocabularyEntry{
			ID:          op.ImportPath + "." + op.Name,
			Library:     op.ImportPath,
			Alias:       op.Package,
			Name:        op.Name,
			Kind:        "native",
			Description: op.Description,
			Result:      op.Result,
			Effects:     append([]string(nil), op.Effects...),
			Origin:      "standard library",
			Enabled:     false,
			Import:      `import "` + op.ImportPath + `"`,
		}
		for _, p := range op.Params {
			e.Params = append(e.Params, VocabularyParam{Name: p.Name, Type: p.Type})
		}
		e.Synonyms = stdSynonyms(op.ImportPath, op.Name)
		e.Patterns = sentencePatterns(op.Package, append([]string{op.Name}, e.Synonyms...), len(e.Params), false)
		out = append(out, e)
	}
	return out
}

func stdSynonyms(path, name string) []string {
	ops, ok := stdRegistry[path]
	if !ok {
		return nil
	}
	return append([]string(nil), ops[name].Synonyms...)
}

// localPreviewEntries discovers local packages under the project module path
// for offline preview. Discovery is bounded and read-only; failing packages
// are reported as library rows with errors, never guessed entries.
func localPreviewEntries(loader *fsLoader, enabledKeys map[string]bool) []VocabularyEntry {
	if loader == nil || loader.ns == "" || loader.tomlDir == "" {
		return nil
	}
	refs, err := discoverLocalRefs(loader.tomlDir, loader.ns)
	if err != nil || len(refs) == 0 {
		return nil
	}
	scout := &fsLoader{cache: map[string]*Module{}, loading: map[string]bool{}, root: loader.root, ns: loader.ns, tomlDir: loader.tomlDir}
	var out []VocabularyEntry
	seenKeys := map[string]bool{}
	for _, ref := range refs {
		mod, ok := scout.resolve(ref, loader.root, 0, 1)
		if !ok {
			continue // resolution diagnostics were scoped to the scout loader
		}
		if enabledKeys[mod.Key] || seenKeys[mod.Key] {
			continue
		}
		seenKeys[mod.Key] = true
		lib := vocabLib{path: ref, key: mod.Key, alias: mod.Name, bare: true, origin: "local " + ref, module: mod}
		out = append(out, moduleEntries(lib, false)...)
	}
	return out
}

// discoverLocalRefs lists package references under the module path root:
// directory packages first (a directory containing .sos files), then .sos
// files directly under the root, bounded in count and depth. Files inside an
// emitted directory package are skipped: the directory is the package.
func discoverLocalRefs(base, ns string) ([]string, error) {
	var refs []string
	var dirs []string
	visited := 0
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		visited++
		if visited > 4096 {
			return filepath.SkipAll
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "vendor" || d.Name() == "build" || d.Name() == "webdist") {
			return filepath.SkipDir
		}
		rel, e := filepath.Rel(base, path)
		if e != nil || rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		depth := len(strings.Split(filepath.ToSlash(rel), "/"))
		switch {
		case d.IsDir():
			if depth > 3 {
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
			return nil
		case strings.HasSuffix(d.Name(), ".sos") && depth == 1:
			if len(refs) >= 64 {
				return filepath.SkipAll
			}
			refs = append(refs, ns+"/"+d.Name())
		}
		return nil
	})
	for _, dir := range dirs {
		entries, e := os.ReadDir(dir)
		if e != nil {
			continue
		}
		hasSos := false
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sos") {
				hasSos = true
				break
			}
		}
		if !hasSos {
			continue
		}
		rel, e := filepath.Rel(base, dir)
		if e != nil {
			continue
		}
		if len(refs) >= 64 {
			break
		}
		refs = append(refs, ns+"/"+filepath.ToSlash(rel))
	}
	sort.Strings(refs)
	return refs, err
}

// actionEffects follows definition-site calls, conservatively reporting unknown
// when a target cannot be proved. Cycles are visited once per module/action.
func actionEffects(mod *Module, name string, seen map[string]bool) []string {
	if mod == nil {
		return []string{"unknown"}
	}
	key := mod.Key + ":" + name
	if seen[key] {
		return nil
	}
	seen[key] = true
	if _, native := mod.Native[name]; native {
		return nil
	}
	action := mod.Actions[name]
	if action == nil {
		return []string{"unknown"}
	}
	effects := map[string]bool{}
	include := func(target *Module, targetName string) {
		for _, effect := range actionEffects(target, targetName, seen) {
			if effect != "pure" {
				effects[effect] = true
			}
		}
	}
	var walk func([]*Statement)
	walk = func(statements []*Statement) {
		for _, st := range statements {
			if UsesJev(st) {
				effects["jev"] = true
			}
			switch st.Kind {
			case "read", "readEach", "find", "save", "folder":
				effects["filesystem"] = true
			case "show":
				effects["output"] = true
			case "call":
				m := match("call", st.Text)
				if alias, operation, qualified := strings.Cut(m[1], "."); qualified {
					include(mod.scope(name)[alias], operation)
				} else {
					include(mod, m[1])
				}
			case "sent":
				m := matchSent(st.Text)
				target, operation, ok := mod.vocabulary(name).resolveName(m[1])
				if !ok {
					effects["unknown"] = true
				} else {
					include(target, operation)
				}
			case "handler":
				m := match("handler", st.Text)
				if m != nil && m[2] != "" {
					walk([]*Statement{{Kind: classifyLine(m[2]), Text: m[2]}})
				}
			case "invalid", "":
				effects["unknown"] = true
			}
			walk(st.Body)
		}
	}
	walk(action.Body)
	out := make([]string, 0, len(effects))
	for effect := range effects {
		out = append(out, effect)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"pure"}
	}
	return out
}
