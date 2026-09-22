package sos

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The filesystem language layer: built-in file record types, the canonical
// English constructions' shared metadata, the std/files module contract used
// as the precise fallback and adapter surface, and the deterministic
// canonicalization of the compatibility calls files.read/files.write.
//
// The module actions declared here are the language-side contract. Runtime
// implementations register the same names with real host operations and
// supersede these declarations wholesale; until then the boundary fails with
// the reserved FileSystemUnavailable failure instead of panicking or silently
// succeeding. This file deliberately sorts after io_stdlib.go and the
// filesystem runtime files so gap-filling registrations see them first.

// ---- built-in file record types ----

// builtInFileDefinitions returns the built-in file record types. The shapes
// are owned by the std/files runtime contract (stdModuleDefinitions) so the
// module export and every program's visible definitions agree by
// construction.
func builtInFileDefinitions() map[string]*RecordDef {
	return stdModuleDefinitions("std/files")
}

// ---- canonical English statement metadata ----

// fileFormAction maps the parser kinds of the canonical English filesystem
// constructions to the module action both layers resolve to. Failures and
// effects are shared with that action's contract: both forms resolve to the
// same checked runtime operations.
var fileFormAction = map[string]string{
	"readFile":             "read_text",
	"writeFile":            "write_text",
	"appendFile":           "append_text",
	"checkExists":          "exists",
	"inspectEntry":         "inspect",
	"listEntries":          "list",
	"walkThrough":          "walk",
	"streamFiles":          "stream",
	"watchFolder":          "watch",
	"copyEntry":            "copy_file",
	"moveEntry":            "move",
	"createFoldersThrough": "create_folders",
	"removeFile":           "remove_file",
	"removeEmptyFolder":    "remove_folder",
	"removeFolder":         "remove_folder_recursively",
}

var fileFormLabels = map[string]string{
	"readFile":             "read file",
	"writeFile":            "write",
	"appendFile":           "append",
	"checkExists":          "check whether",
	"inspectEntry":         "inspect",
	"listEntries":          "list",
	"walkThrough":          "walk through",
	"streamFiles":          "stream files",
	"watchFolder":          "watch folder",
	"copyEntry":            "copy",
	"moveEntry":            "move",
	"createFoldersThrough": "create folders",
	"removeFile":           "remove file",
	"removeEmptyFolder":    "remove empty folder",
	"removeFolder":         "remove folder",
}

func fileFormLabel(kind string) string {
	if label, ok := fileFormLabels[kind]; ok {
		return label
	}
	return kind
}

// fileFormFailures lists the typed failures one English construction may
// raise, so handler validation and enclosing-action contracts see the same
// exposure as the fallback module call.
func fileFormFailures(kind string) []string {
	if action, ok := fileFormAction[kind]; ok {
		if spec, registered := fileActionContract[action]; registered {
			return spec.failures
		}
	}
	return nil
}

// fileFormEffects labels each construction read, write, or destructive. These
// are the effect summaries tooling must surface; they never authorize access.
var fileFormEffects = map[string][]string{
	"readFile":             {"filesystem-read"},
	"checkExists":          {"filesystem-read"},
	"inspectEntry":         {"filesystem-read"},
	"listEntries":          {"filesystem-read"},
	"walkThrough":          {"filesystem-read"},
	"streamFiles":          {"filesystem-read"},
	"watchFolder":          {"filesystem-read"},
	"writeFile":            {"filesystem-write"},
	"appendFile":           {"filesystem-write"},
	"copyEntry":            {"filesystem-read", "filesystem-write"},
	"moveEntry":            {"filesystem-read", "filesystem-write"},
	"createFoldersThrough": {"filesystem-write"},
	"folder":               {"filesystem-write"},
	"removeFile":           {"filesystem-write", "destructive"},
	"removeEmptyFolder":    {"filesystem-write", "destructive"},
	"removeFolder":         {"filesystem-write", "destructive"},
}

// ---- modifier continuation lines ----

// Continuation kinds are classified only under their owning filesystem
// statement, like schema fields under expect headers; they are never
// standalone constructions.
var fileContinuationForms = []struct {
	kind, pattern string
}{
	{"fsPolicy", `^(?:only if (?:it|the destination) does not exist|replacing an existing (?:file|folder|entry))$`},
	{"fsInclude", `^including (?:files and folders|files|folders)$`},
	{"fsMatch", `^matching (.+)$`},
	{"fsExclude", `^excluding (.+)$`},
	{"fsDepth", `^at most (\d+) folders deep$`},
	{"fsLinks", `^(?:without )?following symbolic links$`},
}

var fileContinuationPatterns = func() map[string]*regexp.Regexp {
	out := map[string]*regexp.Regexp{}
	for _, form := range fileContinuationForms {
		out[form.kind] = regexp.MustCompile(form.pattern)
	}
	return out
}()

// classifyFileContinuation returns the continuation kind of one indented
// modifier line, or "" when the line is not a filesystem continuation.
func classifyFileContinuation(text string) string {
	for _, form := range fileContinuationForms {
		if fileContinuationPatterns[form.kind].MatchString(text) {
			return form.kind
		}
	}
	return ""
}

// fileContinuationChildren lists the modifier kinds one filesystem statement
// accepts. on-failure handlers remain allowed for every operation.
var fileContinuationChildren = map[string][]string{
	"writeFile": {"fsPolicy"},

	"copyEntry":   {"fsPolicy"},
	"moveEntry":   {"fsPolicy"},
	"walkThrough": {"fsInclude", "fsMatch", "fsExclude", "fsDepth", "fsLinks"},
	"streamFiles": {"fsMatch", "fsExclude", "fsDepth", "fsLinks"},
	"watchFolder": {"fsMatch", "fsLinks"},
}

func fileContinuationAllowed(parent, child string) bool {
	for _, allowed := range fileContinuationChildren[parent] {
		if allowed == child {
			return true
		}
	}
	return false
}

func init() {
	// Continuation kinds resolve through the shared matcher without becoming
	// standalone classifications: classifyLine iterates the forms table only.
	for _, form := range fileContinuationForms {
		patterns[form.kind] = fileContinuationPatterns[form.kind]
	}
}

// isFileForm reports whether a statement kind is one of the canonical
// filesystem constructions (including the pre-existing create folder form).
func isFileForm(kind string) bool {
	if _, ok := fileFormAction[kind]; ok {
		return true
	}
	return kind == "folder"
}

// isFileHandleForm reports whether the construction binds an owned traversal
// or watcher stream handle.
func isFileHandleForm(kind string) bool {
	return kind == "streamFiles" || kind == "watchFolder"
}

// requiresFilePolicy marks the constructions with no implicit overwrite: the
// create-or-replace policy line is mandatory.
func requiresFilePolicy(kind string) bool {
	return kind == "writeFile" || kind == "copyEntry" || kind == "moveEntry"
}

var fileModifierLabels = map[string]string{
	"fsPolicy":  "policy",
	"fsInclude": "include",
	"fsMatch":   "matching",
	"fsExclude": "excluding",
	"fsDepth":   "folder depth",
	"fsLinks":   "link following",
}

// checkFileFormModifiers validates the modifier continuation lines of one
// canonical filesystem statement: policies are required and unique, each
// modifier appears at most once, depth bounds are positive, and pattern
// lists are checked as expressions.
func checkFileFormModifiers(s *Statement, add func(*Statement, string), checkExpr func(*Statement, string, bool)) {
	seen := map[string]bool{}
	policies := 0
	for _, child := range s.Body {
		if child.Kind == "handler" {
			continue
		}
		if seen[child.Kind] {
			add(child, fileModifierLabels[child.Kind]+" is declared more than once")
			continue
		}
		seen[child.Kind] = true
		switch child.Kind {
		case "fsPolicy":
			policies++
		case "fsMatch", "fsExclude":
			if m := match(child.Kind, child.Text); len(m) > 1 {
				checkExpr(child, m[1], false)
			}
		case "fsDepth":
			if m := match("fsDepth", child.Text); len(m) > 1 {
				if limit, err := strconv.ParseFloat(m[1], 64); err == nil && limit <= 0 {
					add(child, "folder depth requires a positive bound")
				}
			}
		}
	}
	if requiresFilePolicy(s.Kind) && policies == 0 {
		add(s, fileFormLabel(s.Kind)+" requires an explicit policy: add \"only if it does not exist\" or \"replacing an existing file\"")
	}
}

// ---- std/files module contract ----

type fileActionSpec struct {
	params      []NativeParam
	result      string
	description string
	failures    []string
	effects     []string
	targets     []string
	english     []string
}

var fileIOFailureSet = []string{"FileNotFound", "FilePermissionDenied", "FileTooLarge", "InvalidFileType", "InvalidFilePath", "FileSystemUnavailable"}
var fileWriteFailureSet = []string{"FileAlreadyExists", "FilePermissionDenied", "FileTooLarge", "InvalidFileType", "InvalidFilePath", "FileSystemUnavailable"}
var fileStatFailureSet = []string{"FilePermissionDenied", "InvalidFilePath", "FileSystemUnavailable"}
var fileTraversalFailureSet = []string{"FileNotFound", "FilePermissionDenied", "InvalidFileType", "InvalidFilePath", "FileTraversalLimitExceeded", "FileSystemUnavailable"}
var fileStreamFailureSet = []string{"FileNotFound", "FilePermissionDenied", "InvalidFileType", "InvalidFilePath", "FileSystemUnavailable"}

// fileActionContract is the adapter contract for std/files: parameter names
// (never positional boolean hints), result type, effect labels, target
// availability, possible typed failures, and the canonical English
// constructions that resolve to the same runtime operation.
var fileActionContract = map[string]fileActionSpec{
	"read_text": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "text",
		description: "Reads the complete contents of one regular file as UTF-8 text; invalid UTF-8 and files over 16 MiB fail.",
		failures:    fileIOFailureSet,
		effects:     []string{"filesystem-read"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"read file PATH as text called NAME"},
	},
	"write_text": {
		params:      []NativeParam{{Name: "path", Type: "text"}, {Name: "contents", Type: "text"}, {Name: "policy", Type: "text"}},
		result:      "text",
		description: "Writes one regular file with an explicit create-or-replace policy; there is no implicit overwrite.",
		failures:    fileWriteFailureSet,
		effects:     []string{"filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"write CONTENTS to file PATH"},
	},
	"write_text_atomically": {
		params:      []NativeParam{{Name: "path", Type: "text"}, {Name: "contents", Type: "text"}, {Name: "policy", Type: "text"}},
		result:      "text",
		description: "Writes one regular file through a flushed temporary file and an atomic replace; a failure before replacement preserves the old file.",
		failures:    fileWriteFailureSet,
		effects:     []string{"filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"write CONTENTS atomically to file PATH"},
	},
	"append_text": {
		params:      []NativeParam{{Name: "path", Type: "text"}, {Name: "contents", Type: "text"}},
		result:      "text",
		description: "Adds UTF-8 text to one regular file, creating it when absent.",
		failures:    fileIOFailureSet,
		effects:     []string{"filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"append CONTENTS to file PATH"},
	},
	"exists": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "boolean",
		description: "Reports whether a path exists; permission, malformed-path, and I/O errors fail rather than returning false.",
		failures:    fileStatFailureSet,
		effects:     []string{"filesystem-read"},
		targets:     []string{"native", "wasip1"},
		english: []string{
			"check whether file PATH exists called NAME",
			"check whether folder PATH exists called NAME",
			"check whether entry PATH exists called NAME",
		},
	},
	"inspect": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "FileEntry",
		description: "Examines the path itself without following a final symbolic link and returns a typed FileEntry.",
		failures:    []string{"FileNotFound", "FilePermissionDenied", "InvalidFilePath", "FileSystemUnavailable"},
		effects:     []string{"filesystem-read"},
		targets:     []string{"native", "wasip1"},
		english: []string{
			"inspect entry PATH called NAME",
			"inspect file PATH called NAME",
			"inspect folder PATH called NAME",
		},
	},
	"list": {
		params:      []NativeParam{{Name: "path", Type: "text"}, {Name: "kind", Type: "text"}},
		result:      "list",
		description: "Lists the immediate children of one folder, sorted by portable relative path; results are bounded and fail instead of truncating.",
		failures:    []string{"FileNotFound", "FilePermissionDenied", "InvalidFilePath", "FileTraversalLimitExceeded", "FileSystemUnavailable"},
		effects:     []string{"filesystem-read"},
		targets:     []string{"native", "wasip1"},
		english: []string{
			"list entries in folder PATH called NAME",
			"list files in folder PATH called NAME",
			"list folders in folder PATH called NAME",
		},
	},
	"walk": {
		params:      []NativeParam{{Name: "root", Type: "text"}, {Name: "limit", Type: "integer"}, {Name: "include", Type: "list"}, {Name: "exclude", Type: "list"}, {Name: "kinds", Type: "list"}, {Name: "depth", Type: "integer"}, {Name: "follow", Type: "boolean"}},
		result:      "list",
		description: "Walks one folder recursively and returns a bounded list of FileEntry records in stable depth-first lexical order; exceeding the bound fails instead of truncating.",
		failures:    fileTraversalFailureSet,
		effects:     []string{"filesystem-read"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"walk through folder ROOT at most LIMIT entries called NAME"},
	},
	"stream": {
		params:      []NativeParam{{Name: "root", Type: "text"}, {Name: "include", Type: "list"}, {Name: "exclude", Type: "list"}, {Name: "kinds", Type: "list"}, {Name: "depth", Type: "integer"}, {Name: "follow", Type: "boolean"}},
		result:      "stream of FileEntry",
		description: "Streams FileEntry records under one folder incrementally with backpressure, cancellation, and the shared include/exclude/depth/link policy.",
		failures:    fileStreamFailureSet,
		effects:     []string{"filesystem-read"},
		targets:     []string{"native", "wasip1"},
		english: []string{
			"stream files under folder ROOT called NAME",
			"stream entries under folder ROOT called NAME",
			"stream folders under folder ROOT called NAME",
		},
	},
	"watch": {
		params:      []NativeParam{{Name: "root", Type: "text"}, {Name: "include", Type: "list"}, {Name: "exclude", Type: "list"}, {Name: "kinds", Type: "list"}, {Name: "depth", Type: "integer"}, {Name: "follow", Type: "boolean"}},
		result:      "stream of FileChange",
		description: "Watches one folder recursively and streams future FileChange records; an overflowed change means events were dropped and the consumer must rescan. Native only unless a host supplies an equivalent adapter.",
		failures:    []string{"FileNotFound", "FilePermissionDenied", "InvalidFileType", "InvalidFilePath", "FileWatchOverflow", "FileSystemUnavailable"},
		effects:     []string{"filesystem-read"},
		targets:     []string{"native"},
		english: []string{
			"watch folder ROOT recursively called NAME",
			"watch folder ROOT called NAME",
		},
	},
	"copy_file": {
		params:      []NativeParam{{Name: "source", Type: "text"}, {Name: "destination", Type: "text"}, {Name: "policy", Type: "text"}},
		result:      "text",
		description: "Duplicates one file while preserving the source, with an explicit create-or-replace destination policy.",
		failures:    []string{"FileNotFound", "FileAlreadyExists", "FilePermissionDenied", "FileTooLarge", "InvalidFileType", "InvalidFilePath", "FileSystemUnavailable"},
		effects:     []string{"filesystem-read", "filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"copy file SOURCE to DESTINATION"},
	},
	"copy_folder": {
		params:      []NativeParam{{Name: "source", Type: "text"}, {Name: "destination", Type: "text"}, {Name: "policy", Type: "text"}, {Name: "exclusions", Type: "list"}},
		result:      "text",
		description: "Duplicates one folder recursively under the same link, exclusion, cancellation, and entry-limit rules as traversal.",
		failures:    []string{"FileNotFound", "FileAlreadyExists", "FilePermissionDenied", "FileTooLarge", "InvalidFileType", "InvalidFilePath", "FileTraversalLimitExceeded", "FileSystemUnavailable"},
		effects:     []string{"filesystem-read", "filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"copy folder SOURCE to DESTINATION"},
	},
	"move": {
		params:      []NativeParam{{Name: "source", Type: "text"}, {Name: "destination", Type: "text"}, {Name: "policy", Type: "text"}},
		result:      "text",
		description: "Relocates one entry with an explicit create-or-replace destination policy; the cross-device copy fallback is never implicit.",
		failures:    []string{"FileNotFound", "FileAlreadyExists", "FilePermissionDenied", "InvalidFileType", "InvalidFilePath", "FileSystemUnavailable"},
		effects:     []string{"filesystem-read", "filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english: []string{
			"move file SOURCE to DESTINATION",
			"move folder SOURCE to DESTINATION",
		},
	},
	"create_folder": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "text",
		description: "Ensures one directory exists.",
		failures:    fileStatFailureSet,
		effects:     []string{"filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"create folder PATH if missing"},
	},
	"create_folders": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "text",
		description: "Ensures every directory along one path exists.",
		failures:    fileStatFailureSet,
		effects:     []string{"filesystem-write"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"create folders through PATH"},
	},
	"remove_file": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "text",
		description: "Deletes one exact typed file target.",
		failures:    []string{"FileNotFound", "FilePermissionDenied", "InvalidFileType", "InvalidFilePath", "FileSystemUnavailable"},
		effects:     []string{"filesystem-write", "destructive"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"remove file PATH"},
	},
	"remove_folder": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "text",
		description: "Deletes one exact, empty directory target.",
		failures:    []string{"FileNotFound", "FilePermissionDenied", "InvalidFileType", "InvalidFilePath", "FileSystemUnavailable"},
		effects:     []string{"filesystem-write", "destructive"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"remove empty folder PATH"},
	},
	"remove_folder_recursively": {
		params:      []NativeParam{{Name: "path", Type: "text"}},
		result:      "text",
		description: "Deletes one directory including its contents; never follows symbolic links and applies an entry limit before deletion begins.",
		failures:    []string{"FileNotFound", "FilePermissionDenied", "InvalidFileType", "InvalidFilePath", "FileTraversalLimitExceeded", "FileSystemUnavailable"},
		effects:     []string{"filesystem-write", "destructive"},
		targets:     []string{"native", "wasip1"},
		english:     []string{"remove folder PATH including its contents"},
	},
}

// fileActionUnavailable is the honest boundary for contract-only
// registrations: the operation is declared to the language but this build
// provides no host implementation, which is exactly the reserved
// FileSystemUnavailable failure.
func fileActionUnavailable(operation string) func(context.Context, Options, []any) (any, error) {
	return func(_ context.Context, _ Options, args []any) (any, error) {
		return nil, fileUnavailableFailure(operation, args)
	}
}

func fileStreamUnavailable(operation string) func(context.Context, Options, []any) (streamSource, error) {
	return func(_ context.Context, _ Options, args []any) (streamSource, error) {
		return nil, fileUnavailableFailure(operation, args)
	}
}

func fileUnavailableFailure(operation string, args []any) error {
	path := ""
	if len(args) > 0 {
		if text, ok := args[0].(string); ok {
			path = text
		}
	}
	return &typedFailure{kind: "FileSystemUnavailable", value: map[string]any{
		"kind":      "FileSystemUnavailable",
		"message":   fmt.Sprintf("%s has no host implementation in this build", operation),
		"retryable": false,
		"path":      path,
		"operation": operation,
	}}
}

// registerFileActionContract declares the std/files adapter contract. It
// gap-fills: runtime registrations that already provide an implementation
// win, and their metadata is completed only where it is missing.
func registerFileActionContract() {
	if stdRegistry["std/files"] == nil {
		stdRegistry["std/files"] = map[string]NativeOp{}
	}
	ops := stdRegistry["std/files"]
	for name, spec := range fileActionContract {
		if op, registered := ops[name]; registered {
			if len(op.PossibleFailures) == 0 {
				op.PossibleFailures = append([]string(nil), spec.failures...)
				ops[name] = op
			}
			// Runtime registrations that still carry the legacy single
			// "filesystem" effect are refined to the spec's read/write/
			// destructive labels; anything more specific is left alone.
			if doc, documented := stdDocs["std/files."+name]; documented && len(doc.effects) == 1 && doc.effects[0] == "filesystem" {
				doc.effects = append([]string(nil), spec.effects...)
				stdDocs["std/files."+name] = doc
			}
			op = ops[name]
			op.Effects = append([]string(nil), spec.effects...)
			op.Description = spec.description
			op.Result = spec.result
			op.Targets = append([]string(nil), spec.targets...)
			ops[name] = op
			continue
		}
		op := NativeOp{
			Name:             name,
			Params:           append([]NativeParam(nil), spec.params...),
			Result:           spec.result,
			Targets:          append([]string(nil), spec.targets...),
			Effects:          append([]string(nil), spec.effects...),
			Description:      spec.description,
			PossibleFailures: append([]string(nil), spec.failures...),
			ContextFn:        fileActionUnavailable(name),
		}
		if strings.HasPrefix(spec.result, "stream of ") {
			op.StreamFn = fileStreamUnavailable(name)
		}
		ops[name] = op
		stdDocs["std/files."+name] = stdDoc{result: spec.result, description: spec.description, effects: append([]string(nil), spec.effects...)}
	}
}

func init() {
	registerFileActionContract()
}

// fileEnglishPatterns lists the canonical English constructions for one
// std/files action, if any.
func fileEnglishPatterns(key, action string) []string {
	if key != "std/files" {
		return nil
	}
	spec, ok := fileActionContract[action]
	if !ok {
		return nil
	}
	return spec.english
}

// fileNamedArgumentPattern renders the module fallback with parameter names
// instead of positional value hints.
func fileNamedArgumentPattern(alias, action string) string {
	spec, ok := fileActionContract[action]
	if !ok {
		return ""
	}
	names := make([]string, 0, len(spec.params))
	for _, param := range spec.params {
		names = append(names, param.Name)
	}
	joined := strings.Join(names, ", ")
	if strings.HasPrefix(spec.result, "stream of ") {
		return "stream " + alias + "." + action + " with " + joined + " called NAME"
	}
	if joined == "" {
		return alias + "." + action
	}
	return alias + "." + action + " with " + joined
}

// ---- compatibility canonicalization ----

// canonicalizeFileCompatibility rewrites the accepted compatibility calls to
// their preferred modern forms: files.read becomes files.read_text, and
// files.write becomes files.write_text with an explicit "replace" policy,
// because the legacy operation replaced contents unconditionally. The rewrite
// is line-preserving, quote-aware, and deterministic; files.discover has no
// bounded canonical equivalent and is left untouched.
func canonicalizeFileCompatibility(source string) string {
	lines := strings.Split(source, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		replacement, ok := canonicalizeFileCompatLine(trimmed)
		if !ok {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + replacement
		changed = true
	}
	if !changed {
		return source
	}
	return strings.Join(lines, "\n")
}

var fileCompatCallRe = regexp.MustCompile(`^(?:call )?files\.(read|write) with (.+)$`)

func canonicalizeFileCompatLine(text string) (string, bool) {
	m := fileCompatCallRe.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	verb, rest := m[1], m[2]
	args, sink := splitCalledSuffix(rest)
	// Count only unquoted commas: a rewrite must never move an argument that
	// lives inside a literal, and arguments containing list literals are left
	// for the author to modernize by hand.
	count := strings.Count(string(maskQuoted(args)), ",") + 1
	if verb == "read" {
		if count != 1 {
			return "", false
		}
		return strings.Replace(text, "files.read with", "files.read_text with", 1), true
	}
	if count != 2 {
		return "", false
	}
	head := strings.Replace(text, "files.write with", "files.write_text with", 1)
	body := strings.TrimSuffix(head, " called "+sink)
	if sink != "" {
		return body + `, "replace" called ` + sink, true
	}
	return body + `, "replace"`, true
}

// splitCalledSuffix splits "args called sink" on the last unquoted " called ".
func splitCalledSuffix(rest string) (args, sink string) {
	masked := string(maskQuoted(rest))
	if idx := strings.LastIndex(masked, " called "); idx >= 0 {
		if tail := rest[idx+len(" called "):]; isPlainName(tail) {
			return rest[:idx], tail
		}
	}
	return rest, ""
}

func isPlainName(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
