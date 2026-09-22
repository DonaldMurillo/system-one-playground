package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilitiesReportsPlatformTimingDiagnostics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"capabilities", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var caps map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &caps); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if caps["os"] == "" || caps["arch"] == "" {
		t.Fatalf("platform missing: %#v", caps)
	}
	clocks, _ := caps["clocks"].([]any)
	if len(clocks) < 2 {
		t.Fatalf("clock kinds missing: %#v", caps["clocks"])
	}
	kinds := map[string]bool{}
	for _, raw := range clocks {
		clock, _ := raw.(map[string]any)
		kind, _ := clock["kind"].(string)
		kinds[kind] = true
	}
	if !kinds["host"] || !kinds["virtual"] {
		t.Fatalf("host and virtual clock kinds required: %#v", kinds)
	}
	zones, _ := caps["timeZoneDatabase"].(map[string]any)
	if zones["available"] != true {
		t.Fatalf("test host must resolve a sample IANA zone: %#v", zones)
	}
	if caps["calendarSchedules"] != true {
		t.Fatalf("calendar schedules must be supported when zones resolve: %#v", caps)
	}
}

func TestCapabilitiesHumanOutputMentionsClocksAndZones(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"capabilities"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{"clocks:", "time-zone database:", "calendar schedules:", "stream control:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("human output missing %q:\n%s", want, text)
		}
	}
}

func TestCapabilitiesRejectsUnknownFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunCLI([]string{"capabilities", "--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d", code)
	}
}
