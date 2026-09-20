package sosconfig

import (
	"strings"
	"testing"
)

func parseT(t *testing.T, doc string) Config {
	t.Helper()
	cfg, err := Parse([]byte(doc), false)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func TestLibrariesParse(t *testing.T) {
	cfg := parseT(t, `
version = 1

[[language.libraries]]
path = "std/text"

[[language.libraries]]
path = "example.com/acme/media"
as = "media"
`)
	if cfg.Language.Libraries == nil || len(*cfg.Language.Libraries) != 2 {
		t.Fatalf("expected 2 libraries, got %+v", cfg.Language.Libraries)
	}
	got := *cfg.Language.Libraries
	if got[0].Path != "std/text" || got[0].As != "" {
		t.Errorf("first library = %+v", got[0])
	}
	if got[1].Path != "example.com/acme/media" || got[1].As != "media" {
		t.Errorf("second library = %+v", got[1])
	}
}

func TestLibrariesRejectInvalidEntries(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{"version = 1\n[[language.libraries]]\npath = \"\"\n", "path must not be empty"},
		{"version = 1\n[[language.libraries]]\npath = \"../escape\"\n", "must be a logical module identity"},
		{"version = 1\n[[language.libraries]]\npath = \"std/text\"\nas = \"1st\"\n", "as must be an identifier"},
		{"version = 1\n[[language.libraries]]\npath = \"std/text\"\nas = \"with space\"\n", "as must be an identifier"},
	} {
		_, err := Parse([]byte(tc.doc), false)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) = %v, want %q", tc.doc, err, tc.want)
		}
	}
}

func TestLibrariesExplicitEmptyDisablesInheritance(t *testing.T) {
	global := parseT(t, "version = 1\n[[language.libraries]]\npath = \"std/text\"\n")
	project := Config{Version: 1}
	empty := []Library{}
	project.Language.Libraries = &empty
	e, err := Resolve(Layer{"global", global}, Layer{"project", project})
	if err != nil {
		t.Fatal(err)
	}
	if e.Libraries != nil && len(e.Libraries) != 0 {
		t.Errorf("explicit empty list must disable inherited libraries, got %+v", e.Libraries)
	}
	if e.Origins["libraries"] != "project" {
		t.Errorf("origin = %q, want project", e.Origins["libraries"])
	}
}

func TestLibrariesProjectReplacesGlobal(t *testing.T) {
	global := parseT(t, "version = 1\n[[language.libraries]]\npath = \"std/text\"\n[[language.libraries]]\npath = \"std/json\"\n")
	project := parseT(t, "version = 1\n[[language.libraries]]\npath = \"std/json\"\n")
	e, err := Resolve(Layer{"global", global}, Layer{"project", project})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Libraries) != 1 || e.Libraries[0].Path != "std/json" {
		t.Errorf("project list must replace the global list, got %+v", e.Libraries)
	}
}

func TestLibrariesAbsentProjectInheritsGlobal(t *testing.T) {
	global := parseT(t, "version = 1\n[[language.libraries]]\npath = \"std/text\"\n")
	e, err := Resolve(Layer{"global", global}, Layer{"project", Config{Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Libraries) != 1 || e.Libraries[0].Path != "std/text" {
		t.Errorf("absent project language must inherit the global list, got %+v", e.Libraries)
	}
	if e.Origins["libraries"] != "global" {
		t.Errorf("origin = %q, want global", e.Origins["libraries"])
	}
}

func TestLanguageRejectedInFrontmatter(t *testing.T) {
	_, _, err := Extract("+++\nversion = 1\n[language]\nlibraries = []\n+++\nbody\n")
	if err == nil || !strings.Contains(err.Error(), "language settings are not allowed in frontmatter") {
		t.Fatalf("frontmatter language must be clearly rejected, got %v", err)
	}
	if !strings.Contains(err.Error(), "put [language] in the project sos.toml") {
		t.Errorf("error must direct to the project sos.toml: %v", err)
	}
}

func TestLibrariesEmptyTableInherits(t *testing.T) {
	// An empty [language] table has no libraries key: nil means inherit.
	global := parseT(t, "version = 1\n[[language.libraries]]\npath = \"std/text\"\n")
	empty := parseT(t, "version = 1\n[language]\n")
	e, err := Resolve(Layer{"global", global}, Layer{"project", empty})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Libraries) != 1 {
		t.Errorf("empty [language] table must inherit, got %+v", e.Libraries)
	}
}
