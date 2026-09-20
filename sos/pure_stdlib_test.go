package sos

import (
	"reflect"
	"testing"
)

func TestPureCollectionTransforms(t *testing.T) {
	percentile := stdRegistry["std/list"]["percentile"].Fn
	sample := []any{9.0, 1.0, 5.0, 3.0}
	got, err := percentile([]any{sample, 50.0})
	if err != nil || got != 3.0 {
		t.Fatalf("percentile: %v %v", got, err)
	}
	if !reflect.DeepEqual(sample, []any{9.0, 1.0, 5.0, 3.0}) {
		t.Fatal("mutated sample")
	}
	if _, err := percentile([]any{[]any{}, 50.0}); err == nil {
		t.Fatal("empty accepted")
	}
	slice := stdRegistry["std/text"]["slice"].Fn
	got, err = slice([]any{"aé日z", 1, 3})
	if err != nil || got != "é日" {
		t.Fatalf("slice: %v %v", got, err)
	}
	if _, err := slice([]any{"text", 3.0, 2.0}); err == nil {
		t.Fatal("reversed bounds accepted")
	}
	original := map[string]any{"a": 1.0}
	got, err = stdRegistry["std/record"]["set"].Fn([]any{original, "a", 2.0})
	if err != nil || original["a"] != 1.0 || got.(map[string]any)["a"] != 2.0 {
		t.Fatalf("record set: %v %v", got, err)
	}
}
