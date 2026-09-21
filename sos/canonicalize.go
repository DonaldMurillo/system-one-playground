package sos

import "fmt"

// Canonicalize returns the fully canonical source produced by a successful,
// current analysis. The canonical program is checked again here because an
// Analysis is a public transport value and may have been mutated by a client.
func Canonicalize(source string, analysis *Analysis, moduleTables ...*ModuleTable) (string, error) {
	if analysis == nil {
		return "", fmt.Errorf("canonicalization requires an analysis")
	}
	if analysis.SourceHash != semanticSourceHash(source) {
		return "", fmt.Errorf("analysis is stale for the current source")
	}
	if analysis.Canonical == "" || len(analysis.Diagnostics) != 0 {
		return "", fmt.Errorf("analysis did not produce a valid canonical program")
	}
	var modules *ModuleTable
	if len(moduleTables) != 0 {
		modules = moduleTables[0]
	}
	if diagnostics := checkSource(analysis.Canonical, modules); len(diagnostics) != 0 {
		return "", fmt.Errorf("canonical program is invalid at %d:%d: %s", diagnostics[0].Line, diagnostics[0].Column, diagnostics[0].Message)
	}
	return analysis.Canonical, nil
}
