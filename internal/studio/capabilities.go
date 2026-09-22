package studio

import (
	"net/http"
	"runtime"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

// handleCapabilities reports the host's timing capabilities so the editor can
// surface clock, timer, and time-zone prerequisites before a run misbehaves.
// The facts mirror `sos capabilities` on the CLI and the debug adapter's
// sos/capabilities request.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method", "GET required")
		return
	}
	_, zoneErr := time.LoadLocation("America/New_York")
	zones := map[string]any{"available": zoneErr == nil, "sampleZone": "America/New_York"}
	if zoneErr != nil {
		zones["error"] = zoneErr.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"os":                runtime.GOOS,
		"arch":              runtime.GOARCH,
		"clocks":            []map[string]any{{"kind": "host", "available": true, "monotonic": true}, {"kind": "virtual", "available": true, "testHarnessOnly": true}},
		"timers":            map[string]any{"oneShot": true, "anchoredRepeating": true, "cancelableWaiting": true},
		"timeZoneDatabase":  zones,
		"calendarSchedules": zoneErr == nil,
		"streamControl":     true,
		"runtimeVersion":    sos.Version,
	})
}
