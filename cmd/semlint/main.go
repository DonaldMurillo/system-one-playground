// Command semlint is a semantic linter: a battery of rules that each pair a
// deterministic site pattern with one judgment a regex cannot make.
//
// Ordinary linters answer questions with a shape: is there a try/catch, is
// this identifier unused, does this file end in a newline. The rules here
// answer questions without one: does this guard actually keep the feature
// working, is this comment still true, would these two tabs disagree. Each
// rule finds its candidate sites in code, for free, then asks the model only
// about what it found.
//
// Rules are data, in rules/*.json. Two sets ship with the linter: "default",
// which is general purpose and covers Go, JavaScript, TypeScript and Python,
// and "browser-storage". Your own file can add rules or override a shipped one
// by redefining its id.
//
//	semlint ./src                                   # the default set
//	semlint -sets=all ./src                         # every shipped set
//	semlint -rules-file=./my-rules.json ./src       # your rules too
//	semlint -sites-only ./src                       # deterministic pass, no API
//	semlint -list                                   # what would run
//	semlint -format=hints ./src                     # shaped for an agent
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/typesafe"
)

func main() {
	format := flag.String("format", "text", "text, json, or hints (compact, for an agent's context)")
	ruleList := flag.String("rules", "", "comma-separated rule ids to keep; default is all in the active sets")
	sets := flag.String("sets", "default", "comma-separated built-in rule sets, or \"all\"")
	rulesFile := flag.String("rules-file", "", "comma-separated paths to your own rules files, added to the sets")
	maxUnits := flag.Int("max-units", 400, "refuse to run beyond this many units; 0 disables the guard")
	diffPath := flag.String("diff", "", "lint only units a unified diff touches; a path, or - for standard input")
	since := flag.String("since", "", "lint only units changed since this git ref; empty ref with -since=HEAD means the working tree")
	onlyChanged := flag.Bool("changed-lines-only", false, "with a diff, report only findings whose line the diff actually touched, not everything in a touched unit")
	calibrate := flag.Bool("calibrate", false, "report every rule's raw probability instead of applying thresholds")
	separation := flag.Bool("separation", false, "measure each rule's defective and correct populations against labelled fixtures and report the gap")
	sepRuns := flag.Int("runs", 3, "with -separation, how many times to measure each fixture")
	sitesOnly := flag.Bool("sites-only", false, "run the deterministic pass only and report candidate sites")
	listRules := flag.Bool("list", false, "list the rule battery and exit")
	minConf := flag.Float64("min-confidence", 0, "drop findings below this confidence")
	severity := flag.String("severity", "", "only report at or above this severity: hint, warning, error")
	workers := flag.Int("workers", 8, "concurrent requests")
	model := flag.String("model", "", "TypeSafe model override")
	timeout := flag.Duration("timeout", 3*time.Minute, "overall deadline")
	flag.Usage = usage
	flag.Parse()

	all, err := loadRules(*sets, *rulesFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "semlint: %v\n", err)
		os.Exit(2)
	}
	selected := selectRules(all, *ruleList)
	if *calibrate || *separation {
		// Calibration mode: drop every threshold so the raw distribution is
		// visible. Read both a defective and a correct sample, then put the
		// threshold in the gap between them.
		for i := range selected {
			if selected[i].Fires != nil {
				selected[i].Fires = noulFires(0.01)
			}
		}
	}
	reg := newRegistry(selected)
	if *listRules {
		printRules(reg)
		return
	}
	if len(reg.rules) == 0 {
		fmt.Fprintln(os.Stderr, "semlint: no rules selected")
		os.Exit(2)
	}

	// A diff narrows the work to what changed, which is the difference between
	// thirteen thousand units and the handful a commit touches.
	cwd, _ := os.Getwd()
	var changed ChangedLines
	switch {
	case *diffPath != "":
		changed, err = LoadDiff(*diffPath, cwd)
	case *since != "":
		ref := *since
		if ref == "HEAD" || ref == "working-tree" {
			ref = ""
		}
		changed, err = DiffFromGit(ref, cwd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "semlint: %v\n", err)
		os.Exit(2)
	}

	args := flag.Args()
	if len(args) == 0 && changed != nil {
		// With a diff and no paths, the changed files are the paths.
		args = changed.Files()
	}
	paths, err := expandPaths(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "semlint: %v\n", err)
		os.Exit(2)
	}
	if len(paths) == 0 {
		if changed != nil {
			fmt.Println("no lintable files in the diff")
			return
		}
		fmt.Fprintln(os.Stderr, "semlint: no input files; pass files or directories")
		os.Exit(2)
	}

	if *sitesOnly {
		reportSites(paths, reg, changed)
		return
	}

	loadDotEnv(".env")
	client, err := typesafe.New(typesafe.WithTimeout(60 * time.Second))
	if err != nil {
		fmt.Fprintf(os.Stderr, "semlint: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if *separation {
		if err := RunSeparation(ctx, client, paths, reg, *workers, *sepRuns, *model); err != nil {
			fmt.Fprintf(os.Stderr, "semlint: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *maxUnits > 0 {
		if n := countUnits(paths, reg, changed); n > *maxUnits {
			fmt.Fprintf(os.Stderr,
				"semlint: %d units would be judged, above the -max-units limit of %d.\n"+
					"Narrow the paths, pick fewer rules, or raise the limit deliberately.\n"+
					"Run with -sites-only to see what would be asked, for free.\n", n, *maxUnits)
			os.Exit(2)
		}
	}

	findings, st, err := Lint(ctx, client, paths, reg, *workers, *model, changed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "semlint: %v\n", err)
		os.Exit(1)
	}
	findings = filterFindings(findings, *minConf, *severity)
	if *onlyChanged && changed != nil {
		// A unit the diff touches can carry findings about code the diff did
		// not, which is useful in review and noise in a commit hook.
		var kept []Finding
		for _, f := range findings {
			if changed.Touches(f.File, f.Line, f.Line) {
				kept = append(kept, f)
			}
		}
		findings = kept
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"findings": findings, "stats": statsJSON(st), "skipped": st.Skipped})
	case "hints":
		printHints(findings, st)
	default:
		printText(findings, st, len(reg.ids()))
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
}

// usage explains what the tool is before listing flags. A bare flag dump tells
// a reader nothing about what semlint checks or which flags matter, and an
// agent handed this output has to guess.
func usage() {
	w := flag.CommandLine.Output()
	fmt.Fprint(w, `semlint - a semantic linter

Finds defects an ordinary linter cannot express: a comment that is no longer
true, a guard that does not actually guard, a value read back without being
validated, a key written one way and read another. Each rule pairs a regex
that finds candidate lines with one question a regex cannot answer.

USAGE
  semlint [flags] [files or directories]

COMMON USES
  semlint -sets=all .                       lint a tree with every rule
  semlint -sets=all -since=HEAD             only what you have changed
  semlint -sets=all -since=HEAD~1           only what the last commit changed
  git diff | semlint -sets=all -diff=-      only what a diff touches
  semlint -sets=all -format=hints .         output shaped for an agent
  semlint -list                             show the rules that would run
  semlint -sites-only .                     what would be asked, free, no API

NOTES
  -sets defaults to "default" (general rules). Use -sets=all to include the
  browser-storage rules as well, or -list to see what is active.
  Exit status is 1 when anything is found, 0 when nothing is.
  Needs TYPESAFE_API_KEY in the environment, a .env file, or
  ~/.config/semlint/env.
  Findings carry a confidence. Two rules ship as hints because their
  populations overlap; raise -severity or -min-confidence to filter.

FLAGS
`)
	flag.PrintDefaults()
}

// loadRules compiles the requested built-in sets plus any rules files given.
// A later definition of the same rule id replaces an earlier one, so a project
// can override a shipped rule by redefining it in its own file.
func loadRules(sets, files string) ([]Rule, error) {
	var out []Rule
	add := func(rs []Rule) {
		for _, r := range rs {
			replaced := false
			for i := range out {
				if out[i].ID == r.ID && out[i].Selector.Ext == r.Selector.Ext {
					out[i], replaced = r, true
					break
				}
			}
			if !replaced {
				out = append(out, r)
			}
		}
	}
	names := splitList(sets)
	if len(names) == 1 && names[0] == "all" {
		names = builtinSets
	}
	for _, n := range names {
		rs, err := LoadBuiltin(n)
		if err != nil {
			return nil, err
		}
		add(rs)
	}
	for _, f := range splitList(files) {
		rs, err := LoadRuleFile(f)
		if err != nil {
			return nil, err
		}
		for i := range rs {
			rs[i].Source = f
		}
		add(rs)
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func selectRules(all []Rule, list string) []Rule {
	if strings.TrimSpace(list) == "" {
		return all
	}
	want := map[string]bool{}
	for _, id := range splitList(list) {
		want[id] = true
	}
	var out []Rule
	for _, r := range all {
		if want[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// countUnits runs the deterministic pass to size the job before paying for it.
func countUnits(paths []string, reg *ruleRegistry, changed ChangedLines) int {
	n := 0
	for _, p := range paths {
		us, err := ExtractUnits(p, reg.rules)
		if err != nil {
			continue
		}
		for _, u := range us {
			if changed == nil || changed.Touches(u.File, u.StartLine, u.EndLine) {
				n++
			}
		}
	}
	return n
}

var severityRank = map[string]int{"hint": 0, "warning": 1, "error": 2}

func filterFindings(in []Finding, minConf float64, minSeverity string) []Finding {
	floor := -1
	if minSeverity != "" {
		if r, ok := severityRank[minSeverity]; ok {
			floor = r
		}
	}
	var out []Finding
	for _, f := range in {
		if f.Confidence < minConf {
			continue
		}
		if severityRank[f.Severity] < floor {
			continue
		}
		out = append(out, f)
	}
	return out
}

// expandPaths turns files, directories and globs into a file list.
func expandPaths(args []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] && lintable(p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, a := range args {
		matches, err := filepath.Glob(a)
		if err != nil || len(matches) == 0 {
			matches = []string{a}
		}
		for _, m := range matches {
			st, err := os.Stat(m)
			if err != nil {
				return nil, err
			}
			if !st.IsDir() {
				add(m)
				continue
			}
			err = filepath.WalkDir(m, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					switch d.Name() {
					case "node_modules", "vendor", ".git", "dist", "testdata":
						return filepath.SkipDir
					}
					return nil
				}
				add(p)
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// lintable reports whether a file is a type some language profile knows.
func lintable(p string) bool {
	if strings.HasSuffix(p, ".min.js") || strings.HasSuffix(p, ".d.ts") {
		return false
	}
	ext := filepath.Ext(p)
	if ext == ".mjs" || ext == ".cjs" {
		ext = ".js"
	}
	_, ok := languageProfiles[ext]
	return ok
}

// reportSites prints the deterministic pass alone. Useful on its own: it shows
// exactly what would be asked about, and costs nothing.
func reportSites(paths []string, reg *ruleRegistry, changed ChangedLines) {
	total, files := 0, 0
	byRule := map[string]int{}
	for _, p := range paths {
		all, err := ExtractUnits(p, reg.rules)
		if err != nil {
			continue
		}
		var units []Unit
		for _, u := range all {
			if changed == nil || changed.Touches(u.File, u.StartLine, u.EndLine) {
				units = append(units, u)
			}
		}
		if len(units) == 0 {
			continue
		}
		files++
		fmt.Printf("%s\n", p)
		for _, u := range units {
			fmt.Printf("  lines %d-%d  rules: %s\n", u.StartLine, u.EndLine, strings.Join(u.RuleIDs(), ", "))
			for _, s := range u.Sites {
				total++
				byRule[s.RuleID]++
				fmt.Printf("    %d: %s\n", s.Line, truncate(s.Text, 90))
			}
		}
	}
	fmt.Printf("\n%d sites in %d files; no model calls were made\n", total, files)
	var ids []string
	for id := range byRule {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %-34s %d\n", id, byRule[id])
	}
}

func printRules(reg *ruleRegistry) {
	ids := reg.ids()
	fmt.Printf("%d rules from %d compiled variants\n\n", len(ids), len(reg.rules))
	for _, id := range ids {
		r := reg.get(id)
		kind := "asks the model"
		if r.Check != nil {
			kind = "deterministic"
		}
		var exts []string
		for _, v := range reg.byID[id] {
			exts = append(exts, v.Selector.Ext)
		}
		fmt.Printf("%-30s %-8s %-14s %s  [%s]\n", r.ID, r.Severity, kind, strings.Join(exts, " "), r.Source)
		fmt.Printf("%-30s %s\n", "", r.Message)
		if r.Why != "" {
			fmt.Printf("%-30s %s\n", "", wrap(r.Why, 92, 31))
		}
		fmt.Println()
	}
}

func printText(findings []Finding, st Stats, nRules int) {
	for _, f := range findings {
		fmt.Printf("%s:%d [%s] %s (%s, p=%.2f)\n", f.File, f.Line, f.Rule, f.Message, f.Severity, f.Confidence)
		if f.Snippet != "" {
			fmt.Printf("    %s\n", truncate(f.Snippet, 100))
		}
	}
	if len(findings) == 0 {
		fmt.Println("no findings")
	}
	fmt.Printf("\n%d findings from %d rules over %d sites in %d units, %d files\n",
		len(findings), nRules, st.Sites, st.Units, st.Files)
	fmt.Printf("%d requests carrying %d questions, %d tokens, %s wall, p50 %s\n",
		st.Requests, st.Questions, st.Tokens, st.Wall.Round(time.Millisecond), percentile(st.Latencies, 50).Round(time.Millisecond))
	if st.UnitsFiltered > 0 {
		fmt.Printf("%d units skipped because the diff did not touch them\n", st.UnitsFiltered)
	}
	if st.PartialContext > 0 {
		fmt.Printf("%d units were judged on partial context (oversized function or trimmed to fit); rules were told what was missing and instructed to abstain\n", st.PartialContext)
	}
	for _, sk := range st.Skipped {
		fmt.Printf("SKIPPED %s:%d %s\n", sk.File, sk.Line, truncate(sk.Reason, 120))
	}
	if st.Requests > 0 {
		fmt.Printf("%.1f questions per request; one request per rule would have been %d\n",
			float64(st.Questions)/float64(st.Requests), st.Questions)
	}
}

// printHints emits the compact form meant to be handed to an agent. It leads
// with the consequence rather than the rule name, because a model acting on a
// hint needs to know what breaks, and it stays short so it does not crowd out
// the code it is about.
func printHints(findings []Finding, st Stats) {
	if len(findings) == 0 {
		fmt.Println("No semantic issues found in the reviewed code.")
		return
	}
	byFile := map[string][]Finding{}
	var order []string
	for _, f := range findings {
		if _, ok := byFile[f.File]; !ok {
			order = append(order, f.File)
		}
		byFile[f.File] = append(byFile[f.File], f)
	}
	fmt.Printf("%d issues found by semantic analysis. Each names a line and what would go wrong.\n", len(findings))
	for _, file := range order {
		fmt.Printf("\n%s\n", file)
		for _, f := range byFile[file] {
			fmt.Printf("  line %d: %s\n", f.Line, f.Message)
			fmt.Printf("    consequence: %s\n", f.Why)
			fmt.Printf("    rule %s, severity %s, confidence %.2f\n", f.Rule, f.Severity, f.Confidence)
		}
	}
}

func statsJSON(st Stats) map[string]any {
	return map[string]any{
		"files": st.Files, "units": st.Units, "sites": st.Sites,
		"requests": st.Requests, "questions": st.Questions, "tokens": st.Tokens,
		"wall_ms": st.Wall.Milliseconds(),
		"p50_ms":  percentile(st.Latencies, 50).Milliseconds(),
		"skipped": len(st.Skipped), "trimmed": st.Trimmed,
		"partial_context": st.PartialContext, "units_filtered": st.UnitsFiltered,
	}
}

func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(p / 100 * float64(len(s)))
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func wrap(s string, width, indent int) string {
	words := strings.Fields(s)
	var b strings.Builder
	col := indent
	pad := strings.Repeat(" ", indent)
	for i, w := range words {
		if col+len(w) > width && i > 0 {
			b.WriteString("\n" + pad)
			col = indent
		}
		b.WriteString(w + " ")
		col += len(w) + 1
	}
	return strings.TrimSpace(b.String())
}

// loadDotEnv finds the API key without requiring it to be exported. A git hook
// runs wherever the repository is, not where this project lives, so a
// user-level file is checked after the project-local ones.
func loadDotEnv(path string) {
	home, _ := os.UserHomeDir()
	candidates := []string{path, filepath.Join("..", path)}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".config", "semlint", "env"),
			filepath.Join(home, ".config", "typesafe", "env"))
	}
	for _, p := range candidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
				if v != "" && os.Getenv(k) == "" {
					os.Setenv(k, v)
				}
			}
		}
		f.Close()
		return
	}
}
