// Package-local module and standard-library coverage for the sos CLI,
// exercising real processes through the shared harness in cli_test.go.
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if e := os.MkdirAll(filepath.Dir(path), 0o755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, []byte(body), 0o644); e != nil {
			t.Fatal(e)
		}
	}
	return dir
}

// A local package exercising definition-site imports and private actions.
const pkgUtil = `package util
import "std/text"

export shout

to quieten with name:
  call text.lower with name called soft
  return soft

to shout with name:
  call quieten with name called soft
  call text.upper with soft called loud
  return loud + "!"
`

func TestRunLocalPackageCall(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"packages/util/util.sos": pkgUtil,
		"main.sos": `import "./packages/util"
make who "World"
call util.shout with who called result
show result
`,
	})
	stdout, stderr, code := runCLI(t, dir, "run", filepath.Join(dir, "main.sos"))
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "WORLD!") {
		t.Errorf("stdout = %q, want it to contain WORLD!", stdout)
	}
}

// Module actions see only their arguments: caller bindings never leak in.
func TestModuleActionsUseDefinitionSiteEnvironment(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"packages/peek/peek.sos": `package peek
export peek

to peek with x:
  show secret
  return x
`,
		"main.sos": `import "./packages/peek"
make secret 41
call peek.peek with 1 called out
show out
`,
	})
	_, stderr, code := runCLI(t, dir, "run", filepath.Join(dir, "main.sos"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (module must not see caller names)", code)
	}
	if !strings.Contains(stderr, "unknown name") || !strings.Contains(stderr, "module peek") {
		t.Errorf("stderr = %q, want an unknown-name error attributed to module peek", stderr)
	}
}

func TestRunPrivateExportFailure(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"packages/util/util.sos": pkgUtil,
		"main.sos": `import "./packages/util"
call util.quieten with "X" called out
show out
`,
	})
	stdout, stderr, code := runCLI(t, dir, "check", filepath.Join(dir, "main.sos"))
	if code != 1 {
		t.Fatalf("check exit = %d, want 1; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "quieten is not exported by module util") {
		t.Errorf("stderr = %q, want a private-export diagnostic", stderr)
	}
	_, stderr, code = runCLI(t, dir, "run", filepath.Join(dir, "main.sos"))
	if code != 1 || !strings.Contains(stderr, "not exported") {
		t.Errorf("run exit = %d stderr = %q, want failure naming the private action", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no output", stdout)
	}
}

func TestCheckImportCycle(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"packages/a/a.sos": `package a
import "../b"
export run
to run with x:
  return x
`,
		"packages/b/b.sos": `package b
import "../a"
export go
to go with x:
  return x
`,
		"main.sos": `import "./packages/a"
call a.run with 1 called out
show out
`,
	})
	_, stderr, code := runCLI(t, dir, "check", filepath.Join(dir, "main.sos"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "import cycle") {
		t.Errorf("stderr = %q, want an import cycle diagnostic", stderr)
	}
}

func TestRunStdTextOps(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.sos": `import "std/text"
call text.trim with "  pad  " called a
call text.upper with a called b
call text.lower with "MiXeD" called c
show b
show c
`,
	})
	stdout, stderr, code := runCLI(t, dir, "run", filepath.Join(dir, "main.sos"))
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"PAD", "mixed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestRunStdJsonRoundTrip(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.sos": `import "std/json"
make record ""
make row with:
  name from "ada"
  score from 5
call json.encode with row called text
show text
call json.decode with text called back
show score of back
`,
	})
	stdout, stderr, code := runCLI(t, dir, "run", filepath.Join(dir, "main.sos"))
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, `"name":"ada"`) || !strings.Contains(stdout, "5") {
		t.Errorf("stdout = %q, want encoded JSON and decoded score", stdout)
	}
}

func TestRunStdOpTypeFailure(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.sos": `import "std/text"
call text.upper with 7 called out
show out
`,
	})
	_, stderr, code := runCLI(t, dir, "run", filepath.Join(dir, "main.sos"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (typed argument mismatch)", code)
	}
	if !strings.Contains(stderr, "must be text") {
		t.Errorf("stderr = %q, want a typed argument error", stderr)
	}
}

func TestModulePathFromProjectSosToml(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"sos.toml":            "version = 1\n[module]\npath = \"example.com/tools\"\n",
		"lib/greet/greet.sos": strings.ReplaceAll(pkgUtil, "util", "greet"),
		"scripts/main.sos": `import "example.com/tools/lib/greet"
call greet.shout with "hey" called out
show out
`,
	})
	stdout, stderr, code := runCLI(t, dir, "run", filepath.Join(dir, "scripts", "main.sos"))
	if code != 0 {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "HEY!") {
		t.Errorf("stdout = %q, want it to contain HEY!", stdout)
	}
}

func TestRejectUnsupportedImports(t *testing.T) {
	cases := []struct {
		name, script, want string
	}{
		{"network", `import "https://example.com/pkg"
show 1
`, "network imports are not supported"},
		{"absolute", `import "/etc/passwd"
show 1
`, "absolute import paths are not supported"},
		{"unknown std", `import "std/regex"
show 1
`, "unknown standard package"},
		{"bare without module path", `import "util"
show 1
`, "requires a [module] path"},
		{"missing file", `import "./nope"
show 1
`, "no module package"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{"main.sos": tc.script})
			_, stderr, code := runCLI(t, dir, "check", filepath.Join(dir, "main.sos"))
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.want)
			}
		})
	}
}

func TestRunWithoutImportContextStillWorks(t *testing.T) {
	// Plain scripts keep their exact behavior with the module-aware loader.
	dir := t.TempDir()
	script := writeScript(t, dir, "total.sos", simpleScript)
	stdout, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 || !strings.Contains(stdout, "5") {
		t.Fatalf("exit = %d stdout = %q stderr = %s", code, stdout, stderr)
	}
}

// The standalone artifact embeds the module graph: package sources can be
// deleted before it runs, from an unrelated working directory.
func TestBuildNativeThenRemoveSourcesAndRun(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"packages/util/util.sos": pkgUtil,
		"main.sos": `import "./packages/util"
import "std/json"
make who "packaged"
call util.shout with who called result
call json.encode with result called encoded
show encoded
`,
	})
	output := hostExecutablePath(filepath.Join(dir, "dist", "greeter"))
	if _, stderr, code := runCLI(t, dir, "build", filepath.Join(dir, "main.sos"), "--output", output); code != 0 {
		t.Fatalf("build exit = %d; stderr:\n%s", code, stderr)
	}
	assertArtifact(t, output)
	if e := os.RemoveAll(filepath.Join(dir, "packages")); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(dir, "main.sos")); e != nil {
		t.Fatal(e)
	}
	elsewhere := t.TempDir()
	var out, errBuf strings.Builder
	cmd := exec.Command(output)
	cmd.Dir = elsewhere
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("artifact run: %v\nstderr:\n%s", err, errBuf.String())
	}
	if !strings.Contains(out.String(), `"PACKAGED!"`) {
		t.Errorf("artifact stdout = %q, want the packaged JSON result", out.String())
	}
}
