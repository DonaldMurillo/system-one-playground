package sos

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func requireCleanCheck(t *testing.T, source string) {
	t.Helper()
	if ds := Check(source); len(ds) != 0 {
		t.Fatalf("diagnostics = %+v", ds)
	}
}

func requireDiagnostic(t *testing.T, source, want string) Diagnostic {
	t.Helper()
	ds := Check(source)
	if len(ds) == 0 {
		t.Fatalf("missing diagnostic containing %q for:\n%s", want, source)
	}
	for _, d := range ds {
		if strings.Contains(d.Message, want) {
			return d
		}
	}
	t.Fatalf("no diagnostic containing %q in %+v for:\n%s", want, ds, source)
	return Diagnostic{}
}

// The canonical English constructions from docs/sysonescript-files-spec.md must
// parse and check cleanly, including their modifier continuation lines.
func TestFilespecCanonicalFormsCheckClean(t *testing.T) {
	sources := []string{
		"read file \"settings.toml\" as text called settings\n",
		"make report \"summary\"\nwrite report to file \"report.txt\"\n  only if it does not exist\n",
		"make report \"summary\"\nwrite report to file \"report.txt\"\n  replacing an existing file\n",
		"make report \"summary\"\nwrite report atomically to file \"report.txt\"\n  replacing an existing file\n",
		"make line \"an entry\"\nappend line to file \"activity.log\"\n",
		"check whether file \"settings.toml\" exists called configured\n",
		"check whether folder \"output\" exists called present\n",
		"check whether entry \"link\" exists called present\n",
		"inspect entry \"settings.toml\" called information\n",
		"inspect file \"settings.toml\" called information\n",
		"inspect folder \"src\" called information\n",
		"list entries in folder \"src\" called entries\n",
		"list files in folder \"src\" called files\n",
		"list folders in folder \"packages\" called packages\n",
		// The spec's materialized traversal with every modifier.
		"walk through folder \"src\" at most 10000 entries called entries\n" +
			"  including files and folders\n" +
			"  matching [\"*.go\", \"*.sos\"]\n" +
			"  excluding [\".git\", \"vendor\", \"generated\"]\n" +
			"  at most 8 folders deep\n" +
			"  without following symbolic links\n",
		"walk through folder \"src\" at most 100 entries called entries\n",
		"walk through folder \"src\" at most 100 entries called entries\n  following symbolic links\n",
		// Streaming traversal plus its consumer.
		"stream files under folder \"src\" called source_files\n" +
			"  matching [\"*.go\", \"*.sos\"]\n" +
			"  excluding [\".git\", \"vendor\"]\n" +
			"  at most 8 folders deep\n" +
			"  without following symbolic links\n" +
			"\n" +
			"for each file from source_files:\n" +
			"  show relative_path of file\n",
		"stream entries under folder \"src\" called everything\n\nfor each entry from everything:\n  show name of entry\n",
		// Watcher plus its consumer.
		"watch folder \"incoming\" recursively called changes\n" +
			"  matching [\"*.pdf\"]\n" +
			"  without following symbolic links\n" +
			"\n" +
			"for each change from changes:\n" +
			"  show path of change\n",
		"watch folder \"logs\" called lines\n\ncollect at most 4 items from lines called snapshot\n",
		"copy file \"report.txt\" to \"archive/report.txt\"\n  only if the destination does not exist\n",
		"copy folder \"site\" to \"archive/site\"\n  replacing an existing folder\n",
		"move file \"draft.txt\" to \"published/report.txt\"\n  replacing an existing file\n",
		"move folder \"draft\" to \"published\"\n  only if the destination does not exist\n",
		"create folder \"output\" if missing\n",
		"create folders through \"output/reports/2026\"\n",
		"remove file \"temporary.txt\"\n",
		"remove empty folder \"cache\"\n",
		"remove folder \"generated\" including its contents\n",
		// Typed sinks flow into typed actions.
		"to describe with entry as FileEntry returning text:\n  finish with name of entry\n" +
			"inspect entry \"settings.toml\" called information\n" +
			"call describe with information called label\n",
	}
	for _, source := range sources {
		requireCleanCheck(t, source)
	}
}

// Each canonical statement parses to its own kind, and its modifier lines
// classify as continuation kinds rather than unknown constructions.
func TestFilespecEnglishFormsParseKinds(t *testing.T) {
	source := "read file \"a\" as text called s\n" +
		"write w to file \"b\"\n  replacing an existing file\n" +
		"write w atomically to file \"b2\"\n  only if it does not exist\n" +
		"append a to file \"c\"\n" +
		"check whether file \"d\" exists called e\n" +
		"inspect entry \"f\" called g\n" +
		"list files in folder \"h\" called i\n" +
		"walk through folder \"j\" at most 10 entries called k\n  matching [\"*\"]\n" +
		"stream files under folder \"l\" called m\n" +
		"watch folder \"n\" recursively called o\n" +
		"copy file \"p\" to \"q\"\n  only if the destination does not exist\n" +
		"move file \"r\" to \"s2\"\n  replacing an existing file\n" +
		"create folders through \"t\"\n" +
		"remove file \"u\"\n" +
		"remove empty folder \"v\"\n" +
		"remove folder \"w\" including its contents\n"
	program, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	want := map[string]string{
		"readFile":             "read file",
		"writeFile":            "write",
		"appendFile":           "append",
		"checkExists":          "check whether",
		"inspectEntry":         "inspect",
		"listEntries":          "list",
		"walkThrough":          "walk",
		"streamFiles":          "stream",
		"watchFolder":          "watch",
		"copyEntry":            "copy",
		"moveEntry":            "move",
		"createFoldersThrough": "create folders through",
		"removeFile":           "remove file",
		"removeEmptyFolder":    "remove empty folder",
		"removeFolder":         "remove folder",
	}
	seen := map[string]bool{}
	var walk func([]*Statement)
	walk = func(statements []*Statement) {
		for _, statement := range statements {
			if prefix, ok := want[statement.Kind]; ok {
				if !strings.HasPrefix(statement.Text, prefix) {
					t.Errorf("kind %s matched %q", statement.Kind, statement.Text)
				}
				seen[statement.Kind] = true
			}
			switch statement.Kind {
			case "writeFile", "copyEntry", "moveEntry":
				if len(statement.Body) != 1 || statement.Body[0].Kind != "fsPolicy" {
					t.Errorf("%s continuation = %+v", statement.Kind, statement.Body)
				}
			case "walkThrough":
				if len(statement.Body) != 1 || statement.Body[0].Kind != "fsMatch" {
					t.Errorf("walk continuation = %+v", statement.Body)
				}
			}
			walk(statement.Body)
		}
	}
	walk(program.Statements)
	for kind := range want {
		if !seen[kind] {
			t.Errorf("kind %s never parsed", kind)
		}
	}
	// The atomic variant is distinguishable.
	atomic := false
	for _, statement := range program.Statements {
		if statement.Kind == "writeFile" && strings.HasPrefix(statement.Text, "write w atomically") {
			atomic = true
		}
	}
	if !atomic {
		t.Fatal("atomic write form missing")
	}
}

// Write, copy, and move never overwrite implicitly: the policy line is
// required and cannot be duplicated.
func TestFilespecPolicyIsRequiredAndUnique(t *testing.T) {
	requireDiagnostic(t, "write report to file \"report.txt\"\n", "requires an explicit policy")
	requireDiagnostic(t, "copy file \"a\" to \"b\"\n", "requires an explicit policy")
	requireDiagnostic(t, "move file \"a\" to \"b\"\n", "requires an explicit policy")
	requireDiagnostic(t,
		"write report to file \"report.txt\"\n  only if it does not exist\n  replacing an existing file\n",
		"declared more than once")
}

// `remove entry` is not canonical and a folder removal must say what it does.
func TestFilespecRemoveSurfaceIsExplicit(t *testing.T) {
	_, diagnostics := Parse("remove entry \"generated\"\n")
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "unknown construction") {
		t.Fatalf("remove entry diagnostics = %+v", diagnostics)
	}
	_, diagnostics = Parse("remove folder \"generated\"\n")
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "unknown construction") {
		t.Fatalf("unqualified remove folder diagnostics = %+v", diagnostics)
	}
}

// Materialized traversal bounds and depth limits must be positive.
func TestFilespecTraversalBoundsAreValidated(t *testing.T) {
	requireDiagnostic(t, "walk through folder \"src\" at most 0 entries called entries\n", "positive")
	requireDiagnostic(t,
		"walk through folder \"src\" at most 10 entries called entries\n  at most 0 folders deep\n",
		"positive")
	requireDiagnostic(t,
		"walk through folder \"src\" at most 10 entries called entries\n  at most 3 folders deep\n  at most 4 folders deep\n",
		"declared more than once")
}

// Reserved runtime failures keep the exact payload shapes the spec requires.
func TestFilespecReservedFailuresHaveSpecShapes(t *testing.T) {
	shapes := map[string][]RecordField{
		"FileNotFound":               {{Name: "path", Type: TypeRef{Name: "text"}}},
		"FileAlreadyExists":          {{Name: "path", Type: TypeRef{Name: "text"}}},
		"FilePermissionDenied":       {{Name: "path", Type: TypeRef{Name: "text"}}, {Name: "operation", Type: TypeRef{Name: "text"}}},
		"FileTooLarge":               {{Name: "path", Type: TypeRef{Name: "text"}}, {Name: "limit", Type: TypeRef{Name: "integer"}}, {Name: "observed", Type: TypeRef{Name: "integer", Optional: true}}},
		"InvalidFileType":            {{Name: "path", Type: TypeRef{Name: "text"}}, {Name: "expected", Type: TypeRef{Name: "text"}}, {Name: "actual", Type: TypeRef{Name: "text"}}},
		"InvalidFilePath":            {{Name: "path", Type: TypeRef{Name: "text"}}, {Name: "reason", Type: TypeRef{Name: "text"}}},
		"FileTraversalLimitExceeded": {{Name: "root", Type: TypeRef{Name: "text"}}, {Name: "limit", Type: TypeRef{Name: "integer"}}},
		"FileWatchOverflow":          {{Name: "root", Type: TypeRef{Name: "text"}}},
		"FileSystemUnavailable":      {{Name: "path", Type: TypeRef{Name: "text"}}, {Name: "operation", Type: TypeRef{Name: "text"}}},
	}
	builtins := builtInFailures()
	for name, fields := range shapes {
		definition, ok := builtins[name]
		if !ok {
			t.Errorf("failure %s is not reserved by the runtime", name)
			continue
		}
		if len(definition.Fields) != len(fields) {
			t.Errorf("%s fields = %+v", name, definition.Fields)
			continue
		}
		for i, field := range fields {
			got := definition.Fields[i]
			if got.Name != field.Name || got.Type.Name != field.Type.Name || got.Type.Optional != field.Type.Optional {
				t.Errorf("%s field %d = %+v, want %+v", name, i, got, field)
			}
		}
	}
}

// FileEntry and FileChange are built-in closed records: usable as types
// everywhere, exported by std/files, and reserved against redefinition.
func TestFilespecBuiltInFileRecords(t *testing.T) {
	requireDiagnostic(t, "define FileEntry:\n  path as text\n", "reserved")
	requireDiagnostic(t, "define FileChange:\n  path as text\n", "reserved")
	requireCleanCheck(t, "to shape with change as FileChange returning text:\n  finish with kind of change\n")

	module, ok := stdModule("std/files")
	if !ok {
		t.Fatal("std/files module missing")
	}
	entry, change := module.Definitions["FileEntry"], module.Definitions["FileChange"]
	if entry == nil || change == nil {
		t.Fatalf("module definitions = %#v", module.Definitions)
	}
	if !module.Exports["FileEntry"] || !module.Exports["FileChange"] {
		t.Fatal("FileEntry/FileChange are not exported")
	}
	defs := map[string]*RecordDef{"FileEntry": entry, "FileChange": change}
	record := map[string]any{
		"path": "a/b.txt", "relative_path": "b.txt", "name": "b.txt", "kind": "file",
		"size": 3.0, "modified_at": "2026-01-02T03:04:05Z", "depth": 1.0, "symbolic_link": false,
	}
	if !typeMatchesRef(record, TypeRef{Name: "FileEntry"}, defs) {
		t.Fatal("complete entry value does not match FileEntry")
	}
	delete(record, "size")
	delete(record, "modified_at")
	if !typeMatchesRef(record, TypeRef{Name: "FileEntry"}, defs) {
		t.Fatal("optional entry fields must stay optional")
	}
	record["surprise"] = true
	if typeMatchesRef(record, TypeRef{Name: "FileEntry"}, defs) {
		t.Fatal("FileEntry must stay a closed record")
	}
}

// File statements expose their typed failures: enclosing actions must declare
// or handle them, and impossible handlers are rejected.
func TestFilespecTypedFailureExposure(t *testing.T) {
	requireDiagnostic(t,
		"to load returning text:\n  read file \"a.txt\" as text called contents\n  finish with contents\n",
		"may pass FileNotFound on")
	requireCleanCheck(t,
		"to load returning text:\n  read file \"a.txt\" as text called contents\n"+
			"    on failure:\n      recover with \"missing\"\n"+
			"  finish with contents\n")
	requireCleanCheck(t,
		"to load returning text may fail with FilePermissionDenied, FileTooLarge, InvalidFileType, InvalidFilePath, FileSystemUnavailable:\n  read file \"a.txt\" as text called contents\n"+
			"    on failure FileNotFound using path:\n      recover with \"missing:\" + path\n"+
			"  finish with contents\n")
	requireDiagnostic(t,
		"to load returning text:\n  read file \"a.txt\" as text called contents\n"+
			"    on failure FileWatchOverflow using root:\n      recover with \"rescan\"\n"+
			"  finish with contents\n",
		"is impossible for this operation")
	// The watcher form exposes the overflow failure; the reader does not.
	requireCleanCheck(t,
		"to observe may fail with FileNotFound, FilePermissionDenied, InvalidFileType, InvalidFilePath, FileWatchOverflow, FileSystemUnavailable:\n  watch folder \"in\" called changes\n\n  for each change from changes:\n    stop reading\n")
}

// Traversal and watcher handles are owned streams with the same
// consume-once, transfer, and scope-exit rules as opened module streams.
func TestFilespecTraversalAndWatcherOwnership(t *testing.T) {
	requireCleanCheck(t,
		"to scan:\n  stream files under folder \"src\" called source_files\n  for each file from source_files:\n    stop reading\n")
	requireDiagnostic(t,
		"to scan:\n  stream files under folder \"src\" called source_files\n"+
			"  for each file from source_files:\n    stop reading\n"+
			"  for each file from source_files:\n    stop reading\n",
		"was already consumed")
	requireDiagnostic(t,
		"to scan:\n  stream files under folder \"src\" called source_files\n  keep source_files where true\n",
		"requires a collection, but source_files is a stream")
	requireDiagnostic(t,
		"to scan:\n  stream files under folder \"src\" called source_files\n",
		"remains active")
	requireDiagnostic(t,
		"to scan:\n  stream files under folder \"src\" called source_files\n  make source_files \"copied\"\n",
		"cannot overwrite active stream source_files")
	requireCleanCheck(t,
		"to scan:\n  stream files under folder \"src\" called source_files\n  collect at most 10 items from source_files called entries\n")
	requireCleanCheck(t,
		"to scan:\n  watch folder \"in\" called changes\n  close stream changes\n")
	requireDiagnostic(t,
		"to scan:\n  watch folder \"in\" called changes\n  for each change from changes:\n    stop reading\n  close stream changes\n",
		"was already consumed")
}

// Canonical-only sources take the byte-preserving fast path: canonical output
// is the English source itself and no provider request is spent.
func TestFilespecCanonicalEnglishTakesFastPath(t *testing.T) {
	source := "read file \"settings.toml\" as text called settings\n" +
		"walk through folder \"src\" at most 100 entries called entries\n  excluding [\".git\"]\n" +
		"remove file \"temporary.txt\"\n"
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("semantic", "semantic")})
	if err != nil {
		t.Fatalf("analyze = %v (%+v)", err, out.Diagnostics)
	}
	if out.Canonical != source {
		t.Fatalf("canonical = %q", out.Canonical)
	}
	if out.Usage.TotalAdmitted != 0 {
		t.Fatalf("fast path spent provider requests: %+v", out.Usage)
	}
}

// Canonicalization prefers read_text and an explicit write policy over the
// compatibility aliases files.read and files.write.
func TestFilespecCompatibilityCallsCanonicalizeForward(t *testing.T) {
	dir := t.TempDir()
	source := "import \"std/files\" as files\n" +
		"call files.read with \"a.txt\" called contents\n" +
		"call files.write with \"a.txt\", \"two\" called rewritten\n"
	program, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	out, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("semantic", "semantic"), Modules: program.Modules})
	if err != nil {
		t.Fatalf("analyze = %v (%+v)", err, out.Diagnostics)
	}
	if !strings.Contains(out.Canonical, "call files.read_text with \"a.txt\" called contents") {
		t.Fatalf("read alias not canonicalized:\n%s", out.Canonical)
	}
	if !strings.Contains(out.Canonical, "call files.write_text with \"a.txt\", \"two\", \"replace\" called rewritten") {
		t.Fatalf("write alias not canonicalized:\n%s", out.Canonical)
	}
	if strings.Contains(out.Canonical, "files.read with") {
		t.Fatalf("legacy read call survived canonicalization:\n%s", out.Canonical)
	}
}

// The vocabulary catalog shows both layers for filesystem operations: the
// canonical English construction and the precise module call, with effect
// labels, possible typed failures, and target availability.
func TestFilespecVocabularyExposesBothLayers(t *testing.T) {
	catalog, diagnostics := Vocabulary("main.sos", "import \"std/files\" as files\n")
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	entries := map[string]VocabularyEntry{}
	for _, entry := range catalog.Entries {
		entries[entry.Name] = entry
	}
	read := entries["read_text"]
	if read.ID == "" {
		t.Fatal("files.read_text missing from catalog")
	}
	if !hasPattern(read.Patterns, "read file PATH as text called NAME") || !hasPattern(read.Patterns, "files.read_text with path") {
		t.Fatalf("read_text patterns = %v", read.Patterns)
	}
	if !containsExactly(read.Effects, "filesystem-read") {
		t.Fatalf("read_text effects = %v", read.Effects)
	}
	if !containsStringIn(read.Targets, "native") {
		t.Fatalf("read_text targets = %v", read.Targets)
	}
	if !containsStringIn(read.PossibleFailures, "FileNotFound") {
		t.Fatalf("read_text failures = %v", read.PossibleFailures)
	}
	written := entries["write_text"]
	if !hasPattern(written.Patterns, "write CONTENTS to file PATH") {
		t.Fatalf("write_text patterns = %v", written.Patterns)
	}
	if !paramNamed(written.Params, "policy") {
		t.Fatal("write_text must name its policy parameter")
	}
	streamed := entries["stream"]
	if streamed.ID == "" || !strings.HasPrefix(streamed.Result, "stream of FileEntry") {
		t.Fatalf("stream entry = %+v", streamed)
	}
	if !hasPattern(streamed.Patterns, "stream files under folder ROOT called NAME") {
		t.Fatalf("stream patterns = %v", streamed.Patterns)
	}
	walked := entries["walk"]
	if !containsStringIn(walked.PossibleFailures, "FileTraversalLimitExceeded") {
		t.Fatalf("walk failures = %v", walked.PossibleFailures)
	}
	if !hasPattern(walked.Patterns, "walk through folder ROOT at most LIMIT entries called NAME") {
		t.Fatalf("walk patterns = %v", walked.Patterns)
	}
	watched := entries["watch"]
	if watched.ID == "" || !strings.HasPrefix(watched.Result, "stream of FileChange") {
		t.Fatalf("watch entry = %+v", watched)
	}
	if !containsStringIn(watched.PossibleFailures, "FileWatchOverflow") {
		t.Fatalf("watch failures = %v", watched.PossibleFailures)
	}
	if !containsStringIn(watched.Targets, "native") || containsStringIn(watched.Targets, "browser") {
		t.Fatalf("watch targets = %v", watched.Targets)
	}
	removed := entries["remove_folder_recursively"]
	if !containsStringIn(removed.Effects, "filesystem-write") || !containsStringIn(removed.Effects, "destructive") {
		t.Fatalf("remove_folder_recursively effects = %v", removed.Effects)
	}
}

func hasPattern(patterns []string, want string) bool {
	return containsStringIn(patterns, want)
}

func containsStringIn(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsExactly(values []string, want string) bool {
	return len(values) == 1 && values[0] == want
}

func paramNamed(params []VocabularyParam, name string) bool {
	for _, param := range params {
		if param.Name == name {
			return true
		}
	}
	return false
}

// Actions using the English forms carry read/write/destructive effect labels.
func TestFilespecActionEffectsLabelFileForms(t *testing.T) {
	dir := writeProject(t, "", map[string]string{
		"modules/svc/main.sos": "package svc\nexport scan\n" +
			"to scan:\n" +
			"  read file \"a.txt\" as text called contents\n" +
			"  write contents to file \"b.txt\"\n    replacing an existing file\n" +
			"  remove folder \"c\" including its contents\n" +
			"  finish\n",
	})
	source := "import \"./modules/svc\" as svc\ncall svc.scan\n"
	program, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	var module *Module
	for _, candidate := range program.Modules.Aliases {
		if candidate.Name == "svc" {
			module = candidate
		}
	}
	if module == nil {
		t.Fatal("svc module missing")
	}
	effects := actionEffects(module, "scan", map[string]bool{})
	sort.Strings(effects)
	want := []string{"destructive", "filesystem-read", "filesystem-write"}
	if len(effects) != len(want) {
		t.Fatalf("effects = %v", effects)
	}
	for i := range want {
		if effects[i] != want[i] {
			t.Fatalf("effects = %v", effects)
		}
	}
}
