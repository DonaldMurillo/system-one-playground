package main

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

const capabilitiesUsage = "usage: sos capabilities [--json]\n"

// timeCapabilities reports the platform timing capabilities the runtime and
// its tooling rely on: clock kinds, timer support, and time-zone database
// availability. Embedders (Studio, the debug adapter) surface the same facts
// so a missing platform prerequisite is visible before a run misbehaves.
func timeCapabilities() map[string]any {
	_, zoneErr := time.LoadLocation("America/New_York")
	timeZones := map[string]any{"available": zoneErr == nil, "sampleZone": "America/New_York"}
	if zoneErr != nil {
		timeZones["error"] = zoneErr.Error()
	}
	return map[string]any{
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
		"clocks": []map[string]any{
			{"kind": "host", "available": true, "monotonic": true},
			{"kind": "virtual", "available": true, "testHarnessOnly": true},
		},
		"timers": map[string]any{
			"oneShot":           true,
			"anchoredRepeating": true,
			"cancelableWaiting": true,
		},
		"timeZoneDatabase":      timeZones,
		"calendarSchedules":     zoneErr == nil,
		"streamControl":         true,
		"debuggerObservability": true,
		"runtimeVersion":        sos.Version,
	}
}

func cmdCapabilities(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		default:
			fmt.Fprint(stderr, capabilitiesUsage)
			return 2
		}
	}
	caps := timeCapabilities()
	if asJSON {
		data, err := json.MarshalIndent(caps, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "sos: capabilities: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	zones := caps["timeZoneDatabase"].(map[string]any)
	fmt.Fprintf(stdout, "platform: %s/%s\n", caps["os"], caps["arch"])
	fmt.Fprintf(stdout, "clocks: host (monotonic), virtual (test harnesses only)\n")
	fmt.Fprintf(stdout, "timers: one-shot, anchored repeating, cancelable waiting\n")
	if zones["available"] == true {
		fmt.Fprintf(stdout, "time-zone database: available (%s resolves)\n", zones["sampleZone"])
		fmt.Fprintf(stdout, "calendar schedules: supported\n")
	} else {
		fmt.Fprintf(stdout, "time-zone database: unavailable (%v)\n", zones["error"])
		fmt.Fprintf(stdout, "calendar schedules: unsupported without a time-zone database\n")
	}
	fmt.Fprintf(stdout, "stream control: supported\n")
	return 0
}
