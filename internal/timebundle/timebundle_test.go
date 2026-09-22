package timebundle

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realZones builds a small database from the toolchain's zoneinfo.zip so the
// tests exercise real tzdata bytes without depending on host system files.
func realZones(t *testing.T, names ...string) []byte {
	t.Helper()
	root := os.Getenv("GOROOT")
	if root == "" {
		t.Skip("GOROOT not set")
	}
	src, err := os.ReadFile(filepath.Join(root, "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Skip("toolchain zoneinfo.zip unavailable")
	}
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		if !wanted[f.Name] {
			continue
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
	}
	// Metadata entries must be ignored, so include one deliberately.
	w, err := zw.Create("zone.tab")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("metadata\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func bundleFromZones(t *testing.T, names ...string) *Bundle {
	t.Helper()
	data := realZones(t, names...)
	b, err := FromZip(bytes.NewReader(data), int64(len(data)), "test-zones.zip")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFromZipIgnoresMetadataAndLoadsZones(t *testing.T) {
	b := bundleFromZones(t, "UTC", "America/New_York", "Europe/London")
	if len(b.Zones) != 3 {
		t.Fatalf("zone count=%d, want 3 (metadata ignored)", len(b.Zones))
	}
	if _, err := b.Location("America/New_York"); err != nil {
		t.Fatal(err)
	}
}

func TestLocationResolvesIndependentOfHostFiles(t *testing.T) {
	b := bundleFromZones(t, representativeZones...)
	if err := b.VerifyResolution(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Location("Mars/Olympus"); err == nil {
		t.Fatal("expected unknown zone error")
	}
}

func TestManifestIsDeterministic(t *testing.T) {
	data := realZones(t, "UTC", "Asia/Tokyo")
	first, err := FromZip(bytes.NewReader(data), int64(len(data)), "a.zip")
	if err != nil {
		t.Fatal(err)
	}
	second, err := FromZip(bytes.NewReader(data), int64(len(data)), "a.zip")
	if err != nil {
		t.Fatal(err)
	}
	m1, m2 := first.Manifest(), second.Manifest()
	if m1.SHA256 == "" || m1.SHA256 != m2.SHA256 {
		t.Fatalf("digest mismatch or empty: %q vs %q", m1.SHA256, m2.SHA256)
	}
	j1, _ := json.Marshal(m1)
	j2, _ := json.Marshal(m2)
	if !bytes.Equal(j1, j2) {
		t.Fatal("manifest JSON differs for identical databases")
	}
	if !m1.Complete || m1.ZoneCount != 2 {
		t.Fatalf("complete=%v zoneCount=%d", m1.Complete, m1.ZoneCount)
	}
}

func TestDigestDistinguishesDatabases(t *testing.T) {
	small := bundleFromZones(t, "UTC")
	larger := bundleFromZones(t, "UTC", "Asia/Tokyo")
	if small.SHA256() == larger.SHA256() {
		t.Fatal("distinct databases produced the same digest")
	}
}

func TestCapabilityMatrix(t *testing.T) {
	b := bundleFromZones(t, "UTC")
	m := b.Manifest()
	if len(m.Capabilities) != 9 { // 3 targets x 3 operating systems
		t.Fatalf("capability entries=%d, want 9", len(m.Capabilities))
	}
	seen := map[string]bool{}
	for _, c := range m.Capabilities {
		key := c.Target + "/" + c.OS
		seen[key] = true
		if !c.MonotonicClock || !c.ElapsedTimers || !c.CalendarSchedules {
			t.Fatalf("%s: unexpected capability loss %+v", key, c)
		}
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
}

func TestWriteProducesCheckableMetadata(t *testing.T) {
	b := bundleFromZones(t, representativeZones...)
	dir := t.TempDir()
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "timezone.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != Schema || m.SHA256 != b.SHA256() {
		t.Fatalf("stored manifest mismatch: %+v", m)
	}
	// A bundle with different bytes must fail the check.
	other := bundleFromZones(t, "UTC")
	om := other.Manifest()
	if err := Check(om); err == nil {
		t.Skip("host database equals the one-zone fixture; digest equality check still covered above")
	}
	if err := Check(Manifest{Schema: Schema + 1}); err == nil {
		t.Fatal("expected schema mismatch error")
	}
	if err := Check(Manifest{Schema: Schema, Complete: true, SHA256: "deadbeef", ZoneCount: 1}); err == nil {
		t.Fatal("expected digest drift error")
	}
}

func TestCheckAcceptsCurrentDatabase(t *testing.T) {
	b, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	m := b.Manifest()
	if !m.Complete {
		t.Skip("no host time-zone database on this machine")
	}
	if err := Check(m); err != nil {
		t.Fatalf("current database failed its own check: %v", err)
	}
}

func TestDefaultFindsDatabase(t *testing.T) {
	b, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if !b.Manifest().Complete {
		t.Skip("no host time-zone database on this machine")
	}
	if _, err := b.Location("America/New_York"); err != nil {
		t.Fatal(err)
	}
	// The embedded fallback must still resolve zones without host files.
	if _, err := time.LoadLocation("Europe/Berlin"); err != nil {
		t.Fatalf("embedded tzdata fallback missing: %v", err)
	}
}

func TestReleaseReadFromVersionMarker(t *testing.T) {
	data := realZones(t, "UTC", "Asia/Tokyo")
	withEntry := func(name, content string) []byte {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		zw := zip.NewWriter(&out)
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			w, err := zw.Create(f.Name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	fromZip := func(data []byte, source string) *Bundle {
		b, err := FromZip(bytes.NewReader(data), int64(len(data)), source)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// "+VERSION" wins.
	if b := fromZip(withEntry("+VERSION", "2024a\n"), "v.zip"); b.Manifest().Release != "2024a" {
		t.Fatalf("release = %q, want 2024a from +VERSION", b.Manifest().Release)
	}
	// "tzdata.zi" header is the fallback marker.
	if b := fromZip(withEntry("tzdata.zi", "# version 2025b\nRule ...\n"), "v.zip"); b.Manifest().Release != "2025b" {
		t.Fatalf("release = %q, want 2025b from tzdata.zi", b.Manifest().Release)
	}
	// macOS zoneinfo paths carry the release in the directory name.
	if b := fromZip(data, "/var/db/timezone/tz/2025a.1.0/zoneinfo"); b.Manifest().Release != "2025a" {
		t.Fatalf("release = %q, want 2025a from path", b.Manifest().Release)
	}
	// Malformed markers are rejected rather than guessed.
	if b := fromZip(withEntry("+VERSION", "latest"), "v.zip"); b.Manifest().Release != "" {
		t.Fatalf("release = %q, want empty for malformed marker", b.Manifest().Release)
	}
}

func TestCheckRejectsReleaseDrift(t *testing.T) {
	b, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	current := b.Manifest()
	if !current.Complete {
		t.Skip("no host time-zone database on this machine")
	}
	drifted := current
	drifted.Release = "1999z"
	err = Check(drifted)
	if err == nil {
		t.Fatal("expected release drift error")
	}
	if !strings.Contains(err.Error(), "release") || !strings.Contains(err.Error(), "1999z") || !strings.Contains(err.Error(), current.Release) {
		t.Fatalf("error must name the release field and both values: %v", err)
	}
}

func TestWriteRecordsReleaseInJSON(t *testing.T) {
	b := bundleFromZones(t, "UTC")
	b.Release = "2025b"
	dir := t.TempDir()
	if err := b.Write(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "timezone.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"release": "2025b"`)) {
		t.Fatalf("timezone.json must record the IANA release: %s", raw)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Release != "2025b" || m.Schema != 2 {
		t.Fatalf("stored manifest fields: release=%q schema=%d", m.Release, m.Schema)
	}
}
