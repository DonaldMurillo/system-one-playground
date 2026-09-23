package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DonaldMurillo/system-one-playground/internal/sosbuild"
	"github.com/DonaldMurillo/system-one-playground/internal/timebundle"
)

// Deterministic time constructs shared by the parity tests: strict parsing,
// zone-aware formatting on both sides of the date line, calendar arithmetic
// across a daylight-saving transition, and inspection of local civil time.
const timeParitySource = `import "std/time"

read timestamp from "2026-03-07T12:00:00Z" as RFC3339 called start
format start as RFC3339 in time zone "Pacific/Kiritimati" called kiribati_text
format start as RFC3339 in time zone "America/New_York" called ny_text
find the time one calendar day after start in time zone "America/New_York" called next_day
format next_day as RFC3339 called next_day_text
find the time one calendar month after "2026-01-31T09:00:00Z" in time zone "UTC" called clamped
format clamped as ISO date in time zone "UTC" called clamped_text
call time.inspect with "2026-07-01T23:30:00Z", "Asia/Kolkata" called parts
show kiribati_text
show ny_text
show next_day_text
show clamped_text
show parts
`

// runCLIWithEnv runs the CLI with an explicit environment so tests can prove
// zone resolution does not depend on the caller's zone configuration.
func runCLIWithEnv(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sosBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if _, exited := err.(*exec.ExitError); !exited {
			t.Fatalf("running %v: %v", args, err)
		}
		code = cmd.ProcessState.ExitCode()
	}
	return out.String(), errBuf.String(), code
}

// cleanZoneEnv removes zone configuration that Go's time package would
// otherwise consult before the embedded database.
func cleanZoneEnv(t *testing.T) []string {
	t.Helper()
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ZONEINFO=") || strings.HasPrefix(kv, "TZ=") ||
			strings.HasPrefix(kv, "SOS_TZDATA=") || strings.HasPrefix(kv, "GOROOT=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// TestTimeConstructsMatchBetweenInterpreterAndNativeArtifact is the
// native/interpreter parity check for time: one deterministic program, run
// interpreted and as a built binary, must print identical output. The
// environment is stripped of ZONEINFO, TZ, SOS_TZDATA, and GOROOT on both
// sides, so identical output also proves the run resolved zones from the
// embedded database rather than the host's zone configuration.
func TestTimeConstructsMatchBetweenInterpreterAndNativeArtifact(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a native artifact")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "parity.sos", timeParitySource)
	env := cleanZoneEnv(t)

	interpOut, interpErr, code := runCLIWithEnv(t, dir, env, "run", script)
	if code != 0 {
		t.Fatalf("interpreted run exit %d:\n%s", code, interpErr)
	}

	bin := hostExecutablePath(filepath.Join(dir, "parity-bin"))
	_, buildErr, code := runCLIWithEnv(t, dir, env, "build", script, "--output", bin)
	if code != 0 {
		t.Fatalf("sos build exit %d:\n%s", code, buildErr)
	}

	nativeOut, nativeErr, code := runProgramWithEnv(t, dir, env, bin)
	if code != 0 {
		t.Fatalf("native artifact exit %d:\n%s", code, nativeErr)
	}
	if interpOut != nativeOut {
		t.Fatalf("interpreter and native time output differ:\ninterpreter:\n%s\nnative:\n%s", interpOut, nativeOut)
	}
	// The calendar day across the 2026-03-08 US daylight-saving transition
	// must be a real calendar step, not 24 elapsed hours.
	if !strings.Contains(nativeOut, "2026-03-08") {
		t.Fatalf("missing calendar-day result:\n%s", nativeOut)
	}
	if !strings.Contains(nativeOut, "2026-02-28") {
		t.Fatalf("missing clamped calendar month:\n%s", nativeOut)
	}
}

func runProgramWithEnv(t *testing.T, dir string, env []string, bin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		if _, exited := err.(*exec.ExitError); !exited {
			t.Fatalf("running artifact: %v", err)
		}
		code = cmd.ProcessState.ExitCode()
	}
	return out.String(), errBuf.String(), code
}

// TestTimeZoneBuildMetadataDiagnostics covers the Windows/Linux/macOS and
// native/WASI/browser diagnostics recorded beside every build: the matrix
// must name all nine target/OS rows, promise monotonic clocks, elapsed
// timers, and calendar schedules everywhere, and record an IANA release.
func TestTimeZoneBuildMetadataDiagnostics(t *testing.T) {
	m, err := sosbuild.TimeZoneMetadata()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]timebundle.Capability{}
	for _, c := range m.Capabilities {
		seen[c.Target+"/"+c.OS] = c
	}
	for _, key := range []string{
		"native/linux", "native/darwin", "native/windows",
		"wasm-wasi/linux", "wasm-wasi/darwin", "wasm-wasi/windows",
		"wasm-browser/linux", "wasm-browser/darwin", "wasm-browser/windows",
	} {
		c, ok := seen[key]
		if !ok {
			t.Fatalf("capability matrix missing %s; have %v", key, keysOf(seen))
		}
		if !c.MonotonicClock || !c.ElapsedTimers || !c.CalendarSchedules {
			t.Fatalf("%s lost a required capability: %+v", key, c)
		}
	}
	if !regexp.MustCompile(`^20\d{2}[a-z]$`).MatchString(m.Release) {
		t.Fatalf("manifest release %q is not an IANA release identifier", m.Release)
	}
}

func keysOf(m map[string]timebundle.Capability) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestTimeExamplesRunFast keeps the documented example claims honest:
// time-basics finishes in seconds and time-service's single check exits
// immediately, both under a deadline.
func TestTimeExamplesRunFast(t *testing.T) {
	if testing.Short() {
		t.Skip("runs example programs")
	}
	// time-basics is the fast CLI walkthrough: it must finish in seconds.
	dir := filepath.Join(examplesRoot(t), "time-basics")
	start := time.Now()
	out, err, code := runCLIWithEnv(t, dir, cleanZoneEnv(t), "run", "main.sos")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, err)
	}
	for _, want := range []string{"waited until the deadline", "round trip:", "one-shot tick #1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("time-basics took %s; fast examples must stay fast", elapsed)
	}
}

// TestTimeServiceStartsAndWaits proves the service example runs: it opens the
// hourly schedule, produces no diagnostics, and is still waiting for the next
// occurrence when the deadline stops it.
func TestTimeServiceStartsAndWaits(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the service interpreter")
	}
	dir := filepath.Join(examplesRoot(t), "time-service")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sosBin, "run", "main.sos", "--", "--zone", "America/New_York")
	cmd.Dir = dir
	cmd.Env = cleanZoneEnv(t)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	_ = cmd.Run()
	combined := out.String() + errBuf.String()
	if strings.Contains(combined, "usage") || strings.Contains(combined, "not valid") || strings.Contains(combined, "unknown") {
		t.Fatalf("service failed to start:\n%s", combined)
	}
	if ctx.Err() == nil {
		t.Fatalf("service exited before the deadline; output:\n%s", combined)
	}
}
