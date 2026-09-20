package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTimeoutFlagsRejectInvalidBounds(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	script := writeScript(t, dir, "timeout.sos", "save \"effect\" as text in \"effect.txt\"\n")
	for _, limit := range []string{"0", "-1s", "9223372036854775807", "bad"} {
		_, err, code := runCLI(t, dir, "run", script, "--timeout", limit)
		if code != 2 || !strings.Contains(err, "invalid --timeout") {
			t.Fatalf("%s: exit=%d err=%s", limit, code, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "effect.txt")); !os.IsNotExist(err) {
		t.Fatalf("invalid flag executed effect: %v", err)
	}
}

func TestNativeEnvironmentBudgetBounds(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "")
	dir := t.TempDir()
	script := writeScript(t, dir, "bounded.sos", "judge \"hello\" by jev \"Urgent\" called answer\n")
	binary := filepath.Join(dir, "bounded")
	_, err, code := runCLI(t, dir, "build", script, "--output", binary)
	if code != 0 {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, value, want string }{
		{"SOS_MAX_CALLS", "0", "budget exhausted"},
		{"SOS_MAX_CALLS", "bad", "invalid SOS_MAX_CALLS"},
		{"SOS_MAX_CALLS", "-1", "invalid SOS_MAX_CALLS"},
		{"SOS_TIMEOUT", "0s", "invalid SOS_TIMEOUT"},
		{"SOS_TIMEOUT", "9223372036854775807", "invalid SOS_TIMEOUT"},
	} {
		cmd := exec.Command(binary)
		cmd.Dir = dir
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "SOS_MAX_CALLS=") && !strings.HasPrefix(env, "SOS_TIMEOUT=") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Env = append(cmd.Env, tc.key+"="+tc.value)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), tc.want) {
			t.Fatalf("%s=%s: %s (%v)", tc.key, tc.value, out, err)
		}
	}
}
