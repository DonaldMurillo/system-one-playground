package sosbuild

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestFailedBuildPreservesExistingArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "app")
	before := []byte("known-good-artifact")
	if err := os.WriteFile(output, before, 0o755); err != nil {
		t.Fatal(err)
	}
	failingGo := filepath.Join(dir, "failing-go")
	if err := os.WriteFile(failingGo, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	program, diagnostics := sos.Parse("show \"hello\"\n")
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if err := Build(context.Background(), BuildOptions{Program: program, Output: output, Name: "main.sos", GoBinary: failingGo}); err == nil {
		t.Fatal("expected build failure")
	}
	after, err := os.ReadFile(output)
	if err != nil || string(after) != string(before) {
		t.Fatalf("artifact after failed build=%q err=%v", after, err)
	}
}

func TestCanceledBuildPreservesExistingArtifact(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "app")
	if err := os.WriteFile(output, []byte("existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	program, diagnostics := sos.Parse("show \"hello\"\n")
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Build(ctx, BuildOptions{Program: program, Output: output, Name: "main.sos"}); err == nil {
		t.Fatal("expected cancellation")
	}
	after, err := os.ReadFile(output)
	if err != nil || string(after) != "existing" {
		t.Fatalf("artifact after canceled build=%q err=%v", after, err)
	}
}

func TestPublishStagedPathsRollsBackAllDestinations(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "app")
	second := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(first, []byte("old-app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("old-manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "staged-app")
	if err := os.WriteFile(staged, []byte("new-app"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := publishStagedPaths([]stagedPath{{staged, first}, {filepath.Join(dir, "missing-stage"), second}})
	if err == nil {
		t.Fatal("expected publish failure")
	}
	for path, want := range map[string]string{first: "old-app", second: "old-manifest"} {
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != want {
			t.Fatalf("%s=%q err=%v", path, data, readErr)
		}
	}
}

func TestPublishStagedBundleReplacesDirectoryAsAUnit(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "bundle")
	staged := filepath.Join(dir, "staged")
	if err := os.MkdirAll(filepath.Join(destination, "modules", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "manifest.json"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staged, "modules", "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "manifest.json"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := publishStagedPaths([]stagedPath{{staged, destination}}); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil || string(manifest) != "new" {
		t.Fatalf("manifest=%q err=%v", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "modules", "old")); !os.IsNotExist(err) {
		t.Fatalf("stale module survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "modules", "new")); err != nil {
		t.Fatalf("new module missing: %v", err)
	}
}
