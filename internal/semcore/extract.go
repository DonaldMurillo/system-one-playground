package semcore

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Site is one place a rule's selector matched.
type Site struct {
	RuleID string `json:"rule_id"`
	Line   int    `json:"line"` // 1-indexed
	Text   string `json:"text"` // the matching line, trimmed
	// Unit is the enclosing unit's text, so a deterministic check can ask
	// whether something is absent from the whole function rather than one line.
	Unit string `json:"unit"`
}

// Unit is the span of code a batch of questions is asked about: the function
// enclosing one or more sites, plus the file's header comment so that rules
// about documented intent have something to compare against.
type Unit struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Code      string `json:"code"`
	Header    string `json:"header"` // the file's leading comment block
	// Enclosing is the block one level out, minus the unit itself.
	Enclosing string `json:"enclosing"`
	// Peers are every other line in the file that the same rules matched. A
	// comment claiming something about "every sink" cannot be checked from one
	// sink, and a rule that cannot check a claim must not report it.
	Peers string `json:"peers"`
	// Cut records every place context was dropped to fit a budget. A rule
	// answering from partial evidence is the worst failure this tool has: it
	// reports confidently about code it was never shown. Whatever is listed
	// here is told to the model so it can abstain instead.
	Cut []string `json:"cut"`
	// Module is the file's top-level constants and helper definitions. Without
	// it a rule judges a function blind to the helpers it calls: three findings
	// on correct code disappeared once the key-building helper came along.
	Module string `json:"module"`
	Sites  []Site `json:"sites"`
}

// RuleIDs returns the distinct rules with a site in this unit.
func (u Unit) RuleIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range u.Sites {
		if !seen[s.RuleID] {
			seen[s.RuleID] = true
			out = append(out, s.RuleID)
		}
	}
	sort.Strings(out)
	return out
}

// SiteLines describes where each rule matched, for the question to point at.
func (u Unit) SiteLines(ruleID string) []int {
	var out []int
	for _, s := range u.Sites {
		if s.RuleID == ruleID {
			out = append(out, s.Line)
		}
	}
	return out
}

const (
	maxUnitLines = 220
	peerCap      = 40
)

// located is one site plus the block that encloses it.
type located struct {
	site  Site
	start int
	end   int
	// windowed is true when no enclosing block fitted and the unit is a plain
	// line window, which means the surrounding function was cut away.
	windowed bool
}

// blockOpeners holds the per-language pattern for a line that opens a block
// worth treating as a unit. This must be language-aware: a JavaScript-only
// pattern silently matched nothing in Go, so every Go unit fell back to a line
// window that straddled function boundaries and findings landed on unrelated
// code. Recall on a Go fixture was 3 of 12 until this was fixed.
var blockOpeners = map[string]*regexp.Regexp{
	".go": regexp.MustCompile(`^\s*func\s`),
	".js": regexp.MustCompile(`(^|\s|=|\(|,)(function\b|\([^)]*\)\s*=>|async\b)|^\s*(export\s+)?(async\s+)?function\b|^\s*[A-Za-z_$][\w$]*\s*\([^)]*\)\s*\{`),
}

// openerFor returns the block pattern for a file type, defaulting to the
// JavaScript-family one for .ts and .mjs.
func openerFor(ext string) *regexp.Regexp {
	if re, ok := blockOpeners[ext]; ok {
		return re
	}
	return blockOpeners[".js"]
}

var (
	reLineComment  = regexp.MustCompile(`//.*$`)
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reStringLit    = regexp.MustCompile(`'(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|` + "`(?:[^`\\\\]|\\\\.)*`")
)

// ExtractUnits finds every site in a file and groups them into units.
// This is the deterministic half of the framework: no model is involved, so
// scanning a whole repository for candidate sites costs nothing.
func ExtractUnits(path string, active []Rule) ([]Unit, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ExtractText(path, string(raw), active), nil
}

// ExtractText extracts units without accessing the filesystem. Path is a language/location label.
func ExtractText(path, src string, active []Rule) []Unit {
	ext := filepath.Ext(path)
	switch ext {
	case ".mjs", ".cjs":
		ext = ".js"
	}
	opener := openerFor(ext)
	lines := strings.Split(src, "\n")
	depth := braceDepths(lines)

	// Collect sites from every rule whose selector matches this file type.
	var found []located
	for _, r := range active {
		if r.Selector.Ext != "" && r.Selector.Ext != ext {
			continue
		}
		for i, line := range lines {
			// Match the raw line, not the comment-stripped one. Stripping is
			// for brace counting; using it here made every rule whose site
			// lives in a comment, such as the TODO rules, silently unmatchable.
			if !r.Selector.Site.MatchString(line) {
				continue
			}
			start, end, windowed := enclosingBlock(lines, depth, i, opener)
			found = append(found, located{
				site:  Site{RuleID: r.ID, Line: i + 1, Text: strings.TrimSpace(line)},
				start: start, end: end,
				windowed: windowed,
			})
		}
	}
	if len(found) == 0 {
		return nil
	}

	// Sites sharing an enclosing block share a unit, so one request covers
	// every rule that has anything to say about that function.
	byBlock := map[[2]int][]Site{}
	windowed := map[[2]int]bool{}
	for _, f := range found {
		key := [2]int{f.start, f.end}
		byBlock[key] = append(byBlock[key], f.site)
		if f.windowed {
			windowed[key] = true
		}
	}

	header := headerComment(lines)
	module, moduleTruncated := moduleScope(lines, depth)
	var units []Unit
	for key, sites := range byBlock {
		sort.Slice(sites, func(i, j int) bool { return sites[i].Line < sites[j].Line })
		u := Unit{
			File:      path,
			StartLine: key[0] + 1,
			EndLine:   key[1] + 1,
			Code:      numbered(lines, key[0], key[1]),
			Header:    header,
			Module:    module,
			Enclosing: enclosingScope(lines, depth, key[0], key[1], opener),
			Peers:     peerSites(lines, found2sites(found), key[0], key[1]),
			Sites:     sites,
		}
		if windowed[key] {
			u.Cut = append(u.Cut, fmt.Sprintf(
				"The enclosing function was larger than %d lines, so `code` is only a window around the marked lines. Code above and below it, including any enclosing guard, is not shown.", maxUnitLines))
		}
		if moduleTruncated {
			u.Cut = append(u.Cut, "`module_scope_declarations` lists only the first declarations in the file; later ones are not shown.")
		}
		if len(strings.Split(u.Peers, "\n")) >= peerCap {
			u.Cut = append(u.Cut, "`other_matching_lines_in_file` lists only some of the matching lines; there may be more.")
		}
		units = append(units, u)
	}
	// Give each site its unit's text so unit-level checks can run without the
	// model and without a second pass.
	for i := range units {
		for j := range units[i].Sites {
			units[i].Sites[j].Unit = units[i].Code
		}
	}
	sort.Slice(units, func(i, j int) bool { return units[i].StartLine < units[j].StartLine })
	return units
}

// found2sites flattens every located site in the file.
func found2sites(found []located) []Site {
	out := make([]Site, 0, len(found))
	for _, f := range found {
		out = append(out, f.site)
	}
	return out
}

// peerSites lists the matching lines elsewhere in the file, so a rule judging
// a claim about the whole file can see the rest of it.
func peerSites(lines []string, sites []Site, unitStart, unitEnd int) string {
	seen := map[int]bool{}
	var out []string
	for _, s := range sites {
		i := s.Line - 1
		if i >= unitStart && i <= unitEnd {
			continue // already in the unit
		}
		if seen[i] || i < 0 || i >= len(lines) {
			continue
		}
		seen[i] = true
		out = append(out, padLineNo(s.Line)+strings.TrimRight(lines[i], " \t"))
		if len(out) >= peerCap {
			break
		}
	}
	return strings.Join(out, "\n")
}

// stripNoise removes comments and string literals so a match or a brace inside
// them does not count. It is a lexical approximation, not a parser: a brace
// inside a regex literal can still skew the depth, which only widens a unit.
func stripNoise(s string) string {
	s = reBlockComment.ReplaceAllString(s, "")
	s = reStringLit.ReplaceAllString(s, `""`)
	s = reLineComment.ReplaceAllString(s, "")
	return s
}

// braceDepths returns the brace depth at the start of each line.
func braceDepths(lines []string) []int {
	depth := make([]int, len(lines)+1)
	d := 0
	for i, line := range lines {
		depth[i] = d
		c := stripNoise(line)
		d += strings.Count(c, "{") - strings.Count(c, "}")
		if d < 0 {
			d = 0
		}
	}
	depth[len(lines)] = d
	return depth
}

// enclosingBlock finds the innermost function-looking block containing line i,
// falling back to a window when the file has no clear structure. The unit stays
// as small as the code allows so a finding points at one function; the context
// a small function is missing arrives separately, as enclosing and module scope.
func enclosingBlock(lines []string, depth []int, i int, opener *regexp.Regexp) (int, int, bool) {
	for start := i; start >= 0; start-- {
		if !opener.MatchString(stripNoise(lines[start])) {
			continue
		}
		open := depth[start]
		if depth[i] <= open && start != i {
			continue // this block closes before the site
		}
		for e := start + 1; e < len(lines); e++ {
			if depth[e] <= open && e > start+1 {
				if e >= i && e-start <= maxUnitLines {
					return withLeadingComment(lines, start), e, false
				}
				break
			}
		}
	}
	const window = 30
	return max(0, i-window), min(len(lines)-1, i+window), true
}

// enclosingScope returns the block one level outside the unit, minus the unit
// itself. A short helper often relies on a guard, listener or cleanup its
// parent installs, and judging the helper alone reports that as missing.
func enclosingScope(lines []string, depth []int, unitStart, unitEnd int, opener *regexp.Regexp) string {
	if unitStart == 0 {
		return ""
	}
	inner := depth[unitStart]
	for start := unitStart - 1; start >= 0; start-- {
		if depth[start] >= inner {
			continue
		}
		if !opener.MatchString(stripNoise(lines[start])) {
			continue
		}
		open := depth[start]
		for e := start + 1; e < len(lines); e++ {
			if depth[e] <= open && e > start+1 {
				if e < unitEnd {
					return ""
				}
				var out []string
				for j := start; j <= e && j < len(lines); j++ {
					if j >= unitStart && j <= unitEnd {
						if j == unitStart {
							out = append(out, "       ... the lines under review appear above ...")
						}
						continue
					}
					out = append(out, padLineNo(j+1)+strings.TrimRight(lines[j], " \t"))
					if len(out) > 70 {
						return strings.Join(out, "\n")
					}
				}
				return strings.Join(out, "\n")
			}
		}
		break
	}
	return ""
}

// withLeadingComment extends a unit upward over the comment block above it,
// because a rule about documented intent needs the documentation.
func withLeadingComment(lines []string, start int) int {
	s := start
	for s > 0 {
		prev := strings.TrimSpace(lines[s-1])
		if strings.HasPrefix(prev, "//") || strings.HasPrefix(prev, "*") ||
			strings.HasPrefix(prev, "/*") || strings.HasSuffix(prev, "*/") {
			s--
			continue
		}
		break
	}
	return s
}

// reModuleDecl matches a top-level constant or helper definition.
var reModuleDecl = regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var|function|class)\s+[A-Za-z_$]`)

// reModuleDeclGo matches a Go package-level declaration.
var reModuleDeclGo = regexp.MustCompile(`^\s*(?:const|var|type|func)\s+[A-Za-z_(]`)

// moduleScope collects the file's top-level declarations. A rule asking about
// a storage key needs to see the constant that builds it, and that constant is
// almost never inside the function being judged.
func moduleScope(lines []string, depth []int) (string, bool) {
	var out []string
	truncated := false
	budget := 90
	for i, line := range lines {
		if depth[i] > 1 {
			continue
		}
		if len(out) >= budget {
			truncated = true
			break
		}
		if !reModuleDecl.MatchString(stripNoise(line)) && !reModuleDeclGo.MatchString(stripNoise(line)) {
			continue
		}
		out = append(out, padLineNo(i+1)+strings.TrimRight(line, " \t"))
		// A short single-line definition carries its whole meaning; a longer
		// one contributes its signature, which is enough to resolve a call.
		if strings.Count(stripNoise(line), "{") > strings.Count(stripNoise(line), "}") {
			for j := i + 1; j < len(lines) && j < i+4; j++ {
				out = append(out, padLineNo(j+1)+strings.TrimRight(lines[j], " \t"))
				if depth[j+1] <= depth[i] {
					break
				}
			}
		}
	}
	return strings.Join(out, "\n"), truncated
}

// headerComment returns the file's opening comment block, which is where this
// codebase records the invariants each module claims to uphold.
func headerComment(lines []string) string {
	var out []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" && len(out) == 0 {
			continue
		}
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") {
			out = append(out, t)
			continue
		}
		break
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return strings.Join(out, "\n")
}

// numbered renders a span with line numbers so a finding can name a real line
// and the model can refer to `site_line` precisely.
func numbered(lines []string, start, end int) string {
	var b strings.Builder
	for i := start; i <= end && i < len(lines); i++ {
		b.WriteString(padLineNo(i + 1))
		b.WriteString(lines[i])
		b.WriteByte('\n')
	}
	return b.String()
}

func padLineNo(n int) string {
	s := "     "
	d := itoa(n)
	if len(d) >= len(s) {
		return d + ": "
	}
	return s[:len(s)-len(d)] + d + ": "
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
