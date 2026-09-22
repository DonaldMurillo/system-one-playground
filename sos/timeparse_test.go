package sos

import (
	"errors"
	"testing"
	"time"
)

func TestParseDurationValueAcceptsReadableAndCompactForms(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"500 milliseconds", 500 * time.Millisecond},
		{"30 seconds", 30 * time.Second},
		{"5 minutes", 5 * time.Minute},
		{"2 hours", 2 * time.Hour},
		{"7 days", 7 * 24 * time.Hour},
		{"500ms", 500 * time.Millisecond},
		{"30s", 30 * time.Second},
		{"2h", 2 * time.Hour},
		{"1h30m", 90 * time.Minute},
		{"1 hour 30 minutes", 90 * time.Minute},
		{"0 seconds", 0},
		{"0.5 seconds", 500 * time.Millisecond},
		{"1.5ms", 1500 * time.Microsecond},
	}
	for _, c := range cases {
		got, err := ParseDurationValue(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseDurationValue(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
}

func TestParseDurationValueRejectsInvalidInput(t *testing.T) {
	bad := []string{
		"", "  ", "-5 seconds", "5 seconds -3 seconds", "seconds",
		"5", "5 lightyears", "5 months", "2 years", "1.2.3 seconds",
		"999999999999999999999 hours", "5 seconds 4", "++3 seconds",
	}
	for _, in := range bad {
		_, err := ParseDurationValue(in)
		if err == nil {
			t.Errorf("ParseDurationValue(%q) accepted", in)
			continue
		}
		var typed *typedFailure
		if !errors.As(err, &typed) || typed.kind != "InvalidDuration" {
			t.Errorf("ParseDurationValue(%q) error = %#v; want InvalidDuration", in, err)
		}
	}
}

func TestParseDurationValueOverflowFails(t *testing.T) {
	_, err := ParseDurationValue("300000000h")
	if err == nil {
		t.Fatal("overflowing duration accepted")
	}
	var typed *typedFailure
	if !errors.As(err, &typed) || typed.kind != "InvalidDuration" {
		t.Fatalf("overflow error = %#v", err)
	}
}

func TestFormatDurationWords(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{500 * time.Millisecond, "500 milliseconds"},
		{time.Millisecond, "1 millisecond"},
		{30 * time.Second, "30 seconds"},
		{time.Second, "1 second"},
		{5 * time.Minute, "5 minutes"},
		{2 * time.Hour, "2 hours"},
		{7 * 24 * time.Hour, "7 days"},
	}
	for _, c := range cases {
		if got := FormatDurationWords(c.in); got != c.want {
			t.Errorf("FormatDurationWords(%v) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestLoadZoneStrict(t *testing.T) {
	for _, ok := range []string{"UTC", "America/New_York", "Europe/London", "Asia/Tokyo"} {
		if _, err := LoadZone(ok); err != nil {
			t.Errorf("LoadZone(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "+05:30", "-08:00", "Local", "../secret", "/etc/passwd", "Not/A..Zone", "Mars/Olympus"} {
		_, err := LoadZone(bad)
		if err == nil {
			t.Errorf("LoadZone(%q) accepted", bad)
			continue
		}
		var typed *typedFailure
		if !errors.As(err, &typed) || typed.kind != "InvalidTimeZone" {
			t.Errorf("LoadZone(%q) error = %#v; want InvalidTimeZone", bad, err)
		}
	}
}

func TestParseTimestampStrictFormats(t *testing.T) {
	nyc, err := LoadZone("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		text, format, zone string
		want               time.Time
	}{
		{"2026-09-21T12:00:00Z", FormatRFC3339, "", time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)},
		{"2026-09-21T12:00:00+05:30", FormatRFC3339, "", time.Date(2026, 9, 21, 6, 30, 0, 0, time.UTC)},
		{"2026-09-21T12:00:00.123456789Z", FormatRFC3339Nano, "", time.Date(2026, 9, 21, 12, 0, 0, 123456789, time.UTC)},
		{"2026-09-21", FormatISODate, "America/New_York", time.Date(2026, 9, 21, 0, 0, 0, 0, nyc)},
		{"2026-09-21T09:15:00", FormatISOLocal, "America/New_York", time.Date(2026, 9, 21, 9, 15, 0, 0, nyc)},
		{"Sun, 21 Sep 2026 12:00:00 GMT", FormatHTTPDate, "", time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := ParseTimestampValue(c.text, c.format, c.zone)
		if err != nil {
			t.Errorf("ParseTimestampValue(%q, %q) = %v", c.text, c.format, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("ParseTimestampValue(%q, %q) = %v; want %v", c.text, c.format, got, c.want)
		}
	}
}

func TestParseTimestampRejectsGuessing(t *testing.T) {
	bad := []struct {
		text, format, zone string
	}{
		// Fractional seconds are a separate named format.
		{"2026-09-21T12:00:00.5Z", FormatRFC3339, ""},
		// Missing zone never defaults to local time.
		{"2026-09-21T12:00:00", FormatRFC3339, ""},
		{"2026-09-21T12:00:00", FormatRFC3339Nano, ""},
		// ISO date and local datetime require an explicit zone.
		{"2026-09-21", FormatISODate, ""},
		{"2026-09-21T09:15:00", FormatISOLocal, ""},
		// Ambiguous date order without a named format fails.
		{"09/21/2026", FormatRFC3339, ""},
		{"21-09-2026", FormatISODate, "UTC"},
		// Two-digit years are never expanded by century.
		{"26-09-21", FormatISODate, "UTC"},
		// HTTP dates must be GMT.
		{"Sun, 21 Sep 2026 12:00:00 EST", FormatHTTPDate, ""},
		{"Sun, 21 Sep 2026 12:00:00 GMT extra", FormatHTTPDate, ""},
		// Invalid UTF-8 fails cleanly.
		{"2026-09-21\xff\xfeT12:00:00Z", FormatRFC3339, ""},
		// Surrounding whitespace is not trimmed silently.
		{" 2026-09-21T12:00:00Z", FormatRFC3339, ""},
		// ISO local datetime must not smuggle an offset.
		{"2026-09-21T09:15:00Z", FormatISOLocal, "UTC"},
	}
	for _, c := range bad {
		_, err := ParseTimestampValue(c.text, c.format, c.zone)
		if err == nil {
			t.Errorf("ParseTimestampValue(%q, %q, %q) accepted", c.text, c.format, c.zone)
		}
	}
}

func TestFormatTimestampNamedFormats(t *testing.T) {
	ts := time.Date(2026, 9, 21, 8, 30, 15, 500, time.UTC)
	cases := []struct {
		format, zone, want string
	}{
		{FormatRFC3339, "", "2026-09-21T08:30:15Z"},
		{FormatRFC3339Nano, "", "2026-09-21T08:30:15.0000005Z"},
		{FormatISODate, "America/New_York", "2026-09-21"},
		{FormatISOLocal, "America/New_York", "2026-09-21T04:30:15"},
		{FormatHTTPDate, "Asia/Tokyo", "Mon, 21 Sep 2026 08:30:15 GMT"},
	}
	for _, c := range cases {
		got, err := FormatTimestampValue(ts, c.format, c.zone)
		if err != nil {
			t.Errorf("FormatTimestampValue(%q) = %v", c.format, err)
			continue
		}
		if got != c.want {
			t.Errorf("FormatTimestampValue(%q, %q) = %q; want %q", c.format, c.zone, got, c.want)
		}
	}
}
