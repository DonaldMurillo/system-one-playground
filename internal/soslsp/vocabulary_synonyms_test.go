package soslsp

import "testing"

func TestSentenceSynonymsRespectParentRequirement(t *testing.T) {
	for _, bare := range []bool{true, false} {
		targets := []wordTarget{{Name: "upper", Synonyms: []string{"uppercase"}, Qualifier: "words", Bare: bare, Enabled: true, Result: "text"}}
		for _, name := range []string{"words.upper", "words.uppercase"} {
			if got := resolveSent(name, targets); got == nil || got.Result != "text" {
				t.Fatalf("qualified synonym %s did not resolve", name)
			}
		}
		for _, name := range []string{"upper", "uppercase"} {
			if got := resolveSent(name, targets); (got != nil) != bare {
				t.Fatalf("%s resolution ignored parent requirement (bare=%v)", name, bare)
			}
		}
	}
}
