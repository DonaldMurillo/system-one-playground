package sos

import (
	"context"
	"testing"
)

func TestCanonicalizeUsesValidatedAnalysisAndRejectsStaleSource(t *testing.T) {
	source := "make age 21\nif age bigger 18 show \"adult\"\n"
	a, err := Analyze(context.Background(), source, AnalyzeOptions{Config: semanticConfig("semantic", "semantic")})
	requireSuccess(t, a, err)
	got, err := Canonicalize(source, a)
	if err != nil || got != "make age 21\nwhen age > 18:\n  show \"adult\"\n" {
		t.Fatalf("canonicalize = %q, %v", got, err)
	}
	if _, err := Canonicalize(source+"# changed\n", a); err == nil {
		t.Fatal("stale analysis was accepted")
	}
	a.Canonical = "definitely not canonical\n"
	if _, err := Canonicalize(source, a); err == nil {
		t.Fatal("mutated invalid canonical source was accepted")
	}
}
