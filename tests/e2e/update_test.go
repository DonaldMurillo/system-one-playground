package e2e

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSysoneUpdateCheckUsesPublishedCLIReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"tag_name":"v0.6.9"},{"tag_name":"vscode-v0.6.1"}]`))
	}))
	defer server.Close()

	bin := filepath.Join(t.TempDir(), "sysone")
	if output, err := exec.Command("go", "build", "-o", bin, "github.com/DonaldMurillo/system-one-playground/cmd/sysone").CombinedOutput(); err != nil {
		t.Fatalf("build sysone: %v %s", err, output)
	}
	cmd := exec.Command(bin, "update", "--check")
	cmd.Env = append(cmd.Environ(), "SYSONESCRIPT_RELEASES_API="+server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update --check: %v %s", err, output)
	}
	text := string(output)
	if !strings.Contains(text, "0.6.1 is available") || !strings.Contains(text, "you have 0.6.0") {
		t.Fatalf("unexpected update output: %s", text)
	}
}
