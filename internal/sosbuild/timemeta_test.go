package sosbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonaldMurillo/system-one-playground/internal/timebundle"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}
func TestGeneratedMainEmbedsTimeZoneDatabase(t *testing.T) {
	var out bytes.Buffer
	if err := mainTemplate.Execute(&out, mainData{Name: "main.sos", Source: "show \"hi\"\n", Browser: true}); err != nil {
		t.Fatal(err)
	}
	src := out.String()
	if !strings.Contains(src, "_ \"time/tzdata\"") {
		t.Fatal("generated main must embed time/tzdata so artifacts are independent of host zone files")
	}
}

func TestTimeZoneMetadataCoversAllTargetsAndOSes(t *testing.T) {
	m, err := TimeZoneMetadata()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range m.Capabilities {
		seen[c.Target+"/"+c.OS] = true
	}
	for _, key := range []string{
		"native/linux", "native/darwin", "native/windows",
		"wasm-wasi/linux", "wasm-wasi/darwin", "wasm-wasi/windows",
		"wasm-browser/linux", "wasm-browser/darwin", "wasm-browser/windows",
	} {
		if !seen[key] {
			t.Fatalf("missing capability entry %s", key)
		}
	}
	if m.Schema != timebundle.Schema {
		t.Fatalf("schema %d != %d", m.Schema, timebundle.Schema)
	}
}

func TestBuildRecordsTimeZoneMetadataBesideArtifact(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "timedapp")
	program, diagnostics := sos.Parse("show \"hello\"\n")
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if err := Build(testContext(t), BuildOptions{Program: program, Output: output, Name: "main.sos"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output + ".timezone.json")
	if err != nil {
		t.Fatalf("artifact metadata missing: %v", err)
	}
	var m timebundle.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Complete || m.ZoneCount == 0 || m.SHA256 == "" {
		t.Fatalf("incomplete timezone provenance: %+v", m)
	}
	if err := timebundle.Check(m); err != nil {
		t.Fatalf("recorded provenance does not verify: %v", err)
	}
}
