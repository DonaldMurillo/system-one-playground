package main

import (
	"strings"
	"testing"
)

func TestDebuggerRedactsSecretsAcrossEvaluateLogpointsAndContainers(t *testing.T) {
	if got := debugValue("api_key", "top-secret")["value"]; got != "<redacted>" {
		t.Fatalf("evaluate exposed secret: %v", got)
	}
	variables := map[string]any{"token": "top-secret"}
	if got := interpolateLogpoint("token={token}", variables); strings.Contains(got, "top-secret") {
		t.Fatalf("logpoint exposed secret: %q", got)
	}
	redacted := redactDebugSecrets(map[string]any{"nested": map[string]any{"password": "top-secret"}})
	if got := debugDisplay("trace", redacted); strings.Contains(got, "top-secret") {
		t.Fatalf("container exposed secret: %q", got)
	}
	if debugCanExpand("api_token", map[string]any{"value": "top-secret"}) {
		t.Fatal("secret container could be expanded through a variables reference")
	}
	if !debugCanExpand("response", map[string]any{"status": "ok"}) {
		t.Fatal("ordinary container should remain expandable")
	}
}

type debugStreamFixture struct{ snapshots *int }

func (s debugStreamFixture) DebugStreamState() map[string]any {
	*s.snapshots++
	return map[string]any{"state": "active", "item type": "Event", "items received": 3, "items buffered": 1, "credit available": 15, "producer": "events.finite"}
}

func TestDebugStreamInspectionUsesSnapshotWithoutReadingItems(t *testing.T) {
	snapshots := 0
	stream := debugStreamFixture{snapshots: &snapshots}
	if !debugCanExpand("events", stream) {
		t.Fatal("stream state must be expandable")
	}
	refs := map[int]any{}
	next := 100
	variables := debugVariables(stream, &refs, &next, nil)
	if snapshots != 1 {
		t.Fatalf("snapshot calls = %d, want 1", snapshots)
	}
	if len(variables) != 6 {
		t.Fatalf("stream variables = %#v", variables)
	}
	found := false
	for _, raw := range variables {
		variable := raw.(map[string]any)
		if variable["name"] == "state" && variable["value"] == "active" {
			found = true
		}
	}
	if !found {
		t.Fatalf("active state missing: %#v", variables)
	}
}
