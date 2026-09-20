package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestHostIOPrimitives(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "skip"), 0755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, dir, "src/a.go", "alpha")
	writeScript(t, dir, "src/b.go", "beta")
	writeScript(t, dir, "src/skip/no.go", "excluded")
	script := writeScript(t, dir, "main.sos", `import "std/files" as files
import "std/path" as path
import "std/io" as io
call files.discover with "src", ["skip"] called found
show found
call path.join with "src", "a.go" called filename
call files.read with filename called content
call files.write with "copied.txt", content called written
call io.error with "checked" called logged
`)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("run failed %d: %s", code, stderr)
	}
	var files []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &files); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if strings.Join(files, ",") != "a.go,b.go" {
		t.Fatalf("files: %v", files)
	}
	if stderr != "checked\n" {
		t.Fatalf("stderr %q", stderr)
	}
	copied, err := os.ReadFile(filepath.Join(dir, "copied.txt"))
	if err != nil || string(copied) != "alpha" {
		t.Fatalf("copy %q: %v", copied, err)
	}
}

func TestStandardStreamsAndExit(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", `import "std/io" as io
call io.read with 64 called input
show input
call io.exit with 7 called unused
show "unreachable"
`)
	cmd := exec.Command(sosBin, "run", script)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("pipeline")
	var out, stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	exited, ok := err.(*exec.ExitError)
	if !ok || exited.ExitCode() != 7 {
		t.Fatalf("exit %v, stderr=%s", err, stderr.String())
	}
	if !strings.Contains(out.String(), "pipeline") || strings.Contains(out.String(), "unreachable") {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestProcessOutcome(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", `import "std/process" as process
call process.run with "go", ["version"] called result
show result
`)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("process: %s", stderr)
	}
	var result struct {
		Stdout string
		Status int
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != 0 || !strings.HasPrefix(result.Stdout, "go version") {
		t.Fatalf("result: %+v", result)
	}
}

func TestTextListRecordPrimitives(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", `import "std/text" as text
import "std/list" as list
import "std/record" as record
call text.split with "a,b", "," called parts
call list.at with parts, 1 called second
call text.slice with "aé日z", 1, 3 called sliced
call text.matches with "éx\nβ match", "(match)" called matches
make original {"a": 1}
call record.set with original, "b", 2 called updated
call record.keys with updated called keys
show second
show sliced
show matches
show original
show keys
`)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("run: %s", stderr)
	}
	for _, want := range []string{"b", "é日", `"byteStart":7`, `"column":3`, `"line":2`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestHostIOBuildTargets(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", `import "std/io" as io
call io.error with "native artifact" called logged
call io.exit with 9 called unused
`)
	binary := filepath.Join(t.TempDir(), "io-app")
	if _, stderr, code := runCLI(t, dir, "build", script, "--output", binary); code != 0 {
		t.Fatalf("build: %s", stderr)
	}
	_, stderr, code := runAppArtifact(t, dir, binary)
	if code != 9 || !strings.Contains(stderr, "native artifact") {
		t.Fatalf("native exit %d: %s", code, stderr)
	}
	script = writeScript(t, dir, "process.sos", `import "std/process" as process
call process.run with "go", ["version"] called result
show result
`)
	_, stderr, code = runCLI(t, dir, "build", script, "--target", "wasm-wasi", "--output", filepath.Join(dir, "process.wasm"))
	if code == 0 || !strings.Contains(stderr, "std/process") {
		t.Fatalf("WASI rejection exit %d: %s", code, stderr)
	}
}

func TestProcessCancellationAndFailure(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "bad.sos", `import "std/process" as process
call process.run with "go", ["not-a-go-command"] called result
show result
`)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 {
		t.Fatalf("nonzero process should return outcome: %s", stderr)
	}
	var outcome struct {
		Status int
		Stderr string
	}
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Status == 0 || outcome.Stderr == "" {
		t.Fatalf("outcome: %+v", outcome)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep executable unavailable")
	}
	quoted, _ := json.Marshal(sleep)
	source := "import \"std/process\" as process\ncall process.run with " + string(quoted) + ", [\"10\"] called result\n"
	p, diagnostics := sos.LoadProgram(filepath.Join(dir, "cancel.sos"), source)
	if len(diagnostics) > 0 {
		t.Fatalf("load: %v", diagnostics)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = sos.Run(ctx, p, sos.Options{Dir: dir})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("subprocess failed to stop promptly")
	}
}
