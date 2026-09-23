package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/internal/studio"
)

type workspaceAPI struct {
	t      *testing.T
	server *studio.Server
	host   *httptest.Server
}

func newWorkspaceAPI(t *testing.T, dir string) *workspaceAPI {
	t.Helper()
	server, err := studio.New(studio.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server)
	t.Cleanup(host.Close)
	return &workspaceAPI{t, server, host}
}
func (a *workspaceAPI) call(path string, payload map[string]any, want int) map[string]any {
	a.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		a.t.Fatal(err)
	}
	req, err := http.NewRequest("POST", a.host.URL+path, bytes.NewReader(b))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("X-Studio-Token", a.server.Token())
	res, err := a.host.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	if res.StatusCode != want {
		a.t.Fatalf("%s action %v: status %d want %d: %s", path, payload["action"], res.StatusCode, want, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		a.t.Fatal(err)
	}
	return out
}
func (a *workspaceAPI) project(payload map[string]any, want int) map[string]any {
	a.t.Helper()
	return a.call("/api/project", payload, want)
}

func TestProjectWorkspaceFilesAndConfinement(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	outside := t.TempDir()
	writeScript(t, outside, "secret.sos", "outside secret")
	writeScript(t, dir, ".env", "TYPESAFE_API_KEY=never-return-this\n")
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	a := newWorkspaceAPI(t, dir)
	a.project(map[string]any{"action": "mkdir", "path": "src/lib"}, 200)
	created := a.project(map[string]any{"action": "write", "path": "src/main.sos", "source": "show 1\n"}, 200)
	revision := created["revision"]
	if revision == "" || revision == nil {
		t.Fatal("missing revision")
	}
	read := a.project(map[string]any{"action": "read", "path": "src/main.sos"}, 200)
	if read["source"] != "show 1\n" || read["revision"] != revision {
		t.Fatalf("readback: %v", read)
	}
	a.project(map[string]any{"action": "write", "path": "src/main.sos", "source": "show 2\n"}, 409)
	a.project(map[string]any{"action": "write", "path": "src/main.sos", "source": "show 2\n", "revision": revision}, 200)
	a.project(map[string]any{"action": "write", "path": "src/main.sos", "source": "show 3\n", "revision": revision}, 409)
	for _, path := range []string{"../secret.sos", filepath.Join(outside, "secret.sos"), ".env", "src/../.env", "linked/secret.sos"} {
		for _, action := range []string{"read", "write", "mkdir"} {
			a.project(map[string]any{"action": action, "path": path, "source": "overwrite"}, 400)
		}
	}
	for _, endpoint := range []string{"/api/check", "/api/run", "/api/analyze", "/api/lsp"} {
		a.call(endpoint, map[string]any{"path": "linked/secret.sos", "source": "show 1\n", "method": "textDocument/hover", "position": map[string]int{"line": 0, "character": 1}}, 400)
	}
	tree := a.project(map[string]any{"action": "tree"}, 200)
	encoded, _ := json.Marshal(tree)
	for _, secret := range []string{".env", "linked", "never-return-this"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("tree disclosed %s", secret)
		}
	}
	if !strings.Contains(string(encoded), "src/main.sos") {
		t.Fatal("tree missing source")
	}
	a.project(map[string]any{"action": "settings", "mode": "studio"}, 200)
	if got := a.project(map[string]any{"action": "settings"}, 200); got["mode"] != "studio" {
		t.Fatalf("settings: %v", got)
	}
	if got := newWorkspaceAPI(t, dir).project(map[string]any{"action": "settings"}, 200); got["mode"] != "studio" {
		t.Fatal("settings not persisted")
	}
	secret, err := os.ReadFile(filepath.Join(outside, "secret.sos"))
	if err != nil || string(secret) != "outside secret" {
		t.Fatal("outside file changed")
	}
}

func TestProjectWorkspaceEnvironmentAndProviderIsolation(t *testing.T) {
	fx := semNewFixture(t, semServeCooperative)
	processKey := os.Getenv("TYPESAFE_API_KEY")
	secret := "project-secret-not-in-results"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("provider did not receive project API key")
		}
		fx.serve(w, r)
	}))
	defer provider.Close()

	dir := t.TempDir()
	a := newWorkspaceAPI(t, dir)
	a.project(map[string]any{"action": "setEnvironment", "name": "TYPESAFE_BASE_URL", "value": provider.URL}, 200)
	response := a.project(map[string]any{"action": "setEnvironment", "name": "TYPESAFE_API_KEY", "value": secret}, 200)
	raw, _ := json.Marshal(response)
	if strings.Contains(string(raw), secret) {
		t.Fatal("setEnvironment disclosed value")
	}
	a.project(map[string]any{"action": "setEnvironment", "name": "TYPESAFE_DEFAULT_MODEL", "value": "project-fixture-model"}, 200)
	a.project(map[string]any{"action": "setEnvironment", "name": "BAD NAME", "value": secret}, 400)
	a.project(map[string]any{"action": "setEnvironment", "name": "VALID", "value": "line1\nline2"}, 400)
	a.project(map[string]any{"action": "setEnvironment", "name": "TOO_BIG", "value": strings.Repeat("x", 1<<20)}, 400)
	listing := a.project(map[string]any{"action": "environment"}, 200)
	raw, _ = json.Marshal(listing)
	if strings.Contains(string(raw), secret) {
		t.Fatal("environment listing disclosed secret")
	}
	st, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatalf(".env permissions: %v", st.Mode().Perm())
	}
	run := a.call("/api/run", map[string]any{"source": "judge \"blocked\" by jev \"Urgent\" called answer\nshow answer.p_yes\n"}, 200)
	if run["ok"] != true {
		t.Fatalf("project provider invocation: %v", run)
	}
	raw, _ = json.Marshal(run)
	if strings.Contains(string(raw), secret) {
		t.Fatal("run response or trace disclosed key")
	}
	fx.mu.Lock()
	model := fx.requests[len(fx.requests)-1].Model
	fx.mu.Unlock()
	if model != "project-fixture-model" {
		t.Fatalf("project model not used: %q", model)
	}
	if os.Getenv("TYPESAFE_API_KEY") != processKey {
		t.Fatal("project run mutated process environment")
	}
	explicit := a.call("/api/run", map[string]any{"source": "make items [{\"message\": \"blocked\"}]\nkeep items where jev:\n  ask \"Urgent\"\n  using message\n  model \"explicit-model\"\n  accept probability at least 0.85\n  on uncertain discard\nshow items\n"}, 200)
	if explicit["ok"] != true {
		t.Fatalf("explicit model: %v", explicit)
	}
	fx.mu.Lock()
	model = fx.requests[len(fx.requests)-1].Model
	fx.mu.Unlock()
	if model != "explicit-model" {
		t.Fatalf("explicit source model lost precedence: %s", model)
	}
	semanticSource := "+++\nversion = 1\n[interpretation]\nmode = \"semantic\"\n+++\nmake tickets [{\"team\": \"payments\"}]\nfilter tickets where team is \"payments\"\nshow tickets\n"
	first := a.call("/api/analyze", map[string]any{"source": semanticSource}, 200)
	if first["analysis"] == nil {
		t.Fatalf("analysis: %v", first)
	}
	fx.mu.Lock()
	model = fx.requests[len(fx.requests)-1].Model
	fx.mu.Unlock()
	if model != "project-fixture-model" {
		t.Fatalf("semantic analysis ignored project model: %s", model)
	}
	a.project(map[string]any{"action": "setEnvironment", "name": "TYPESAFE_DEFAULT_MODEL", "value": "new-project-model"}, 200)
	second := a.call("/api/analyze", map[string]any{"source": semanticSource}, 200)
	if second["reused"] == true {
		t.Fatal("environment mutation reused old analysis")
	}
	fx.mu.Lock()
	model = fx.requests[len(fx.requests)-1].Model
	fx.mu.Unlock()
	if model != "new-project-model" {
		t.Fatalf("new analysis used stale model: %s", model)
	}

	// Switching a native window must not export its .env into other sessions.
	b := newWorkspaceAPI(t, t.TempDir())
	if err := b.server.SetDir(dir); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TYPESAFE_API_KEY") != processKey {
		t.Fatal("switching project mutated process environment")
	}
	fx.requireContacts(t, 1)
}

func TestProjectWorkspaceNestedSourceContext(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	a := newWorkspaceAPI(t, dir)
	a.project(map[string]any{"action": "mkdir", "path": "tools/lib"}, 200)
	a.project(map[string]any{"action": "write", "path": "tools/sos.toml", "source": "version = 1\n[module]\npath = \"example.com/tool\"\n"}, 200)
	a.project(map[string]any{"action": "write", "path": "tools/lib/main.sos", "source": "package lib\nexport label\nto label:\n  return \"nested package\"\n"}, 200)
	source := "import \"example.com/tool/lib\" as lib\ncall lib.label called label\nshow label\n"
	result := a.call("/api/check", map[string]any{"path": "tools/main.sos", "source": source}, 200)
	diagnostics, _ := result["diagnostics"].([]any)
	if len(diagnostics) != 0 {
		t.Fatalf("nested check: %v", result)
	}
	result = a.call("/api/run", map[string]any{"path": "tools/main.sos", "source": source}, 200)
	if result["ok"] != true || result["output"] != "nested package\n" {
		t.Fatalf("nested run: %v", result)
	}
}

func TestProjectWorkspaceRejectsNamedPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX FIFO fixture does not represent Windows named pipes")
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("named-pipe fixture requires mkfifo")
	}
	dir := t.TempDir()
	if out, err := exec.Command(mkfifo, filepath.Join(dir, "pipe")).CombinedOutput(); err != nil {
		t.Fatalf("create FIFO: %v %s", err, out)
	}
	a := newWorkspaceAPI(t, dir)
	a.project(map[string]any{"action": "read", "path": "pipe"}, 400)
	a.project(map[string]any{"action": "write", "path": "pipe", "source": "replace"}, 400)
}
