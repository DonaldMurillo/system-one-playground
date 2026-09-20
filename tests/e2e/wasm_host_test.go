package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowserHostClearsTimersAfterConfiguredRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds WASM")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required to execute generated host")
	}
	isolateConfigHome(t)
	dir := t.TempDir()
	source := writeScript(t, dir, "deadline.sos", frontmatter("version = 1\n[budget.run]\nrequests = 0\ntimeout = \"100ms\"\n")+"show \"finished\"\n")
	_, stderr, code := runCLI(t, dir, "build", source, "--output", filepath.Join(dir, "program.wasm"), "--target", "wasm-browser")
	if code != 0 {
		t.Fatal(stderr)
	}
	// Run the generated browser host script unchanged in a minimal Node host.
	// The queued deadline must not call Go again after main exits.
	harness := `const fs=require('node:fs');
globalThis.crypto=require('node:crypto').webcrypto;
require('./wasm_exec.js');
const output={textContent:''};
globalThis.document={getElementById:()=>output};
globalThis.fetch=async name=>new Response(fs.readFileSync(name),{headers:{'Content-Type':'application/wasm'}});
const html=fs.readFileSync('index.html','utf8');
require('node:vm').runInThisContext(html.match(/<script>([\s\S]*?)<\/script>/)[1]);
`
	path := filepath.Join(dir, "host.cjs")
	if err := os.WriteFile(path, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "finished") || strings.Contains(string(out), "Error") {
		t.Fatalf("host failed: %v\n%s", err, out)
	}
}
