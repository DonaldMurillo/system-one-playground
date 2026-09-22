package sos

import (
	"net/http"
	"strings"
	"time"
	_ "time/tzdata"
)

// Named timestamp formats from the specification. Custom Go layouts remain
// available through the technical operations by passing a layout directly.
const (
	FormatRFC3339       = "RFC3339"
	FormatRFC3339Nano   = "RFC3339Nano"
	FormatISODate       = "ISO date"
	FormatISOLocal      = "ISO local date and time"
	FormatHTTPDate      = "HTTP date"
	isoDateLayout       = "2006-01-02"
	isoLocalLayout      = "2006-01-02T15:04:05"
	httpDateLayoutValue = http.TimeFormat
)

var readableDurationUnits = map[string]time.Duration{
	"millisecond": time.Millisecond, "milliseconds": time.Millisecond,
	"ms":     time.Millisecond,
	"second": time.Second, "seconds": time.Second,
	"s":      time.Second,
	"minute": time.Minute, "minutes": time.Minute,
	"m":    time.Minute,
	"hour": time.Hour, "hours": time.Hour,
	"h":   time.Hour,
	"day": 24 * time.Hour, "days": 24 * time.Hour,
	"d": 24 * time.Hour,
}

func isDurationUnit(value string) bool {
	_, ok := readableDurationUnits[strings.ToLower(value)]
	return ok
}

// ParseDurationValue parses one duration literal in readable ("500
// milliseconds", "1 hour 30 minutes") or compact ("500ms", "1h30m") form.
// Parsing is strict: negative values, unknown units, missing numbers,
// overflow, and trailing junk are typed InvalidDuration failures. Calendar
// months and years are not durations and are rejected.
func ParseDurationValue(s string) (time.Duration, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return 0, invalidDuration(s, "empty value")
	}
	if strings.HasPrefix(text, "-") {
		return 0, invalidDuration(s, "negative durations are not valid")
	}
	// Split into whitespace-and-sign free number/unit components; compact
	// forms like 1h30m need digit boundaries, readable forms allow spaces.
	total := int64(0)
	overflow := false
	i := 0
	components := 0
	for i < len(text) {
		start := i
		for i < len(text) && (text[i] >= '0' && text[i] <= '9' || text[i] == '.') {
			i++
		}
		if i == start {
			return 0, invalidDuration(s, "expected a number")
		}
		number := text[start:i]
		if strings.Count(number, ".") > 1 {
			return 0, invalidDuration(s, "invalid number "+number)
		}
		unitStart := i
		for i < len(text) && !(text[i] >= '0' && text[i] <= '9') {
			i++
		}
		unit := strings.TrimSpace(text[unitStart:i])
		mult, ok := readableDurationUnits[strings.ToLower(unit)]
		if !ok {
			if strings.EqualFold(unit, "month") || strings.EqualFold(unit, "months") ||
				strings.EqualFold(unit, "year") || strings.EqualFold(unit, "years") {
				return 0, invalidDuration(s, "calendar "+unit+" is not a duration")
			}
			return 0, invalidDuration(s, "unknown unit "+unit)
		}
		value, frac, bad := parseDurationNumber(number)
		if bad != "" {
			return 0, invalidDuration(s, bad)
		}
		whole := value*int64(mult) + frac*int64(mult)/1_000_000_000
		if value != 0 && whole/int64(mult) != value {
			overflow = true
		}
		added := total + whole
		if added < total {
			overflow = true
		}
		total = added
		components++
		if overflow || total < 0 {
			return 0, invalidDuration(s, "duration overflows")
		}
	}
	if components == 0 {
		return 0, invalidDuration(s, "no components")
	}
	return time.Duration(total), nil
}

// parseDurationNumber splits "12.5" into 12 and 500 milli-fraction scaled by
// 1e9. Only up to nine fractional digits are meaningful for nanoseconds.
func parseDurationNumber(number string) (whole int64, fracNanos int64, err string) {
	intPart, fracPart, hasDot := strings.Cut(number, ".")
	var w int64
	for _, c := range intPart {
		if c < '0' || c > '9' {
			return 0, 0, "invalid number " + number
		}
		next := w*10 + int64(c-'0')
		if next < w {
			return 0, 0, "duration overflows"
		}
		w = next
	}
	frac := int64(0)
	if hasDot {
		scale := int64(100_000_000)
		digits := 0
		for _, c := range fracPart {
			if c < '0' || c > '9' {
				return 0, 0, "invalid number " + number
			}
			if digits < 9 {
				frac += int64(c-'0') * scale
				scale /= 10
				digits++
			}
		}
		if fracPart == "" {
			return 0, 0, "invalid number " + number
		}
	}
	return w, frac, ""
}

// LoadZone resolves a canonical zone name: UTC or an IANA identifier such as
// America/New_York. Fixed offsets, "Local", relative paths, and unknown names
// are typed InvalidTimeZone failures.
func LoadZone(name string) (*time.Location, error) {
	if name == "UTC" {
		return time.UTC, nil
	}
	if name == "" || name == "Local" || !strings.Contains(name, "/") ||
		strings.Contains(name, "..") || strings.HasPrefix(name, "/") ||
		strings.HasPrefix(name, "+") || strings.HasPrefix(name, "-") {
		return nil, invalidTimeZone(name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalidTimeZone(name)
	}
	return loc, nil
}

// FormatDurationWords renders a duration in canonical readable words, e.g.
// "500 milliseconds", "90 seconds". Generated source never uses compact form.
func FormatDurationWords(d time.Duration) string {
	if d < 0 {
		return "-" + FormatDurationWords(-d)
	}
	switch {
	case d%time.Millisecond == 0 && d < time.Second && d > 0 ||
		(d > 0 && d < time.Second && d%time.Millisecond == 0):
		return itoaWords(int64(d/time.Millisecond), "millisecond")
	case d%time.Second == 0 && d < time.Minute:
		return itoaWords(int64(d/time.Second), "second")
	case d%time.Minute == 0 && d < time.Hour:
		return itoaWords(int64(d/time.Minute), "minute")
	case d%time.Hour == 0 && d < 24*time.Hour:
		return itoaWords(int64(d/time.Hour), "hour")
	case d%(24*time.Hour) == 0:
		return itoaWords(int64(d/(24*time.Hour)), "day")
	default:
		return d.String()
	}
}

func itoaWords(n int64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return itoa(n) + " " + unit + "s"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ParseTimestampValue parses text strictly in a named format (or a custom Go
// layout for technical operations). It never guesses locale, date order,
// missing zone, or two-digit-year century: ISO local formats require an
// explicit zone, RFC3339 requires an offset, HTTP dates must end in GMT.
func ParseTimestampValue(text, format, zone string) (time.Time, error) {
	loc := (*time.Location)(nil)
	if zone != "" {
		var err error
		loc, err = LoadZone(zone)
		if err != nil {
			return time.Time{}, err
		}
	}
	bad := func(reason string) error { return invalidTimestamp(text, reason) }
	formatErr := func() error { return invalidTimeFormat(text, format) }
	trimmed := strings.TrimSpace(text)
	if trimmed != text {
		return time.Time{}, formatErr()
	}
	switch format {
	case FormatRFC3339:
		if strings.ContainsAny(text, ".") {
			return time.Time{}, bad("fractional seconds require the RFC3339 nanoseconds format")
		}
		t, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return time.Time{}, formatErr()
		}
		return t, nil
	case FormatRFC3339Nano:
		t, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return time.Time{}, formatErr()
		}
		return t, nil
	case FormatISODate:
		if loc == nil {
			return time.Time{}, bad("ISO date requires an explicit time zone")
		}
		t, err := time.ParseInLocation(isoDateLayout, text, loc)
		if err != nil {
			return time.Time{}, formatErr()
		}
		return t, nil
	case FormatISOLocal:
		if loc == nil {
			return time.Time{}, bad("ISO local date and time requires an explicit time zone")
		}
		if strings.Contains(text, "Z") || strings.Contains(text, "+") {
			return time.Time{}, bad("ISO local date and time must not carry an offset; use the zone parameter")
		}
		t, err := time.ParseInLocation(isoLocalLayout, text, loc)
		if err != nil {
			return time.Time{}, formatErr()
		}
		return t, nil
	case FormatHTTPDate:
		if !strings.HasSuffix(text, "GMT") {
			return time.Time{}, bad("HTTP date must be expressed in GMT")
		}
		t, err := time.Parse(httpDateLayoutValue, text)
		if err != nil {
			return time.Time{}, formatErr()
		}
		return t, nil
	default:
		// Custom technical layout. Zone applies when the layout has no
		// offset element.
		if loc == nil {
			t, err := time.Parse(format, text)
			if err != nil {
				return time.Time{}, formatErr()
			}
			return t, nil
		}
		t, err := time.ParseInLocation(format, text, loc)
		if err != nil {
			return time.Time{}, formatErr()
		}
		return t, nil
	}
}

// FormatTimestampValue renders t in a named format (or custom layout) after
// converting to zone. An empty zone keeps t's own location; UTC applies for
// formats that mandate it.
func FormatTimestampValue(t time.Time, format, zone string) (string, error) {
	if zone != "" {
		loc, err := LoadZone(zone)
		if err != nil {
			return "", err
		}
		t = t.In(loc)
	}
	switch format {
	case FormatRFC3339:
		return t.Format(time.RFC3339), nil
	case FormatRFC3339Nano:
		return t.Format(time.RFC3339Nano), nil
	case FormatISODate:
		return t.Format(isoDateLayout), nil
	case FormatISOLocal:
		return t.Format(isoLocalLayout), nil
	case FormatHTTPDate:
		return t.UTC().Format(httpDateLayoutValue), nil
	default:
		return t.Format(format), nil
	}
}
