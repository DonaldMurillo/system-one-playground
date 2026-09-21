package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func loadResolution(path string) (*sos.Analysis, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("resolution exceeds 8 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var analysis sos.Analysis
	if err := dec.Decode(&analysis); err != nil {
		return nil, fmt.Errorf("resolution: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("resolution has trailing data")
	}
	return &analysis, nil
}

func saveResolution(path string, analysis *sos.Analysis) error {
	data, err := json.MarshalIndent(analysis, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sos-resolution-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return replaceFile(f.Name(), path)
}

// parseForExecution leaves noncanonical source to the semantic analyzer while
// retaining deterministic CLI declarations for argument checks and help.
// Body name/effect checks happen after command selection in Run or full build analysis.
func parseForExecution(path string, stderr io.Writer) (*sos.Program, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	if len(data) > 1<<20 {
		fmt.Fprintln(stderr, "source exceeds 1 MiB limit")
		return nil, false
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	policy, err := sos.EffectiveConfig(string(data), dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	p, ds := sos.ResolveModules(path, string(data))
	// Only unknown sentences may be deferred to interpretation. Structural
	// declaration errors must also block help in assisted/semantic modes.
	var immediate []sos.Diagnostic
	for _, d := range ds {
		if policy.Interpretation == "canonical" || !strings.HasPrefix(d.Message, "unknown construction:") {
			immediate = append(immediate, d)
		}
	}
	if printDiagnostics(stderr, path, immediate) {
		return nil, false
	}
	return p, true
}

func cmdExplain(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: sos explain FILE [--model M] [--save PATH] [--locked PATH] [--max-calls N]\n"
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	path := args[0]
	flags := flag.NewFlagSet("explain", flag.ContinueOnError)
	flags.SetOutput(stderr)
	model := flags.String("model", "", "interpretation model")
	save := flags.String("save", "", "save validated resolution")
	locked := flags.String("locked", "", "validate a saved resolution without provider calls")
	maxCalls := flags.Int("max-calls", -1, "further request ceiling")
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	invalid := false
	flags.Visit(func(f *flag.Flag) {
		if (f.Name == "locked" || f.Name == "save") && f.Value.String() == "" {
			invalid = true
		}
		if f.Name == "max-calls" && *maxCalls < 0 {
			invalid = true
		}
	})
	if flags.NArg() != 0 || invalid {
		fmt.Fprint(stderr, usage)
		return 2
	}
	source, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(source) > 1<<20 {
		fmt.Fprintln(stderr, "source exceeds 1 MiB limit")
		return 1
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if canonical, resolveErr := filepath.EvalSymlinks(absPath); resolveErr == nil {
		absPath = canonical
	}
	dir := filepath.Dir(absPath)
	if err = sos.LoadEnv(filepath.Join(dir, ".env")); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	policy, err := sos.EffectiveConfig(string(source), dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *maxCalls >= 0 && *maxCalls < policy.Requests {
		policy.Requests = *maxCalls
	}
	saved, err := loadResolution(*locked)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	budget, err := sos.NewRequestBudget(policy.Requests, nil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	if policy.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, policy.Timeout)
		defer cancel()
	}
	program, loadDs := sos.ResolveModules(path, string(source))
	for _, d := range loadDs {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			fmt.Fprintf(stderr, "%s:%d:%d: %s\n", path, d.Line, d.Column, d.Message)
			return 1
		}
	}
	analysis, analyzeErr := sos.Analyze(ctx, string(source), sos.AnalyzeOptions{Config: policy, Budget: budget, Model: *model, Bucket: sos.BudgetInterpretation, Saved: saved, Locked: saved != nil, Modules: program.Modules})
	if analysis != nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(analysis); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if analyzeErr != nil {
		fmt.Fprintln(stderr, analyzeErr)
		return 1
	}
	if *save != "" {
		if err := saveResolution(*save, analysis); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return 0
}

func cmdCanonicalize(args []string, stdout, stderr io.Writer) int {
	const usage = "usage: sos canonicalize FILE [--write] [--diff] [--line N] [--model M] [--max-calls N]\n"
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	path := args[0]
	flags := flag.NewFlagSet("canonicalize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	write := flags.Bool("write", false, "rewrite FILE atomically")
	diff := flags.Bool("diff", false, "preview a unified diff")
	line := flags.Int("line", 0, "canonicalize only the interpretation at one source line")
	model := flags.String("model", "", "interpretation model")
	maxCalls := flags.Int("max-calls", -1, "further request ceiling")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if *diff && *write {
		fmt.Fprintln(stderr, "--diff and --write cannot be used together")
		return 2
	}
	if *line < 0 {
		fmt.Fprintln(stderr, "--line must be positive")
		return 2
	}
	source, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	initialInfo, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if linkInfo, linkErr := os.Lstat(path); linkErr == nil && linkInfo.Mode()&os.ModeSymlink != 0 && *write {
		fmt.Fprintln(stderr, "refusing to replace a symbolic link; canonicalize its target explicitly")
		return 1
	}
	dir, err := canonicalizeProjectDir(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = sos.LoadEnv(filepath.Join(dir, ".env")); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	policy, err := sos.EffectiveConfig(string(source), dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *maxCalls >= 0 && *maxCalls < policy.Requests {
		policy.Requests = *maxCalls
	}
	program, loadDs := sos.ResolveModules(path, string(source))
	for _, d := range loadDs {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			fmt.Fprintf(stderr, "%s:%d:%d: %s\n", path, d.Line, d.Column, d.Message)
			return 1
		}
	}
	a, err := sos.Analyze(context.Background(), string(source), sos.AnalyzeOptions{Config: policy, Model: *model, Bucket: sos.BudgetInterpretation, Modules: program.Modules})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	canonical, err := sos.Canonicalize(string(source), a, program.Modules)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *line > 0 {
		canonical, err = canonicalizeOneLine(string(source), a, *line)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *diff {
		fmt.Fprint(stdout, canonicalDiff(path, string(source), canonical))
		return 0
	}
	if !*write {
		fmt.Fprint(stdout, canonical)
		return 0
	}
	currentSource, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !bytes.Equal(currentSource, source) || !os.SameFile(initialInfo, info) {
		fmt.Fprintln(stderr, "source changed while canonicalization was running; refusing to overwrite it")
		return 1
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sos-canonical-*")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.WriteString(canonical)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = replaceFile(tmpName, path)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "canonicalized %s\n", path)
	return 0
}

func canonicalizeProjectDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if canonical, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = canonical
	}
	dir := filepath.Dir(abs)
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "sos.toml")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return dir, nil
		}
	}
}

func canonicalizeOneLine(source string, analysis *sos.Analysis, line int) (string, error) {
	var replacement string
	for _, decision := range analysis.Decisions {
		if decision.Line == line && decision.Method != "criterion" {
			replacement = decision.Canonical
			break
		}
	}
	if replacement == "" {
		return "", fmt.Errorf("line %d has no canonicalizable interpretation", line)
	}
	newline := "\n"
	if strings.Contains(source, "\r\n") {
		newline = "\r\n"
		replacement = strings.ReplaceAll(replacement, "\n", newline)
	}
	trailing := strings.HasSuffix(source, newline)
	lines := strings.Split(strings.TrimSuffix(source, newline), newline)
	if line > len(lines) {
		return "", fmt.Errorf("line %d is outside the source", line)
	}
	lines[line-1] = replacement
	result := strings.Join(lines, newline)
	if trailing {
		result += newline
	}
	return result, nil
}

func canonicalDiff(path, before, after string) string {
	if before == after {
		return ""
	}
	oldLines, newLines := diffLines(before), diffLines(after)
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -1,%d +1,%d @@\n", path, path, len(oldLines), len(newLines))
	for _, line := range oldLines {
		fmt.Fprintf(&out, "-%s\n", strings.TrimSuffix(line, "\r"))
	}
	if before != "" && !strings.HasSuffix(before, "\n") {
		out.WriteString("\\ No newline at end of file\n")
	}
	for _, line := range newLines {
		fmt.Fprintf(&out, "+%s\n", strings.TrimSuffix(line, "\r"))
	}
	if after != "" && !strings.HasSuffix(after, "\n") {
		out.WriteString("\\ No newline at end of file\n")
	}
	return out.String()
}

func diffLines(source string) []string {
	if source == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}
