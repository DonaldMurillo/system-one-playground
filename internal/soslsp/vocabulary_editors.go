package soslsp

import (
	"regexp"
	"slices"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

// Editor awareness of the open-imports vs aliases vocabulary model. One
// derivation — wordTargets — feeds completion, hover, semantic tokens, inlay
// hints, and auto-import edits; it shares vocabularyFor with the
// sos/vocabulary method, so every feature reports the catalog's truth.

// wordTarget is one callable word in one document context. Bare and Qualifier
// carry the forms actually usable here: open imports expose the bare sentence
// plus the default-alias qualifier; aliased imports require their qualifier.
type wordTarget struct {
	ImportPath       string
	Name             string // canonical operation name
	Enabled          bool   // usable in this file right now
	Bare             bool   // bare sentence form usable (open import)
	Qualifier        string // usable qualified prefix ("" when not usable)
	Params           []sos.VocabularyParam
	Result           string // result type when known
	PossibleFailures []string
	Effects          []string
	Targets          []string
	Doc              string   // prose
	Origin           string   // core origin string: import / config <layer> / standard library / local <path>
	ImportLine       string   // enabling import statement for auto-import edits
	Synonyms         []string // extra bare words for the same operation
}

// reSentLine matches a sentence-call head: a bare word or alias.word at
// statement start. Vocabulary resolution, not this regex, decides validity.
var reSentLine = regexp.MustCompile(`^([A-Za-z_]\w*(?:\.[A-Za-z_]\w*)?)(?:[\s:]|$)`)

// wordTargets derives every catalog word of one source — enabled and
// available — through the shared core resolution. Invalid libraries cannot
// contribute guessed operations from a second index.
func (s *server) wordTargets(uri, filename, source string) []wordTarget {
	catalog, _ := s.vocabularyFor(uri, filename, source)
	var out []wordTarget
	for _, e := range catalog.Entries {
		out = append(out, wordTarget{
			ImportPath: e.Library, Name: e.Name, Enabled: e.Enabled,
			Bare: hasBarePattern(e), Qualifier: qualifierOf(e),
			Params: e.Params, Result: e.Result,
			PossibleFailures: e.PossibleFailures,
			Effects:          append([]string(nil), e.Effects...), Targets: append([]string(nil), e.Targets...),
			Doc: e.Description, Origin: e.Origin, ImportLine: e.Import,
			Synonyms: e.Synonyms,
		})
	}
	return out
}

// hasBarePattern reports whether an entry's usable forms include the bare
// sentence (open import): a pattern starting at the canonical name.
func hasBarePattern(e sos.VocabularyEntry) bool {
	if !e.Enabled {
		return false
	}
	prefix := e.Name + " "
	for _, p := range e.Patterns {
		if p == e.Name || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// qualifierOf extracts the usable qualified prefix from an entry's patterns:
// the "alias." of an "alias.name VALUE" pattern.
func qualifierOf(e sos.VocabularyEntry) string {
	if !e.Enabled {
		return ""
	}
	for _, pattern := range e.Patterns {
		head := strings.Fields(pattern)
		if len(head) == 0 {
			continue
		}
		alias, name, ok := strings.Cut(head[0], ".")
		if ok && name == e.Name {
			return alias
		}
	}
	// External/module actions may expose no sentence patterns because they are
	// called only through explicit `call`/`stream` syntax. Their resolved import
	// alias is still the valid qualifier.
	return e.Alias
}

// entrySignature renders an entry's callable signature, e.g.
// "trim(value as text) → text".
func entrySignature(name string, params []sos.VocabularyParam, result string, failures []string) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, p.Name+" as "+p.Type)
	}
	sig := name + "(" + strings.Join(parts, ", ") + ")"
	if result != "" {
		sig += " → " + result
	}
	if len(failures) > 0 {
		sig += " may fail with " + strings.Join(failures, ", ")
	}
	return sig
}

// sentHead returns the sentence-call name at the head of a trimmed statement
// line, or "".
func sentHead(code string) string {
	if m := reSentLine.FindStringSubmatch(code); m != nil {
		return m[1]
	}
	return ""
}

// resolveSent maps a sentence-call head to its word target in this context:
// bare words resolve only when the open-import vocabulary exposes them;
// alias-qualified heads resolve through the qualifier binding. Aliased
// libraries never resolve bare heads — the prefix is mandatory.
func resolveSent(head string, targets []wordTarget) *wordTarget {
	if alias, word, qualified := strings.Cut(head, "."); qualified {
		for i := range targets {
			t := &targets[i]
			if t.Enabled && t.Qualifier == alias && (t.Name == word || slices.Contains(t.Synonyms, word)) {
				return t
			}
		}
		return nil
	}
	for i := range targets {
		t := &targets[i]
		if t.Enabled && t.Bare && (t.Name == head || slices.Contains(t.Synonyms, head)) {
			return t
		}
	}
	return nil
}
