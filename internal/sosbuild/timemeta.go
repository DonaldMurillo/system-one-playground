package sosbuild

import (
	"fmt"

	"github.com/DonaldMurillo/system-one-playground/internal/timebundle"
)

// TimeZoneMetadata returns the time-zone database provenance and per-target
// clock capability manifest recorded beside standalone artifacts. It answers
// native, WASI, browser, and per-OS diagnostics from one deterministic record.
func TimeZoneMetadata() (timebundle.Manifest, error) {
	b, err := timebundle.Default()
	if err != nil {
		return timebundle.Manifest{}, fmt.Errorf("time-zone database: %w", err)
	}
	return b.Manifest(), nil
}

// writeTimeZoneMetadata writes timezone.json into the staged build directory
// so published artifacts carry the provenance of the database they bundle.
func writeTimeZoneMetadata(dir string) (string, error) {
	b, err := timebundle.Default()
	if err != nil {
		return "", fmt.Errorf("time-zone database: %w", err)
	}
	if err := b.Write(dir); err != nil {
		return "", err
	}
	return dir + "/timezone.json", nil
}
