// Package timebundle captures the IANA time-zone database used at build time,
// records its provenance and per-target clock capabilities as build metadata,
// and verifies zone resolution against the bundled bytes rather than host
// zone files. Native, WASI, and browser artifacts embed Go's time/tzdata
// database; this package documents which database that build was checked
// against so artifacts never silently vary with host zone-file availability.
package timebundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // embedded fallback so LoadLocation never depends on host files
)

// Schema is the version of the timezone.json build-metadata format.
// Version 2 added the Release field (IANA release of the bundled database).
const Schema = 2

// Capability describes timing support for one build target and host OS.
type Capability struct {
	Target            string `json:"target"`
	OS                string `json:"os"`
	MonotonicClock    bool   `json:"monotonicClock"`
	ElapsedTimers     bool   `json:"elapsedTimers"`
	CalendarSchedules bool   `json:"calendarSchedules"`
	Notes             string `json:"notes,omitempty"`
}

// Manifest is the build-time time-zone provenance and capability record
// written beside standalone artifacts as timezone.json. It is deterministic:
// identical databases produce identical manifests, with no build timestamps.
type Manifest struct {
	Schema     int    `json:"schema"`
	Source     string `json:"source"`
	SourceKind string `json:"sourceKind"` // zip, dir, or embedded
	// Release is the IANA release identifier of the bundled database, such
	// as "2025b". It is read from the database when the source carries a
	// version marker and falls back to the release documented for the Go
	// toolchain's embedded time/tzdata. It is empty only when neither is
	// known.
	Release        string       `json:"release,omitempty"`
	ZoneCount      int          `json:"zoneCount"`
	SHA256         string       `json:"sha256,omitempty"`
	Complete       bool         `json:"complete"`
	Representative []string     `json:"representativeZones"`
	Capabilities   []Capability `json:"capabilities"`
}

// representativeZones are zones whose historical and future rules exercise
// DST in both directions, half-hour DST shifts, and whole-offset changes.
var representativeZones = []string{
	"UTC",
	"America/New_York",
	"Europe/London",
	"Europe/Berlin",
	"Australia/Lord_Howe",
	"Pacific/Kiritimati",
	"Asia/Kolkata",
	"Asia/Tokyo",
}

// Bundle is a read-only IANA time-zone database.
type Bundle struct {
	// Source describes where the database came from (path or "go:time/tzdata").
	Source string
	// SourceKind is "zip", "dir", or "embedded".
	SourceKind string
	// Release is the IANA release identifier when the source carries one
	// (a "+VERSION" file, a "tzdata.zi" header, or a macOS zoneinfo path).
	Release string
	// Zones maps IANA names to compiled tzdata bytes.
	Zones map[string][]byte
}

var zoneName = regexp.MustCompile(`^[A-Z][A-Za-z0-9+_-]+(/[A-Z0-9a-z+_-]+)+$|^(UTC|GMT|UCT|Universal|Zulu|CET|EET|MET|WET|EST|MST|HST)$`)

var metadataFiles = map[string]bool{
	"zone.tab": true, "zone1970.tab": true, "iso3166.tab": true,
	"leapseconds": true, "leap-seconds.list": true, "tzdata.zi": true,
	"+VERSION": true, "posixrules": true, "posix/": true, "right/": true,
}

// EmbeddedRelease is the IANA release shipped by this repository's Go
// toolchain (go 1.25.0 embeds tzdata 2025b in time/tzdata). Go exposes no
// runtime API for it, so the constant is updated alongside the go directive
// in go.mod; TestEmbeddedReleaseMatchesToolchain pins the pair.
const EmbeddedRelease = "2025b"

var releaseID = regexp.MustCompile(`^(20\d{2}[a-z])$`)
var macTZPath = regexp.MustCompile(`(?:^|/)(20\d{2}[a-z](?:\.\d+)*)/[^/]+$`)

// releaseFromMarker reads an IANA release identifier from a "+VERSION" file
// or the "# version <release>" header of a "tzdata.zi" file, whichever the
// distribution ships.
func releaseFromMarker(version, zi []byte) string {
	if v := strings.TrimSpace(string(version)); releaseID.MatchString(v) {
		return v
	}
	for _, line := range strings.Split(string(zi), "\n") {
		if v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#")); strings.HasPrefix(v, "version ") {
			if id := strings.TrimSpace(strings.TrimPrefix(v, "version ")); releaseID.MatchString(id) {
				return id
			}
		}
	}
	return ""
}

func acceptZone(name string) bool {
	if metadataFiles[name] || metadataFiles[strings.SplitN(name, "/", 2)[0]+"/"] {
		return false
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return false
	}
	return zoneName.MatchString(name)
}

// Default locates a time-zone database in this order: $SOS_TZDATA (a
// zoneinfo.zip or a zoneinfo directory), the Go toolchain's zoneinfo.zip,
// /usr/share/zoneinfo, /usr/lib/timezone/zoneinfo, then /etc/zoneinfo. When
// none is present it falls back to the Go toolchain's embedded database,
// which can be described but not digested.
func Default() (*Bundle, error) {
	if p := os.Getenv("SOS_TZDATA"); p != "" {
		if b, err := FromPath(p); err == nil {
			return b, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	root := os.Getenv("GOROOT")
	if root == "" {
		root = runtime.GOROOT()
	}
	if root != "" {
		if b, err := FromPath(filepath.Join(root, "lib", "time", "zoneinfo.zip")); err == nil {
			// The toolchain's zoneinfo.zip carries no version marker but is
			// built from the same tzdata as the embedded database.
			if b.Release == "" {
				b.Release = EmbeddedRelease
			}
			return b, nil
		}
	}
	for _, dir := range []string{"/usr/share/zoneinfo", "/usr/lib/timezone/zoneinfo", "/etc/zoneinfo", "/usr/share/lib/zoneinfo"} {
		if b, err := FromPath(dir); err == nil {
			return b, nil
		}
	}
	return &Bundle{Source: "go:time/tzdata", SourceKind: "embedded", Release: EmbeddedRelease, Zones: map[string][]byte{}}, nil
}

// FromPath loads a zone database from a zip file or directory.
func FromPath(path string) (*Bundle, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return FromDir(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return FromZip(bytes.NewReader(data), int64(len(data)), path)
}

// FromZip loads a zone database from zoneinfo.zip layout bytes.
func FromZip(r io.ReaderAt, size int64, source string) (*Bundle, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	b := &Bundle{Source: source, SourceKind: "zip", Zones: map[string][]byte{}, Release: macTZPathRelease(source)}
	var versionFile, ziFile []byte
	for _, f := range z.File {
		if !acceptZone(f.Name) {
			if f.Name == "+VERSION" || f.Name == "tzdata.zi" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				data, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return nil, err
				}
				if f.Name == "+VERSION" {
					versionFile = data
				} else {
					ziFile = data
				}
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		if err := validate(f.Name, data); err != nil {
			return nil, err
		}
		b.Zones[f.Name] = data
	}
	if len(b.Zones) == 0 {
		return nil, fmt.Errorf("no time zones in %s", source)
	}
	if b.Release == "" {
		b.Release = releaseFromMarker(versionFile, ziFile)
	}
	return b, nil
}

// FromDir loads a zone database from an extracted zoneinfo directory.
func FromDir(dir string) (*Bundle, error) {
	b := &Bundle{Source: dir, SourceKind: "dir", Zones: map[string][]byte{}, Release: macTZPathRelease(dir)}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !acceptZone(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := validate(rel, data); err != nil {
			return err
		}
		b.Zones[rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(b.Zones) == 0 {
		return nil, fmt.Errorf("no time zones in %s", dir)
	}
	if b.Release == "" {
		b.Release = releaseFromMarker(readSmall(filepath.Join(dir, "+VERSION")), readSmall(filepath.Join(dir, "tzdata.zi")))
	}
	return b, nil
}

func macTZPathRelease(source string) string {
	if m := macTZPath.FindStringSubmatch(source); m != nil {
		if id := strings.SplitN(m[1], ".", 2)[0]; releaseID.MatchString(id) {
			return id
		}
	}
	return ""
}

func readSmall(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return nil
	}
	return data
}

func validate(name string, data []byte) error {
	if _, err := time.LoadLocationFromTZData(name, data); err != nil {
		return fmt.Errorf("zone %s: %w", name, err)
	}
	return nil
}

// Location resolves a zone from the bundled bytes, independent of host files.
func (b *Bundle) Location(name string) (*time.Location, error) {
	data, ok := b.Zones[name]
	if !ok {
		return nil, fmt.Errorf("unknown time zone %s", name)
	}
	return time.LoadLocationFromTZData(name, data)
}

// SHA256 digests the sorted zone bytes, so identical databases (regardless of
// file order or source kind) produce identical manifests.
func (b *Bundle) SHA256() string {
	names := make([]string, 0, len(b.Zones))
	for name := range b.Zones {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		data := b.Zones[name]
		var size [8]byte
		binaryPut(size[:], uint64(len(data)))
		h.Write(size[:])
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func binaryPut(buf []byte, v uint64) {
	for i := 7; i >= 0; i-- {
		buf[i] = byte(v)
		v >>= 8
	}
}

// capabilities returns the clock capability matrix for every supported target
// and host OS. Elapsed timers need only a monotonic clock, which every target
// host provides; calendar schedules need the bundled database, which every
// artifact embeds via time/tzdata.
func capabilities() []Capability {
	var caps []Capability
	for _, target := range []string{"native", "wasm-wasi", "wasm-browser"} {
		for _, osName := range []string{"linux", "darwin", "windows"} {
			cap := Capability{
				Target:            target,
				OS:                osName,
				MonotonicClock:    true,
				ElapsedTimers:     true,
				CalendarSchedules: true,
			}
			switch target {
			case "wasm-browser":
				cap.Notes = "no filesystem or host zone files; calendar schedules rely entirely on the embedded database"
			case "wasm-wasi":
				cap.Notes = "host zone files are not preopened; calendar schedules rely on the embedded database"
			}
			caps = append(caps, cap)
		}
	}
	return caps
}

// Manifest produces deterministic provenance and capability metadata.
func (b *Bundle) Manifest() Manifest {
	m := Manifest{
		Schema:         Schema,
		Source:         b.Source,
		SourceKind:     b.SourceKind,
		Release:        b.Release,
		ZoneCount:      len(b.Zones),
		Complete:       len(b.Zones) > 0,
		Representative: append([]string(nil), representativeZones...),
		Capabilities:   capabilities(),
	}
	if m.Complete {
		m.SHA256 = b.SHA256()
	} else {
		m.Source = "go:time/tzdata"
		m.SourceKind = "embedded"
		m.Release = EmbeddedRelease
	}
	return m
}

// Write stores the manifest as timezone.json inside dir.
func (b *Bundle) Write(dir string) error {
	data, err := json.MarshalIndent(b.Manifest(), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, "timezone.json"), data, 0o644)
}

// Check verifies that a previously recorded manifest still matches the
// build-time database. It rejects digest, release, source, or zone-count drift.
func Check(m Manifest) error {
	if m.Schema != Schema {
		return fmt.Errorf("timezone manifest schema %d != %d", m.Schema, Schema)
	}
	b, err := Default()
	if err != nil {
		return err
	}
	current := b.Manifest()
	if !current.Complete {
		return errors.New("no host time-zone database available to verify against")
	}
	if !m.Complete {
		return errors.New("recorded manifest has no database digest")
	}
	if m.SHA256 != current.SHA256 {
		return fmt.Errorf("time-zone database changed: recorded %s (release %q, zoneCount %d), current %s (release %q, zoneCount %d)", shortDigest(m.SHA256), m.Release, m.ZoneCount, shortDigest(current.SHA256), current.Release, current.ZoneCount)
	}
	if m.ZoneCount != current.ZoneCount {
		return fmt.Errorf("time-zone zone count changed: recorded %d, current %d", m.ZoneCount, current.ZoneCount)
	}
	if m.Release != "" && current.Release != "" && m.Release != current.Release {
		return fmt.Errorf("time-zone database release changed: recorded %q, current %q", m.Release, current.Release)
	}
	return nil
}

func shortDigest(d string) string {
	if len(d) < 12 {
		return d
	}
	return d[:12]
}

// VerifyResolution exercises representative zones from the bundled bytes,
// including both DST directions, a half-hour DST shift, and a whole-offset
// change, proving resolution does not depend on host zone files.
func (b *Bundle) VerifyResolution() error {
	type expectation struct {
		zone     string
		at       string // RFC3339 instant
		wantName string
		wantOff  int // seconds east of UTC
	}
	checks := []expectation{
		{"UTC", "2026-06-15T12:00:00Z", "UTC", 0},
		{"America/New_York", "2026-01-15T12:00:00Z", "EST", -5 * 3600},
		{"America/New_York", "2026-07-15T12:00:00Z", "EDT", -4 * 3600},
		{"Europe/London", "2026-01-15T12:00:00Z", "GMT", 0},
		{"Europe/London", "2026-07-15T12:00:00Z", "BST", 3600},
		{"Europe/Berlin", "2026-07-15T12:00:00Z", "CEST", 2 * 3600},
		{"Australia/Lord_Howe", "2026-01-15T12:00:00Z", "+11", 11 * 3600},
		{"Australia/Lord_Howe", "2026-06-15T12:00:00Z", "+1030", 10*3600 + 1800},
		{"Pacific/Kiritimati", "1994-01-01T00:00:00Z", "-10", -10 * 3600},
		{"Pacific/Kiritimati", "2026-06-15T12:00:00Z", "+14", 14 * 3600},
		{"Asia/Kolkata", "2026-06-15T12:00:00Z", "IST", 5*3600 + 1800},
		{"Asia/Tokyo", "2026-06-15T12:00:00Z", "JST", 9 * 3600},
	}
	for _, c := range checks {
		if _, err := b.Location(c.zone); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339, c.at)
		if err != nil {
			return err
		}
		loc, err := b.Location(c.zone)
		if err != nil {
			return err
		}
		name, off := at.In(loc).Zone()
		if name != c.wantName || off != c.wantOff {
			return fmt.Errorf("zone %s at %s: got %s %d, want %s %d", c.zone, c.at, name, off, c.wantName, c.wantOff)
		}
	}
	return nil
}
