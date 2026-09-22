package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/internal/soslsp"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

const vocabularyUsage = `usage: sos vocabulary [FILE.sos] [--json] [--query TEXT] [--library PATH]

Offline dictionary of callable vocabulary. Reports the standard library
operations and the workspace's local packages, with the sentence forms
actually enabled by FILE's imports (bare words for open imports,
alias-qualified calls for aliased imports) or by the resolved configuration
when no FILE is given. Vocabulary collisions and import problems are listed
with their origins. Fully offline: no model, no network, no requests.
`

// cmdVocabulary prints the shared vocabulary catalog as text or as the
// sos/vocabulary@1 JSON envelope. Exit codes: 0 success (collisions are
// reported data, not failures), 1 read failure, 2 usage error.
func cmdVocabulary(args []string, stdout, stderr io.Writer) int {
	var jsonOut bool
	var query, library, file string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			jsonOut = true
		case arg == "--query" || arg == "--library":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "sos: %s requires a value\n%s", arg, vocabularyUsage)
				return 2
			}
			i++
			if arg == "--query" {
				query = args[i]
			} else {
				library = args[i]
			}
		case strings.HasPrefix(arg, "--query="):
			query = strings.TrimPrefix(arg, "--query=")
		case strings.HasPrefix(arg, "--library="):
			library = strings.TrimPrefix(arg, "--library=")
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, vocabularyUsage)
			return 0
		case strings.HasPrefix(arg, "--"):
			fmt.Fprintf(stderr, "sos: unknown flag %q\n%s", arg, vocabularyUsage)
			return 2
		default:
			if file != "" {
				fmt.Fprintf(stderr, "sos: one file at most\n%s", vocabularyUsage)
				return 2
			}
			file = arg
		}
	}
	source := ""
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "sos: %v\n", err)
		return 1
	}
	filename := ""
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(stderr, "sos: %v\n", err)
			return 1
		}
		source = string(b)
		if abs, err := filepath.Abs(file); err == nil {
			filename = abs
			root = filepath.Dir(abs)
		}
	}
	result := soslsp.Catalog(root, soslsp.VocabularyRequest{
		Filename: filename,
		Source:   source,
		Query:    strings.ToLower(strings.TrimSpace(query)),
		Library:  strings.TrimSpace(library),
	})
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(stderr, "sos: %v\n", err)
			return 1
		}
		return 0
	}
	printVocabularyText(stdout, file, result)
	return 0
}

// printVocabularyText renders the catalog grouped by library, then the
// diagnostics block (collisions included, with their origins).
func printVocabularyText(w io.Writer, file string, r *soslsp.VocabularyResult) {
	if file != "" {
		fmt.Fprintf(w, "vocabulary for %s\n", file)
	} else {
		fmt.Fprintln(w, "vocabulary for this workspace")
	}
	if r.Root != "" {
		fmt.Fprintf(w, "root: %s\n", r.Root)
	}
	fmt.Fprintln(w)
	c := r.Catalog
	if len(c.Libraries) == 0 && len(c.Entries) == 0 {
		fmt.Fprintln(w, "no libraries found")
		return
	}
	seen := map[string]bool{}
	for _, lib := range c.Libraries {
		seen[lib.Path] = true
		fmt.Fprintf(w, "%s  [%s]\n", lib.Path, lib.Origin)
		if lib.Error != "" {
			fmt.Fprintf(w, "  error: %s\n", lib.Error)
		}
		if lib.Bare {
			fmt.Fprintln(w, "  open import: bare sentences enabled")
			if lib.Alias != "" {
				fmt.Fprintf(w, "  qualifier: %s.\n", lib.Alias)
			}
		} else if lib.Alias != "" {
			fmt.Fprintf(w, "  aliased: calls require the %s. prefix\n", lib.Alias)
		}
		printLibraryEntries(w, c.Entries, lib.Path)
		fmt.Fprintln(w)
	}
	// Preview entries whose library has no binding row still print.
	for _, e := range c.Entries {
		if !seen[e.Library] {
			seen[e.Library] = true
			fmt.Fprintf(w, "%s  [%s · preview]\n", e.Library, e.Origin)
			printLibraryEntries(w, c.Entries, e.Library)
			fmt.Fprintln(w)
		}
	}
	if len(r.Diagnostics) > 0 {
		fmt.Fprintln(w, "diagnostics:")
		for _, d := range r.Diagnostics {
			fmt.Fprintf(w, "  line %d:%d: %s\n", d.Line, d.Column, d.Message)
		}
	}
}

func printLibraryEntries(w io.Writer, entries []sos.VocabularyEntry, path string) {
	for _, e := range entries {
		if e.Library != path {
			continue
		}
		fmt.Fprintf(w, "  %s\n", e.Name)
		if len(e.Patterns) > 0 {
			fmt.Fprintf(w, "    patterns: %s\n", strings.Join(e.Patterns, " · "))
		}
		if sig := entrySig(e); sig != "" {
			fmt.Fprintf(w, "    %s\n", sig)
		}
		if e.Description != "" {
			fmt.Fprintf(w, "    %s\n", e.Description)
		}
		if len(e.Synonyms) > 0 {
			fmt.Fprintf(w, "    synonyms: %s\n", strings.Join(e.Synonyms, ", "))
		}
		if len(e.Effects) > 0 {
			fmt.Fprintf(w, "    effects: %s\n", strings.Join(e.Effects, ", "))
		}
		if len(e.PossibleFailures) > 0 {
			fmt.Fprintf(w, "    may fail with: %s\n", strings.Join(e.PossibleFailures, ", "))
		}
		if len(e.Targets) > 0 {
			fmt.Fprintf(w, "    targets: %s\n", strings.Join(e.Targets, ", "))
		}
	}
}

// entrySig renders "name(arg as type, …) → result" for text mode.
func entrySig(e sos.VocabularyEntry) string {
	params := make([]string, 0, len(e.Params))
	for _, p := range e.Params {
		params = append(params, p.Name+" as "+p.Type)
	}
	if len(params) == 0 && e.Result == "" {
		return ""
	}
	sig := e.Name + "(" + strings.Join(params, ", ") + ")"
	if e.Result != "" {
		sig += " → " + e.Result
	}
	return sig
}
