package sos

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

// Bounds for one program's import graph. Reaching any bound is a diagnostic,
// never a hang: the loader always terminates on finite graphs.
const (
	maxModules     = 64
	maxImportDepth = 16
	maxModuleBytes = 4 << 20
)

// ModuleFile is one source file of a package. Imports are file-local: two
// files in one package may bind the same alias to different modules.
type ModuleFile struct {
	Name    string
	Source  string
	Aliases map[string]*Module
	refs    map[string]ModuleEdge
}

// Module is one importable package: a single file, or a directory whose
// files contribute shared declarations. Modules contribute declarations
// only; their top-level statements never execute and their frontmatter
// never contributes policy.
type Module struct {
	// Key is the resolved identity: a filesystem path for local packages,
	// or "std/name" for standard packages.
	Key string
	// Name is the declared package name (default: the package directory).
	Name string
	// Files are the package's source files in deterministic order.
	Files []ModuleFile
	// Actions, Schemas, and Definitions are the package-wide declaration tables.
	Actions     map[string]*Statement
	Schemas     map[string]*Statement
	Definitions map[string]*RecordDef
	Failures    map[string]*FailureDef
	// fileImports maps each action to its defining file's import scope;
	// fileVocabs maps it to that file's definition-site vocabulary.
	fileImports    map[string]map[string]*Module
	fileVocabs     map[string]*fileVocab
	failureImports map[string]map[string]*Module
	// Native holds compiled-in operations (standard packages).
	Native map[string]NativeOp
	// Exports lists names importers may reference.
	Exports map[string]bool
	// Words maps every callable word (action names and declared synonyms)
	// to its canonical exported name.
	Words map[string]string
}

// scope returns the definition-site import scope for one action.
func (m *Module) scope(action string) map[string]*Module {
	return m.fileImports[action]
}

// vocabulary returns the definition-site vocabulary for one action.
func (m *Module) vocabulary(action string) *fileVocab {
	return m.fileVocabs[action]
}

// ModuleTable is a program's resolved import graph.
type ModuleTable struct {
	// Aliases are the entry file's imports.
	Aliases map[string]*Module `json:"-"`
	// ByKey and Order cover every loaded local package.
	ByKey map[string]*Module `json:"-"`
	Order []string           `json:"-"`
	// vocab is the entry file's resolved vocabulary (imports plus configured
	// libraries); libraries are the config-derived bindings captured for graphs.
	vocab     *fileVocab
	libraries []vocabLib

	entryRefs map[string]ModuleEdge
}

// Vocabulary exposes the entry file's resolved word table for tooling walks.
func (t *ModuleTable) Vocabulary() *fileVocab { return t.vocab }

type ModuleEdge struct {
	Key   string `json:"key"`
	Alias string `json:"alias"`
}

// ModuleFileSpec is one embedded package file for standalone artifacts.
type ModuleFileSpec struct {
	Name    string                `json:"name"`
	Source  string                `json:"source"`
	Imports map[string]ModuleEdge `json:"imports,omitempty"`
}

// ModuleSpec is one embedded package for standalone artifacts.
type ModuleSpec struct {
	Key   string           `json:"key"`
	Name  string           `json:"name"`
	Files []ModuleFileSpec `json:"files"`
}

// LibrarySpec is one resolved configuration library captured in a graph so
// standalone artifacts keep their vocabulary without the source or sos.toml.
type LibrarySpec struct {
	Path   string `json:"path"`
	Key    string `json:"key"`
	Alias  string `json:"alias,omitempty"` // explicit alias; empty means bare words
	Bare   bool   `json:"bare"`
	Origin string `json:"origin"`
}

// ModuleGraph is the JSON-serializable import graph: entry-file edges plus
// every local package with per-file edges, plus resolved configuration
// libraries with their origins. Standard packages are compiled into the
// interpreter and never appear as modules here.
type ModuleGraph struct {
	Entry     map[string]ModuleEdge `json:"entry,omitempty"`
	Modules   []ModuleSpec          `json:"modules,omitempty"`
	Libraries []LibrarySpec         `json:"libraries,omitempty"`
}

// Graph renders this table as an embeddable graph. Refs are recorded per
// file at load time, so no path resolution is needed to rebuild it.
func (t *ModuleTable) Graph() *ModuleGraph {
	if t == nil {
		return &ModuleGraph{}
	}
	g := &ModuleGraph{Entry: t.entryRefs}
	for _, lib := range t.libraries {
		spec := LibrarySpec{Path: lib.path, Key: lib.key, Bare: lib.bare, Origin: lib.origin}
		if !lib.bare {
			spec.Alias = lib.alias
		}
		g.Libraries = append(g.Libraries, spec)
	}
	for _, key := range t.Order {
		m := t.ByKey[key]
		spec := ModuleSpec{Key: key, Name: m.Name}
		for i := range m.Files {
			f := &m.Files[i]
			fs := ModuleFileSpec{Name: f.Name, Source: f.Source}
			if len(f.refs) > 0 {
				fs.Imports = f.refs
			}
			spec.Files = append(spec.Files, fs)
		}
		g.Modules = append(g.Modules, spec)
	}
	return g
}

// OperationInfo describes one exported operation for tooling.
type OperationInfo struct {
	Module           string      `json:"module"`
	Name             string      `json:"name"`
	Kind             string      `json:"kind"` // action, failure, schema, or native
	Params           []string    `json:"params,omitempty"`
	Result           string      `json:"result,omitempty"`
	PossibleFailures []string    `json:"possibleFailures,omitempty"`
	Failure          *FailureDef `json:"failure,omitempty"`
}

// ActionMetadata exposes the stable signature surface used by checkers,
// Studio, and editor clients for actions declared in the current source.
func (p *Program) ActionMetadata() []OperationInfo {
	if p == nil {
		return nil
	}
	var out []OperationInfo
	for _, statement := range p.Statements {
		if statement.Kind != "to" {
			continue
		}
		decl, err := parseActionDecl(statement.Text)
		if err != nil {
			continue
		}
		info := OperationInfo{Name: decl.Name, Kind: "action"}
		for _, param := range decl.Params {
			info.Params = append(info.Params, param.Name+" as "+param.Type.String())
		}
		if decl.HasResult {
			info.Result = decl.Result.String()
		}
		info.PossibleFailures = append(info.PossibleFailures, decl.Failures...)
		info.PossibleFailures = append(info.PossibleFailures, p.actionPossibleFailures(decl.Name, map[string]bool{})...)
		info.PossibleFailures = uniqueSorted(info.PossibleFailures)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p *Program) actionPossibleFailures(name string, visiting map[string]bool) []string {
	if p == nil || visiting[name] {
		return nil
	}
	fn := p.actionDeclaration(name)
	if fn == nil {
		return nil
	}
	visiting[name] = true
	defer delete(visiting, name)
	decl, _ := parseActionDecl(fn.Text)
	result := append([]string(nil), decl.Failures...)
	var walk func([]*Statement)
	walk = func(stmts []*Statement) {
		for _, statement := range stmts {
			switch statement.Kind {
			case "fail":
				result = append(result, match("fail", statement.Text)[1])
			case "call":
				m := match("call", statement.Text)
				var failures []string
				if strings.Contains(m[1], ".") && p.Modules != nil {
					alias, action, _ := strings.Cut(m[1], ".")
					if module := p.Modules.Aliases[alias]; module != nil {
						failures = modulePossibleFailures(module, action, map[string]bool{})
					}
				} else if p.actionDeclaration(m[1]) != nil {
					failures = p.actionPossibleFailures(m[1], visiting)
				}
				result = append(result, unhandledFailures(failures, statement)...)
				walkFailureHandlerBodies(statement.Body, walk)
				continue
			case "sent":
				var failures []string
				if p.Modules != nil && p.Modules.vocab != nil {
					if sent := matchSent(statement.Text); sent != nil {
						if module, action, ok := p.Modules.vocab.resolveName(sent[1]); ok {
							failures = modulePossibleFailures(module, action, map[string]bool{})
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

func unhandledFailures(failures []string, operation *Statement) []string {
	var out []string
	for _, failure := range failures {
		if !callHasFailureHandler(operation, failure) {
			out = append(out, failure)
		}
	}
	return out
}

func walkFailureHandlerBodies(stmts []*Statement, walk func([]*Statement)) {
	for _, statement := range stmts {
		if statement.Kind == "handler" {
			walk(statement.Body)
		}
	}
}

func (p *Program) actionDeclaration(name string) *Statement {
	for _, statement := range p.Statements {
		if statement.Kind == "to" && match("to", statement.Text)[1] == name {
			return statement
		}
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		if value != "" {
			seen[value] = true
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// ExportedOperations lists every importable operation, sorted for stability.
func (p *Program) ExportedOperations() []OperationInfo {
	if p == nil || p.Modules == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []OperationInfo
	for _, key := range p.Modules.Order {
		m := p.Modules.ByKey[key]
		seen[m.Key] = true
		out = append(out, moduleOperations(m)...)
	}
	// Standard packages are compiled in and never enter Order; include the
	// ones this program actually imports.
	for _, m := range p.Modules.Aliases {
		if !seen[m.Key] && strings.HasPrefix(m.Key, "std/") {
			out = append(out, moduleOperations(m)...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func moduleOperations(m *Module) []OperationInfo {
	names := make([]string, 0, len(m.Exports))
	for name := range m.Exports {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]OperationInfo, 0, len(names))
	for _, name := range names {
		info := OperationInfo{Module: m.Name, Name: name}
		if op, ok := m.Native[name]; ok {
			info.Kind = "native"
			for _, p := range op.Params {
				info.Params = append(info.Params, p.Name)
			}
		} else if s := m.Actions[name]; s != nil {
			info.Kind = "action"
			if decl, err := parseActionDecl(s.Text); err == nil {
				if decl.HasResult {
					info.Result = decl.Result.String()
				}
				info.PossibleFailures = append(info.PossibleFailures, decl.Failures...)
			}
			info.PossibleFailures = append(info.PossibleFailures, modulePossibleFailures(m, name, map[string]bool{})...)
			info.PossibleFailures = uniqueSorted(info.PossibleFailures)
			if fm := match("to", s.Text); fm[2] != "" {
				for _, n := range strings.Split(fm[2], ",") {
					info.Params = append(info.Params, strings.TrimSpace(n))
				}
			}
		} else if failure := m.Failures[name]; failure != nil {
			info.Kind = "failure"
			info.Failure = failure
		} else {
			info.Kind = "schema"
		}
		out = append(out, info)
	}
	return out
}

// ImportedStatements exposes imported packages' statements for target
// validation and other tooling walks.
func (p *Program) ImportedStatements() []*Statement {
	if p == nil || p.Modules == nil {
		return nil
	}
	var out []*Statement
	for _, key := range p.Modules.Order {
		m := p.Modules.ByKey[key]
		for _, name := range sortedActionNames(m) {
			out = append(out, m.Actions[name])
		}
	}
	return out
}

func sortedActionNames(m *Module) []string {
	names := make([]string, 0, len(m.Actions))
	for name := range m.Actions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LoadProgram is the canonical entry point for tools: it parses source,
// resolves the local import graph rooted at filename's directory, and
// validates declarations, exports, and qualified calls. Noncanonical
// sentences pass through to semantic analysis as before.
func LoadProgram(filename string, source string) (*Program, []Diagnostic) {
	p, ds := resolveModules(filename, source)
	if len(ds) == 0 {
		ds = append(ds, analyze(p)...)
	}
	return p, ds
}

// ResolveModules parses source and resolves its import graph without the
// entry-file name and effect analysis: Run and build analysis remain the
// authority there, after policy and target preflights.
func ResolveModules(filename string, source string) (*Program, []Diagnostic) {
	return resolveModules(filename, source)
}

func resolveModules(filename string, source string) (*Program, []Diagnostic) {
	p, ds, _ := resolveModulesLoaded(filename, source)
	return p, ds
}

// resolveModulesLoaded is resolveModules retaining the loader so Vocabulary
// can discover local library previews under the same module base.
func resolveModulesLoaded(filename string, source string) (*Program, []Diagnostic, *fsLoader) {
	p, ds := Parse(source)
	for _, d := range ds {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			return p, ds, nil
		}
	}
	root := "."
	if filename != "" {
		if abs, err := filepath.Abs(filename); err == nil {
			root = filepath.Dir(abs)
		}
	}
	l := &fsLoader{cache: map[string]*Module{}, loading: map[string]bool{}, root: root}
	l.findModuleBase()
	if len(l.diags) > 0 {
		return p, l.diags, nil
	}
	bindings, refs, ok := bindImports(p.Statements,
		func(ref string, line int) (*Module, bool) { return l.resolve(ref, root, 0, line) },
		func(line int, format string, args ...any) { l.diag(line, format, args...) })
	// Configured libraries apply to the entry file only: imported packages
	// use their own definition-site imports.
	libBindings := l.configBindings(root)
	libBindings = withoutSourceOverrides(bindings, libBindings)
	vocab := buildFileVocab(append(bindings, libBindings...), entryActions(p), func(line int, format string, args ...any) { l.diag(line, format, args...) })
	fixed := map[int]string{}
	reclassifySent(p.Statements, vocab, fixed)
	ds = dropReclassifiedDiagnostics(ds, fixed)
	if !ok || len(l.diags) > 0 {
		return p, append(ds, l.diags...), nil
	}
	aliases := map[string]*Module{}
	for alias, mod := range vocab.aliases {
		aliases[alias] = mod
	}
	p.Modules = &ModuleTable{Aliases: aliases, ByKey: l.cache, Order: l.order, entryRefs: refs, vocab: vocab, libraries: libMeta(libBindings)}
	return p, ds, l
}

// Source imports explicitly replace configured bindings for the same library.
func withoutSourceOverrides(source, configured []importBinding) []importBinding {
	overridden := map[string]bool{}
	for _, binding := range source {
		overridden[binding.module.Key] = true
	}
	result := make([]importBinding, 0, len(configured))
	for _, binding := range configured {
		if !overridden[binding.module.Key] {
			result = append(result, binding)
		}
	}
	return result
}

// libMeta keeps the config-derived bindings for graph serialization.
func libMeta(bindings []importBinding) []vocabLib {
	var out []vocabLib
	for _, b := range bindings {
		out = append(out, vocabLib{path: b.ref, key: b.module.Key, alias: b.alias, bare: !b.explicit, origin: b.origin, module: b.module})
	}
	return out
}

// entryActions collects every action declared in the entry file, at any
// depth: bare vocabulary words must not shadow them.
func entryActions(p *Program) map[string]bool {
	out := map[string]bool{}
	var walk func([]*Statement)
	walk = func(sts []*Statement) {
		for _, s := range sts {
			if s.Kind == "to" {
				out[match("to", s.Text)[1]] = true
			}
			walk(s.Body)
		}
	}
	walk(p.Statements)
	return out
}

// LoadProgramFromGraph resolves imports from an embedded graph instead of
// the filesystem: standalone artifacts run with package sources deleted.
func LoadProgramFromGraph(filename string, source string, g *ModuleGraph) (*Program, []Diagnostic) {
	p, ds := Parse(source)
	for _, d := range ds {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			return p, ds
		}
	}
	// Noncanonical sentences defer to the embedded resolution at Run, as the
	// pre-module artifact did by parsing the canonical program.
	deferred := len(ds)
	ds = nil
	if g == nil {
		g = &ModuleGraph{}
	}
	gr := &graphLoader{graph: g, specs: map[string]*ModuleSpec{}, built: map[string]*Module{}, building: map[string]bool{}}
	for i := range g.Modules {
		gr.specs[g.Modules[i].Key] = &g.Modules[i]
	}
	bindings, refs, ok := bindImports(p.Statements,
		func(ref string, line int) (*Module, bool) {
			edge, found := g.Entry[ref]
			if !found {
				gr.diag(line, "import %q is missing from the embedded module graph", ref)
				return nil, false
			}
			return gr.build(edge.Key, line)
		},
		func(line int, format string, args ...any) { gr.diag(line, format, args...) })
	var libBindings []importBinding
	for _, ls := range g.Libraries {
		mod, built := gr.build(ls.Key, 1)
		if !built {
			continue
		}
		libBindings = append(libBindings, importBinding{ref: ls.Path, alias: ls.Alias, explicit: ls.Alias != "", line: 1, origin: ls.Origin, module: mod})
	}
	libBindings = withoutSourceOverrides(bindings, libBindings)
	vocab := buildFileVocab(append(bindings, libBindings...), entryActions(p), func(line int, format string, args ...any) { gr.diag(line, format, args...) })
	fixed := map[int]string{}
	reclassifySent(p.Statements, vocab, fixed)
	if !ok || len(gr.diags) > 0 {
		return p, append(ds, gr.diags...)
	}
	aliases := map[string]*Module{}
	for alias, mod := range vocab.aliases {
		aliases[alias] = mod
	}
	p.Modules = &ModuleTable{Aliases: aliases, ByKey: gr.built, Order: gr.order, entryRefs: refs, vocab: vocab, libraries: libMeta(libBindings)}
	if deferred == 0 {
		ds = append(ds, analyze(p)...)
	}
	return p, ds
}

// CheckFile checks source with import resolution rooted at filename's
// directory: the module-aware counterpart of Check.
func CheckFile(filename, source string) []Diagnostic {
	_, ds := LoadProgram(filename, source)
	return ds
}

// bindImports binds one file's import statements using resolve, computing
// default aliases and recording ref edges for graph serialization. Explicit
// aliases are kept distinct from default ones: only explicit aliases
// suppress a library's bare vocabulary.
func bindImports(sts []*Statement, resolve func(ref string, line int) (*Module, bool), diag func(line int, format string, args ...any)) ([]importBinding, map[string]ModuleEdge, bool) {
	var bindings []importBinding
	seen := map[string]bool{}
	refs := map[string]ModuleEdge{}
	ok := true
	for _, s := range sts {
		if s.Kind != "import" {
			continue
		}
		m := match("import", s.Text)
		mod, found := resolve(m[1], s.Line)
		if !found {
			ok = false
			continue
		}
		alias := m[2]
		if alias == "" {
			alias = mod.Name
		}
		if alias != "" && seen[alias] {
			diag(s.Line, "duplicate import alias %q", alias)
			ok = false
			continue
		}
		seen[alias] = alias != ""
		bindings = append(bindings, importBinding{ref: m[1], alias: m[2], explicit: m[2] != "", line: s.Line, origin: "import", module: mod})
		refs[m[1]] = ModuleEdge{Key: mod.Key, Alias: alias}
	}
	return bindings, refs, ok
}

// bindingAliases derives the alias map callers store for scope resolution.
func bindingAliases(bindings []importBinding) map[string]*Module {
	aliases := map[string]*Module{}
	for _, b := range bindings {
		alias := b.alias
		if alias == "" {
			alias = b.module.Name
		}
		if alias != "" {
			aliases[alias] = b.module
		}
	}
	return aliases
}

// moduleFileDecls is one file's parsed contribution to a package.
type moduleFileDecls struct {
	name    string
	source  string
	stmts   []*Statement
	aliases map[string]*Module
	vocab   *fileVocab
	refs    map[string]ModuleEdge
	// ds holds the file's unknown-construction diagnostics, dropped for
	// lines promoted to sentence calls by the file's vocabulary.
	ds []Diagnostic
}

// buildModule assembles one package from its parsed files: declarations are
// shared package-wide, imports stay file-local, and executable top-level
// statements are refused (libraries never run effects on import). Word
// declarations add package vocabulary: word W of A makes W a callable synonym
// for exported action A, with deterministic duplicate checks.
func buildModule(key string, files []*moduleFileDecls) (*Module, []Diagnostic) {
	m := &Module{
		Key:            key,
		Actions:        map[string]*Statement{},
		Schemas:        map[string]*Statement{},
		Definitions:    map[string]*RecordDef{},
		Failures:       map[string]*FailureDef{},
		fileImports:    map[string]map[string]*Module{},
		fileVocabs:     map[string]*fileVocab{},
		failureImports: map[string]map[string]*Module{},
		Exports:        map[string]bool{},
		Words:          map[string]string{},
	}
	var ds []Diagnostic
	for _, f := range files {
		for _, s := range f.stmts {
			switch s.Kind {
			case "package":
				name := match("package", s.Text)[1]
				if m.Name != "" && m.Name != name {
					ds = append(ds, Diagnostic{s.Line, 1, fmt.Sprintf("package name %q conflicts with %q in one package", name, m.Name)})
					continue
				}
				m.Name = name
			case "to":
				name := match("to", s.Text)[1]
				if m.Actions[name] != nil {
					ds = append(ds, Diagnostic{s.Line, 1, "duplicate action " + name + " in package"})
					continue
				}
				m.Actions[name] = s
				m.fileImports[name] = f.aliases
				m.fileVocabs[name] = f.vocab
			case "schema":
				name := match("schema", s.Text)[1]
				if m.Schemas[name] != nil {
					ds = append(ds, Diagnostic{s.Line, 1, "duplicate schema " + name + " in package"})
					continue
				}
				m.Schemas[name] = s
			case "define":
				name := match("define", s.Text)[1]
				if m.Definitions[name] != nil {
					ds = append(ds, Diagnostic{s.Line, 1, "duplicate definition " + name + " in package"})
					continue
				}
				definition, definitionDiagnostics := parseRecordDefinition(s)
				for _, diagnostic := range definitionDiagnostics {
					ds = append(ds, diagnostic)
				}
				m.Definitions[name] = definition
			case "failure":
				name := match("failure", s.Text)[1]
				if m.Failures[name] != nil {
					ds = append(ds, Diagnostic{s.Line, 1, "duplicate failure " + name + " in package"})
					continue
				}
				definition, definitionDiagnostics := parseFailureDefinition(s)
				ds = append(ds, definitionDiagnostics...)
				m.Failures[name] = definition
				m.failureImports[name] = f.aliases
			case "import", "export", "word":
			default:
				ds = append(ds, Diagnostic{s.Line, 1, "module top level allows only package, import, export, word, and declarations"})
			}
		}
	}
	for _, problem := range validateRecordDefinitions(m.Definitions) {
		ds = append(ds, Diagnostic{1, 1, problem})
	}
	for _, failure := range m.Failures {
		definitionScope, typeProblems := visibleDefinitions(m.Definitions, m.failureImports[failure.Name])
		for _, problem := range typeProblems {
			ds = append(ds, Diagnostic{failure.Line, 1, problem})
		}
		for _, field := range failure.Fields {
			if err := validateTypeRefs(field.Type, definitionScope, map[string]bool{}); err != nil {
				ds = append(ds, Diagnostic{field.Line, 1, fmt.Sprintf("%s.%s: %v", failure.Name, field.Name, err)})
			}
		}
	}
	for _, name := range sortedActionNames(m) {
		definitionScope, typeProblems := visibleDefinitions(m.Definitions, m.scope(name))
		for _, problem := range typeProblems {
			ds = append(ds, Diagnostic{m.Actions[name].Line, 1, problem})
		}
		if decl, err := parseActionDecl(m.Actions[name].Text); err != nil {
			ds = append(ds, Diagnostic{m.Actions[name].Line, 1, err.Error()})
		} else {
			for _, param := range decl.Params {
				if err := validateTypeRefs(param.Type, definitionScope, map[string]bool{}); err != nil && param.Type.Name != "any" {
					ds = append(ds, Diagnostic{m.Actions[name].Line, 1, "parameter " + param.Name + ": " + err.Error()})
				}
			}
			if decl.HasResult {
				if err := validateTypeRefs(decl.Result, definitionScope, map[string]bool{}); err != nil {
					ds = append(ds, Diagnostic{m.Actions[name].Line, 1, "return type: " + err.Error()})
				}
			}
		}
		moduleStatements := make([]*Statement, 0, len(m.Actions))
		for _, actionName := range sortedActionNames(m) {
			moduleStatements = append(moduleStatements, m.Actions[actionName])
		}
		moduleProgram := &Program{
			Statements:  moduleStatements,
			Definitions: m.Definitions,
			Failures:    m.Failures,
			Modules:     &ModuleTable{Aliases: m.scope(name), vocab: m.vocabulary(name)},
		}
		ds = append(ds, checkActionContracts(moduleProgram, map[string]*Statement{name: m.Actions[name]}, m.Definitions)...)
	}
	seen := map[string]bool{}
	for _, f := range files {
		for _, s := range f.stmts {
			if s.Kind != "export" {
				continue
			}
			name := match("export", s.Text)[1]
			if m.Actions[name] == nil && m.Schemas[name] == nil && m.Definitions[name] == nil && m.Failures[name] == nil {
				ds = append(ds, Diagnostic{s.Line, 1, "export " + name + " has no matching declaration"})
				continue
			}
			if seen[name] {
				ds = append(ds, Diagnostic{s.Line, 1, "duplicate export " + name})
				continue
			}
			seen[name] = true
			m.Exports[name] = true
		}
	}
	for name := range m.Exports {
		if m.Actions[name] != nil {
			m.Words[name] = name
			decl, _ := parseActionDecl(m.Actions[name].Text)
			possible := append([]string(nil), decl.Failures...)
			possible = append(possible, modulePossibleFailures(m, name, map[string]bool{})...)
			for _, failure := range uniqueSorted(possible) {
				if m.Failures[failure] == nil || !m.Exports[failure] {
					ds = append(ds, Diagnostic{m.Actions[name].Line, 1, fmt.Sprintf("exported action %s exposes failure %s, which must be defined and exported by package %s", name, failure, m.Name)})
				}
			}
		}
	}
	for _, f := range files {
		for _, s := range f.stmts {
			if s.Kind != "word" {
				continue
			}
			wm := match("word", s.Text)
			word, target := wm[1], wm[2]
			if m.Actions[target] == nil {
				ds = append(ds, Diagnostic{s.Line, 1, "word " + word + " names " + target + ", which is not an action in this package"})
				continue
			}
			if !m.Exports[target] {
				ds = append(ds, Diagnostic{s.Line, 1, "word " + word + " requires " + target + " to be exported"})
				continue
			}
			if prev, dup := m.Words[word]; dup && prev != target {
				ds = append(ds, Diagnostic{s.Line, 1, "duplicate word " + word + " in package"})
				continue
			}
			m.Words[word] = target
		}
	}
	if m.Name == "" {
		m.Name = defaultModuleName(key)
	}
	for _, f := range files {
		m.Files = append(m.Files, ModuleFile{Name: f.name, Source: f.source, Aliases: f.aliases, refs: f.refs})
	}
	return m, ds
}

func defaultModuleName(key string) string {
	base := filepath.Base(key)
	base = strings.TrimSuffix(base, ".sos")
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "module"
	}
	return base
}

// packageFiles resolves one reference to package files: REF.sos, the REF
// directory's .sos files, or REF/<basename>.sos.
func packageFiles(dir, ref string) ([]string, error) {
	rel := filepath.FromSlash(ref)
	if strings.HasSuffix(rel, ".sos") {
		p := filepath.Join(dir, rel)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return []string{p}, nil
		}
		return nil, fmt.Errorf("no module file at %s", p)
	}
	first := filepath.Join(dir, rel+".sos")
	if info, err := os.Stat(first); err == nil && !info.IsDir() {
		return []string{first}, nil
	}
	direct := filepath.Join(dir, rel)
	if entries, err := os.ReadDir(direct); err == nil {
		var files []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sos") {
				files = append(files, filepath.Join(direct, e.Name()))
			}
		}
		if len(files) > 0 {
			sort.Strings(files)
			return files, nil
		}
	}
	second := filepath.Join(dir, rel, filepath.Base(rel)+".sos")
	if info, err := os.Stat(second); err == nil && !info.IsDir() {
		return []string{second}, nil
	}
	return nil, fmt.Errorf("no module package at %s, %s, or %s", first, direct, second)
}

// fsLoader resolves imports against the filesystem and the project sos.toml.
type fsLoader struct {
	cache   map[string]*Module
	loading map[string]bool
	order   []string
	root    string // absolute entry-file directory
	tomlDir string // absolute directory of the project sos.toml
	ns      string // raw [module] path value
	layers  []sosconfig.Layer
	diags   []Diagnostic
	total   int
}

func (l *fsLoader) diag(line int, format string, args ...any) {
	l.diags = append(l.diags, Diagnostic{Line: line, Column: 1, Message: fmt.Sprintf(format, args...)})
}

func (l *fsLoader) modDiag(disp string, d Diagnostic) {
	l.diags = append(l.diags, Diagnostic{Line: d.Line, Column: d.Column, Message: "module " + disp + ": " + d.Message})
}

func (l *fsLoader) display(path string) string {
	if rel, err := filepath.Rel(l.root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// findModuleBase reads [module] path from the nearest project sos.toml and
// keeps the loaded layers for library resolution. The module path is a
// logical identity, not policy: it never touches budgets.
func (l *fsLoader) findModuleBase() {
	layers, err := sosconfig.Load(l.root)
	if err != nil {
		l.diag(1, "configuration: %v", err)
		return
	}
	l.layers = layers
	for _, layer := range layers {
		if layer.Config.Module.Path == "" || filepath.Base(layer.Name) != "sos.toml" {
			continue
		}
		l.ns = layer.Config.Module.Path
		l.tomlDir = filepath.Dir(layer.Name)
	}
}

// configBindings resolves the configured [[language.libraries]] list for the
// entry file. The list is the deterministic winner of the layer resolution:
// a project list replaces the global one, and an explicit empty list wins as
// "no libraries". Failures are diagnostics, never silent skips.
func (l *fsLoader) configBindings(root string) []importBinding {
	effective, err := sosconfig.Resolve(l.layers...)
	if err != nil {
		l.diag(1, "configuration: %v", err)
		return nil
	}
	origin := effective.Origins["libraries"]
	if origin == "" || origin == "default" || len(effective.Libraries) == 0 {
		return nil
	}
	var out []importBinding
	for _, lib := range effective.Libraries {
		mod, ok := l.resolve(lib.Path, root, 0, 1)
		if !ok {
			continue // resolve already reported the deterministic failure
		}
		out = append(out, importBinding{ref: lib.Path, alias: lib.As, explicit: lib.As != "", line: 1, origin: "config " + origin, module: mod})
	}
	return out
}

// relModuleRef maps one bare reference under the project module path. The
// path must match the logical module namespace; remote packages are unsupported.
func (l *fsLoader) relModuleRef(ref string) (base, rel string, ok bool) {
	if l.ns == "" || l.ns == ref {
		return "", "", false
	}
	if prefix := l.ns + "/"; strings.HasPrefix(ref, prefix) {
		return l.tomlDir, strings.TrimPrefix(ref, prefix), true
	}
	return "", "", false
}

func (l *fsLoader) resolve(ref, importerDir string, depth int, line int) (*Module, bool) {
	if strings.Contains(ref, "://") {
		l.diag(line, "network imports are not supported: %q", ref)
		return nil, false
	}
	if mod, ok := stdModule(ref); ok {
		return mod, true
	}
	if strings.HasPrefix(ref, "std/") {
		l.diag(line, "unknown standard package %q", ref)
		return nil, false
	}
	if filepath.IsAbs(ref) {
		l.diag(line, "absolute import paths are not supported: %q", ref)
		return nil, false
	}
	if depth >= maxImportDepth {
		l.diag(line, "import depth limit exceeded (%d)", maxImportDepth)
		return nil, false
	}
	base := importerDir
	rel := filepath.FromSlash(ref)
	if !strings.HasPrefix(ref, "./") && !strings.HasPrefix(ref, "../") {
		if l.ns == "" {
			l.diag(line, "import %q requires a [module] path in the project sos.toml or a relative ./ path", ref)
			return nil, false
		}
		var ok bool
		base, rel, ok = l.relModuleRef(ref)
		if !ok || rel == "" {
			l.diag(line, "import %q does not name a package under the module path %q", ref, l.ns)
			return nil, false
		}
	}
	files, err := packageFiles(base, filepath.ToSlash(rel))
	if err != nil {
		l.diag(line, "import %q: %v", ref, err)
		return nil, false
	}
	if len(files) > 256 {
		l.diag(line, "package exceeds the 256 source file limit")
		return nil, false
	}
	for i, path := range files {
		canonical, e := filepath.EvalSymlinks(path)
		if e != nil {
			l.diag(line, "import %q: %v", ref, e)
			return nil, false
		}
		files[i] = canonical
	}
	key := files[0]
	if len(files) > 1 {
		key = filepath.Dir(files[0])
	}
	if l.loading[key] {
		l.diag(line, "import cycle through %q", l.display(key))
		return nil, false
	}
	if m, ok := l.cache[key]; ok {
		return m, true
	}
	if len(l.cache)+len(l.loading) >= maxModules {
		l.diag(line, "module limit exceeded (%d)", maxModules)
		return nil, false
	}
	decls := make([]*moduleFileDecls, 0, len(files))
	for _, path := range files {
		data, e := readPackageSource(path)
		if e != nil {
			l.diag(line, "import %q: %v", ref, e)
			return nil, false
		}
		if len(data) > 1<<20 {
			l.diag(line, "module %s exceeds the 1 MiB limit", l.display(path))
			return nil, false
		}
		l.total += len(data)
		if l.total > maxModuleBytes {
			l.diag(line, "module sources exceed the %d MiB limit", maxModuleBytes>>20)
			return nil, false
		}
		src := string(data)
		mp, ds := Parse(src)
		for _, d := range ds {
			if !strings.HasPrefix(d.Message, "unknown construction:") {
				l.modDiag(l.display(path), d)
				return nil, false
			}
		}
		decls = append(decls, &moduleFileDecls{name: filepath.Base(path), source: src, stmts: mp.Statements, ds: ds})
	}
	pkgDir := filepath.Dir(files[0])
	l.loading[key] = true
	disp := l.display(key)
	pkgActions := map[string]bool{}
	for _, f := range decls {
		for _, s := range f.stmts {
			if s.Kind == "to" {
				pkgActions[match("to", s.Text)[1]] = true
			}
		}
	}
	for _, f := range decls {
		bindings, refs, _ := bindImports(f.stmts,
			func(r string, ln int) (*Module, bool) { return l.resolve(r, pkgDir, depth+1, ln) },
			func(ln int, format string, args ...any) {
				l.diags = append(l.diags, Diagnostic{Line: ln, Column: 1, Message: "module " + disp + ": " + fmt.Sprintf(format, args...)})
			})
		f.aliases, f.refs = bindingAliases(bindings), refs
		f.vocab = buildFileVocab(bindings, pkgActions, func(ln int, format string, args ...any) {
			l.diags = append(l.diags, Diagnostic{Line: ln, Column: 1, Message: "module " + disp + ": " + fmt.Sprintf(format, args...)})
		})
		fixed := map[int]string{}
		reclassifySent(f.stmts, f.vocab, fixed)
		f.ds = dropReclassifiedDiagnostics(f.ds, fixed)
	}
	delete(l.loading, key)
	failed := false
	for _, f := range decls {
		for _, d := range f.ds {
			l.modDiag(disp, d)
			failed = true
		}
	}
	if failed {
		return nil, false
	}
	mod, mds := buildModule(key, decls)
	if len(mds) > 0 {
		for _, d := range mds {
			l.modDiag(disp, d)
		}
		return nil, false
	}
	l.cache[key] = mod
	l.order = append(l.order, key)
	return mod, true
}

// graphLoader resolves imports from embedded edges.
type graphLoader struct {
	graph    *ModuleGraph
	specs    map[string]*ModuleSpec
	built    map[string]*Module
	building map[string]bool
	order    []string
	diags    []Diagnostic
}

func (g *graphLoader) diag(line int, format string, args ...any) {
	g.diags = append(g.diags, Diagnostic{Line: line, Column: 1, Message: fmt.Sprintf(format, args...)})
}

func (g *graphLoader) build(key string, line int) (*Module, bool) {
	if m, ok := g.built[key]; ok {
		return m, true
	}
	if g.building[key] {
		g.diag(line, "import cycle through %q", key)
		return nil, false
	}
	if mod, ok := stdModule(key); ok {
		return mod, true
	}
	spec, ok := g.specs[key]
	if !ok {
		g.diag(line, "module %q is missing from the embedded module graph", key)
		return nil, false
	}
	decls := make([]*moduleFileDecls, 0, len(spec.Files))
	for i := range spec.Files {
		f := &spec.Files[i]
		mp, ds := Parse(f.Source)
		for _, d := range ds {
			if !strings.HasPrefix(d.Message, "unknown construction:") {
				g.diag(line, "module %s: line %d: %s", spec.Name, d.Line, d.Message)
				return nil, false
			}
		}
		decls = append(decls, &moduleFileDecls{name: f.Name, source: f.Source, stmts: mp.Statements, ds: ds})
	}
	g.building[key] = true
	pkgActions := map[string]bool{}
	for _, f := range decls {
		for _, s := range f.stmts {
			if s.Kind == "to" {
				pkgActions[match("to", s.Text)[1]] = true
			}
		}
	}
	for idx := range decls {
		fs := &spec.Files[idx]
		f := decls[idx]
		bindings, refs, _ := bindImports(f.stmts,
			func(r string, ln int) (*Module, bool) {
				edge, found := fs.Imports[r]
				if !found {
					g.diag(ln, "module %s: import %q is missing from the embedded module graph", spec.Name, r)
					return nil, false
				}
				return g.build(edge.Key, ln)
			},
			func(ln int, format string, args ...any) {
				g.diags = append(g.diags, Diagnostic{Line: ln, Column: 1, Message: "module " + spec.Name + ": " + fmt.Sprintf(format, args...)})
			})
		f.aliases, f.refs = bindingAliases(bindings), refs
		f.vocab = buildFileVocab(bindings, pkgActions, func(ln int, format string, args ...any) {
			g.diags = append(g.diags, Diagnostic{Line: ln, Column: 1, Message: "module " + spec.Name + ": " + fmt.Sprintf(format, args...)})
		})
		fixed := map[int]string{}
		reclassifySent(f.stmts, f.vocab, fixed)
		f.ds = dropReclassifiedDiagnostics(f.ds, fixed)
	}
	delete(g.building, key)
	failed := false
	for _, f := range decls {
		for _, d := range f.ds {
			g.diag(line, "module %s: line %d: %s", spec.Name, d.Line, d.Message)
			failed = true
		}
	}
	if failed {
		return nil, false
	}
	mod, mds := buildModule(key, decls)
	if len(mds) > 0 {
		for _, d := range mds {
			g.diag(line, "module %s: line %d: %s", spec.Name, d.Line, d.Message)
		}
		return nil, false
	}
	g.built[key] = mod
	g.order = append(g.order, key)
	return mod, true
}

func readPackageSource(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("package source must be a regular file")
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("package source exceeds 1 MiB")
	}
	return io.ReadAll(io.LimitReader(f, (1<<20)+1))
}
