package sos

import "fmt"

// Canonicalize returns the fully canonical source produced by a successful,
// current analysis. The returned program has already passed the canonical
// checker during Analyze.
func Canonicalize(source string, analysis *Analysis) (string, error) {
	if analysis == nil {
		return "", fmt.Errorf("canonicalization requires an analysis")
	}
	if analysis.SourceHash != semanticSourceHash(source) {
		return "", fmt.Errorf("analysis is stale for the current source")
	}
	if analysis.Canonical == "" || len(analysis.Diagnostics) != 0 {
		return "", fmt.Errorf("analysis did not produce a valid canonical program")
	}
	return analysis.Canonical, nil
}
