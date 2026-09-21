// Command sos is the SysOneScript command-line interface. RunCLI is exported so
// tests and future subcommands (for example an editor integration) share one
// entry point; main stays a three-liner.
//
// Exit codes: 0 success, 1 diagnostics or execution failure, 2 usage error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/internal/sosbuild"
	"github.com/DonaldMurillo/system-one-playground/internal/soslsp"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

const cliUsage = `usage: sos COMMAND [arguments]

commands:
  run FILE [flags] [-- SCRIPT_ARGS]   load .env, check, and run FILE
  explain FILE [--save PATH] [--locked PATH]  inspect semantic interpretation
  canonicalize FILE [--write|--diff] [--line N]
                                      resolve canonical source or one line
  config FILE                         show effective configuration as JSON
  check FILE [--json]                 report diagnostics and failure contracts
  fmt FILE [--write]                  print formatted FILE, or rewrite it
  build FILE --output PATH [--target native|wasm-browser|wasm-wasi]
                                      build a standalone executable
  module check|generate DEFINITION    inspect an external module definition
  module describe|doctor MODULE       inspect or preflight a registered module
  vocabulary [FILE] [--json] [--query TEXT] [--library PATH]
                                      offline dictionary of callable vocabulary
  debug                                 run a Debug Adapter Protocol server
  lsp                                 run the language server on stdio
  version                             print the SysOneScript version
`

const runUsage = `usage: sos run [--model M] [--max-calls N] [--timeout D] [--record PATH] [--replay PATH] [--resolution PATH] [--save-resolution PATH] FILE [-- SCRIPT_ARGS]

.env is loaded from the current directory; its values are never printed.
Run flags must precede script arguments. SCRIPT_ARGS are parsed against the
script's command declarations; --help after FILE prints the script's usage.
--save-resolution is a runner flag only before FILE, leaving the same-named
token available to scripts after FILE.
`

const checkUsage = "usage: sos check FILE [--json]\n"

const fmtUsage = "usage: sos fmt FILE [--write]\n"

const buildUsage = `usage: sos build FILE --output PATH [--target native|wasm-browser|wasm-wasi] [--resolution PATH]

Checks FILE, then builds a standalone artifact. The artifact embeds the
script and executes it on the SysOneScript interpreter: a native executable, a
js/wasm module (wasm-browser, with wasm_exec.js and index.html beside the
output), or a wasip1/wasm module (wasm-wasi). Building requires a Go
toolchain; running the artifact does not. No credentials are embedded.
Native builds that include bundled external modules create an output directory
containing the executable, manifest.json, and checksummed module artifacts.
`

// RunCLI executes the CLI with the given arguments and writers and returns
// the exit code.
func RunCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, cliUsage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest, stdout, stderr)
	case "explain":
		return cmdExplain(rest, stdout, stderr)
	case "canonicalize":
		return cmdCanonicalize(rest, stdout, stderr)
	case "config":
		return cmdConfig(rest, stdout, stderr)
	case "check":
		return cmdCheck(rest, stdout, stderr)
	case "vocabulary":
		return cmdVocabulary(rest, stdout, stderr)
	case "fmt":
		return cmdFmt(rest, stdout, stderr)
	case "build":
		return cmdBuild(rest, stdout, stderr)
	case "module":
		return cmdModule(rest, stdout, stderr)
	case "lsp":
		return cmdLSP(rest, stdout, stderr)
	case "debug":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, "sos: debug takes no command-line arguments; use the DAP launch request")
			return 2
		}
		return runDebugServer(os.Stdin, stdout, stderr)
	case "version":
		if sos.ReleaseMarker != "SysOneScriptVersion=development;SysOneScriptVersionEnd" && sos.ReleaseMarker != "SysOneScriptVersion="+sos.Version+";SysOneScriptVersionEnd" {
			fmt.Fprintln(stderr, "sos: runtime version metadata is inconsistent")
			return 1
		}
		fmt.Fprintf(stdout, "sos %s\n", sos.Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, cliUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "sos: unknown command %q\n", cmd)
		fmt.Fprint(stderr, cliUsage)
		return 2
	}
}

func cmdModule(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || (args[0] != "check" && args[0] != "generate" && args[0] != "describe" && args[0] != "doctor") {
		fmt.Fprintln(stderr, "usage: sos module check|generate DEFINITION | sos module describe|doctor MODULE_PATH")
		return 2
	}
	if args[0] == "doctor" {
		dir, err := os.Getwd()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = sos.DoctorExternalModule(ctx, dir, args[1])
		}
		if err != nil {
			fmt.Fprintf(stderr, "sos: module doctor: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: ready\n", args[1])
		return 0
	}
	var definition *sos.ExternalModuleDefinition
	var err error
	if args[0] == "describe" {
		dir, cwdErr := os.Getwd()
		if cwdErr != nil {
			err = cwdErr
		} else {
			definition, err = sos.ResolveExternalModuleDefinition(dir, args[1])
		}
	} else {
		definition, err = sos.LoadExternalModuleDefinition(args[1])
	}
	if err != nil {
		fmt.Fprintf(stderr, "sos: module %s: %v\n", args[0], err)
		return 1
	}
	if args[0] == "check" {
		if err := definition.ValidateInterface(); err != nil {
			fmt.Fprintf(stderr, "sos: module check: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s %s: %d action(s) · %s\n", definition.Module.Path, definition.Module.Version, len(definition.Actions), definition.Digest())
		return 0
	}
	fmt.Fprint(stdout, definition.GenerateInterface())
	if args[0] == "generate" {
		fmt.Fprintf(stdout, "# definition digest: %s\n", definition.Digest())
	}
	return 0
}

type runOptions struct {
	model          string
	maxCalls       string
	timeout        string
	record         string
	replay         string
	resolution     string
	saveResolution string
}

func isRunFlag(arg string) bool {
	for _, f := range []string{"--model", "--max-calls", "--timeout", "--record", "--replay", "--resolution"} {
		if arg == f || strings.HasPrefix(arg, f+"=") {
			return true
		}
	}
	return false
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	var opts runOptions
	file := ""
	var scriptArgs []string
	take := func(dst *string, i int) (int, bool) {
		if _, v, ok := strings.Cut(args[i], "="); ok {
			*dst = v
			return i + 1, true
		}
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "sos: run: %s requires a value\n", args[i])
			return i + 1, false
		}
		*dst = args[i+1]
		return i + 2, true
	}
parse:
	for i := 0; i < len(args); {
		arg := args[i]
		switch {
		case arg == "--":
			scriptArgs = append(scriptArgs, args[i+1:]...)
			break parse
		case file != "" && !isRunFlag(arg):
			// First token after FILE that is not a sos flag belongs to the
			// script, including later sos-style flags.
			scriptArgs = append(scriptArgs, args[i:]...)
			break parse
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, runUsage)
			return 0
		case arg == "--model" || strings.HasPrefix(arg, "--model="):
			var ok bool
			if i, ok = take(&opts.model, i); !ok {
				return 2
			}
		case arg == "--max-calls" || strings.HasPrefix(arg, "--max-calls="):
			var ok bool
			if i, ok = take(&opts.maxCalls, i); !ok {
				return 2
			}
		case arg == "--timeout" || strings.HasPrefix(arg, "--timeout="):
			var ok bool
			if i, ok = take(&opts.timeout, i); !ok {
				return 2
			}
		case arg == "--record" || strings.HasPrefix(arg, "--record="):
			var ok bool
			if i, ok = take(&opts.record, i); !ok {
				return 2
			}
		case arg == "--resolution" || strings.HasPrefix(arg, "--resolution="):
			var ok bool
			if i, ok = take(&opts.resolution, i); !ok {
				return 2
			}
			if opts.resolution == "" {
				fmt.Fprintln(stderr, "--resolution requires a nonempty path")
				return 2
			}
		case arg == "--save-resolution" || strings.HasPrefix(arg, "--save-resolution="):
			var ok bool
			if i, ok = take(&opts.saveResolution, i); !ok {
				return 2
			}
			if opts.saveResolution == "" {
				fmt.Fprintln(stderr, "--save-resolution requires a nonempty path")
				return 2
			}
		case arg == "--replay" || strings.HasPrefix(arg, "--replay="):
			var ok bool
			if i, ok = take(&opts.replay, i); !ok {
				return 2
			}
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "sos: run: unknown flag %s\n%s", arg, runUsage)
			return 2
		default:
			file = arg
			i++
		}
	}
	if file == "" {
		fmt.Fprintln(stderr, "sos: run: missing FILE")
		fmt.Fprint(stderr, runUsage)
		return 2
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "sos: run: %v\n", err)
		return 1
	}
	// .env comes from the working directory; sos.LoadEnv never logs values.
	if err := sos.LoadEnv(filepath.Join(dir, ".env")); err != nil {
		fmt.Fprintf(stderr, "sos: run: %v\n", err)
		return 1
	}
	program, ok := parseForExecution(file, stderr)
	if !ok {
		return 1
	}
	selection, err := sos.SelectCommand(program, scriptArgs)
	if err == nil && selection.Help {
		fmt.Fprint(stdout, selection.Usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "sos: run: %v\n", err)
		if selection != nil {
			fmt.Fprint(stderr, selection.Usage)
		}
		return 2
	}
	maxCalls := 0
	if opts.maxCalls != "" {
		n, err := strconv.Atoi(opts.maxCalls)
		if err != nil || n <= 0 {
			fmt.Fprintf(stderr, "sos: run: invalid --max-calls %q\n", opts.maxCalls)
			return 2
		}
		maxCalls = n
	}
	var timeout time.Duration
	if opts.timeout != "" {
		timeout, err = parseDuration(opts.timeout)
		if err != nil {
			fmt.Fprintf(stderr, "sos: run: invalid --timeout %q (want e.g. 30s)\n", opts.timeout)
			return 2
		}
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	saved, err := loadResolution(opts.resolution)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result, runErr := sos.Run(ctx, program, sos.Options{
		Resolution: saved, Locked: saved != nil,
		Dir:         dir,
		Args:        selection.Values,
		CommandPath: selection.Path,
		Stdin:       os.Stdin,
		Stdout:      stdout,
		Stderr:      stderr,
		MaxCalls:    maxCalls,
		Model:       opts.model,
		Record:      opts.record,
		Replay:      opts.replay,
		OnTrace: func(t sos.Trace) {
			fmt.Fprintf(stderr, "sos: trace line=%d model=%s %dms inputTokens=%d replay=%v\n",
				t.Line, t.Model, t.Milliseconds, t.InputTokens, t.Replay)
		},
	})
	if result != nil {
		reportUsage(stderr, result.Usage, runErr)
		reportInterpretations(stderr, result.Analysis)
		if opts.saveResolution != "" && runErr == nil && result.Analysis != nil && result.Analysis.Canonical != "" && len(result.Analysis.Diagnostics) == 0 {
			if err := saveResolution(opts.saveResolution, result.Analysis); err != nil {
				fmt.Fprintf(stderr, "sos: run: save resolution: %v\n", err)
				return 1
			}
		}
	}
	var exit interface{ ExitCode() int }
	if errors.As(runErr, &exit) {
		var stopped *sos.StopError
		if errors.As(runErr, &stopped) {
			fmt.Fprintf(stderr, "sos: run: %s\n", stopped.Message)
		}
		return exit.ExitCode()
	}
	if runErr != nil {
		failure := sos.FailureValue(runErr)
		if kind, ok := failure["kind"].(string); ok && kind != "runtime" {
			message, _ := failure["message"].(string)
			payload, _ := json.Marshal(failure)
			fmt.Fprintf(stderr, "sos: run: failure %s: %s (%s)\n", kind, message, payload)
		} else {
			fmt.Fprintf(stderr, "sos: run: %v\n", runErr)
		}
		return 1
	}
	return 0
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	file := ""
	jsonOut := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, checkUsage)
			return 0
		case arg == "--json":
			jsonOut = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "sos: check: unknown flag %s\n%s", arg, checkUsage)
			return 2
		case file == "":
			file = arg
		default:
			fmt.Fprintf(stderr, "sos: check: unexpected argument %s\n%s", arg, checkUsage)
			return 2
		}
	}
	if file == "" {
		fmt.Fprint(stderr, checkUsage)
		return 2
	}
	source, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "sos: check: %v\n", err)
		return 1
	}
	program, diagnostics := sos.LoadProgram(file, string(source))
	if jsonOut {
		payload := map[string]any{
			"diagnostics":      diagnostics,
			"actions":          program.ActionMetadata(),
			"possibleFailures": failureMetadata(program),
		}
		if err := json.NewEncoder(stdout).Encode(payload); err != nil {
			fmt.Fprintf(stderr, "sos: check: %v\n", err)
			return 1
		}
		if len(diagnostics) > 0 {
			return 1
		}
		return 0
	}
	if printDiagnostics(stderr, file, diagnostics) {
		return 1
	}
	return 0
}

func failureMetadata(program *sos.Program) map[string][]string {
	result := map[string][]string{}
	if program == nil {
		return result
	}
	for _, action := range program.ActionMetadata() {
		if len(action.PossibleFailures) > 0 {
			result[action.Name] = append([]string(nil), action.PossibleFailures...)
		}
	}
	return result
}

func cmdFmt(args []string, stdout, stderr io.Writer) int {
	file := ""
	write := false
	for _, arg := range args {
		switch {
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, fmtUsage)
			return 0
		case arg == "--write":
			write = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "sos: fmt: unknown flag %s\n%s", arg, fmtUsage)
			return 2
		case file == "":
			file = arg
		default:
			fmt.Fprintf(stderr, "sos: fmt: unexpected argument %s\n%s", arg, fmtUsage)
			return 2
		}
	}
	if file == "" {
		fmt.Fprint(stderr, fmtUsage)
		return 2
	}
	source, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "sos: fmt: %v\n", err)
		return 1
	}
	formatted, diags := sos.Format(string(source))
	if printDiagnostics(stderr, file, diags) {
		return 1
	}
	if write {
		mode := os.FileMode(0o644)
		if info, statErr := os.Stat(file); statErr == nil {
			mode = info.Mode()
		}
		if err := os.WriteFile(file, []byte(formatted), mode); err != nil {
			fmt.Fprintf(stderr, "sos: fmt: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprint(stdout, formatted)
	return 0
}

func cmdBuild(args []string, stdout, stderr io.Writer) int {
	file := ""
	output := ""
	resolution := ""
	target := string(sosbuild.TargetNative)
	take := func(i int) (string, int, bool) {
		if _, v, ok := strings.Cut(args[i], "="); ok {
			return v, i + 1, true
		}
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "sos: build: %s requires a value\n", args[i])
			return "", i + 1, false
		}
		return args[i+1], i + 2, true
	}
	for i := 0; i < len(args); {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			fmt.Fprint(stdout, buildUsage)
			return 0
		case arg == "--resolution" || strings.HasPrefix(arg, "--resolution="):
			var ok bool
			if resolution, i, ok = take(i); !ok {
				return 2
			}
			if resolution == "" {
				fmt.Fprintln(stderr, "--resolution requires a nonempty path")
				return 2
			}
		case arg == "--output" || arg == "-o" || strings.HasPrefix(arg, "--output="):
			var ok bool
			if output, i, ok = take(i); !ok {
				return 2
			}
		case arg == "--target" || strings.HasPrefix(arg, "--target="):
			var ok bool
			if target, i, ok = take(i); !ok {
				return 2
			}
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "sos: build: unknown flag %s\n%s", arg, buildUsage)
			return 2
		case file == "":
			file = arg
			i++
		default:
			fmt.Fprintf(stderr, "sos: build: unexpected argument %s\n%s", arg, buildUsage)
			return 2
		}
	}
	if file == "" {
		fmt.Fprint(stderr, buildUsage)
		return 2
	}
	switch sosbuild.Target(target) {
	case sosbuild.TargetNative, sosbuild.TargetWasmBrowser, sosbuild.TargetWasmWasi:
	default:
		fmt.Fprintf(stderr, "sos: build: unknown target %q (want native, wasm-browser, or wasm-wasi)\n", target)
		return 2
	}
	if output == "" {
		stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		if sosbuild.Target(target) != sosbuild.TargetNative {
			stem += ".wasm"
		}
		output = stem
	}
	program, ok := parseForExecution(file, stderr)
	if !ok {
		return 1
	}
	saved, err := loadResolution(resolution)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := sos.LoadEnv(".env"); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := sosbuild.Build(context.Background(), sosbuild.BuildOptions{
		Resolution: saved,
		OnAnalysis: func(a *sos.Analysis, err error) {
			if a != nil {
				reportUsage(stderr, a.Usage, err)
			}
		},
		Program: program,
		Name:    file,
		Output:  output,
		Target:  sosbuild.Target(target),
	}); err != nil {
		fmt.Fprintf(stderr, "sos: build: %v\n", err)
		return 1
	}
	return 0
}

// cmdLSP runs the language server on real stdio: JSON-RPC framing needs the
// process streams, not the test writers.
func cmdLSP(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: sos lsp")
		return 2
	}
	if err := soslsp.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(stderr, "sos: lsp: %v\n", err)
		return 1
	}
	return 0
}

// parseAndCheck reads, parses, checks, and resolves imports for path,
// printing diagnostics as path:line:column: message.
func parseAndCheck(path string, stderr io.Writer) (*sos.Program, bool) {
	source, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "sos: %v\n", err)
		return nil, false
	}
	program, diags := sos.LoadProgram(path, string(source))
	if printDiagnostics(stderr, path, diags) {
		return nil, false
	}
	return program, true
}

func printDiagnostics(w io.Writer, path string, diags []sos.Diagnostic) bool {
	for _, d := range diags {
		fmt.Fprintf(w, "%s:%d:%d: %s\n", path, d.Line, d.Column, d.Message)
	}
	return len(diags) > 0
}

// parseDuration accepts Go duration strings ("90s", "2m") or bare seconds.
func parseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		if _, e := strconv.ParseInt(s, 10, 64); e == nil {
			d, err = time.ParseDuration(s + "s")
		}
	}
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid positive duration %q", s)
	}
	return d, nil
}

// cmdConfig prints policy and its provenance without executing a script.
func cmdConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: sos config FILE")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := sos.EffectiveConfig(string(data), dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// reportUsage includes all consumers sharing this operation's request budget.
func reportUsage(w io.Writer, usage sos.BudgetSnapshot, err error) {
	var budgetErr *sos.BudgetError
	if usage.TotalAdmitted == 0 && !errors.As(err, &budgetErr) {
		return
	}
	tokens, unresolved := 0, 0
	for _, bucket := range usage.Buckets {
		tokens += bucket.ReportedInputTokens
		unresolved += bucket.Unresolved
	}
	cost := float64(tokens) * 0.042 / 1_000_000
	fmt.Fprintf(w, "sos: usage requests=%d/%d inputTokens=%d unresolved=%d estimatedUSD=%.8f (Jev 1.13 input estimate; not an invoice)\n", usage.TotalAdmitted, usage.TotalLimit, tokens, unresolved, cost)
}

func reportInterpretations(w io.Writer, analysis *sos.Analysis) {
	if analysis == nil {
		return
	}
	for _, d := range analysis.Decisions {
		if d.Method == "deterministic" {
			fmt.Fprintf(w, "sos: interpretation line=%d method=deterministic confidence=100%% usage=no-Jev-request\n", d.Line)
			continue
		}
		if d.Method != "jev" && d.Method != "memoized" {
			continue
		}
		if d.Method == "jev" && !d.UsageKnown {
			fmt.Fprintf(w, "sos: interpretation line=%d method=jev confidence=%.0f%% usage=unavailable\n", d.Line, d.Confidence*100)
			continue
		}
		cost := float64(d.InputTokens) * 0.042 / 1_000_000
		shared := ""
		if d.UsageShared {
			shared = fmt.Sprintf(" sharedAcross=%d", d.BatchSize)
		}
		fmt.Fprintf(w, "sos: interpretation line=%d method=%s confidence=%.0f%% inputTokens=%d estimatedUSD=%.8f%s\n", d.Line, d.Method, d.Confidence*100, d.InputTokens, cost, shared)
	}
}
