package sos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Native watcher streams for std/files. The primary trigger is the
// operating system's own notification backend: inotify on Linux, kqueue
// vnode filters on macOS, ReadDirectoryChangesW on Windows, and a periodic
// timer only on platforms without any of them. Each notification burst
// triggers one bounded reconciliation scan; the diff between scans — not the
// raw kernel event — becomes the delivered FileChange records, so semantics
// stay identical on every platform: lexical batching,
// include/exclude/depth/link rules, move inference, and overflow reporting
// that demands a rescan. Each invocation owns its backend, queue, counters,
// and terminal failures; closing one watcher never closes another.

const (
	defaultWatchInterval = 200 * time.Millisecond
	defaultWatchBatches  = 256

	// The settle window bounds how long one notification burst coalesces
	// after its first event: kernels split a single logical change into
	// several events and one rename into a from/to pair. It follows the
	// requested interval between fixed bounds so faster watchers coalesce
	// tighter bursts.
	watchMinSettle   = 2 * time.Millisecond
	watchMaxSettle   = 50 * time.Millisecond
	watchSettleBurst = 1024

	changeCreated    = "created"
	changeModified   = "modified"
	changeRemoved    = "removed"
	changeMoved      = "moved"
	changeOverflowed = "overflowed"
)

// watchEntry is one snapshot line used for difference detection.
type watchEntry struct {
	kind     string
	size     int64
	modified time.Time
	mode     os.FileMode
	identity os.FileInfo
}

// watchChange is one detected filesystem change before it is shaped into a
// FileChange record. previous holds the relative path a moved entry came
// from; it is reported as previous_path so consumers can follow a file
// across names.
type watchChange struct {
	path      string
	rel       string
	kind      string
	entryKind string
	previous  string
	observed  time.Time
}

func (c watchChange) value() map[string]any {
	record := map[string]any{
		"path":          c.path,
		"relative_path": c.rel,
		"kind":          c.kind,
		"entry_kind":    c.entryKind,
		"observed_at":   c.observed,
	}
	if c.kind == changeMoved && c.previous != "" {
		record["previous_path"] = c.previous
	}
	return record
}

// scanWatchSnapshot materializes one bounded snapshot of the watched folder.
// Kinds and include patterns are widened so recursion, removals, and filter
// reporting all observe the same tree; the caller's filters apply to the
// emitted changes.
func scanWatchSnapshot(ctx context.Context, spec traversalSpec) (map[string]watchEntry, error) {
	scanSpec := spec
	scanSpec.kinds = map[string]bool{fileKind: true, folderKind: true, linkKind: true, otherKind: true}
	scanSpec.include = nil
	walker, err := newTreeWalker(scanSpec)
	if err != nil {
		return nil, err
	}
	out := map[string]watchEntry{}
	for {
		item, more, err := walker.nextItem(ctx)
		if err != nil {
			return nil, err
		}
		if !more {
			return out, nil
		}
		if len(out) >= maxListEntries {
			return nil, fileFailure("FileTraversalLimitExceeded", fmt.Sprintf("watch snapshot exceeds %d entries", maxListEntries), map[string]any{"root": spec.rootDisplay, "limit": float64(maxListEntries)})
		}
		// On Windows, os.FileInfo may resolve its file ID lazily from the
		// pathname. Prime it while the old name still exists so a later
		// rename can be paired with the new name by os.SameFile.
		if item.kind == fileKind && item.identity != nil {
			_ = os.SameFile(item.identity, item.identity)
		}
		out[item.rel] = watchEntry{kind: item.kind, size: item.size, modified: item.modified, mode: item.identity.Mode(), identity: item.identity}
	}
}

// diffWatchSnapshots computes the changes between two snapshots in lexical
// order and applies the caller's kinds and include filters. A removal paired
// with a creation of the same identity — a rename preserves the entry kind
// and, for files, the size and modification time — collapses into one moved
// change so consumers can follow a file across names. Only regular files are
// paired: equal size and modification time is a strong rename signal, while
// folder timestamps carry no comparable identity.
func diffWatchSnapshots(spec traversalSpec, previous, current map[string]watchEntry, observed time.Time) []watchChange {
	reported := func(entryKind, rel string) bool {
		return spec.kinds[entryKind] && spec.included(rel)
	}
	var removed, created []string
	for rel, entry := range previous {
		if _, kept := current[rel]; !kept && reported(entry.kind, rel) {
			removed = append(removed, rel)
		}
	}
	for rel, entry := range current {
		if _, known := previous[rel]; !known && reported(entry.kind, rel) {
			created = append(created, rel)
		}
	}
	sort.Strings(removed)
	sort.Strings(created)
	movedFrom := map[string]string{} // created rel → removed rel it replaces
	for _, gone := range removed {
		before := previous[gone]
		if before.kind != fileKind {
			continue
		}
		for _, appeared := range created {
			if _, taken := movedFrom[appeared]; taken {
				continue
			}
			after := current[appeared]
			sameIdentity := before.identity != nil && after.identity != nil && os.SameFile(before.identity, after.identity)
			if before.identity == nil && after.identity == nil {
				sameIdentity = before.size == after.size && before.modified.Equal(after.modified)
			}
			if after.kind == fileKind && sameIdentity {
				movedFrom[appeared] = gone
				break
			}
		}
	}
	consumed := make(map[string]bool, len(movedFrom))
	for _, gone := range movedFrom {
		consumed[gone] = true
	}
	paths := make([]string, 0, len(previous)+len(current))
	for rel := range previous {
		paths = append(paths, rel)
	}
	for rel := range current {
		if _, had := previous[rel]; !had {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	host := spec.root
	var changes []watchChange
	emit := func(kind, rel, entryKind string) {
		if spec.kinds[entryKind] && spec.included(rel) {
			changes = append(changes, watchChange{
				path:      toPortablePath(host, rel),
				rel:       rel,
				kind:      kind,
				entryKind: entryKind,
				observed:  observed,
			})
		}
	}
	for _, rel := range paths {
		before, hadBefore := previous[rel]
		after, hadAfter := current[rel]
		switch {
		case hadAfter && !hadBefore:
			if from, moved := movedFrom[rel]; moved {
				changes = append(changes, watchChange{
					path:      toPortablePath(host, rel),
					rel:       rel,
					kind:      changeMoved,
					entryKind: after.kind,
					previous:  from,
					observed:  observed,
				})
				break
			}
			emit(changeCreated, rel, after.kind)
		case hadBefore && !hadAfter:
			if !consumed[rel] {
				emit(changeRemoved, rel, before.kind)
			}
		case before.kind != after.kind:
			emit(changeRemoved, rel, before.kind)
			emit(changeCreated, rel, after.kind)
		case (after.kind == fileKind && (before.size != after.size || !before.modified.Equal(after.modified))) || before.mode != after.mode:
			emit(changeModified, rel, after.kind)
		}
	}
	return changes
}

func toPortablePath(host, rel string) string {
	if rel == "" {
		return filepath.ToSlash(host)
	}
	return filepath.ToSlash(host + string(filepath.Separator) + filepath.FromSlash(rel))
}

// nativeWatchEvent is one operating-system notification reduced to a
// reconciliation trigger. The kernel event's details are deliberately
// discarded: the scan diff, not the raw event, defines the delivered change
// records, which is what keeps the semantics identical across platforms.
type nativeWatchEvent struct {
	overflow bool  // the kernel dropped notifications; consumers must rescan
	failure  error // a terminal native read failure; preserve its host cause
}

// nativeWatchBackend is the platform trigger for one watched tree. Events
// arrive on a channel owned by the backend; sync re-registers per-entry
// watches against a fresh snapshot so later content writes keep triggering;
// close releases every kernel resource and waits for the backend's goroutine
// to exit. sync and close run on the watcher's driver goroutine, so
// implementations need no internal locking for their registration state.
type nativeWatchBackend interface {
	events() <-chan nativeWatchEvent
	sync(spec traversalSpec, snapshot map[string]watchEntry) error
	close() error
}

// watchTargets returns the host paths whose per-entry registrations keep
// notifications flowing for a snapshot: the root, every folder whose children
// are still observed, and every regular file, because content writes to an
// existing file never touch its parent directory entry. Links and special
// entries need no registration: retargeting or replacing them changes the
// parent directory entry, which is already watched.
func watchTargets(spec traversalSpec, snapshot map[string]watchEntry) map[string]struct{} {
	targets := map[string]struct{}{spec.root: {}}
	for rel, entry := range snapshot {
		switch entry.kind {
		case fileKind:
		case folderKind:
			if spec.maxDepth > 0 && strings.Count(rel, "/")+1 >= spec.maxDepth {
				continue // children at this depth are beyond the observed tree
			}
		default:
			continue
		}
		targets[filepath.Join(spec.root, filepath.FromSlash(rel))] = struct{}{}
	}
	return targets
}

// fileWatchSource is one independently owned recursive watcher stream.
type fileWatchSource struct {
	spec     traversalSpec
	settle   time.Duration
	scanMu   sync.Mutex // serializes reconciliation and an overflow baseline refresh
	backend  nativeWatchBackend
	previous map[string]watchEntry

	mu          sync.Mutex
	queue       chan []watchChange
	queuedItems int
	pending     []watchChange
	overflow    bool
	terminalErr error
	finished    bool

	stop      context.CancelFunc
	stoppedCh chan struct{}
	endCh     chan struct{}
	stopOnce  sync.Once
	endOnce   sync.Once
}

// newFileWatchSource starts one recursive watcher. The initial snapshot and
// the backend's recursive registration are both taken synchronously: when
// startup fails, every partial resource is released before a stream is
// created, so nothing leaks and open fails.
func newFileWatchSource(parent context.Context, spec traversalSpec, interval time.Duration, queueBatches int) (*fileWatchSource, error) {
	if interval <= 0 {
		interval = defaultWatchInterval
	}
	if queueBatches <= 0 {
		queueBatches = defaultWatchBatches
	}
	settle := interval
	if settle < watchMinSettle {
		settle = watchMinSettle
	}
	if settle > watchMaxSettle {
		settle = watchMaxSettle
	}
	previous, err := scanWatchSnapshot(parent, spec)
	if err != nil {
		return nil, err
	}
	backend, err := newNativeWatchBackend(spec, interval, previous)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	source := &fileWatchSource{
		spec:      spec,
		settle:    settle,
		backend:   backend,
		previous:  previous,
		queue:     make(chan []watchChange, queueBatches),
		stop:      cancel,
		stoppedCh: make(chan struct{}),
		endCh:     make(chan struct{}),
	}
	go source.watch(ctx, backend)
	return source, nil
}

func (s *fileWatchSource) signalEnd() {
	s.endOnce.Do(func() { close(s.endCh) })
}

func (s *fileWatchSource) watchFailure(err error) error {
	// Windows can return access denied from a still-open directory handle
	// after the watched root has been removed. Its path is gone, so report
	// the portable FileNotFound failure promised by the watch contract.
	if _, statErr := os.Stat(s.spec.root); errors.Is(statErr, os.ErrNotExist) {
		return fileOpError(statErr, s.spec.rootDisplay, traverseOp)
	}
	var typed *typedFailure
	if errors.As(err, &typed) {
		return err
	}
	return fileOpError(err, s.spec.rootDisplay, traverseOp)
}

// watch drives one opened watcher. Platform notifications are the primary
// trigger: each burst is settled and answered with one bounded
// reconciliation scan whose diff becomes the next batch, then the backend is
// synced so entries created since the previous scan hold registrations of
// their own. A scan that fails terminally ends the stream with that typed
// failure; an overflowing kernel queue is reported through the same
// overflowed change a slow consumer produces.
func (s *fileWatchSource) watch(ctx context.Context, backend nativeWatchBackend) {
	defer close(s.stoppedCh)
	defer func() {
		s.scanMu.Lock()
		defer s.scanMu.Unlock()
		_ = backend.close()
	}()
	for {
		var trigger nativeWatchEvent
		select {
		case <-ctx.Done():
			return
		case event, ok := <-backend.events():
			if !ok {
				s.mu.Lock()
				s.terminalErr = fileFailure("FileSystemUnavailable", "native watcher stopped unexpectedly", map[string]any{"path": s.spec.rootDisplay, "operation": "watch"})
				s.mu.Unlock()
				s.signalEnd()
				return
			}
			trigger = event
		}
		if trigger.failure != nil {
			s.mu.Lock()
			s.terminalErr = s.watchFailure(trigger.failure)
			s.mu.Unlock()
			s.signalEnd()
			return
		}
		s.scanMu.Lock()
		overflow, readErr := s.coalesce(backend, trigger)
		if readErr != nil {
			s.scanMu.Unlock()
			s.mu.Lock()
			s.terminalErr = s.watchFailure(readErr)
			s.mu.Unlock()
			s.signalEnd()
			return
		}
		current, err := scanWatchSnapshot(ctx, s.spec)
		if err != nil {
			s.scanMu.Unlock()
			if ctx.Err() != nil {
				return // canceled mid-scan: a clean stop, not a failure
			}
			s.mu.Lock()
			s.terminalErr = s.watchFailure(err)
			s.mu.Unlock()
			s.signalEnd()
			return
		}
		changes := diffWatchSnapshots(s.spec, s.previous, current, time.Now())
		s.previous = current
		if err := backend.sync(s.spec, current); err != nil {
			s.mu.Lock()
			s.overflow = true
			s.mu.Unlock()
		}
		if overflow {
			s.mu.Lock()
			s.overflow = true
			s.mu.Unlock()
		}
		if len(changes) == 0 {
			s.scanMu.Unlock()
			continue
		}
		s.mu.Lock()
		select {
		case s.queue <- changes:
			s.queuedItems += len(changes)
		case <-ctx.Done():
			s.mu.Unlock()
			s.scanMu.Unlock()
			return
		default:
			// The consumer is behind. Drop the observation and require a
			// rescan instead of claiming the dropped changes were seen.
			s.overflow = true
		}
		s.mu.Unlock()
		s.scanMu.Unlock()
	}
}

// coalesce settles one notification burst: kernels split a single logical
// change into several events — and one rename into a from/to pair — so the
// driver waits the settle window and drains whatever else arrived before
// scanning once.
func (s *fileWatchSource) coalesce(backend nativeWatchBackend, first nativeWatchEvent) (bool, error) {
	overflow := first.overflow
	timer := time.NewTimer(s.settle)
	defer timer.Stop()
	<-timer.C
	for range watchSettleBurst {
		select {
		case next, ok := <-backend.events():
			if !ok {
				return overflow, fmt.Errorf("native watcher stopped unexpectedly")
			}
			if next.failure != nil {
				return overflow, next.failure
			}
			overflow = overflow || next.overflow
		default:
			return overflow, nil
		}
	}
	return overflow, nil
}

func (s *fileWatchSource) next(ctx context.Context) (any, bool, error) {
	for {
		s.mu.Lock()
		if s.finished {
			s.mu.Unlock()
			return nil, false, nil
		}
		if s.terminalErr != nil {
			s.finished = true
			err := s.terminalErr
			s.mu.Unlock()
			return nil, false, err
		}
		if s.overflow {
			s.mu.Unlock()
			s.scanMu.Lock()
			s.mu.Lock()
			if !s.overflow {
				s.mu.Unlock()
				s.scanMu.Unlock()
				continue
			}
			s.mu.Unlock()
			// A reported overflow is a new observation boundary. Rebase the
			// producer before returning it: notifications already in flight may
			// describe pre-overflow changes, and a tiny queue can otherwise fill
			// with those stale changes before the caller creates its next file.
			if s.backend != nil {
				current, err := scanWatchSnapshot(ctx, s.spec)
				if err == nil {
					err = s.backend.sync(s.spec, current)
				}
				if err != nil {
					s.scanMu.Unlock()
					return nil, false, err
				}
				s.previous = current
			}
			s.mu.Lock()
			// An overflow invalidates every earlier observation, including a
			// batch still occupying the queue. The consumer must rescan, and
			// freeing the queue lets future changes arrive after that rescan.
			s.overflow = false
			s.pending = nil
			for len(s.queue) > 0 {
				<-s.queue
			}
			s.queuedItems = 0
			s.mu.Unlock()
			s.scanMu.Unlock()
			return watchChange{path: s.spec.rootDisplay, rel: ".", kind: changeOverflowed, entryKind: otherKind, observed: time.Now()}.value(), true, nil
		}
		if len(s.pending) > 0 {
			change := s.pending[0]
			s.pending = s.pending[1:]
			s.mu.Unlock()
			return change.value(), true, nil
		}
		s.mu.Unlock()
		select {
		case batch := <-s.queue:
			s.mu.Lock()
			s.queuedItems -= len(batch)
			s.pending = batch
			s.mu.Unlock()
		case <-s.endCh:
			s.mu.Lock()
			if s.terminalErr == nil {
				s.finished = true
			}
			s.mu.Unlock()
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

// cancel stops the poller and waits for it to exit so closing a watcher
// releases its goroutine before the statement completes.
func (s *fileWatchSource) cancel(context.Context) error {
	s.stopOnce.Do(func() {
		s.stop()
		<-s.stoppedCh
		s.signalEnd()
	})
	return nil
}

func (s *fileWatchSource) requestStreamStop() error { return s.cancel(context.Background()) }

func (s *fileWatchSource) stopped() chan struct{} { return s.stoppedCh }
func (s *fileWatchSource) streamScope() (string, int, bool) {
	return s.spec.rootDisplay, s.spec.maxDepth, true
}

// streamMetrics reports buffered batches and items without consuming them.
func (s *fileWatchSource) streamMetrics() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) + s.queuedItems, 0
}

// ---- scan/watch/reconcile ----

// reconcileFileChange folds one observed change into a previous scan so
// scripts can hold a complete state without re-reading the folder. An
// overflowed change fails with FileWatchOverflow because the fold can no
// longer be known complete; the caller must rescan.
func reconcileFileChange(entries []any, change map[string]any) ([]any, error) {
	kind, _ := change["kind"].(string)
	rel, _ := change["relative_path"].(string)
	path, _ := change["path"].(string)
	entryKind, _ := change["entry_kind"].(string)
	remove := func(target string, subtree bool) []any {
		if target == "" {
			return entries
		}
		kept := make([]any, 0, len(entries))
		for _, item := range entries {
			if record, ok := item.(map[string]any); ok {
				path, _ := record["relative_path"].(string)
				if path == target || (subtree && strings.HasPrefix(path, target+"/")) {
					continue
				}
			}
			kept = append(kept, item)
		}
		return kept
	}
	insert := func() []any {
		if rel == "" {
			return entries
		}
		previous := ""
		if kind == changeMoved {
			previous, _ = change["previous_path"].(string)
		}
		kept := remove(previous, entryKind == folderKind)
		name := rel
		if index := strings.LastIndex(rel, "/"); index >= 0 {
			name = rel[index+1:]
		}
		depth := 1
		for _, part := range strings.Split(rel, "/") {
			if part == "" {
				depth--
			}
		}
		depth += strings.Count(rel, "/")
		entry := map[string]any{
			"path":          path,
			"relative_path": rel,
			"name":          name,
			"kind":          entryKind,
			"depth":         float64(depth),
			"symbolic_link": entryKind == string(linkKind),
		}
		kept = append(kept, entry)
		sort.SliceStable(kept, func(i, j int) bool {
			first, _ := kept[i].(map[string]any)["relative_path"].(string)
			second, _ := kept[j].(map[string]any)["relative_path"].(string)
			return first < second
		})
		return kept
	}
	switch kind {
	case changeCreated, changeMoved:
		return insert(), nil
	case changeModified:
		return entries, nil
	case changeRemoved:
		return remove(rel, entryKind == folderKind), nil
	case changeOverflowed:
		root, _ := change["path"].(string)
		return nil, fileFailure("FileWatchOverflow", fmt.Sprintf("watch of %s overflowed; rescan the folder to restore a complete state", root), map[string]any{"root": root})
	default:
		return nil, fmt.Errorf("cannot reconcile change kind %q", kind)
	}
}

func reconcileFileChangeAction(args []any) (any, error) {
	entries, ok := args[0].([]any)
	if !ok {
		return nil, fmt.Errorf("entries must be a list of FileEntry records")
	}
	change, ok := args[1].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("change must be a FileChange record")
	}
	return reconcileFileChange(append([]any(nil), entries...), change)
}

func watchFileTreeAction(ctx context.Context, opts Options, args []any) (streamSource, error) {
	options, err := traversalOptionsFromArgs(opts, args[0], args[1], args[2], args[3], args[4], args[5])
	if err != nil {
		return nil, err
	}
	spec, err := parseTraversalSpec(opts, options)
	if err != nil {
		return nil, err
	}
	return newFileWatchSource(ctx, spec, defaultWatchInterval, defaultWatchBatches)
}

func registerFilesystemWatchOps() {
	if stdRegistry["std/files"] == nil {
		stdRegistry["std/files"] = map[string]NativeOp{}
	}
	params := []NativeParam{
		{Name: "root", Type: "text"},
		{Name: "include", Type: "list"},
		{Name: "exclude", Type: "list"},
		{Name: "kinds", Type: "list"},
		{Name: "depth", Type: "integer"},
		{Name: "follow", Type: "boolean"},
	}
	stdRegistry["std/files"]["watch"] = NativeOp{
		Name:             "watch",
		Params:           params,
		Result:           "stream of FileChange",
		Targets:          []string{"native"},
		StreamFn:         watchFileTreeAction,
		Effects:          append([]string(nil), fileActionContract["watch"].effects...),
		PossibleFailures: append([]string(nil), fileActionContract["watch"].failures...),
	}
	stdRegistry["std/files"]["reconcile"] = NativeOp{
		Name:   "reconcile",
		Params: []NativeParam{{Name: "entries", Type: "list"}, {Name: "change", Type: "record"}},
		Result: "list",
		Fn:     reconcileFileChangeAction,
	}
	stdDocs["std/files.watch"] = stdDoc{"stream of FileChange", "Watches one folder recursively and streams FileChange records; an overflowed change means events were dropped and the consumer must rescan. Native only.", []string{"filesystem"}}
	stdDocs["std/files.reconcile"] = stdDoc{"list", "Folds one FileChange into a previous scan result so scripts keep a complete state between rescans; overflowed changes fail with FileWatchOverflow.", []string{"pure"}}
}

func init() { registerFilesystemWatchOps() }
