package sos

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProject assembles an on-disk project: sos.toml plus optional files.
func writeProject(t *testing.T, toml string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SOS_CONFIG_HOME", filepath.Join(dir, "no-global-config"))
	if toml != "" {
		if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runSentence(t *testing.T, dir, source string) (map[string]any, error) {
	t.Helper()
	p, ds := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(ds) > 0 {
		return nil, dsError(ds)
	}
	res, err := Run(context.Background(), p, Options{Dir: dir})
	if res != nil {
		return res.Variables, err
	}
	return nil, err
}

func dsError(ds []Diagnostic) error {
	if len(ds) == 0 {
		return nil
	}
	return &testDiag{ds[0]}
}

type testDiag struct{ d Diagnostic }

func (t *testDiag) Error() string { return t.d.Message }

func TestBareImportExposesVocabularyAndKeepsQualifiedCalls(t *testing.T) {
	dir := writeProject(t, "", nil)
	vars, err := runSentence(t, dir, "import \"std/text\"\ntrim \"  hi  \" called a\ntext.upper a called b\ncall text.trim with \"  x  \" called c\nshow b\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["a"] != "hi" || vars["b"] != "HI" || vars["c"] != "x" {
		t.Errorf("a=%v b=%v c=%v", vars["a"], vars["b"], vars["c"])
	}
}

func TestAliasedImportRequiresPrefix(t *testing.T) {
	dir := writeProject(t, "", nil)
	// Bare use of an aliased library's word must not resolve: it stays an
	// unknown construction (deterministic, never a paid guess).
	_, ds := LoadProgram(filepath.Join(dir, "main.sos"), "import \"std/json\" as json\nencode 1 called v\n")
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "unknown construction") {
		t.Fatalf("bare use of aliased word must remain unknown construction, got %+v", ds)
	}
	vars, err := runSentence(t, dir, "import \"std/json\" as json\njson.encode 1 called v\nshow v\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["v"] != "1" {
		t.Errorf("v = %v, want 1", vars["v"])
	}
}

func TestStdSynonymsUppercaseLowercase(t *testing.T) {
	dir := writeProject(t, "", nil)
	vars, err := runSentence(t, dir, "import \"std/text\"\nuppercase \"aB\" called u\nlowercase \"aB\" called l\nshow u\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["u"] != "AB" || vars["l"] != "ab" {
		t.Errorf("u=%v l=%v", vars["u"], vars["l"])
	}
}

func TestConfigLibrariesEnableVocabulary(t *testing.T) {
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"

[[language.libraries]]
path = "std/text"

[[language.libraries]]
path = "example.com/acme/media"
as = "media"
`, map[string]string{
		"media/media.sos": "package media\nimport \"std/text\"\nexport preview\nword clip of preview\n\nto preview with clip:\n  text.trim clip called clean\n  return clean\n",
	})
	vars, err := runSentence(t, dir, "trim \"  a  \" called a\nmedia.clip \"  b  \" called b\ncall media.preview with \"  c  \" called c\nshow a\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["a"] != "a" || vars["b"] != "b" || vars["c"] != "c" {
		t.Errorf("a=%v b=%v c=%v", vars["a"], vars["b"], vars["c"])
	}
}

func TestConfigProjectReplacesGlobalAndEmptyDisables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SOS_CONFIG_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("version = 1\n[[language.libraries]]\npath = \"std/json\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Project replaces: only std/text enabled, std/json not.
	dir := writeProject(t, "version = 1\n[[language.libraries]]\npath = \"std/text\"\n", nil)
	if vars, err := runSentence(t, dir, "trim \"  a  \" called a\nshow a\n"); err != nil || vars["a"] != "a" {
		t.Fatalf("project library must enable trim: %v %v", vars, err)
	}
	_, ds := LoadProgram(filepath.Join(dir, "main.sos"), "encode 1 called v\n")
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "unknown construction") {
		t.Fatalf("replaced global library must not stay enabled, got %+v", ds)
	}
	// Explicit empty list disables everything inherited.
	dir2 := writeProject(t, "version = 1\n[language]\nlibraries = []\n", nil)
	_, ds2 := LoadProgram(filepath.Join(dir2, "main.sos"), "trim \"  a  \" called a\n")
	if len(ds2) != 1 || !strings.Contains(ds2[0].Message, "unknown construction") {
		t.Fatalf("explicit empty libraries must disable bare words, got %+v", ds2)
	}
}

func TestBareWordCollisionsAreDeterministicErrors(t *testing.T) {
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"

[[language.libraries]]
path = "std/text"

[[language.libraries]]
path = "example.com/acme/text2"
`, map[string]string{
		"text2/text2.sos": "package text2\nexport trim\n\nto trim with value:\n  return value\n",
	})
	_, ds := LoadProgram(filepath.Join(dir, "main.sos"), "show 1\n")
	if len(ds) == 0 || !strings.Contains(ds[0].Message, "collides with the same word") {
		t.Fatalf("two bare libraries exposing trim must collide, got %+v", ds)
	}
	if !strings.Contains(ds[0].Message, "as") {
		t.Errorf("collision must suggest an as alias: %v", ds[0].Message)
	}
}

func TestBareWordCollidingWithLocalActionIsError(t *testing.T) {
	dir := writeProject(t, "", nil)
	_, ds := LoadProgram(filepath.Join(dir, "main.sos"), "import \"std/text\"\nto trim with value:\n  return value\nshow 1\n")
	if len(ds) == 0 || !strings.Contains(ds[0].Message, "collides with action \"trim\"") {
		t.Fatalf("bare word colliding with a local action must error, got %+v", ds)
	}
}

func TestReservedWordCannotBeExposedBare(t *testing.T) {
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"

[[language.libraries]]
path = "example.com/acme/showy"
`, map[string]string{
		"showy/showy.sos": "package showy\nexport show\n\nto show with value:\n  return value\n",
	})
	_, ds := LoadProgram(filepath.Join(dir, "main.sos"), "show 1\n")
	if len(ds) == 0 || !strings.Contains(ds[0].Message, "cannot be exposed bare") {
		t.Fatalf("reserved sentence word must be rejected bare, got %+v", ds)
	}
}

func TestWordDeclarationSynonymAndErrors(t *testing.T) {
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"
`, map[string]string{
		"media/media.sos": "package media\nexport preview\nword clip of preview\n\nto preview with clip:\n  return clip\n",
	})
	// Unaliased import: bare word clip works.
	vars, err := runSentence(t, dir, "import \"example.com/acme/media\"\nclip \"z\" called v\nshow v\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["v"] != "z" {
		t.Errorf("v = %v", vars["v"])
	}
	// Unexported target: deterministic package error.
	bad := writeProject(t, "version = 1\n[module]\npath = \"example.com/acme\"\n", map[string]string{
		"media/media.sos": "package media\nto preview with clip:\n  return clip\nword clip of preview\n",
	})
	_, ds := LoadProgram(filepath.Join(bad, "main.sos"), "import \"example.com/acme/media\"\nshow 1\n")
	if len(ds) == 0 || !strings.Contains(ds[0].Message, "requires preview to be exported") {
		t.Fatalf("word of unexported action must error, got %+v", ds)
	}
	// Duplicate word: deterministic package error.
	dup := writeProject(t, "version = 1\n[module]\npath = \"example.com/acme\"\n", map[string]string{
		"media/media.sos": "package media\nexport preview\nexport again\nword clip of preview\nword clip of again\n\nto preview with clip:\n  return clip\n\nto again with clip:\n  return clip\n",
	})
	_, ds2 := LoadProgram(filepath.Join(dup, "main.sos"), "import \"example.com/acme/media\"\nshow 1\n")
	if len(ds2) == 0 || !strings.Contains(ds2[0].Message, "duplicate word clip") {
		t.Fatalf("duplicate word must error, got %+v", ds2)
	}
}

func TestModuleDoesNotInheritCallerVocabulary(t *testing.T) {
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"

[[language.libraries]]
path = "std/text"
`, map[string]string{
		"media/media.sos": "package media\nexport preview\n\nto preview with clip:\n  trim clip called clean\n  return clean\n",
	})
	_, ds := LoadProgram(filepath.Join(dir, "main.sos"), "import \"example.com/acme/media\" as media\ntrim \" a \" called a\nmedia.preview \" b \" called b\nshow a\n")
	found := false
	for _, d := range ds {
		if strings.Contains(d.Message, "module") && strings.Contains(d.Message, "unknown construction: trim clip called clean") {
			found = true
		}
	}
	if !found {
		t.Fatalf("modules must use definition-site vocabulary only, got %+v", ds)
	}
}

func TestSentMultiArgumentUsesWithSyntax(t *testing.T) {
	dir := writeProject(t, "version = 1\n[module]\npath = \"example.com/acme\"\n", map[string]string{
		"media/media.sos": "package media\nexport join\n\nto join with first, second:\n  return second\n",
	})
	// Two arguments must arrive: the action returns the second one.
	vars, err := runSentence(t, dir, "import \"example.com/acme/media\"\njoin with 2, 3 called v\nshow v\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["v"] != float64(3) {
		t.Errorf("v = %v (%T), want 3", vars["v"], vars["v"])
	}
	qvars, err := runSentence(t, dir, "import \"example.com/acme/media\" as media\nmedia.join with 10, 4 called q\nshow q\n")
	if err != nil {
		t.Fatal(err)
	}
	if qvars["q"] != float64(4) {
		t.Errorf("q = %v (%T), want 4", qvars["q"], qvars["q"])
	}
}

func TestSentArityMismatchIsCheckError(t *testing.T) {
	dir := writeProject(t, "version = 1\n[module]\npath = \"example.com/acme\"\n", map[string]string{
		"media/media.sos": "package media\nexport join\n\nto join with first, second:\n  return first\n",
	})
	_, ds := LoadProgram(filepath.Join(dir, "main.sos"), "import \"example.com/acme/media\"\njoin \"only\" called v\nshow v\n")
	// The vocabulary word resolves independently of arity, so users get one
	// actionable signature diagnostic rather than an unknown construction.
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "join expects 2 argument(s)") {
		t.Fatalf("arity-mismatched sentence call must report one signature error, got %+v", ds)
	}
}

func TestVocabularyCatalogShapeAndDeterminism(t *testing.T) {
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"

[[language.libraries]]
path = "std/text"
`, map[string]string{
		"media/media.sos": "package media\nexport preview\nword clip of preview\n\nto preview with clip:\n  return clip\n",
	})
	cat, ds := Vocabulary(filepath.Join(dir, "main.sos"), "trim \"  a  \" called a\nshow a\n")
	if len(ds) != 0 {
		t.Fatalf("diags: %+v", ds)
	}
	again, _ := Vocabulary(filepath.Join(dir, "main.sos"), "trim \"  a  \" called a\nshow a\n")
	b1, _ := json.Marshal(cat)
	b2, _ := json.Marshal(again)
	if string(b1) != string(b2) {
		t.Error("catalog must be deterministic")
	}
	var raw map[string]any
	if err := json.Unmarshal(b1, &raw); err != nil {
		t.Fatal(err)
	}
	entries, _ := raw["entries"].([]any)
	if len(entries) == 0 {
		t.Fatal("entries must not be empty")
	}
	wantKeys := []string{"id", "library", "name", "kind", "patterns", "origin", "enabled"}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		for _, k := range wantKeys {
			if _, ok := m[k]; !ok {
				t.Errorf("entry %v missing key %q (lowerCamelCase contract)", m["id"], k)
			}
		}
	}
	// Optional keys are omitempty: they appear exactly when they carry data.
	if m := byKey(t, entries, "std/text.upper"); m["description"] == nil || m["result"] == nil || m["synonyms"] == nil {
		t.Errorf("upper must carry description, result, synonyms: %+v", m)
	}
	if m := byKey(t, entries, "std/json.encode"); m["import"] == nil {
		t.Errorf("preview entries must carry an enabling import: %+v", m)
	}
	byID := map[string]map[string]any{}
	for _, e := range entries {
		m := e.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if m := byID["std/text.trim"]; m == nil || m["enabled"] != true {
		t.Errorf("std/text.trim must be enabled: %+v", byID["std/text.trim"])
	}
	if m := byID["std/text.upper"]; m == nil {
		t.Fatal("std/text.upper missing")
	} else {
		if syn, _ := m["synonyms"].([]any); len(syn) != 1 || syn[0] != "uppercase" {
			t.Errorf("upper synonyms = %+v", m["synonyms"])
		}
		if m["description"] == "" || m["result"] != "text" {
			t.Errorf("upper must carry description and result: %+v", m)
		}
	}
	if m := byID["std/json.encode"]; m == nil || m["enabled"] != false {
		t.Errorf("std/json.encode must appear disabled: %+v", byID["std/json.encode"])
	}
	localFound := false
	for id, m := range byID {
		if strings.HasPrefix(id, "example.com/acme/media") && m["origin"] == "local example.com/acme/media" {
			localFound = true
			if m["enabled"] != false {
				t.Errorf("local preview must be disabled: %+v", m)
			}
		}
	}
	if !localFound {
		t.Error("local package preview entries missing")
	}
	// Enabled entry reports the config origin.
	if m := byID["std/text.trim"]; !strings.Contains(m["origin"].(string), "config ") {
		t.Errorf("enabled entry origin must name the config layer: %+v", m["origin"])
	}
}

func byKey(t *testing.T, entries []any, id string) map[string]any {
	t.Helper()
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if m["id"] == id {
			return m
		}
	}
	t.Fatalf("entry %q missing", id)
	return nil
}

func TestVocabularyGraphRoundTripWithoutSourcesOrConfig(t *testing.T) {
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"

[[language.libraries]]
path = "std/text"

[[language.libraries]]
path = "example.com/acme/media"
as = "media"
`, map[string]string{
		"media/media.sos": "package media\nimport \"std/text\"\nexport preview\n\nto preview with clip:\n  text.trim clip called clean\n  return clean\n",
	})
	source := "trim \"  a  \" called a\nmedia.preview \"  b  \" called b\nshow a\n"
	p, ds := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	graphJSON, err := json.Marshal(p.Modules.Graph())
	if err != nil {
		t.Fatal(err)
	}
	var g ModuleGraph
	if err := json.Unmarshal(graphJSON, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Libraries) != 2 {
		t.Fatalf("graph must capture config libraries with origins: %+v", g.Libraries)
	}
	if g.Libraries[0].Path != "std/text" || !g.Libraries[0].Bare || g.Libraries[0].Origin == "" {
		t.Errorf("library spec[0] = %+v", g.Libraries[0])
	}
	if g.Libraries[1].Alias != "media" || g.Libraries[1].Bare {
		t.Errorf("library spec[1] = %+v", g.Libraries[1])
	}
	// Rebuild with sources and config gone: an empty graph input loader reads
	// only the embedded specs, and the module sources are embedded.
	orphan := t.TempDir()
	q, ds2 := LoadProgramFromGraph(filepath.Join(orphan, "standalone"), source, &g)
	if len(ds2) > 0 {
		t.Fatal(ds2)
	}
	var out strings.Builder
	res, err := Run(context.Background(), q, Options{Dir: orphan, Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Variables["a"] != "a" || res.Variables["b"] != "b" {
		t.Errorf("a=%v b=%v", res.Variables["a"], res.Variables["b"])
	}
}
func TestVocabularyConflictsNeverReachSemanticEngine(t *testing.T) {
	// A conflicting vocabulary is a deterministic check error even when the
	// line itself would otherwise flow to interpretation.
	dir := writeProject(t, `
version = 1

[module]
path = "example.com/acme"

[[language.libraries]]
path = "std/text"

[[language.libraries]]
path = "example.com/acme/text2"
`, map[string]string{
		"text2/text2.sos": "package text2\nexport trim\n\nto trim with value:\n  return value\n",
	})
	ds := CheckFile(filepath.Join(dir, "main.sos"), "trim \"  a  \" called a\n")
	if len(ds) == 0 {
		t.Fatal("conflict must be reported")
	}
	for _, d := range ds {
		if strings.Contains(d.Message, "collides with the same word") {
			return
		}
	}
	t.Fatalf("expected collision diagnostic, got %+v", ds)
}

func TestSentStatementInsidePackageUsesOwnImports(t *testing.T) {
	dir := writeProject(t, "version = 1\n[module]\npath = \"example.com/acme\"\n", map[string]string{
		"media/media.sos": "package media\nimport \"std/text\"\nexport preview\n\nto preview with clip:\n  trim clip called clean\n  return clean\n",
	})
	vars, err := runSentence(t, dir, "import \"example.com/acme/media\" as media\nmedia.preview \"  q  \" called v\nshow v\n")
	if err != nil {
		t.Fatal(err)
	}
	if vars["v"] != "q" {
		t.Errorf("v = %v", vars["v"])
	}
}
