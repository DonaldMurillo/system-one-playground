package sosbuild

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestPrepareExternalBundleRejectsMissingTargetAndWrongChecksum(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "plugin")
	data := []byte("plugin")
	if err := os.WriteFile(artifact, data, 0o755); err != nil {
		t.Fatal(err)
	}
	osName := runtime.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	target := osName + "-" + runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		target = osName + "-x64"
	}
	base := sos.ExternalModuleSpec{Key: "external:test/module", Definition: "schema=1", DistributionMode: "bundled"}
	for name, artifacts := range map[string][]sos.ExternalArtifactSpec{
		"missing target": {{Target: "not-this-host", Path: artifact, SHA256: "sha256:unused"}},
		"wrong checksum": {{Target: target, Path: artifact, SHA256: "sha256:deadbeef"}},
	} {
		t.Run(name, func(t *testing.T) {
			spec := base
			spec.Artifacts = artifacts
			err := prepareExternalBundle(&sos.ModuleGraph{External: []sos.ExternalModuleSpec{spec}}, t.TempDir(), "app")
			if err == nil {
				t.Fatal("expected bundle failure")
			}
		})
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	spec := base
	spec.Artifacts = []sos.ExternalArtifactSpec{{Target: target, Path: artifact, SHA256: "sha256:" + sum}}
	out := t.TempDir()
	if err := prepareExternalBundle(&sos.ModuleGraph{External: []sos.ExternalModuleSpec{spec}}, out, "app"); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil || !strings.Contains(string(manifest), sum) {
		t.Fatalf("manifest=%q err=%v", manifest, err)
	}
}

func TestNormalizeGraphForDistributionRemovesSourceRoots(t *testing.T) {
	for _, root := range []string{filepath.Join(string(filepath.Separator), "one", "project"), filepath.Join(string(filepath.Separator), "other", "project")} {
		key := filepath.Join(root, "lib")
		graph := &sos.ModuleGraph{
			Entry:     map[string]sos.ModuleEdge{"lib": {Key: key, Alias: "lib"}},
			Modules:   []sos.ModuleSpec{{Key: key, Name: "lib", Files: []sos.ModuleFileSpec{{Name: "lib.sos", Source: "to value:\n  return 1\n"}}}},
			Libraries: []sos.LibrarySpec{{Path: "example/lib", Key: key, Origin: filepath.Join(root, "sos.toml")}},
		}
		normalizeGraphForDistribution(graph, root)
		encoded, err := json.Marshal(graph)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), root) || graph.Entry["lib"].Key != "project:lib" || graph.Libraries[0].Origin != "" {
			t.Fatalf("graph retained source-root data: %s", encoded)
		}
	}
}
