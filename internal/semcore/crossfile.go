package semcore

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Cross-file context closes the last blind spot in the unit model. A unit is
// one function, and a rule judging it can consult the rest of its file, but a
// key written in one module and read in another is invisible to both halves:
// each side looks correct on its own and the pair is broken.
//
// The index is built in the deterministic pass, so finding the related lines
// costs nothing. Only the lines themselves are sent, never whole files.

// CrossIndex holds every site in the run, grouped by the identifiers on its line.
type CrossIndex struct {
	byIdent map[string][]indexedSite
}

type indexedSite struct {
	File string
	Line int
	Text string
}

// reIdent matches identifiers and the dotted or quoted key fragments that
// actually tie two files together, such as a shared storage prefix.
var reIdent = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]{3,}(?:[.-][A-Za-z0-9_$]+)*`)

// commonWords never tie two files together, so indexing them turns the index
// into a list of everything.
var commonWords = map[string]bool{
	"function": true, "return": true, "const": true, "true": true, "false": true,
	"null": true, "this": true, "else": true, "catch": true, "error": true,
	"string": true, "number": true, "object": true, "value": true, "length": true,
	"window": true, "document": true, "console": true, "require": true, "import": true,
	"export": true, "default": true, "async": true, "await": true, "typeof": true,
	"undefined": true, "getItem": true, "setItem": true, "removeItem": true,
	"JSON": true, "parse": true, "stringify": true, "func": true, "nil": true,
	"range": true, "make": true, "append": true, "struct": true, "interface": true,
}

// identifiers returns the distinctive tokens in a piece of text.
func identifiers(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reIdent.FindAllString(s, -1) {
		if commonWords[m] {
			continue
		}
		out[m] = true
	}
	return out
}

// BuildCrossIndex indexes every unit's sites by the identifiers on their lines.
func BuildCrossIndex(units []Unit) *CrossIndex {
	ix := &CrossIndex{byIdent: map[string][]indexedSite{}}
	seen := map[string]bool{}
	for _, u := range units {
		for _, s := range u.Sites {
			key := u.File + ":" + itoa(s.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			site := indexedSite{File: u.File, Line: s.Line, Text: s.Text}
			for id := range identifiers(s.Text) {
				ix.byIdent[id] = append(ix.byIdent[id], site)
			}
		}
	}
	return ix
}

const crossFileCap = 25

// Related returns lines in other files that share a distinctive identifier with
// this unit. Shared identifiers are what make two files part of one mechanism:
// a key prefix, a helper name, a constant.
func (ix *CrossIndex) Related(u Unit) string {
	if ix == nil {
		return ""
	}
	want := identifiers(u.Code)
	type hit struct {
		site   indexedSite
		shared []string
	}
	byLoc := map[string]*hit{}
	for id := range want {
		for _, s := range ix.byIdent[id] {
			if s.File == u.File {
				continue // same file is already covered by the peer lines
			}
			key := s.File + ":" + itoa(s.Line)
			h, ok := byLoc[key]
			if !ok {
				h = &hit{site: s}
				byLoc[key] = h
			}
			h.shared = append(h.shared, id)
		}
	}
	if len(byLoc) == 0 {
		return ""
	}
	hits := make([]*hit, 0, len(byLoc))
	for _, h := range byLoc {
		sort.Strings(h.shared)
		hits = append(hits, h)
	}
	// The more identifiers two lines share, the more likely they are the same
	// mechanism rather than a coincidence of naming.
	sort.Slice(hits, func(i, j int) bool {
		if len(hits[i].shared) != len(hits[j].shared) {
			return len(hits[i].shared) > len(hits[j].shared)
		}
		if hits[i].site.File != hits[j].site.File {
			return hits[i].site.File < hits[j].site.File
		}
		return hits[i].site.Line < hits[j].site.Line
	})
	if len(hits) > crossFileCap {
		hits = hits[:crossFileCap]
	}
	var b strings.Builder
	for _, h := range hits {
		b.WriteString(filepath.Base(h.site.File))
		b.WriteString(":")
		b.WriteString(itoa(h.site.Line))
		b.WriteString("  ")
		b.WriteString(strings.TrimSpace(h.site.Text))
		b.WriteString("   [shares: ")
		b.WriteString(strings.Join(h.shared, ", "))
		b.WriteString("]\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
