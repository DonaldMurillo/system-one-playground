package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI help exposes the full filesystem tooling contract: English patterns and
// fallback calls, parameter names, effects, possible typed failures, and
// target availability.
func TestFilespecVocabularyCommandShowsFileContract(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "demo.sos")
	if err := os.WriteFile(file, []byte("import \"std/files\" as files\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := RunCLI([]string{"vocabulary", file}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"read file PATH as text called NAME",
		"write CONTENTS to file PATH",
		"walk through folder ROOT at most LIMIT entries called NAME",
		"stream files under folder ROOT called NAME",
		"watch folder ROOT recursively called NAME",
		"remove folder PATH including its contents",
		"may fail with: ",
		"FileNotFound",
		"FileTraversalLimitExceeded",
		"targets: ",
		"filesystem-read",
		"filesystem-write",
		"destructive",
		"policy as text",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestFilespecVocabularyJSONCarriesFailuresAndTargets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "demo.sos")
	if err := os.WriteFile(file, []byte("import \"std/files\" as files\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := RunCLI([]string{"vocabulary", file, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	var envelope struct {
		Schema  string `json:"schema"`
		Catalog struct {
			Entries []struct {
				Name             string   `json:"name"`
				Patterns         []string `json:"patterns"`
				Params           []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"params"`
				PossibleFailures []string `json:"possibleFailures"`
				Effects          []string `json:"effects"`
				Targets          []string `json:"targets"`
			} `json:"entries"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if envelope.Schema != "sos/vocabulary@1" {
		t.Fatalf("schema = %q", envelope.Schema)
	}
	found := map[string]bool{}
	for _, entry := range envelope.Catalog.Entries {
		switch entry.Name {
		case "read_text":
			found["read_text"] = true
			if !sliceContains(entry.PossibleFailures, "FileNotFound") {
				t.Errorf("read_text failures = %v", entry.PossibleFailures)
			}
			if !sliceContains(entry.Effects, "filesystem-read") {
				t.Errorf("read_text effects = %v", entry.Effects)
			}
			if !sliceContains(entry.Targets, "native") {
				t.Errorf("read_text targets = %v", entry.Targets)
			}
		case "write_text":
			found["write_text"] = true
			hasPolicy := false
			for _, param := range entry.Params {
				if param.Name == "policy" {
					hasPolicy = true
				}
			}
			if !hasPolicy {
				t.Errorf("write_text params = %+v", entry.Params)
			}
		case "watch":
			found["watch"] = true
			if !sliceContains(entry.PossibleFailures, "FileWatchOverflow") {
				t.Errorf("watch failures = %v", entry.PossibleFailures)
			}
		}
	}
	for _, name := range []string{"read_text", "write_text", "watch"} {
		if !found[name] {
			t.Errorf("%s missing from JSON catalog", name)
		}
	}
}

func sliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
