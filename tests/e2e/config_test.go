// Config-layer end-to-end tests: global config.toml under SOS_CONFIG_HOME,
// the nearest project sos.toml, and optional +++ frontmatter, resolved into
// the effective policy the CLI prints, enforces, and embeds into builds.
//
// Every test points SOS_CONFIG_HOME at an empty directory so the developer's
// real global config never leaks in, and no test performs a live provider
// request: judgment scripts run only under budget zero or runtime deny, which
// are refused before any provider is contacted.
package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// judgeScript is the budget-zero judgment probe from the task: any effective
// requests ceiling of zero (or a runtime deny) must refuse it without an API
// key or network.
const judgeScript = "judge \"text\" by jev \"Urgent\" called result\nshow result\n"

// isolateConfigHome points SOS_CONFIG_HOME at an empty directory and returns
// it, so global-config tests neither read nor clobber real user settings.
func isolateConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SOS_CONFIG_HOME", dir)
	return dir
}

// writeConfigFile writes a TOML config (config.toml or sos.toml), creating
// parent directories as needed.
func writeConfigFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// frontmatter wraps TOML in +++ delimiters to prepend to a script body.
func frontmatter(toml string) string {
	return "+++\n" + toml + "\n+++\n"
}

// effectiveConfig decodes `sos config FILE` stdout and requires the six
// documented Effective fields: editor, interpretation, runtime, requests,
// timeout_ns, origins.
func effectiveConfig(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("config output is not a JSON object: %v\n%s", err, stdout)
	}
	// The output may wrap the effective policy under an "Effective" key or
	// encode it at the top level; both carry the same six fields.
	if nested, ok := raw["Effective"].(map[string]any); ok {
		raw = nested
	}
	for _, field := range []string{"editor", "interpretation", "runtime", "requests", "timeout_ns", "origins"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("config JSON missing field %q in:\n%s", field, stdout)
		}
	}
	// origins is a provenance object mapping each policy field to the layer
	// that supplied it.
	origins, ok := raw["origins"].(map[string]any)
	if !ok {
		t.Fatalf("origins is not an object in:\n%s", stdout)
	}
	for key, source := range origins {
		if _, ok := source.(string); !ok {
			t.Errorf("origins[%q] is not a string in:\n%s", key, stdout)
		}
	}
	return raw
}

// cfgOrigin returns the provenance layer recorded for a policy field.
func cfgOrigin(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	origins, ok := m["origins"].(map[string]any)
	if !ok {
		t.Fatalf("origins is not an object in:\n%v", m)
	}
	v, ok := origins[key].(string)
	if !ok {
		t.Fatalf("origins is missing %q in:\n%v", key, m)
	}
	return v
}

func cfgString(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("config field %q is not a string in:\n%v", key, m)
	}
	return v
}

func cfgInt(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("config field %q is not a number in:\n%v", key, m)
	}
	return int64(v)
}

// runArtifact executes a built standalone binary from dir, mirroring runCLI
// for artifacts instead of the sos CLI.
func runArtifact(t *testing.T, path, dir string) (stdout, stderr string, code int) {
	cmd := exec.Command(path)
	cmd.Dir = dir
	var out, errBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running artifact %s: %v", path, err)
		}
		code = exit.ExitCode()
	}
	return out.String(), errBuf.String(), code
}

func TestConfigPrintsEffectiveJSON(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "plain.sos", simpleScript)

	_, stderr, code := runCLI(t, dir, "config")
	if code != 2 {
		t.Fatalf("config without FILE: exit = %d, want 2; stderr:\n%s", code, stderr)
	}

	stdout, stderr, code := runCLI(t, dir, "config", script)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	cfg := effectiveConfig(t, stdout)
	for _, field := range []string{"editor", "interpretation", "runtime"} {
		if _, ok := cfg[field].(string); !ok {
			t.Errorf("field %q is not a string in:\n%s", field, stdout)
		}
	}
	// effectiveConfig validated the origins object; every policy field must
	// carry a provenance entry, "default" when no layer supplied one.
	origins := cfg["origins"].(map[string]any)
	for _, key := range []string{"editor", "interpretation", "runtime", "requests", "timeout"} {
		if _, ok := origins[key]; !ok {
			t.Errorf("origins has no entry for %q in:\n%s", key, stdout)
		}
	}
}

// config validates the TOML layers only; the script body is not parsed.
func TestConfigValidatesTOMLOnly(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "garbage.sos", frontmatter("version = 1")+"frobnicate the wibble\n")

	stdout, stderr, code := runCLI(t, dir, "config", script)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0 (body must not be parsed); stderr:\n%s", code, stderr)
	}
	effectiveConfig(t, stdout)
}

// `sos config` displays assisted/semantic preferences.
func TestConfigDisplaysNonCanonicalPreferences(t *testing.T) {
	home := isolateConfigHome(t)
	writeConfigFile(t, home, "config.toml", "version = 1\n\n[editor]\nassistance = \"automatic\"\n")
	dir := t.TempDir()
	script := writeScript(t, dir, "prefs.sos", frontmatter("version = 1\n\n[interpretation]\nmode = \"assisted\"\n\n[runtime]\njudgment = \"semantic\"\n")+simpleScript)

	stdout, stderr, code := runCLI(t, dir, "config", script)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	cfg := effectiveConfig(t, stdout)
	if got := cfgString(t, cfg, "editor"); got != "automatic" {
		t.Errorf("editor = %q, want automatic (from global config)", got)
	}
	if got := cfgString(t, cfg, "interpretation"); got != "assisted" {
		t.Errorf("interpretation = %q, want assisted (from frontmatter)", got)
	}
	if got := cfgString(t, cfg, "runtime"); got != "semantic" {
		t.Errorf("runtime = %q, want semantic (from frontmatter)", got)
	}
	if got := cfgOrigin(t, cfg, "editor"); got != filepath.Join(home, "config.toml") {
		t.Errorf("origins[editor] = %q, want the global config.toml path", got)
	}
	for _, key := range []string{"interpretation", "runtime"} {
		if got := cfgOrigin(t, cfg, key); got != "frontmatter" {
			t.Errorf("origins[%q] = %q, want frontmatter", key, got)
		}
	}
}

// Preferences resolve global -> project -> file; the project file is found by
// walking cwd ancestors; frontmatter cannot set editor preferences.
func TestConfigLayerPrecedence(t *testing.T) {
	home := isolateConfigHome(t)
	writeConfigFile(t, home, "config.toml", "version = 1\n\n[editor]\nassistance = \"off\"\n\n[interpretation]\nmode = \"canonical\"\n")
	proj := t.TempDir()
	writeConfigFile(t, proj, "sos.toml", "version = 1\n\n[editor]\nassistance = \"on-demand\"\n\n[interpretation]\nmode = \"assisted\"\n")
	inner := filepath.Join(proj, "reports", "daily")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}

	plain := writeScript(t, inner, "plain.sos", simpleScript)
	stdout, stderr, code := runCLI(t, inner, "config", plain)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	cfg := effectiveConfig(t, stdout)
	if got := cfgString(t, cfg, "editor"); got != "on-demand" {
		t.Errorf("editor = %q, want on-demand (project over global, found via ancestor walk)", got)
	}
	if got := cfgString(t, cfg, "interpretation"); got != "assisted" {
		t.Errorf("interpretation = %q, want assisted (project over global)", got)
	}
	if got := cfgOrigin(t, cfg, "editor"); got != filepath.Join(proj, "sos.toml") {
		t.Errorf("origins[editor] = %q, want the project sos.toml path", got)
	}

	withFile := writeScript(t, inner, "file.sos", frontmatter("version = 1\n\n[interpretation]\nmode = \"semantic\"\n")+simpleScript)
	stdout, stderr, code = runCLI(t, inner, "config", withFile)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	cfg = effectiveConfig(t, stdout)
	if got := cfgString(t, cfg, "interpretation"); got != "semantic" {
		t.Errorf("interpretation = %q, want semantic (frontmatter over project)", got)
	}
	if got := cfgOrigin(t, cfg, "interpretation"); got != "frontmatter" {
		t.Errorf("origins[interpretation] = %q, want frontmatter", got)
	}
	if got := cfgString(t, cfg, "editor"); got != "on-demand" {
		t.Errorf("editor = %q, want on-demand (frontmatter cannot set editor)", got)
	}
}

// Only the nearest sos.toml up the ancestor chain applies.
func TestConfigNearestProjectConfig(t *testing.T) {
	isolateConfigHome(t)
	outer := t.TempDir()
	writeConfigFile(t, outer, "sos.toml", "version = 1\n\n[interpretation]\nmode = \"semantic\"\n")
	mid := filepath.Join(outer, "mid")
	if err := os.MkdirAll(mid, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, mid, "sos.toml", "version = 1\n\n[interpretation]\nmode = \"canonical\"\n")
	inner := filepath.Join(mid, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}

	script := writeScript(t, inner, "nearest.sos", simpleScript)
	stdout, stderr, code := runCLI(t, inner, "config", script)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	cfg := effectiveConfig(t, stdout)
	if got := cfgString(t, cfg, "interpretation"); got != "canonical" {
		t.Errorf("interpretation = %q, want canonical (nearest sos.toml wins)", got)
	}
}

// Explicit requests/timeout ceilings intersect: the smallest wins, and the
// timeout is reported in nanoseconds.
func TestConfigBudgetCeilingsIntersect(t *testing.T) {
	home := isolateConfigHome(t)
	writeConfigFile(t, home, "config.toml", "version = 1\n\n[budget.run]\nrequests = 10\ntimeout = \"45s\"\n")
	proj := t.TempDir()
	writeConfigFile(t, proj, "sos.toml", "version = 1\n\n[budget.run]\nrequests = 3\n")
	script := writeScript(t, proj, "budget.sos", frontmatter("version = 1\n\n[budget.run]\nrequests = 7\ntimeout = \"2m\"\n")+simpleScript)

	stdout, stderr, code := runCLI(t, proj, "config", script)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	cfg := effectiveConfig(t, stdout)
	if got := cfgInt(t, cfg, "requests"); got != 3 {
		t.Errorf("requests = %d, want 3 (minimum of 10, 3, 7)", got)
	}
	if got := cfgInt(t, cfg, "timeout_ns"); got != int64(45*time.Second) {
		t.Errorf("timeout_ns = %d, want %d (minimum of 45s and 2m)", got, int64(45*time.Second))
	}
	if got := cfgOrigin(t, cfg, "requests"); got != filepath.Join(proj, "sos.toml") {
		t.Errorf("origins[requests] = %q, want the project sos.toml path", got)
	}

	// Later layers cannot relax an explicit ceiling by naming a larger one.
	raiser := writeScript(t, proj, "raise.sos", frontmatter("version = 1\n\n[budget.run]\nrequests = 50\ntimeout = \"5m\"\n")+simpleScript)
	stdout, stderr, code = runCLI(t, proj, "config", raiser)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	cfg = effectiveConfig(t, stdout)
	if got := cfgInt(t, cfg, "requests"); got != 3 {
		t.Errorf("requests = %d, want 3 (frontmatter 50 cannot raise the project ceiling)", got)
	}
	if got := cfgInt(t, cfg, "timeout_ns"); got != int64(45*time.Second) {
		t.Errorf("timeout_ns = %d, want %d (frontmatter 5m cannot raise the global ceiling)", got, int64(45*time.Second))
	}
}

// A deny judgment cannot be overridden by a later layer.
func TestConfigDenyIrreversible(t *testing.T) {
	home := isolateConfigHome(t)
	writeConfigFile(t, home, "config.toml", "version = 1\n\n[runtime]\njudgment = \"deny\"\n")

	proj := t.TempDir()
	writeConfigFile(t, proj, "sos.toml", "version = 1\n\n[runtime]\njudgment = \"explicit\"\n")
	script := writeScript(t, proj, "deny.sos", simpleScript)
	stdout, stderr, code := runCLI(t, proj, "config", script)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if got := cfgString(t, effectiveConfig(t, stdout), "runtime"); got != "deny" {
		t.Errorf("runtime = %q, want deny (global deny survives project explicit)", got)
	}

	fileDeny := writeScript(t, proj, "deny-file.sos", frontmatter("version = 1\n\n[runtime]\njudgment = \"explicit\"\n")+simpleScript)
	stdout, stderr, code = runCLI(t, proj, "config", fileDeny)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if got := cfgString(t, effectiveConfig(t, stdout), "runtime"); got != "deny" {
		t.Errorf("runtime = %q, want deny (global deny survives frontmatter explicit)", got)
	}
}

func TestConfigRejectsInvalidTOML(t *testing.T) {
	cases := []struct{ name, toml string }{
		{"missing version", "[editor]\nassistance = \"off\"\n"},
		{"wrong version", "version = 2\n"},
		{"unknown field", "version = 1\n[editor]\nassistance = \"off\"\nspeed = \"fast\"\n"},
		{"unknown section", "version = 1\n[mystery]\nmode = \"dark\"\n"},
		{"bad editor enum", "version = 1\n[editor]\nassistance = \"sometimes\"\n"},
		{"bad interpretation enum", "version = 1\n[interpretation]\nmode = \"loose\"\n"},
		{"bad runtime enum", "version = 1\n[runtime]\njudgment = \"maybe\"\n"},
		{"negative requests", "version = 1\n[budget.run]\nrequests = -1\n"},
		{"string requests", "version = 1\n[budget.run]\nrequests = \"3\"\n"},
		{"zero timeout", "version = 1\n[budget.run]\ntimeout = \"0s\"\n"},
		{"negative timeout", "version = 1\n[budget.run]\ntimeout = \"-5s\"\n"},
		{"empty editor enum", "version = 1\n[editor]\nassistance = \"\"\n"},
		{"empty interpretation enum", "version = 1\n[interpretation]\nmode = \"\"\n"},
		{"empty runtime enum", "version = 1\n[runtime]\njudgment = \"\"\n"},
		{"empty timeout", "version = 1\n[budget.run]\ntimeout = \"\"\n"},
		{"malformed toml", "version = \n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateConfigHome(t)
			writeConfigFile(t, home, "config.toml", tc.toml)
			dir := t.TempDir()
			script := writeScript(t, dir, "s.sos", simpleScript)
			stdout, stderr, code := runCLI(t, dir, "config", script)
			if code != 1 {
				t.Fatalf("config exit = %d, want 1 for %q; stdout:\n%sstderr:\n%s", code, tc.toml, stdout, stderr)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Errorf("no diagnostic for invalid config %q", tc.toml)
			}
		})
	}
}

// Frontmatter shares the schema but may not configure the editor.
func TestConfigRejectsFrontmatterEditor(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "editor.sos", frontmatter("version = 1\n\n[editor]\nassistance = \"off\"\n")+simpleScript)

	stdout, stderr, code := runCLI(t, dir, "config", script)
	if code != 1 {
		t.Fatalf("config exit = %d, want 1; stdout:\n%sstderr:\n%s", code, stdout, stderr)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("no diagnostic for editor preferences in frontmatter")
	}
}

// Canonical source stays local in every interpretation/runtime mode.
func TestRunCanonicalSourceInAllModes(t *testing.T) {
	for _, tc := range []struct{ name, toml string }{
		{"assisted interpretation", "version = 1\n[interpretation]\nmode = \"assisted\"\n"},
		{"semantic interpretation", "version = 1\n[interpretation]\nmode = \"semantic\"\n"},
		{"semantic runtime", "version = 1\n[runtime]\njudgment = \"semantic\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateConfigHome(t)
			dir := t.TempDir()
			script := writeScript(t, dir, "mode.sos", frontmatter(tc.toml)+simpleScript)
			stdout, stderr, code := runCLI(t, dir, "run", script)
			if code != 0 {
				t.Fatalf("run exit = %d, want 0; stdout:\n%sstderr:\n%s", code, stdout, stderr)
			}
			if strings.Contains(stderr, "usage requests=") {
				t.Errorf("canonical program contacted provider: %s", stderr)
			}
		})
	}
}

// A zero requests ceiling refuses provider contact without an API key, while
// canonical execution still runs.
func TestRunBudgetZeroDeniesJudgment(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	zeroBudget := "version = 1\n\n[budget.run]\nrequests = 0\n"

	probe := writeScript(t, dir, "probe.sos", frontmatter(zeroBudget)+judgeScript)
	stdout, stderr, code := runCLI(t, dir, "run", probe)
	if code != 1 {
		t.Fatalf("run exit = %d, want 1; stdout:\n%sstderr:\n%s", code, stdout, stderr)
	}
	if combined := stdout + stderr; !strings.Contains(combined, "budget exhausted") {
		t.Errorf("output = %q, want a budget-exhausted diagnostic", combined)
	}

	canonical := writeScript(t, dir, "canonical.sos", frontmatter(zeroBudget)+simpleScript)
	stdout, stderr, code = runCLI(t, dir, "run", canonical)
	if code != 0 {
		t.Fatalf("canonical run exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "5") {
		t.Errorf("stdout = %q, want the arithmetic result 5", stdout)
	}
}

// Runtime judgment deny refuses the live call with a denied diagnostic.
func TestRunRuntimeDenyDiagnostic(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "deny.sos", frontmatter("version = 1\n\n[runtime]\njudgment = \"deny\"\n\n[budget.run]\nrequests = 10\n")+judgeScript)

	stdout, stderr, code := runCLI(t, dir, "run", script)
	if code != 1 {
		t.Fatalf("run exit = %d, want 1; stdout:\n%sstderr:\n%s", code, stdout, stderr)
	}
	if combined := stdout + stderr; !strings.Contains(combined, "denied") {
		t.Errorf("output = %q, want a denied diagnostic", combined)
	}
}

// --max-calls stays positive-only regardless of configuration.
func TestRunMaxCallsRemainsPositiveOnly(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)

	for _, value := range []string{"0", "-3"} {
		_, stderr, code := runCLI(t, dir, "run", script, "--max-calls", value)
		if code != 2 {
			t.Fatalf("run --max-calls %s: exit = %d, want 2; stderr:\n%s", value, code, stderr)
		}
		if !strings.Contains(stderr, "invalid --max-calls") {
			t.Errorf("stderr = %q, want invalid --max-calls for %s", stderr, value)
		}
	}
	stdout, stderr, code := runCLI(t, dir, "run", script, "--max-calls", "5")
	if code != 0 {
		t.Fatalf("run --max-calls 5: exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "5") {
		t.Errorf("stdout = %q, want the arithmetic result 5", stdout)
	}
}

// Invalid frontmatter, global, or project config is rejected before any side
// effect of the script runs.
func TestInvalidConfigRejectedBeforeSideEffects(t *testing.T) {
	body := "save \"oops\" as text in \"should-not-exist\"\n"

	t.Run("invalid frontmatter", func(t *testing.T) {
		isolateConfigHome(t)
		dir := t.TempDir()
		script := writeScript(t, dir, "bad-fm.sos", frontmatter("version = 2")+body)
		_, stderr, code := runCLI(t, dir, "run", script)
		if code != 1 {
			t.Fatalf("run exit = %d, want 1; stderr:\n%s", code, stderr)
		}
		if strings.TrimSpace(stderr) == "" {
			t.Error("no diagnostic for invalid frontmatter")
		}
		if _, err := os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
			t.Error("side effect occurred despite invalid frontmatter")
		}
	})

	t.Run("invalid global config", func(t *testing.T) {
		home := isolateConfigHome(t)
		writeConfigFile(t, home, "config.toml", "version = 1\n[mystery]\nmode = \"dark\"\n")
		dir := t.TempDir()
		script := writeScript(t, dir, "ok.sos", body)
		_, stderr, code := runCLI(t, dir, "run", script)
		if code != 1 {
			t.Fatalf("run exit = %d, want 1; stderr:\n%s", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "should-not-exist")); !os.IsNotExist(err) {
			t.Error("side effect occurred despite invalid global config")
		}
	})

	t.Run("invalid project config", func(t *testing.T) {
		isolateConfigHome(t)
		proj := t.TempDir()
		writeConfigFile(t, proj, "sos.toml", "version = 1\n[budget.run]\nrequests = -2\n")
		script := writeScript(t, proj, "ok.sos", body)
		_, stderr, code := runCLI(t, proj, "run", script)
		if code != 1 {
			t.Fatalf("run exit = %d, want 1; stderr:\n%s", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(proj, "should-not-exist")); !os.IsNotExist(err) {
			t.Error("side effect occurred despite invalid project config")
		}
	})
}

// With frontmatter present, body diagnostics keep their original file lines.
func TestCheckBodyDiagnosticsUseOriginalLines(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	source := frontmatter("version = 1\n\n[budget.run]\nrequests = 0") + "frobnicate the wibble\n"
	// The frontmatter occupies lines 1-6, so the bad statement is line 7.
	script := writeScript(t, dir, "lined.sos", source)

	_, stderr, code := runCLI(t, dir, "check", script)
	if code != 1 {
		t.Fatalf("check exit = %d, want 1; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "lined.sos") || !strings.Contains(stderr, ":7:") {
		t.Errorf("stderr = %q, want a line-7 diagnostic naming lined.sos", stderr)
	}
}

// fmt preserves the frontmatter verbatim, including multiline TOML string
// contents; the body is still formatted.
func TestFmtPreservesFrontmatterMultilineString(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	// The multiline basic string is exactly "90s": TOML trims the newline
	// after the opening delimiter.
	source := "+++\nversion = 1\n\n[budget.run]\ntimeout = \"\"\"\n90s\"\"\"\n+++\n" + simpleScript
	script := writeScript(t, dir, "keep.sos", source)

	stdout, stderr, code := runCLI(t, dir, "fmt", script)
	if code != 0 {
		t.Fatalf("fmt exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "timeout = \"\"\"\n90s\"\"\"") {
		t.Errorf("fmt output drops the multiline TOML string:\n%s", stdout)
	}
	if !strings.Contains(stdout, "make total 2") {
		t.Errorf("fmt output loses the formatted body:\n%s", stdout)
	}

	// The preserved frontmatter still means 90s to the config resolver.
	stdout, stderr, code = runCLI(t, dir, "config", script)
	if code != 0 {
		t.Fatalf("config exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if got := cfgInt(t, effectiveConfig(t, stdout), "timeout_ns"); got != int64(90*time.Second) {
		t.Errorf("timeout_ns = %d, want %d", got, int64(90*time.Second))
	}
}

// A native build keeps the file's zero budget even when the artifact is later
// invoked where no configuration exists.
func TestBuildNativeKeepsFileBudgetZero(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	zeroBudget := "version = 1\n\n[budget.run]\nrequests = 0\n"

	probe := writeScript(t, dir, "probe.sos", frontmatter(zeroBudget)+judgeScript)
	probeBin := hostExecutablePath(filepath.Join(dir, "bin", "probe"))
	_, stderr, code := runCLI(t, dir, "build", probe, "--output", probeBin)
	if code != 0 {
		t.Fatalf("build exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	assertArtifact(t, probeBin)

	// Run away from every config source: fresh home, unrelated directory.
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	elsewhere := t.TempDir()
	stdout, stderr, code := runArtifact(t, probeBin, elsewhere)
	if code == 0 {
		t.Fatalf("artifact exit = 0, want nonzero; stdout:\n%sstderr:\n%s", stdout, stderr)
	}
	if combined := stdout + stderr; !strings.Contains(combined, "budget exhausted") {
		t.Errorf("artifact output = %q, want a budget-exhausted diagnostic", combined)
	}

	// Canonical arithmetic under the same zero budget still works.
	canonical := writeScript(t, dir, "canonical.sos", frontmatter(zeroBudget)+simpleScript)
	canonicalBin := hostExecutablePath(filepath.Join(dir, "bin", "canonical"))
	_, stderr, code = runCLI(t, dir, "build", canonical, "--output", canonicalBin)
	if code != 0 {
		t.Fatalf("build exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	stdout, stderr, code = runArtifact(t, canonicalBin, elsewhere)
	if code != 0 {
		t.Fatalf("canonical artifact exit = %d, want 0; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "5") {
		t.Errorf("artifact stdout = %q, want the arithmetic result 5", stdout)
	}
}

// Native builds bake in effective ceilings resolved from global and project
// config at build time, with nothing in the file itself.
func TestBuildNativeKeepsHostCeilings(t *testing.T) {
	// buildAway builds from cwd: host layers are discovered by walking the
	// build working directory's ancestors, so callers pass the directory whose
	// project config must be captured.
	buildAway := func(t *testing.T, cwd, script string) string {
		bin := hostExecutablePath(filepath.Join(t.TempDir(), "app"))
		_, stderr, code := runCLI(t, cwd, "build", script, "--output", bin)
		if code != 0 {
			t.Fatalf("build exit = %d, want 0; stderr:\n%s", code, stderr)
		}
		assertArtifact(t, bin)
		// Invoking where no configuration exists must not relax the policy.
		t.Setenv("SOS_CONFIG_HOME", t.TempDir())
		return bin
	}

	t.Run("global ceiling", func(t *testing.T) {
		home := isolateConfigHome(t)
		writeConfigFile(t, home, "config.toml", "version = 1\n\n[budget.run]\nrequests = 0\n")
		dir := t.TempDir()
		script := writeScript(t, dir, "judge.sos", judgeScript)
		bin := buildAway(t, dir, script)
		stdout, stderr, code := runArtifact(t, bin, t.TempDir())
		if code == 0 {
			t.Fatalf("artifact exit = 0, want nonzero; stdout:\n%sstderr:\n%s", stdout, stderr)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "budget exhausted") {
			t.Errorf("artifact output = %q, want a budget-exhausted diagnostic", combined)
		}
	})

	t.Run("project deny", func(t *testing.T) {
		isolateConfigHome(t)
		proj := t.TempDir()
		writeConfigFile(t, proj, "sos.toml", "version = 1\n\n[runtime]\njudgment = \"deny\"\n")
		script := writeScript(t, proj, "judge.sos", judgeScript)
		bin := buildAway(t, proj, script)
		stdout, stderr, code := runArtifact(t, bin, t.TempDir())
		if code == 0 {
			t.Fatalf("artifact exit = 0, want nonzero; stdout:\n%sstderr:\n%s", stdout, stderr)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "denied") {
			t.Errorf("artifact output = %q, want a denied diagnostic", combined)
		}
	})
}
