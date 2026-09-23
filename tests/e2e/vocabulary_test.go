package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/DonaldMurillo/system-one-playground/sos"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVocabularyConfiguredImportsAndStandalone(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", `version = 1
[[language.libraries]]
path = "std/text"
[[language.libraries]]
path = "std/json"
as = "json"
`)
	script := writeScript(t, dir, "main.sos", `trim "  hello  " called clean
uppercase clean called heading
json.encode heading called payload
show payload
`)
	for _, operation := range []string{"check", "fmt", "run"} {
		out, stderr, code := runCLI(t, dir, operation, script)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", operation, code, stderr)
		}
		if operation == "run" && strings.TrimSpace(out) != `"HELLO"` {
			t.Fatalf("unexpected output %q", out)
		}
	}
	binary := hostExecutablePath(filepath.Join(t.TempDir(), "vocabulary"))
	if _, stderr, code := runCLI(t, dir, "build", script, "-o", binary); code != 0 {
		t.Fatalf("build: %s", stderr)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != `"HELLO"` {
		t.Fatalf("standalone vocabulary: %v: %s", err, out)
	}
}

func TestVocabularyAliasRequiresParent(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", `import "std/text" as words
words.trim "  hello  " called clean
show clean
`)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 || strings.TrimSpace(out) != "hello" {
		t.Fatalf("qualified sentence: %d %q %s", code, out, stderr)
	}
	writeScript(t, dir, "main.sos", `import "std/text" as words
trim "  hello  " called clean
show clean
`)
	if _, stderr, code = runCLI(t, dir, "check", script); code == 0 {
		t.Fatalf("aliased library exposed bare vocabulary: %s", stderr)
	}
}

func TestVocabularyConflictRejectsCheckRunAndBuild(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	library := filepath.Join(dir, "custom")
	if err := os.Mkdir(library, 0755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, library, "main.sos", `package custom
export trim
word polish of trim
to trim with value:
  return value
`)
	script := writeScript(t, dir, "main.sos", `import "std/text"
import "./custom"
show "must not execute"
`)
	for _, operation := range []string{"check", "run", "build"} {
		args := []string{operation, script}
		if operation == "build" {
			args = append(args, "-o", filepath.Join(dir, "bad"))
		}
		out, stderr, code := runCLI(t, dir, args...)
		if code == 0 {
			t.Fatalf("%s accepted conflicting definitions: %s %s", operation, out, stderr)
		}
		if strings.Contains(out, "must not execute") {
			t.Fatalf("%s performed effects before conflict diagnostic", operation)
		}
		if !strings.Contains(stderr, "trim") {
			t.Fatalf("%s missing conflicting vocabulary: %s", operation, stderr)
		}
	}
	writeScript(t, dir, "main.sos", `import "std/text"
import "./custom" as custom
trim "  hello  " called clean
custom.polish clean called result
show result
`)
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 || strings.TrimSpace(out) != "hello" {
		t.Fatalf("alias did not resolve conflict: %d %q %s", code, out, stderr)
	}
}

func TestVocabularyProjectReplacesGlobalLibraries(t *testing.T) {
	global := t.TempDir()
	t.Setenv("SOS_CONFIG_HOME", global)
	writeScript(t, global, "config.toml", "version = 1\n[[language.libraries]]\npath = \"std/text\"\n")
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", "trim \"  global  \" called clean\nshow clean\n")
	out, stderr, code := runCLI(t, dir, "run", script)
	if code != 0 || strings.TrimSpace(out) != "global" {
		t.Fatalf("global vocabulary: %d %q %s", code, out, stderr)
	}
	writeScript(t, dir, "sos.toml", "version = 1\n[language]\nlibraries = []\n")
	if _, stderr, code = runCLI(t, dir, "check", script); code == 0 {
		t.Fatalf("empty project list did not disable global vocabulary: %s", stderr)
	}
}

func TestVocabularyDictionaryUsesProjectAndSourceContext(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", `version = 1
[[language.libraries]]
path = "std/text"
`)
	script := writeScript(t, dir, "main.sos", `import "std/json" as wire
show "dictionary must not execute me"
`)
	out, stderr, code := runCLI(t, dir, "vocabulary", script, "--json")
	if code != 0 {
		t.Fatalf("dictionary: %d %s", code, stderr)
	}
	var response struct {
		Schema  string                 `json:"schema"`
		Catalog *sos.VocabularyCatalog `json:"catalog"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("invalid dictionary JSON: %v: %s", err, out)
	}
	if response.Schema != "sos/vocabulary@1" || response.Catalog == nil {
		t.Fatalf("unexpected dictionary envelope: %s", out)
	}
	textFound, jsonFound := false, false
	for _, lib := range response.Catalog.Libraries {
		if lib.Path == "std/text" && lib.Bare {
			textFound = true
		}
		if lib.Path == "std/json" && !lib.Bare && lib.Alias == "wire" {
			jsonFound = true
		}
	}
	if !textFound || !jsonFound {
		t.Fatalf("missing configured/source library scopes: %s", out)
	}
	for _, entry := range response.Catalog.Entries {
		if entry.Library != "std/json" || !entry.Enabled {
			continue
		}
		for _, pattern := range entry.Patterns {
			if !strings.HasPrefix(pattern, "wire.") {
				t.Fatalf("dictionary promises invalid bare form: %+v", entry)
			}
		}
	}

	out, stderr, code = runCLI(t, dir, "vocabulary", script, "--json", "--query", "whitespace", "--library", "std/text")
	if code != 0 || !strings.Contains(out, "trim") || strings.Contains(out, `"library": "std/json"`) {
		t.Fatalf("dictionary search: %d %s %s", code, out, stderr)
	}
}

func TestVocabularyBrowserWasmKeepsConfiguredImports(t *testing.T) {
	if testing.Short() {
		t.Skip("builds WASM")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required to execute generated browser host")
	}
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	sourceDir, artifactDir := t.TempDir(), t.TempDir()
	writeScript(t, sourceDir, "sos.toml", "version = 1\n[[language.libraries]]\npath = \"std/text\"\n")
	source := writeScript(t, sourceDir, "main.sos", "uppercase \"hello wasm\" called heading\nshow heading\n")
	_, stderr, code := runCLI(t, sourceDir, "build", source, "--output", filepath.Join(artifactDir, "program.wasm"), "--target", "wasm-browser")
	if code != 0 {
		t.Fatal(stderr)
	}
	if err := os.RemoveAll(sourceDir); err != nil {
		t.Fatal(err)
	}
	harness := `const fs=require('node:fs');
globalThis.crypto=require('node:crypto').webcrypto;
require('./wasm_exec.js');
const output={textContent:''};
globalThis.document={getElementById:()=>output};
globalThis.fetch=async name=>new Response(fs.readFileSync(name),{headers:{'Content-Type':'application/wasm'}});
const html=fs.readFileSync('index.html','utf8');
require('node:vm').runInThisContext(html.match(/<script>([\s\S]*?)<\/script>/)[1]);
`
	path := writeScript(t, artifactDir, "host.cjs", harness)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, path)
	cmd.Dir = artifactDir
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "HELLO WASM") || strings.Contains(string(out), "Error") {
		t.Fatalf("standalone browser vocabulary: %v\n%s", err, out)
	}
}

func TestVocabularySourceAliasOverridesConfiguredOpenImport(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[[language.libraries]]\npath = \"std/text\"\n")
	source := writeScript(t, dir, "main.sos", "import \"std/text\" as words\nwords.trim \"  hello  \" called result\nshow result\n")
	out, stderr, code := runCLI(t, dir, "run", source)
	if code != 0 || strings.TrimSpace(out) != "hello" {
		t.Fatalf("source override: %d %s %s", code, out, stderr)
	}
	writeScript(t, dir, "main.sos", "import \"std/text\" as words\ntrim \"  hello  \" called result\nshow result\n")
	if _, stderr, code = runCLI(t, dir, "check", source); code == 0 {
		t.Fatalf("source alias still exposed configured bare vocabulary: %s", stderr)
	}
}

func TestVocabularyConnectorsAndZeroArgumentActions(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	lib := filepath.Join(dir, "greetings")
	if err := os.Mkdir(lib, 0755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, lib, "main.sos", "package greetings\nexport hello\nto hello:\n  return \"hello\"\n")
	source := writeScript(t, dir, "main.sos", `import "std/text"
import "./greetings"
hello called greeting
trim with "  called result  " called clean
show greeting
show clean
`)
	out, stderr, code := runCLI(t, dir, "run", source)
	if code != 0 || strings.TrimSpace(out) != "hello\ncalled result" {
		t.Fatalf("sentence connectors: %d %q %s", code, out, stderr)
	}
}

func TestVocabularyCoreSynonymsCannotBeShadowed(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	for _, word := range []string{"show", "print", "emit", "set"} {
		t.Run(word, func(t *testing.T) {
			dir := t.TempDir()
			lib := filepath.Join(dir, "custom")
			if err := os.Mkdir(lib, 0755); err != nil {
				t.Fatal(err)
			}
			writeScript(t, lib, "main.sos", "package custom\nexport "+word+"\nto "+word+" with value:\n  return value\n")
			source := writeScript(t, dir, "main.sos", "import \"./custom\"\n")
			_, stderr, code := runCLI(t, dir, "check", source)
			if code == 0 || !strings.Contains(stderr, word) {
				t.Fatalf("reserved word %s accepted: %d %s", word, code, stderr)
			}
		})
	}
}

func TestVocabularyStdioDictionaryUsesConfiguredAlias(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[[language.libraries]]\npath = \"std/text\"\nas = \"words\"\n")
	source := "words.trim \"  hello  \" called clean\nshow clean\n"
	script := writeScript(t, dir, "main.sos", source)
	uri := (&url.URL{Scheme: "file", Path: script}).String()
	rootURI := (&url.URL{Scheme: "file", Path: dir}).String()
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{"rootUri": rootURI, "capabilities": map[string]any{}}))
	input.Write(notification("initialized", map[string]any{}))
	input.Write(notification("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "sos", "version": 1, "text": source}}))
	input.Write(request(2, "sos/vocabulary", map[string]any{"textDocument": map[string]any{"uri": uri}, "query": "trim"}))
	input.Write(request(3, "shutdown", nil))
	input.Write(notification("exit", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sosBin, "lsp")
	cmd.Dir = dir
	cmd.Stdin = &input
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("LSP process: %v", err)
	}
	for _, msg := range parseFrames(t, out) {
		if msg["id"] != float64(2) {
			continue
		}
		result := getMap(t, msg, "result")
		catalog := getMap(t, result, "catalog")
		entries, ok := catalog["entries"].([]any)
		if !ok {
			t.Fatalf("no dictionary entries: %+v", result)
		}
		for _, row := range entries {
			entry := row.(map[string]any)
			if entry["name"] != "trim" || entry["enabled"] != true {
				continue
			}
			patterns, ok := entry["patterns"].([]any)
			if !ok || len(patterns) == 0 {
				t.Fatalf("missing usable patterns: %+v", entry)
			}
			for _, pattern := range patterns {
				if !strings.HasPrefix(pattern.(string), "words.") {
					t.Fatalf("LSP exposed bare form: %+v", entry)
				}
			}
			return
		}
		t.Fatalf("LSP lost configured alias: %+v", result)
	}
	t.Fatal("no dictionary response")
}

func TestVocabularyDictionaryDoesNotCallIndirectEffectsPure(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	for _, name := range []string{"effects", "facade"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(t, filepath.Join(dir, "effects"), "main.sos", "package effects\nexport announce\nto announce with value:\n  show value\n  return value\n")
	writeScript(t, filepath.Join(dir, "facade"), "main.sos", "package facade\nimport \"../effects\" as effects\nexport announce\nto announce with value:\n  call effects.announce with value called result\n  return result\n")
	source := writeScript(t, dir, "main.sos", "import \"./facade\" as facade\n")
	out, stderr, code := runCLI(t, dir, "vocabulary", source, "--json")
	if code != 0 {
		t.Fatalf("dictionary: %d %s", code, stderr)
	}
	var response struct {
		Catalog *sos.VocabularyCatalog `json:"catalog"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil || response.Catalog == nil {
		t.Fatalf("dictionary JSON: %v %s", err, out)
	}
	for _, entry := range response.Catalog.Entries {
		if entry.Library != "./facade" || entry.Name != "announce" {
			continue
		}
		for _, effect := range entry.Effects {
			if effect == "output" || effect == "unknown" {
				return
			}
		}
		t.Fatalf("indirect output reported as harmless: %+v", entry)
	}
	t.Fatal("missing imported action metadata")
}

func TestVocabularyDictionaryDiscoversDirectoryPackagesOnce(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[module]\npath = \"example.com/vocab\"\n")
	pkg := filepath.Join(dir, "cleaning")
	if err := os.Mkdir(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, pkg, "public.sos", "package cleaning\nexport tidy\nto tidy with value:\n  call helper with value called result\n  return result\n")
	writeScript(t, pkg, "private.sos", "package cleaning\nto helper with value:\n  return value\n")
	source := writeScript(t, dir, "main.sos", "show \"hello\"\n")
	for _, enabled := range []bool{false, true} {
		if enabled {
			writeScript(t, dir, "main.sos", "import \"example.com/vocab/cleaning\" as cleaning\n")
		}
		out, stderr, code := runCLI(t, dir, "vocabulary", source, "--json")
		if code != 0 {
			t.Fatalf("dictionary: %d %s", code, stderr)
		}
		var response struct {
			Catalog *sos.VocabularyCatalog `json:"catalog"`
		}
		if err := json.Unmarshal([]byte(out), &response); err != nil || response.Catalog == nil {
			t.Fatalf("dictionary JSON: %v %s", err, out)
		}
		count := 0
		for _, entry := range response.Catalog.Entries {
			if entry.Library == "example.com/vocab/cleaning" && entry.Name == "tidy" {
				count++
				if entry.Enabled != enabled {
					t.Fatalf("wrong enabled state: %+v", entry)
				}
			}
		}
		if count != 1 {
			t.Fatalf("expected one package preview/import, got %d: %s", count, out)
		}
	}
}

func TestVocabularyInsideNestedCLICommand(t *testing.T) {
	t.Setenv("SOS_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	writeScript(t, dir, "sos.toml", "version = 1\n[[language.libraries]]\npath = \"std/text\"\n")
	source := writeScript(t, dir, "main.sos", `command greeting:
  option name as text default "  hello  "
  command shout:
    trim name called clean
    uppercase clean called loud
    show loud
`)
	out, stderr, code := runCLI(t, dir, "run", source, "--", "shout")
	if code != 0 || strings.TrimSpace(out) != "HELLO" {
		t.Fatalf("nested command vocabulary: %d %q %s", code, out, stderr)
	}
}
