package soslsp

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/sos"

	toml "github.com/pelletier/go-toml/v2"
)

// stdAction is one standard library action offered for auto-import even when
// no workspace is available. Only these exist today; the core std registry
// should feed this catalog once it exposes metadata (see lsp-report.md).
type stdAction struct {
	ImportPath string // e.g. std/text
	Alias      string // suggested import alias
	Name       string // action name
	Detail     string
	Doc        string
}

var stdCatalog = func() []stdAction {
	var catalog []stdAction
	for _, op := range sos.StandardOperations() {
		params := []string{}
		for _, p := range op.Params {
			params = append(params, p.Name+" as "+p.Type)
		}
		catalog = append(catalog, stdAction{ImportPath: op.ImportPath, Alias: op.Package, Name: op.Name,
			Detail: fmt.Sprintf("%s · %s (%s) → %s", op.ImportPath, op.Name, strings.Join(params, ", "), op.Result),
			Doc:    op.Description + " Pure operation; no file, network, or Jev effects."})
	}
	return catalog
}()

// indexedAction is an exported action discovered in a local package.
type indexedAction struct {
	Name string
	Line int // zero based declaration line
	Doc  string
}

// indexedPackage is one directory of .sos files sharing declarations, per the
// directory-package model: the import ref is <module identity>/<rel dir>.
type indexedPackage struct {
	Name       string // declared `package NAME`, else directory base name
	ImportPath string // logical ref written in import edits
	Dir        string // absolute directory (server side only)
	Files      []string
	Actions    []indexedAction
}

// packageIndex is the bounded scan result for one workspace root. A nil or
// empty index still serves standard library auto-imports.
type packageIndex struct {
	root     string
	identity string // [module] path logical identity, "" when absent
	moduleOK bool
	packages map[string]*indexedPackage // keyed by package name
}

var (
	rePackageDecl = regexp.MustCompile(`^\s*package\s+([A-Za-z_]\w*)\s*$`)
	reExportDecl  = regexp.MustCompile(`^\s*export\s+([A-Za-z_]\w*)\s*$`)
	reImportStmt  = regexp.MustCompile(`(?m)^\s*import\s+"([^"]*)"(?:\s+as\s+([A-Za-z_]\w*))?\s*$`)
	reCallUse     = regexp.MustCompile(`^\s*call\s+([A-Za-z_]\w*)\.([A-Za-z_]\w*)`)
	reFrontmatter = regexp.MustCompile(`(?s)^\+\+\+\n.*?\n\+\+\+\n`)
)

// Bounds for the workspace scan: an editor index must never turn into an
// unbounded filesystem walk.
const (
	indexMaxFiles = 500
	indexMaxDepth = 4
	indexMaxBytes = 1 << 20
)

// indexWorkspace scans the directory packages of the sos.toml at root. The
// [module] path is the module's logical identity, not a search directory:
// import "<identity>/<dir>" resolves to <root>/<dir>. Errors are swallowed
// deliberately: a missing or malformed module leaves the index empty and
// standard library imports still work.
func indexWorkspace(root string) *packageIndex {
	idx := &packageIndex{root: root, packages: map[string]*indexedPackage{}}
	if root == "" {
		return idx
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return idx
	}
	identity, ok := moduleIdentity(root)
	if !ok {
		return idx
	}
	idx.identity = identity
	idx.moduleOK = true
	files, totalBytes, visited := 0, 0, 0
	// Walk errors are tolerated: an editor index must survive stale or
	// unreadable workspace state without crashing.
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil {
			return nil
		}
		visited++
		if visited > 4096 || files >= indexMaxFiles || totalBytes >= 4<<20 {
			return filepath.SkipAll
		}
		if !d.IsDir() {
			return nil
		}
		if path == root {
			return depthOK(root, path)
		}
		if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "vendor" || d.Name() == "build" || d.Name() == "webdist" {
			return filepath.SkipDir
		}
		if err := depthOK(root, path); err != nil {
			return err
		}
		if pkg := indexPackageDir(root, path, identity, &files, &totalBytes); pkg != nil {
			if _, exists := idx.packages[pkg.Name]; !exists {
				idx.packages[pkg.Name] = pkg
			}
		}
		return nil
	})
	return idx
}

// depthOK bounds the walk depth; returning an error skips deeper directories.
func depthOK(base, path string) error {
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.Count(rel, string(filepath.Separator)) >= indexMaxDepth {
		return filepath.SkipDir
	}
	return nil
}

// indexPackageDir reads every .sos file of one directory and merges their
// declarations into a single package (directory packages share declarations).
func indexPackageDir(root, dir, identity string, files, totalBytes *int) *indexedPackage {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	pkg := &indexedPackage{Dir: dir}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return nil
	}
	pkg.ImportPath = filepath.ToSlash(rel)
	if identity != "" {
		pkg.ImportPath = identity + "/" + pkg.ImportPath
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 || e.IsDir() || !strings.HasSuffix(e.Name(), ".sos") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !withinDir(root, path) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > indexMaxBytes || int(info.Size())+*totalBytes > 4<<20 {
			continue
		}
		if *files >= indexMaxFiles {
			break
		}
		(*files)++
		*totalBytes += int(info.Size())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // tolerate unreadable or stale files
		}
		pkg.Files = append(pkg.Files, path)
		var doc string
		for i, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(stripLineComment(raw))
			if m := rePackageDecl.FindStringSubmatch(line); m != nil {
				if pkg.Name == "" {
					pkg.Name = m[1]
				}
				doc = ""
				continue
			}
			if m := reExportDecl.FindStringSubmatch(line); m != nil {
				pkg.Actions = append(pkg.Actions, indexedAction{Name: m[1], Line: i, Doc: doc})
				doc = ""
				continue
			}
			// A plain non-empty line directly above an export is treated as
			// its documentation (packages use describe-style prose).
			doc = strings.Trim(line, `"`)
		}
	}
	if pkg.Name == "" {
		pkg.Name = filepath.Base(dir)
	}
	if len(pkg.Actions) == 0 || len(pkg.Files) == 0 {
		return nil
	}
	return pkg
}

// moduleIdentity reads the [module] path logical identity from sos.toml.
func moduleIdentity(root string) (string, bool) {
	configPath := filepath.Join(root, "sos.toml")
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", false
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", false
	}
	var cfg struct {
		Module struct {
			Path string `toml:"path"`
		} `toml:"module"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	if strings.TrimSpace(cfg.Module.Path) == "" {
		return "", false
	}
	return cfg.Module.Path, true
}

// withinDir reports whether path is base or lives underneath it.
func withinDir(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// stdActionsForAlias returns the catalog entries of one std namespace.
func stdActionsForAlias(alias string) []stdAction {
	var out []stdAction
	for _, a := range stdCatalog {
		if a.Alias == alias {
			out = append(out, a)
		}
	}
	return out
}

// stdImportForAlias maps an import path or alias to its catalog twin.
func stdImportForAlias(aliasOrPath string) (stdAction, bool) {
	for _, a := range stdCatalog {
		if a.Alias == aliasOrPath || a.ImportPath == aliasOrPath {
			return a, true
		}
	}
	return stdAction{}, false
}

// module's package name, approximated by the ref's last path segment.
func defaultAlias(importPath string) string {
	if i := strings.LastIndex(importPath, "/"); i >= 0 {
		return importPath[i+1:]
	}
	return importPath
}

// importEdit computes the insertion of an import declaration. Insertion
// preserves TOML frontmatter and a leading `package NAME` line, uses the
// document's line terminator, and terminates a final unterminated line.
func importEdit(source, importPath, alias string) map[string]any {
	newline := "\n"
	if strings.Contains(source, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(source, "\n")
	insertLine := 0
	if strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")) == "+++" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "+++" {
				insertLine = i + 1
				break
			}
		}
	}
	for i := insertLine; i < len(lines); i++ {
		code := strings.TrimSpace(stripLineComment(lines[i]))
		if code == "" {
			continue
		}
		if rePackageDecl.MatchString(code) || reImportStmt.MatchString(code) {
			insertLine = i + 1
			continue
		}
		break
	}
	var text string
	if alias == "" {
		// Open import: bare vocabulary plus the default qualifier.
		text = "import \"" + importPath + "\"" + newline
	} else {
		text = "import \"" + importPath + "\" as " + alias + newline
	}
	char := 0
	if insertLine >= len(lines) {
		insertLine = len(lines) - 1
		char = utf16Len(strings.TrimRight(lines[insertLine], "\r"))
		text = newline + text
	}
	return map[string]any{"range": lspRange{Start: lspPosition{Line: insertLine, Character: char}, End: lspPosition{Line: insertLine, Character: char}}, "newText": text}
}

// importAlias reuses an existing binding or chooses a name that cannot shadow
// another package or a declared value in this document.
func importAlias(source, path, preferred string) string {
	used := map[string]bool{}
	for _, raw := range strings.Split(source, "\n") {
		m := reImportStmt.FindStringSubmatch(strings.TrimSpace(stripLineComment(raw)))
		if m == nil {
			continue
		}
		alias := m[2]
		if alias == "" {
			alias = defaultAlias(m[1])
			if m[1] == path {
				alias = preferred
			}
		}
		if m[1] == path {
			return alias
		}
		used[alias] = true
	}
	for _, b := range scopeBindings(source) {
		used[b.name] = true
	}
	alias := preferred
	for n := 2; used[alias]; n++ {
		alias = fmt.Sprintf("%s%d", preferred, n)
	}
	return alias
}

// hasImport reports whether source already binds the alias: either an
// explicit `as alias` or a default-alias import of any path.
func hasImport(source, alias string) bool {
	for _, m := range reImportStmt.FindAllStringSubmatch(source, -1) {
		if m[2] == alias || (m[2] == "" && defaultAlias(m[1]) == alias) {
			return true
		}
	}
	return false
}

// hasImportPath reports whether source already imports the exact path under
// any alias.
func hasImportPath(source, importPath string) bool {
	for _, m := range reImportStmt.FindAllStringSubmatch(source, -1) {
		if m[1] == importPath {
			return true
		}
	}
	return false
}

// qualifiedCall is one `call alias.action` occurrence.
type qualifiedCall struct {
	Line   int // zero based
	Alias  string
	Action string
}

func qualifiedCalls(source string) []qualifiedCall {
	var out []qualifiedCall
	for i, line := range strings.Split(source, "\n") {
		if m := reCallUse.FindStringSubmatch(stripLineComment(line)); m != nil {
			out = append(out, qualifiedCall{Line: i, Alias: m[1], Action: m[2]})
		}
	}
	return out
}

// sortedPackageNames is a deterministic iteration order for menus and tests.
func (idx *packageIndex) sortedPackageNames() []string {
	if idx == nil {
		return nil
	}
	names := make([]string, 0, len(idx.packages))
	for name := range idx.packages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
