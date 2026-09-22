package sos

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Filesystem traversal implements the recursive-walk and streaming surface of
// docs/sysonescript-files-spec.md: a bounded materialized walk, a pull-based
// incremental stream in stable depth-first lexical order, and the shared
// include/exclude/depth/symbolic-link policy also used by the watcher. Typed
// failures and the FileEntry record come from the shared boundary in
// filesystem.go.

const (
	fileKind   = "file"
	folderKind = "folder"
	linkKind   = "link"
	otherKind  = "other"
	traverseOp = "traverse"
)

// traversalOptions is the caller-facing, Go-native form of one traversal
// request before validation.
type traversalOptions struct {
	root        string
	include     []string
	exclude     []string
	kinds       []string
	depth       int
	follow      bool
	allowEscape bool
}

// traversalSpec is the validated, immutable traversal policy shared by the
// walker, the stream source, and the watcher.
type traversalSpec struct {
	root        string // resolved absolute host path of the starting folder
	rootDisplay string // portable (/) form used in failures and diagnostics
	include     []string
	exclude     []string
	kinds       map[string]bool
	maxDepth    int // 0 means unlimited
	followLinks bool
	allowEscape bool
}

// parseTraversalSpec validates options and resolves the starting folder once
// so entry paths, escape checks, and failure fields share one identity.
func parseTraversalSpec(opts Options, o traversalOptions) (traversalSpec, error) {
	if strings.TrimSpace(o.root) == "" {
		return traversalSpec{}, fmt.Errorf("traversal root is required")
	}
	include, err := parseGlobList(o.include, "include")
	if err != nil {
		return traversalSpec{}, err
	}
	exclude, err := parseGlobList(o.exclude, "exclude")
	if err != nil {
		return traversalSpec{}, err
	}
	kinds := map[string]bool{}
	if len(o.kinds) == 0 {
		kinds[fileKind], kinds[folderKind] = true, true
	}
	for _, kind := range o.kinds {
		switch kind {
		case fileKind, folderKind, linkKind, otherKind:
			kinds[kind] = true
		default:
			return traversalSpec{}, fmt.Errorf("entry kind %q must be one of file, folder, link, other", kind)
		}
	}
	if o.depth < 0 {
		return traversalSpec{}, fmt.Errorf("depth must be a non-negative folder count")
	}
	absolute, err := filepath.Abs(ioPath(opts, o.root))
	if err != nil {
		return traversalSpec{}, fileFailure("InvalidFilePath", fmt.Sprintf("path %q cannot be resolved: %v", o.root, err), map[string]any{"path": filepath.ToSlash(o.root), "reason": "path cannot be resolved"})
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return traversalSpec{}, fileOpError(err, filepath.ToSlash(o.root), traverseOp)
	}
	target, err := os.Stat(resolved)
	if err != nil {
		return traversalSpec{}, fileOpError(err, filepath.ToSlash(o.root), traverseOp)
	}
	if !target.IsDir() {
		initial, _ := os.Lstat(absolute)
		actual := otherKind
		if initial != nil {
			actual = classifyMode(initial.Mode())
		}
		return traversalSpec{}, fileFailure("InvalidFileType", fmt.Sprintf("traversal root %s is a %s, not a folder", filepath.ToSlash(o.root), actual), map[string]any{"path": filepath.ToSlash(o.root), "expected": folderKind, "actual": actual})
	}
	return traversalSpec{
		root:        resolved,
		rootDisplay: filepath.ToSlash(resolved),
		include:     include,
		exclude:     exclude,
		kinds:       kinds,
		maxDepth:    o.depth,
		followLinks: o.follow,
		allowEscape: o.allowEscape,
	}, nil
}

// parseGlobList validates one pattern list. Patterns containing "/" match
// slash-separated relative paths; other patterns match single components.
// "**" is rejected until recursive-glob semantics are implemented explicitly.
func parseGlobList(patterns []string, label string) ([]string, error) {
	for _, pattern := range patterns {
		if strings.Contains(pattern, "**") {
			return nil, fmt.Errorf("%s pattern %q: ** is not accepted", label, pattern)
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("%s pattern %q: %w", label, pattern, err)
		}
	}
	return patterns, nil
}

func globMatches(pattern, rel string) bool {
	if strings.Contains(pattern, "/") {
		matched, _ := path.Match(pattern, rel)
		return matched
	}
	for _, part := range strings.Split(rel, "/") {
		if matched, _ := path.Match(pattern, part); matched {
			return true
		}
	}
	return false
}

func (s traversalSpec) excluded(rel string) bool {
	for _, pattern := range s.exclude {
		if globMatches(pattern, rel) {
			return true
		}
	}
	return false
}

func (s traversalSpec) included(rel string) bool {
	if len(s.include) == 0 {
		return true
	}
	for _, pattern := range s.include {
		if globMatches(pattern, rel) {
			return true
		}
	}
	return false
}

// walkItem is one traversed entry before it is shaped into a FileEntry
// record. Size and timestamps come from the emitted identity: the resolved
// target for followed links, otherwise the lstat result.
type walkItem struct {
	rel      string // slash-separated path relative to the starting folder
	hostPath string
	name     string
	kind     string
	size     int64
	hasSize  bool
	modified time.Time
	identity os.FileInfo
	depth    int // path-component count below the root
	symLink  bool
}

// walkFrame is one open directory during depth-first traversal.
type walkFrame struct {
	dir     string
	rel     string
	entries []os.DirEntry
	pos     int
}

// treeWalker performs a pull-based, stable depth-first lexical walk. Each
// nextEntry call does bounded work, so traversal never outruns its consumer.
type treeWalker struct {
	spec    traversalSpec
	stack   []*walkFrame
	visited []os.FileInfo // resolved directory identities, link-following only
	done    bool
}

func sortDirEntries(entries []os.DirEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
}

func newTreeWalker(spec traversalSpec) (*treeWalker, error) {
	entries, err := os.ReadDir(spec.root)
	if err != nil {
		return nil, fileOpError(err, spec.rootDisplay, traverseOp)
	}
	sortDirEntries(entries)
	walker := &treeWalker{spec: spec, stack: []*walkFrame{{dir: spec.root, entries: entries}}}
	if spec.followLinks {
		if info, err := os.Stat(spec.root); err == nil {
			walker.visited = append(walker.visited, info)
		}
	}
	return walker, nil
}

func (w *treeWalker) alreadyVisited(info os.FileInfo) bool {
	for _, seen := range w.visited {
		if os.SameFile(seen, info) {
			return true
		}
	}
	return false
}

// nextEntry returns the next matching entry in depth-first lexical order.
func (w *treeWalker) nextEntry(ctx context.Context) (map[string]any, bool, error) {
	item, more, err := w.nextItem(ctx)
	if err != nil || !more {
		return nil, false, err
	}
	return fileEntryValue(item), true, nil
}

func (w *treeWalker) nextItem(ctx context.Context) (walkItem, bool, error) {
	if w.done {
		return walkItem{}, false, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			w.done = true
			return walkItem{}, false, err
		}
		if len(w.stack) == 0 {
			w.done = true
			return walkItem{}, false, nil
		}
		frame := w.stack[len(w.stack)-1]
		if frame.pos >= len(frame.entries) {
			w.stack = w.stack[:len(w.stack)-1]
			continue
		}
		entry := frame.entries[frame.pos]
		frame.pos++
		name := entry.Name()
		rel := name
		depth := 1
		if frame.rel != "" {
			rel = frame.rel + "/" + name
			depth = strings.Count(frame.rel, "/") + 2
		}
		if w.spec.excluded(rel) {
			continue
		}
		if w.spec.maxDepth > 0 && depth > w.spec.maxDepth {
			continue
		}
		childPath := filepath.Join(frame.dir, name)
		info, err := os.Lstat(childPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue // disappeared between listing and inspection
			}
			w.done = true
			return walkItem{}, false, fileOpError(err, filepath.ToSlash(childPath), traverseOp)
		}
		kind, isLink := classifyMode(info.Mode()), info.Mode()&os.ModeSymlink != 0
		emitKind, symLink := kind, isLink
		var targetInfo os.FileInfo
		cycle := false
		if isLink && w.spec.followLinks {
			resolved, resolveErr := filepath.EvalSymlinks(childPath)
			if resolveErr == nil {
				if !w.spec.allowEscape && !pathWithin(w.spec.root, resolved) {
					w.done = true
					return walkItem{}, false, fileFailure("InvalidFilePath",
						fmt.Sprintf("symbolic link %s targets %s outside the starting folder", filepath.ToSlash(childPath), filepath.ToSlash(resolved)),
						map[string]any{"path": filepath.ToSlash(childPath), "reason": "symbolic link target escapes the starting folder"})
				}
				if resolvedInfo, statErr := os.Stat(resolved); statErr == nil {
					if resolvedInfo.IsDir() {
						if w.alreadyVisited(resolvedInfo) {
							// Cycle: report the link as a folder entry but
							// never descend, since its identity is already
							// on the walk.
							cycle = true
						}
						emitKind, targetInfo = folderKind, resolvedInfo
					} else {
						emitKind, targetInfo = classifyMode(resolvedInfo.Mode()), resolvedInfo
					}
				} else if !os.IsNotExist(statErr) {
					w.done = true
					return walkItem{}, false, fileOpError(statErr, filepath.ToSlash(childPath), traverseOp)
				}
			} else if !os.IsNotExist(resolveErr) {
				w.done = true
				return walkItem{}, false, fileOpError(resolveErr, filepath.ToSlash(childPath), traverseOp)
			}
			// A broken link keeps kind "link" and is never followed.
		}
		if emitKind == folderKind && !cycle {
			children, readErr := os.ReadDir(childPath)
			if readErr != nil {
				if os.IsNotExist(readErr) {
					continue
				}
				w.done = true
				return walkItem{}, false, fileOpError(readErr, filepath.ToSlash(childPath), traverseOp)
			}
			sortDirEntries(children)
			if w.spec.followLinks {
				identity := targetInfo
				if identity == nil {
					identity = info
				}
				if !w.alreadyVisited(identity) {
					w.visited = append(w.visited, identity)
				}
			}
			w.stack = append(w.stack, &walkFrame{dir: childPath, rel: rel, entries: children})
		}
		if !w.spec.kinds[emitKind] || !w.spec.included(rel) {
			continue
		}
		source := info
		if targetInfo != nil {
			source = targetInfo
		}
		item := walkItem{rel: rel, hostPath: childPath, name: name, kind: emitKind, depth: depth, symLink: symLink, modified: source.ModTime(), identity: source}
		if source.Mode().IsRegular() {
			item.size, item.hasSize = source.Size(), true
		}
		return item, true, nil
	}
}

// walkFileTree materializes the traversal behind an explicit entry bound and
// fails with FileTraversalLimitExceeded instead of silently truncating.
func walkFileTree(ctx context.Context, spec traversalSpec, limit int) ([]any, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("materialized traversal requires a positive entry bound")
	}
	walker, err := newTreeWalker(spec)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, min(limit, 1024))
	for len(items) < limit {
		entry, more, err := walker.nextEntry(ctx)
		if err != nil {
			return nil, err
		}
		if !more {
			return items, nil
		}
		items = append(items, entry)
	}
	if _, more, err := walker.nextEntry(ctx); err != nil {
		return nil, err
	} else if more {
		return nil, fileFailure("FileTraversalLimitExceeded",
			fmt.Sprintf("walk through %s exceeds its bound of %d entries", spec.rootDisplay, limit),
			map[string]any{"root": spec.rootDisplay, "limit": float64(limit)})
	}
	return items, nil
}

// fileStreamSource adapts the pull-based walker to the runtime stream
// boundary. No goroutine is created: each next reads exactly one entry, so
// backpressure is structural and cancellation is immediate. Every invocation
// owns its walker, so streams over the same folder stay independent.
type fileStreamSource struct {
	mu       sync.Mutex
	walker   *treeWalker
	openErr  error
	closed   bool
	finished bool
}

func newFileStreamSource(spec traversalSpec) *fileStreamSource {
	walker, err := newTreeWalker(spec)
	return &fileStreamSource{walker: walker, openErr: err}
}

func (s *fileStreamSource) next(ctx context.Context) (any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil, false, s.openErr
	}
	if s.openErr != nil || s.closed {
		s.finished = true
		return nil, false, s.openErr
	}
	entry, more, err := s.walker.nextEntry(ctx)
	if err != nil || !more {
		s.finished, s.openErr = true, err
		return nil, false, err
	}
	return entry, true, nil
}

func (s *fileStreamSource) cancel(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *fileStreamSource) streamScope() (string, int, bool) {
	if s.walker == nil {
		return "", 0, false
	}
	return s.walker.spec.rootDisplay, s.walker.spec.maxDepth, false
}

// ---- module actions ----

func stringListArg(value any, label string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of text", label)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must contain only text", label)
		}
		out = append(out, text)
	}
	return out, nil
}

func intArg(value any, label string) (int, error) {
	if value == nil {
		return 0, nil
	}
	n, ok := number(value)
	if !ok || math.IsNaN(n) || n < 0 || math.Trunc(n) != n || n > math.MaxInt32 {
		return 0, fmt.Errorf("%s must be a non-negative integer", label)
	}
	return int(n), nil
}

func boolArg(value any, label string) (bool, error) {
	if value == nil {
		return false, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", label)
	}
	return flag, nil
}

func traversalOptionsFromArgs(opts Options, rootArg, includeArg, excludeArg, kindsArg, depthArg, followArg any) (traversalOptions, error) {
	root, ok := rootArg.(string)
	if !ok || strings.TrimSpace(root) == "" {
		return traversalOptions{}, fmt.Errorf("root must be text naming the starting folder")
	}
	include, err := stringListArg(includeArg, "include")
	if err != nil {
		return traversalOptions{}, err
	}
	exclude, err := stringListArg(excludeArg, "exclude")
	if err != nil {
		return traversalOptions{}, err
	}
	kinds, err := stringListArg(kindsArg, "kinds")
	if err != nil {
		return traversalOptions{}, err
	}
	depth, err := intArg(depthArg, "depth")
	if err != nil {
		return traversalOptions{}, err
	}
	follow, err := boolArg(followArg, "follow")
	if err != nil {
		return traversalOptions{}, err
	}
	return traversalOptions{root: root, include: include, exclude: exclude, kinds: kinds, depth: depth, follow: follow}, nil
}

func walkFileTreeAction(ctx context.Context, opts Options, args []any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, err := traversalOptionsFromArgs(opts, args[0], args[2], args[3], args[4], args[5], args[6])
	if err != nil {
		return nil, err
	}
	limit, err := intArg(args[1], "limit")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be an explicit positive entry bound")
	}
	spec, err := parseTraversalSpec(opts, options)
	if err != nil {
		return nil, err
	}
	return walkFileTree(ctx, spec, limit)
}

func streamFileTreeAction(ctx context.Context, opts Options, args []any) (streamSource, error) {
	options, err := traversalOptionsFromArgs(opts, args[0], args[1], args[2], args[3], args[4], args[5])
	if err != nil {
		return nil, err
	}
	spec, err := parseTraversalSpec(opts, options)
	if err != nil {
		return nil, err
	}
	walker, err := newTreeWalker(spec)
	if err != nil {
		return nil, err
	}
	return &fileStreamSource{walker: walker}, nil
}

func registerFilesystemTraversalOps() {
	if stdRegistry["std/files"] == nil {
		stdRegistry["std/files"] = map[string]NativeOp{}
	}
	traversalParams := []NativeParam{
		{Name: "root", Type: "text"},
		{Name: "include", Type: "list"},
		{Name: "exclude", Type: "list"},
		{Name: "kinds", Type: "list"},
		{Name: "depth", Type: "integer"},
		{Name: "follow", Type: "boolean"},
	}
	stdRegistry["std/files"]["walk"] = NativeOp{
		Name: "walk",
		Params: append([]NativeParam{
			{Name: "root", Type: "text"},
			{Name: "limit", Type: "integer"},
		}, traversalParams[1:]...),
		Result:           "list",
		Targets:          []string{"native", "wasip1"},
		ContextFn:        walkFileTreeAction,
		Effects:          append([]string(nil), fileActionContract["walk"].effects...),
		PossibleFailures: append([]string(nil), fileActionContract["walk"].failures...),
	}
	stdRegistry["std/files"]["stream"] = NativeOp{
		Name:             "stream",
		Params:           traversalParams,
		Result:           "stream of FileEntry",
		Targets:          []string{"native", "wasip1"},
		StreamFn:         streamFileTreeAction,
		Effects:          append([]string(nil), fileActionContract["stream"].effects...),
		PossibleFailures: append([]string(nil), fileActionContract["stream"].failures...),
	}
	stdDocs["std/files.walk"] = stdDoc{"list", "Walks one folder recursively and returns a bounded list of FileEntry records in stable depth-first lexical order; exceeding the bound fails instead of truncating.", []string{"filesystem"}}
	stdDocs["std/files.stream"] = stdDoc{"stream of FileEntry", "Streams FileEntry records under one folder incrementally with backpressure, cancellation, and the shared include/exclude/depth/link policy.", []string{"filesystem"}}
}

// openNativeModuleStream opens one standard-library streaming operation as an
// independently owned stream. It mirrors the external-module open path while
// keeping native producers pull-based.
func (r *runtime) openNativeModuleStream(mod *Module, op NativeOp, actionName, binding string, line int, expressions []string) (*streamHandle, error) {
	if len(op.Targets) > 0 && !containsString(op.Targets, "native") && !containsString(op.Targets, currentExternalTarget()) {
		return nil, fmt.Errorf("%s is unavailable on %s", actionName, currentExternalTarget())
	}
	if len(expressions) != len(op.Params) {
		return nil, fmt.Errorf("%s expects %d argument(s)", actionName, len(op.Params))
	}
	values := make([]any, len(expressions))
	for i, expression := range expressions {
		value, err := r.eval(expression, nil)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	if err := op.check(values); err != nil {
		return nil, err
	}
	source, err := op.StreamFn(r.ctx, r.opts, values)
	if err != nil {
		return nil, err
	}
	itemType, err := parseType(strings.TrimSpace(strings.TrimPrefix(op.Result, "stream of ")), false)
	// Built-in record names such as FileEntry stay open until the language
	// layer ships them as declared definitions; unmatched names type as any.
	if err != nil || (itemType.Element == nil && itemType.Name != "any" && !isScalarType(itemType.Name) && r.definitions[itemType.Name] == nil) {
		itemType = TypeRef{Name: "any"}
	}
	stream := newStreamHandleWithContext(r.ctx, itemType, actionName, source)
	stream.id = fmt.Sprintf("stream-%d", r.shared.streams.Add(1))
	stream.binding, stream.line, stream.emit = binding, line, r.opts.OnStreamEvent
	if r.opts.Streams != nil {
		r.opts.Streams.register(stream)
	}
	stream.publish("opened")
	return stream, nil
}

func init() { registerFilesystemTraversalOps() }
