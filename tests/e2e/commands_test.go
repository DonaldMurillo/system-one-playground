// Acceptance tests for the reworked multi-command CLI surface specified in
// docs/sysonescript-commands.md and gated by stage 1 of
// docs/sysonescript-cli-roadmap.md: nested group/leaf command bodies, describe
// lines, inherited options, typed required inputs, finite text choices,
// generated help, and validation before any effect.
//
// Every case runs against both surfaces that must agree — the script runner
// (`sos run FILE -- ARGS`) and a native executable produced by `sos build` —
// comparing exit codes and stdout exactly. Provider safety uses the package's
// deterministic endpoints: a canary fixture (any contact fails the test) for
// offline paths and the cooperative fixture for the one judged path.
//
// These tests target the new grammar and may fail until the engine ships
// sos.SelectCommand and the new command declarations.
package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ticketsSource is the shared application fixture. Root group `tickets`
// contributes inherited options (including a finite-choice option); leaves
// cover switches, typed options with defaults, positional arguments, file
// writes, and one provider judgment; nested group `reports` adds a second
// group level with its own option and a required option without a default.
const ticketsSource = `command tickets:
  describe "Inspect and organize support tickets"
  option source as file default "tickets.json"
  option criterion as text choices "urgent", "all" default "urgent"

  command list:
    describe "List open tickets"
    switch urgent default off
    option limit as integer default 20
    option factor as number default 1.5
    option since as duration default 24h
    show "list source={source} criterion={criterion} limit={limit} urgent={urgent} factor={factor} since={since}"

  command export:
    describe "Export all tickets as JSON"
    argument destination as file
    argument label as text
    show "export source={source} destination={destination} label={label}"
    make report with:
      criterion from criterion
      label from label
    save report as json in destination

  command ask:
    describe "Judge one message"
    argument message as text
    judge message by jev "The request is urgent" called verdict
    show "verdict {verdict.p_yes}"
    save verdict as json in "verdict.json"

  command reports:
    describe "Reporting tools"
    option format as text choices "table", "json" default "table"

    command summary:
      describe "Summarize tickets"
      argument bucket as text
      option window as text
      show "summary format={format} source={source} bucket={bucket} window={window}"
`

// Declaration-error fixtures: each must be rejected offline before any body
// statement executes.
const groupWithPositionalSource = `command root:
  describe "Group commands take no positional inputs"
  argument payload as file
  command child:
    show "child ran"
`

const mixedBodySource = `command root:
  describe "Neither group nor leaf"
  show "root statement ran"
  command child:
    show "child ran"
`

const topLevelEffectsSource = `show "top-level ran"
command root:
  command child:
    show "child ran"
`

const shadowedOptionSource = `command tickets:
  option source as file default "tickets.json"
  command list:
    option source as file default "other.json"
    show "list ran"
`

const duplicateSiblingSource = `command tickets:
  command list:
    show "first"
  command list:
    show "second"
`

const duplicateInputSource = `command tickets:
  command list:
    option limit as integer default 20
    option limit as integer default 5
    show "list ran"
`

// cmdCase is one script-argument invocation expectation, checked against the
// runner and the generated native executable.
type cmdCase struct {
	name        string
	args        []string
	code        int
	stdoutEmpty bool
	stdoutHas   []string
	stdoutLacks []string
	stderrHas   []string
	// filesAbsent/filesPresent hold paths relative to each run's directory.
	filesAbsent  []string
	filesPresent []string
}

var (
	ticketsOnce sync.Once
	ticketsBin  string
	ticketsErr  error
)

// ticketsAppBin builds the shared tickets application once per test binary
// and returns its native executable path. The build directory intentionally
// outlives individual tests because every parity case reuses one artifact.
func ticketsAppBin(t *testing.T) string {
	t.Helper()
	ticketsOnce.Do(func() {
		dir := filepath.Join(filepath.Dir(sosBin), "command-app")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			ticketsErr = err
			return
		}
		script := filepath.Join(dir, "tickets.sos")
		if err := os.WriteFile(script, []byte(ticketsSource), 0o644); err != nil {
			ticketsErr = err
			return
		}
		ticketsBin = filepath.Join(dir, "tickets-app")
		_, stderr, code := runCLI(t, dir, "build", script, "--output", ticketsBin)
		if code != 0 {
			ticketsErr = fmt.Errorf("sos build exit %d:\n%s", code, stderr)
			return
		}
		assertArtifact(t, ticketsBin)
	})
	if ticketsErr != nil {
		t.Fatalf("building tickets app: %v", ticketsErr)
	}
	return ticketsBin
}

// runAppArtifact executes the generated native binary in dir with args.
func runAppArtifact(t *testing.T, dir, bin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running artifact %v: %v", args, err)
		}
		return out.String(), errBuf.String(), exit.ExitCode()
	}
	return out.String(), errBuf.String(), 0
}

// execCommandCases runs every case through both the script runner and the
// native artifact, requiring equal stdout and equal, expected exit codes.
func execCommandCases(t *testing.T, cases []cmdCase) {
	t.Helper()
	bin := ticketsAppBin(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Runner surface: `sos run tickets.sos -- ARGS`.
			rdir := t.TempDir()
			writeScript(t, rdir, "tickets.sos", ticketsSource)
			runnerOut, runnerErr, runnerCode := runCLI(t, rdir, append([]string{"run", "tickets.sos", "--"}, tc.args...)...)
			// Artifact surface: the packaged executable takes the same ARGS.
			adir := t.TempDir()
			artifactOut, artifactErr, artifactCode := runAppArtifact(t, adir, bin, tc.args...)

			if runnerCode != tc.code {
				t.Errorf("runner exit = %d, want %d; stderr:\n%s", runnerCode, tc.code, runnerErr)
			}
			if artifactCode != tc.code {
				t.Errorf("artifact exit = %d, want %d; stderr:\n%s", artifactCode, tc.code, artifactErr)
			}
			if runnerOut != artifactOut {
				t.Errorf("stdout differs between runner and artifact: runner %q, artifact %q", runnerOut, artifactOut)
			}
			if tc.stdoutEmpty && runnerOut != "" {
				t.Errorf("runner stdout = %q, want it empty", runnerOut)
			}
			for _, out := range []string{runnerOut, artifactOut} {
				for _, want := range tc.stdoutHas {
					if !strings.Contains(out, want) {
						t.Errorf("stdout %q lacks %q", out, want)
					}
				}
				for _, lack := range tc.stdoutLacks {
					if strings.Contains(out, lack) {
						t.Errorf("stdout %q must not contain %q", out, lack)
					}
				}
			}
			for _, errText := range []string{runnerErr, artifactErr} {
				for _, want := range tc.stderrHas {
					if !strings.Contains(errText, want) {
						t.Errorf("stderr %q lacks %q", errText, want)
					}
				}
			}
			for _, f := range tc.filesAbsent {
				for _, dir := range []string{rdir, adir} {
					if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
						t.Errorf("%s was written despite exit code %d", f, tc.code)
					}
				}
			}
			for _, f := range tc.filesPresent {
				for _, dir := range []string{rdir, adir} {
					if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
						t.Errorf("%s missing after successful run in %s: %v", f, dir, err)
					}
				}
			}
		})
	}
}

func TestCommandHelpAndSelectionParity(t *testing.T) {
	semCanary(t)
	execCommandCases(t, []cmdCase{
		{
			name: "group alone prints help and succeeds",
			args: nil,
			code: 0,
			stdoutHas: []string{"tickets", "list", "export", "ask", "reports",
				"Inspect and organize support tickets"},
			stdoutLacks: []string{"list source=", "export source=", "verdict ", "summary format="},
		},
		{
			name: "root help flag matches group help role",
			args: []string{"--help"},
			code: 0,
			stdoutHas: []string{"tickets", "list", "export", "ask", "reports",
				"Inspect and organize support tickets"},
			stdoutLacks: []string{"list source=", "export source=", "verdict ", "summary format="},
		},
		{
			name:        "nested group alone prints its help",
			args:        []string{"reports"},
			code:        0,
			stdoutHas:   []string{"reports", "summary", "Reporting tools"},
			stdoutLacks: []string{"summary format=", "list source=", "export source="},
		},
		{
			name:        "nested group help flag",
			args:        []string{"reports", "--help"},
			code:        0,
			stdoutHas:   []string{"reports", "summary", "Reporting tools"},
			stdoutLacks: []string{"summary format=", "list source=", "export source="},
		},
		{
			name:        "leaf help bypasses required inputs",
			args:        []string{"export", "--help"},
			code:        0,
			stdoutHas:   []string{"export", "destination", "label", "Export all tickets as JSON"},
			stdoutLacks: []string{"export source="},
		},
		{
			name:        "nested leaf help bypasses required argument and option",
			args:        []string{"reports", "summary", "--help"},
			code:        0,
			stdoutHas:   []string{"summary", "bucket", "window"},
			stdoutLacks: []string{"summary format="},
		},
		{
			name:        "help lists input surface with defaults",
			args:        []string{"list", "--help"},
			code:        0,
			stdoutHas:   []string{"--source", "--criterion", "--limit", "--urgent", "tickets.json", "20", "List open tickets"},
			stdoutLacks: []string{"list source="},
		},
		{
			name:        "unknown child is a usage error",
			args:        []string{"bogus"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"bogus"},
		},
		{
			name:        "unknown nested child is a usage error",
			args:        []string{"reports", "bogus"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"bogus"},
		},
		{
			name:        "help does not excuse an unknown child",
			args:        []string{"bogus", "--help"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"bogus"},
		},
	})
}

func TestCommandInputValidationParity(t *testing.T) {
	semCanary(t)
	execCommandCases(t, []cmdCase{
		{
			name:        "missing positional argument",
			args:        []string{"export"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"destination"},
		},
		{
			name:        "missing later positional blocks earlier one's effects",
			args:        []string{"export", "out.json"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"label"},
			filesAbsent: []string{"out.json"},
		},
		{
			name:        "missing required option",
			args:        []string{"reports", "summary", "archive"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"window"},
		},
		{
			name:        "missing nested positional with option supplied",
			args:        []string{"reports", "summary", "--window", "7d"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"bucket"},
		},
		{
			name:        "missing text argument on judging leaf",
			args:        []string{"ask"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"message"},
			filesAbsent: []string{"verdict.json"},
		},
		{
			name:        "unknown option on leaf",
			args:        []string{"list", "--bogus"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"--bogus"},
		},
		{
			name:        "unknown option before child name",
			args:        []string{"--bogus", "list"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"--bogus"},
		},
		{
			name:        "child option before child name is rejected",
			args:        []string{"--limit", "5", "list"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"--limit"},
		},
		{
			name:        "sibling option is unknown here",
			args:        []string{"ask", "hello", "--limit", "3"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"--limit"},
			filesAbsent: []string{"verdict.json"},
		},
		{
			name:        "repeated option",
			args:        []string{"list", "--limit", "1", "--limit", "2"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"limit"},
		},
		{
			name:        "repeated inherited option across placements",
			args:        []string{"--source", "a.json", "list", "--source", "b.json"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"source"},
		},
		{
			name:        "repeated switch",
			args:        []string{"list", "--urgent", "--urgent=false"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"urgent"},
		},
		{
			name:        "invalid integer value",
			args:        []string{"list", "--limit", "abc"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"limit"},
		},
		{
			name:        "fractional integer value",
			args:        []string{"list", "--limit", "2.5"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"limit"},
		},
		{
			name:        "invalid number value",
			args:        []string{"list", "--factor", "abc"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"factor"},
		},
		{
			name:        "invalid duration value",
			args:        []string{"list", "--since", "notaduration"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"since"},
		},
		{
			name:        "invalid switch value",
			args:        []string{"list", "--urgent=maybe"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"urgent"},
		},
		{
			name:        "unknown choice after child",
			args:        []string{"list", "--criterion", "bogus"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"criterion"},
		},
		{
			name:        "unknown choice before child blocks writes",
			args:        []string{"--criterion", "bogus", "export", "out.json", "backup"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"criterion"},
			filesAbsent: []string{"out.json"},
		},
		{
			name:        "unexpected positional on leaf without arguments",
			args:        []string{"list", "extra"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"extra"},
		},
		{
			name:        "extra positional after -- stays positional",
			args:        []string{"export", "--", "-dash.json", "backup", "--bogus"},
			code:        2,
			stdoutEmpty: true,
			stderrHas:   []string{"--bogus"},
			filesAbsent: []string{"-dash.json"},
		},
	})
}

func TestCommandDefaultsAndPlacementParity(t *testing.T) {
	semCanary(t)
	execCommandCases(t, []cmdCase{
		{
			name:      "declared defaults apply across parent and child",
			args:      []string{"list"},
			code:      0,
			stdoutHas: []string{"source=tickets.json", "criterion=urgent", "limit=20"},
		},
		{
			name:      "inherited option before child",
			args:      []string{"--source", "backlog.json", "list"},
			code:      0,
			stdoutHas: []string{"source=backlog.json"},
		},
		{
			name:      "inherited option after child",
			args:      []string{"list", "--source", "backlog.json"},
			code:      0,
			stdoutHas: []string{"source=backlog.json"},
		},
		{
			name:      "option equals form",
			args:      []string{"list", "--limit=5"},
			code:      0,
			stdoutHas: []string{"limit=5"},
		},
		{
			name:      "declared choice override",
			args:      []string{"list", "--criterion", "all"},
			code:      0,
			stdoutHas: []string{"criterion=all"},
		},
		{
			name:         "positional arguments bind in declaration order",
			args:         []string{"export", "out.json", "backup"},
			code:         0,
			stdoutHas:    []string{"destination=out.json", "label=backup"},
			filesPresent: []string{"out.json"},
		},
		{
			name:         "dash-prefixed positional after --",
			args:         []string{"export", "--", "-dash.json", "backup"},
			code:         0,
			stdoutHas:    []string{"destination=-dash.json", "label=backup"},
			filesPresent: []string{"-dash.json"},
		},
		{
			name: "nested ancestors' options before child",
			args: []string{"--source", "s.json", "reports", "--format", "json",
				"summary", "archive", "--window", "7d"},
			code:      0,
			stdoutHas: []string{"source=s.json", "format=json", "bucket=archive", "window=7d"},
		},
		{
			name: "nested options after child",
			args: []string{"reports", "summary", "archive", "--window", "7d",
				"--format", "json", "--source", "s.json"},
			code:      0,
			stdoutHas: []string{"format=json", "source=s.json", "bucket=archive"},
		},
	})
}

func TestCommandSwitchForms(t *testing.T) {
	semCanary(t)
	bin := ticketsAppBin(t)
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		dir := t.TempDir()
		writeScript(t, dir, "tickets.sos", ticketsSource)
		out, stderr, code := runCLI(t, dir, append([]string{"run", "tickets.sos", "--"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit = %d; stderr:\n%s", args, code, stderr)
		}
		return out
	}
	bare := run(t, "list")
	explicitOff := run(t, "list", "--urgent=false")
	if bare != explicitOff {
		t.Errorf("default-off switch differs from --urgent=false: %q vs %q", bare, explicitOff)
	}
	on := run(t, "list", "--urgent")
	if on == bare {
		t.Errorf("--urgent did not change output: %q", on)
	}
	if !strings.Contains(on, "urgent=true") {
		t.Errorf("--urgent output %q lacks urgent=true", on)
	}
	// The artifact agrees on both switch forms.
	adir := t.TempDir()
	artifactBare, _, code := runAppArtifact(t, adir, bin, "list")
	if code != 0 {
		t.Fatalf("artifact list: exit = %d", code)
	}
	bdir := t.TempDir()
	artifactOff, _, code := runAppArtifact(t, bdir, bin, "list", "--urgent=false")
	if code != 0 {
		t.Fatalf("artifact list --urgent=false: exit = %d", code)
	}
	if artifactBare != artifactOff || artifactBare != bare {
		t.Errorf("artifact switch outputs differ: bare %q, off %q, runner %q", artifactBare, artifactOff, bare)
	}
}

func TestCommandSelectedBodyOnlyExecutes(t *testing.T) {
	fx := semCanary(t) // any provider contact fails: unselected `ask` must not judge
	bin := ticketsAppBin(t)
	bodies := []struct {
		args  []string
		marks []string // marks[0] belongs to the selected leaf; the rest must not run
	}{
		{[]string{"list"}, []string{"list source=", "export source=", "verdict ", "summary format="}},
		{[]string{"export", "out.json", "backup"}, []string{"export source=", "list source=", "verdict ", "summary format="}},
		{[]string{"reports", "summary", "archive", "--window", "7d"},
			[]string{"summary format=", "list source=", "export source=", "verdict "}},
	}
	for _, b := range bodies {
		dir := t.TempDir()
		writeScript(t, dir, "tickets.sos", ticketsSource)
		out, stderr, code := runCLI(t, dir, append([]string{"run", "tickets.sos", "--"}, b.args...)...)
		if code != 0 {
			t.Fatalf("%v: exit = %d; stderr:\n%s", b.args, code, stderr)
		}
		if !strings.Contains(out, b.marks[0]) {
			t.Errorf("%v: selected body did not run; stdout %q", b.args, out)
		}
		for _, other := range b.marks[1:] {
			if strings.Contains(out, other) {
				t.Errorf("%v: unselected body executed (%q in %q)", b.args, other, out)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "verdict.json")); err == nil {
			t.Errorf("%v: unselected ask leaf wrote verdict.json", b.args)
		}
		adir := t.TempDir()
		aout, _, acode := runAppArtifact(t, adir, bin, b.args...)
		if acode != 0 || aout != out {
			t.Errorf("%v: artifact mismatch: exit = %d, stdout %q (runner %q)", b.args, acode, aout, out)
		}
		if _, err := os.Stat(filepath.Join(adir, "verdict.json")); err == nil {
			t.Errorf("%v: unselected ask leaf wrote verdict.json via artifact", b.args)
		}
	}
	fx.requireNoContacts(t)
}

// The one judged path: a valid selection makes exactly one provider request
// through the real client against the cooperative fixture, writes its file,
// and the runner and native artifact agree on stdout.
func TestCommandProviderEffectsRunOnlyAfterValidation(t *testing.T) {
	fx := semNewFixture(t, semServeCooperative)
	bin := ticketsAppBin(t)
	message := "customer is blocked from working"

	dir := t.TempDir()
	writeScript(t, dir, "tickets.sos", ticketsSource)
	out, stderr, code := runCLI(t, dir, "run", "tickets.sos", "--", "ask", message)
	if code != 0 {
		t.Fatalf("runner ask: exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(out, "verdict") {
		t.Errorf("runner ask stdout = %q, want a verdict line", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "verdict.json")); err != nil {
		t.Errorf("verdict.json missing after successful run: %v", err)
	}

	adir := t.TempDir()
	aout, aerr, acode := runAppArtifact(t, adir, bin, "ask", message)
	if acode != 0 {
		t.Fatalf("artifact ask: exit = %d; stderr:\n%s", acode, aerr)
	}
	if aout != out {
		t.Errorf("provider run stdout differs: runner %q, artifact %q", out, aout)
	}
	if _, err := os.Stat(filepath.Join(adir, "verdict.json")); err != nil {
		t.Errorf("artifact verdict.json missing: %v", err)
	}
	fx.requireContacts(t, 2)
}

func TestCommandDeclarationDiagnostics(t *testing.T) {
	semCanary(t)
	cases := []struct {
		name   string
		file   string
		source string
		// names must appear in the diagnostic: declaration identifiers are
		// data, not diagnostic wording, so requiring them pins actionability.
		names []string
	}{
		{"group with positional argument", "group-positional.sos", groupWithPositionalSource, []string{"payload"}},
		{"mixed group and leaf body", "mixed-body.sos", mixedBodySource, nil},
		{"top-level statements in CLI script", "top-level.sos", topLevelEffectsSource, nil},
		{"shadowed inherited option", "shadow.sos", shadowedOptionSource, []string{"source"}},
		{"duplicate sibling command", "dup-command.sos", duplicateSiblingSource, []string{"list"}},
		{"duplicate input declaration", "dup-input.sos", duplicateInputSource, []string{"limit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeScript(t, dir, tc.file, tc.source)
			stdout, stderr, code := runCLI(t, dir, "run", tc.file)
			if code != 1 {
				t.Fatalf("run exit = %d, want 1; stderr:\n%s", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want no body output for a declaration error", stdout)
			}
			if !strings.Contains(stderr, tc.file) {
				t.Errorf("stderr %q lacks file name %q", stderr, tc.file)
			}
			for _, name := range tc.names {
				if !strings.Contains(stderr, name) {
					t.Errorf("stderr %q lacks declaration name %q", stderr, name)
				}
			}
			// Offline `check` reports the same declaration errors.
			_, checkErr, checkCode := runCLI(t, dir, "check", tc.file)
			if checkCode != 1 || !strings.Contains(checkErr, tc.file) {
				t.Errorf("check exit = %d, want 1 with diagnostics naming %s:\n%s", checkCode, tc.file, checkErr)
			}
		})
	}
}
