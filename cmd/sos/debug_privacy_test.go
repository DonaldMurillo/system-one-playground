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
}
