package soslsp

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

// The sos/vocabulary custom method: an offline, read-only dictionary of every
// callable word available in one document context. The catalog payload is the
// core's shared sos.Vocabulary result verbatim (core-contract.md §1) plus its
// diagnostics; this package keeps no second registry. Editor features
// (completion, hover, tokens, hints) derive from the same call through
// wordTargets, so the dictionary and the editor can never disagree.

// vocabParams is the sos/vocabulary request. Source wins over textDocument
// when both are present; with neither, the workspace catalog alone is served.
type vocabParams struct {
	TextDocument *textDocumentIdentifier `json:"textDocument"`
	Source       *string                 `json:"source"`
	Query        string                  `json:"query"`
	Library      string                  `json:"library"`
}

// VocabularyResult is the sos/vocabulary@1 envelope: the core catalog plus
// its diagnostics (vocabulary collisions arrive here with their origins).
type VocabularyResult struct {
	Schema      string                 `json:"schema"`
	Root        string                 `json:"root,omitempty"`
	Catalog     *sos.VocabularyCatalog `json:"catalog"`
	Diagnostics []sos.Diagnostic       `json:"diagnostics"`
}

// sosVocabulary handles the custom sos/vocabulary request.
func (s *server) sosVocabulary(id json.RawMessage, params json.RawMessage) {
	var p vocabParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			s.writeError(id, codeInvalidParams, "invalid sos/vocabulary params: "+err.Error())
			return
		}
	}
	source, uri := s.resolveVocabContext(p)
	catalog, diags := s.vocabularyFor(uri, vocabFilename(uri, s.workspaceRoot), source)
	result := &VocabularyResult{
		Schema: "sos/vocabulary@1", Root: s.workspaceRoot,
		Catalog: filterVocabCatalog(catalog,
			strings.ToLower(strings.TrimSpace(p.Query)),
			strings.TrimSpace(p.Library)),
		Diagnostics: diags,
	}
	s.writeResult(id, result)
}

// resolveVocabContext picks the document context: inline source first, then
// an open document. Empty results serve the bare workspace catalog.
func (s *server) resolveVocabContext(p vocabParams) (source, uri string) {
	if p.Source != nil {
		return *p.Source, ""
	}
	if p.TextDocument != nil {
		if doc, open := s.docs[p.TextDocument.URI]; open {
			return doc.text, p.TextDocument.URI
		}
	}
	return "", ""
}

// vocabFilename maps a document URI to the filename the core resolves
// modules and configuration from: the real path for files on disk, else the
// workspace root's buffer so configuration still applies.
func vocabFilename(uri, workspaceRoot string) string {
	if path := uriToPath(uri); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if workspaceRoot != "" {
		return workspaceRoot + "/buffer.sos"
	}
	return ""
}

// vocabularyFor resolves the core catalog for one document, caching per URI
// and text so keystroke-driven features share one resolution.
func (s *server) vocabularyFor(uri, filename, text string) (*sos.VocabularyCatalog, []sos.Diagnostic) {
	if uri != "" {
		if c, ok := s.vocabCache[uri]; ok && c.text == text {
			return c.catalog, c.diags
		}
	}
	catalog, diags := sos.Vocabulary(filename, text)
	if uri != "" {
		if s.vocabCache == nil {
			s.vocabCache = map[string]*vocabCacheEntry{}
		}
		s.vocabCache[uri] = &vocabCacheEntry{text: text, catalog: catalog, diags: diags}
	}
	return catalog, diags
}

type vocabCacheEntry struct {
	text    string
	catalog *sos.VocabularyCatalog
	diags   []sos.Diagnostic
}

// VocabularyRequest selects a catalog: a document context plus optional
// server-side filters. Clients may equally filter locally: a request without
// Query returns the full catalog.
type VocabularyRequest struct {
	Filename string
	Source   string
	Query    string // lowercased substring filter
	Library  string // restrict to one import path
}

// Catalog assembles the vocabulary catalog for one root without a live
// editor session. It backs the CLI dictionary command; root may be empty for
// the builtin catalog alone.
func Catalog(root string, req VocabularyRequest) *VocabularyResult {
	catalog, diags := sos.Vocabulary(req.Filename, req.Source)
	result := &VocabularyResult{
		Schema:      "sos/vocabulary@1",
		Root:        root,
		Catalog:     filterVocabCatalog(catalog, req.Query, req.Library),
		Diagnostics: diags,
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []sos.Diagnostic{}
	}
	return result
}

// filterVocabCatalog applies the optional server-side query/library filters.
// Filters never fabricate entries: they only narrow.
func filterVocabCatalog(c *sos.VocabularyCatalog, query, library string) *sos.VocabularyCatalog {
	if c == nil {
		return &sos.VocabularyCatalog{Entries: []sos.VocabularyEntry{}, Libraries: []sos.VocabularyLibrary{}}
	}
	entries := c.Entries
	libs := c.Libraries
	if library != "" {
		entries = filterEntries(entries, func(e sos.VocabularyEntry) bool { return e.Library == library })
		libs = filterLibs(libs, func(l sos.VocabularyLibrary) bool { return l.Path == library })
	}
	if query != "" {
		entries = filterEntries(entries, func(e sos.VocabularyEntry) bool { return vocabEntryMatches(e, query) })
	}
	if entries == nil {
		entries = []sos.VocabularyEntry{}
	}
	if libs == nil {
		libs = []sos.VocabularyLibrary{}
	}
	return &sos.VocabularyCatalog{Entries: entries, Libraries: libs, Definitions: append([]sos.RecordDef(nil), c.Definitions...), Failures: append([]sos.FailureDef(nil), c.Failures...)}
}

func filterEntries(entries []sos.VocabularyEntry, keep func(sos.VocabularyEntry) bool) []sos.VocabularyEntry {
	var out []sos.VocabularyEntry
	for _, e := range entries {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func filterLibs(libs []sos.VocabularyLibrary, keep func(sos.VocabularyLibrary) bool) []sos.VocabularyLibrary {
	var out []sos.VocabularyLibrary
	for _, l := range libs {
		if keep(l) {
			out = append(out, l)
		}
	}
	return out
}

// vocabEntryMatches reports whether an entry addresses the lowercased query
// by name, synonym, pattern, description, or library.
func vocabEntryMatches(e sos.VocabularyEntry, query string) bool {
	hay := []string{e.Name, e.Library, e.Alias, e.ID, e.Description, e.Result}
	hay = append(hay, e.Synonyms...)
	hay = append(hay, e.Patterns...)
	for _, h := range hay {
		if h != "" && strings.Contains(strings.ToLower(h), query) {
			return true
		}
	}
	return false
}
