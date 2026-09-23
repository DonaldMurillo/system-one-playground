package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSysoneProjectCLIAndMCP(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	bin := hostExecutablePath(filepath.Join(filepath.Dir(sosBin), "sysone"))
	if output, err := exec.Command("go", "build", "-o", bin, "github.com/DonaldMurillo/system-one-playground/cmd/sysone").CombinedOutput(); err != nil {
		t.Fatalf("build sysone: %v %s", err, output)
	}
	root := t.TempDir()
	cli := func(input string, wantOK bool, args ...string) map[string]any {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"--project", root}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		var result map[string]any
		if e := json.Unmarshal(out, &result); e != nil {
			t.Fatalf("JSON %v: %s", e, out)
		}
		return result
	}
	cli("", true, "mkdir", "src")
	first := cli("show 42\n", true, "write", "src/main.sos")
	read := cli("", true, "read", "src/main.sos")
	if read["source"] != "show 42\n" || read["revision"] != first["revision"] {
		t.Fatal(read)
	}
	cli("show 9\n", false, "write", "src/main.sos")
	cli("show 43\n", true, "write", "src/main.sos", "--revision", read["revision"].(string))
	cli("show 0\n", false, "write", "src/main.sos", "--revision", read["revision"].(string))
	cli("", false, "read", "../outside.sos")
	cli("project-secret-acceptance", true, "env", "set", "TYPESAFE_API_KEY")
	env := cli("", true, "env", "list")
	encoded, _ := json.Marshal(env)
	if bytes.Contains(encoded, []byte("project-secret-acceptance")) {
		t.Fatal("secret returned")
	}
	cli("", false, "read", ".env")
	tree := cli("", true, "tree")
	encoded, _ = json.Marshal(tree)
	if bytes.Contains(encoded, []byte(".env")) {
		t.Fatal("secret file listed")
	}
	if mode := cli("", true, "settings", "studio"); mode["mode"] != "studio" {
		t.Fatal(mode)
	}
	if mode := cli("", true, "settings"); mode["mode"] != "studio" {
		t.Fatal(mode)
	}
	cmd := exec.Command(bin, "--project", root, "run", "src/main.sos")
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "43" {
		t.Fatalf("language run: %v %s", err, out)
	}
	cmd = exec.Command(bin, "--project", root, "check", "src/main.sos")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("language check: %v %s", err, out)
	}
	built := hostExecutablePath(filepath.Join(t.TempDir(), "sysone-program"))
	cmd = exec.Command(bin, "--project", root, "build", "src/main.sos", "--output", built)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("language build: %v %s", err, out)
	}
	if out, err := exec.Command(built).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "43" {
		t.Fatalf("built language: %v %s", err, out)
	}
	cli(`{"source":"show 44\n","path":"src/main.sos"}`, true, "api", "run")
	cli(`{"source":"notvalid stuff\n","path":"src/main.sos"}`, false, "api", "check")

	cmd = exec.Command(bin, "--project", root, "mcp")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	encoder := json.NewEncoder(stdin)
	request := func(id int, method string, params any) map[string]any {
		t.Helper()
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("MCP ended: %v %s", scanner.Err(), stderr.String())
		}
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response["id"] != float64(id) {
			t.Fatal(response)
		}
		return response
	}
	if r := request(1, "tools/list", nil); r["error"] == nil {
		t.Fatal("accepted uninitialized tools request")
	}
	if r := request(2, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "acceptance", "version": "1"}}); r["error"] != nil {
		t.Fatal(r)
	}
	encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	r := request(3, "tools/list", map[string]any{})
	if len(r["result"].(map[string]any)["tools"].([]any)) < 8 {
		t.Fatal(r)
	}
	call := func(id int, name string, args map[string]any, bad bool) map[string]any {
		t.Helper()
		r := request(id, "tools/call", map[string]any{"name": name, "arguments": args})
		if r["error"] != nil {
			t.Fatal(r)
		}
		result := r["result"].(map[string]any)
		if result["isError"] != bad {
			t.Fatal(r)
		}
		return result["structuredContent"].(map[string]any)
	}
	r = call(4, "project_read", map[string]any{"path": "src/main.sos"}, false)
	call(5, "project_write", map[string]any{"path": "src/main.sos", "revision": r["revision"], "source": "show 77\n"}, false)
	call(6, "project_write", map[string]any{"path": "src/main.sos", "revision": r["revision"], "source": "show 0\n"}, true)
	r = call(7, "run", map[string]any{"path": "src/main.sos", "source": "show 77\n"}, false)
	encoded, _ = json.Marshal(r)
	if !bytes.Contains(encoded, []byte("77")) {
		t.Fatal(r)
	}
	call(8, "check", map[string]any{"path": "src/main.sos", "source": "invalid stuff\n"}, true)
	call(9, "project_read", map[string]any{"path": ".env"}, true)
	r = call(10, "environment_list", map[string]any{}, false)
	encoded, _ = json.Marshal(r)
	if bytes.Contains(encoded, []byte("project-secret-acceptance")) {
		t.Fatal("MCP returned secret")
	}
	if r := request(11, "tools/call", map[string]any{"name": "project_read", "arguments": map[string]any{"path": 42}}); r["error"] == nil {
		t.Fatal("invalid argument accepted")
	}
	call(12, "build", map[string]any{"path": "src/main.sos", "output": "../escape"}, true)
	call(13, "build", map[string]any{"path": "../outside.sos", "output": "escaped"}, true)
	call(14, "build", map[string]any{"path": "src/main.sos", "output": "missing/bin"}, true)
	call(15, "build", map[string]any{"path": "src/main.sos", "output": ".env"}, true)
	// Exercise saved nested source + relative imports through the real MCP build.
	call(16, "project_write", map[string]any{"path": "src/lib.sos", "revision": "", "source": "package words\nexport clean\nto clean with value:\n  return value\n"}, false)
	call(17, "project_write", map[string]any{"path": "src/cli.sos", "revision": "", "source": "import \"./lib.sos\" as words\nwords.clean \"compiled project\" called result\nshow result\n"}, false)
	mcpOutput := filepath.Base(hostExecutablePath("mcp-built"))
	r = call(18, "build", map[string]any{"path": "src/cli.sos", "output": mcpOutput}, false)
	if r["ok"] != true {
		t.Fatal(r)
	}
	call(19, "build", map[string]any{"path": "src/cli.sos", "output": mcpOutput}, true)
	if err := os.Remove(filepath.Join(root, "src/lib.sos")); err != nil {
		t.Fatal(err)
	}
	builtCommand := exec.Command(hostExecutablePath(filepath.Join(root, "mcp-built")))
	builtCommand.Dir = t.TempDir()
	if out, err := builtCommand.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "compiled project" {
		t.Fatalf("MCP standalone graph: %v %s", err, out)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("MCP shutdown: %v %s", err, stderr.String())
	}
	if b, err := os.ReadFile(filepath.Join(root, "src/main.sos")); err != nil || string(b) != "show 77\n" {
		t.Fatalf("persisted edit: %v %s", err, b)
	}
}
