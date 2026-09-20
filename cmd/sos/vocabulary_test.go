package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dictionary command shares the language server's catalog; these pin the
// CLI surface: usage errors, JSON envelope, text rendering, exit codes.

func TestVocabularyUsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := RunCLI([]string{"vocabulary", "--nope"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown flag exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "usage: sos vocabulary") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if code := RunCLI([]string{"vocabulary", "--query"}, &out, &errOut); code != 2 {
		t.Fatalf("missing flag value exit = %d", code)
	}
}

func TestVocabularyJSONEnvelope(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "demo.sos")
	if err := os.WriteFile(file, []byte("import \"std/text\"\n"), 0o644); err != nil {
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
				ID      string   `json:"id"`
				Enabled bool     `json:"enabled"`
				Bare    bool     `json:"bare,omitempty"`
				Alias   string   `json:"alias,omitempty"`
				Pattern []string `json:"patterns"`
			} `json:"entries"`
			Libraries []struct {
				Path string `json:"path"`
				Bare bool   `json:"bare"`
			} `json:"libraries"`
		} `json:"catalog"`
		Diagnostics []map[string]any `json:"diagnostics"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if envelope.Schema != "sos/vocabulary@1" {
		t.Errorf("schema = %q", envelope.Schema)
	}
	if len(envelope.Diagnostics) != 0 && envelope.Diagnostics == nil {
		t.Errorf("diagnostics must serialize as an array")
	}
	found := false
	for _, e := range envelope.Catalog.Entries {
		if e.ID == "std/text.trim" {
			found = true
			if !e.Enabled {
				t.Error("trim must be enabled by the open import")
			}
			hasBare := false
			for _, p := range e.Pattern {
				if p == "trim VALUE" {
					hasBare = true
				}
			}
			if !hasBare {
				t.Errorf("open import patterns = %v", e.Pattern)
			}
		}
	}
	if !found {
		t.Error("std/text.trim missing from CLI catalog")
	}
}

func TestVocabularyTextModeReportsForms(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "demo.sos")
	if err := os.WriteFile(file, []byte("import \"std/text\" as words\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := RunCLI([]string{"vocabulary", file, "--library", "std/text"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "aliased: calls require the words. prefix") {
		t.Errorf("aliased import must report the mandatory prefix; got:\n%s", text)
	}
	if strings.Contains(text, "bare sentences") {
		t.Errorf("aliased import must not report bare sentences; got:\n%s", text)
	}
}

func TestVocabularyMissingFileIsNotUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := RunCLI([]string{"vocabulary", "/nonexistent.sos"}, &out, &errOut); code != 1 {
		t.Fatalf("missing file exit = %d", code)
	}
}
