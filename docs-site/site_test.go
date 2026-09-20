package main

import (
	docsite "sysonescript.local/docs-site/docs"
	"testing"
)

func TestPublicDocumentationRoutes(t *testing.T) {
	if err := docsite.NewRouter().Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := buildSite(); err != nil {
		t.Fatal(err)
	}
}
