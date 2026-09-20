package sosconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractReportsUnknownKeyPosition(t *testing.T) {
	_, _, err := Extract("+++\nversion=1\n[budget.run]\nrequsets=0\n+++\nshow 5\n")
	var positioned *Error
	if !errors.As(err, &positioned) || positioned.Line != 4 || !strings.Contains(positioned.Message, "budget.run.requsets") {
		t.Fatalf("positioned error: %v", err)
	}
}

func TestSemanticConfigErrorsHavePositions(t *testing.T) {
	for _, tc := range []struct {
		source string
		line   int
	}{
		{"version=1\n[runtime]\njudgment='yes'\n", 3},
		{"version=1\n[budget.run]\nrequests=-1\n", 3},
		{"version=1\n[budget.run]\ntimeout=''\n", 3},
		{"# comment\nversion=2\n", 2},
		{"version=1\nruntime = { judgment = 'yes' }\n", 2},
		{"version=1\n'runtime'.judgment = 'yes'\n", 2},
	} {
		_, _, err := Extract("+++\n" + tc.source + "+++\nshow 5\n")
		var positioned *Error
		if !errors.As(err, &positioned) || positioned.Line != tc.line+1 {
			t.Fatalf("%q: %v", tc.source, err)
		}
	}
}

func mustParse(t *testing.T, src string) Config {
	t.Helper()
	c, err := Parse([]byte(src), false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseRejectsInvalidConfiguration(t *testing.T) {
	for _, src := range []string{
		"", "version=2", "version=1\nversion=1", "version=1\nunknown=true",
		"version=1\n[editor]\nassistance='sometimes'", "version=1\n[editor]\nassistance=''",
		"version=1\n[editor]\nassistance='off,on-demand'",
		"version=1\n[interpretation]\nmode='magic'", "version=1\n[runtime]\njudgment='yes'",
		"version=1\n[budget.run]\nrequests=-1", "version=1\n[budget.run]\nrequests=1.5",
		"version=1\n[budget.run]\nrequests='5'", "version=1\n[budget.run]\ntimeout='0s'",
		"version=1\n[budget.run]\ntimeout=''", "version=1\n[budget.run]\ntimeout='-1s'",
		"version=1\n[budget.project]\nrequests=10", "version=1\n[budget.run]\nusd='1.00'",
		"version=1\n[budget.run]\ntimeout='999999999999999999999999h'",
	} {
		t.Run(src, func(t *testing.T) {
			if _, err := Parse([]byte(src), false); err == nil {
				t.Fatalf("accepted %q", src)
			}
		})
	}
	if _, err := Parse([]byte("version=1\n#"+strings.Repeat("x", maxBytes)), false); err == nil {
		t.Fatal("oversized config accepted")
	}
}

func TestResolveCeilingsAndOrigins(t *testing.T) {
	global := mustParse(t, "version=1\n[runtime]\njudgment='deny'\n[budget.run]\nrequests=20\ntimeout='5s'")
	project := mustParse(t, "version=1\n[runtime]\njudgment='explicit'\n[budget.run]\nrequests=200\ntimeout='10s'")
	file := mustParse(t, "version=1\n[budget.run]\nrequests=0\ntimeout='2s'")
	got, err := Resolve(Layer{"global", global}, Layer{"project", project}, Layer{"file", file})
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 0 || got.Timeout != 2*time.Second || got.Runtime != "deny" {
		t.Fatalf("ceilings: %+v", got)
	}
	if got.Origins["runtime"] != "global" || got.Origins["requests"] != "file" {
		t.Fatalf("origins: %+v", got)
	}
	got, err = Resolve(Layer{"project", project})
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 200 {
		t.Fatalf("default incorrectly caps explicit allowance: %+v", got)
	}
	got, err = Resolve()
	if err != nil || got.Requests != 100 || got.Interpretation != "canonical" {
		t.Fatalf("defaults: %+v %v", got, err)
	}
}

func TestExtractPreservesBodyLines(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		src := "\ufeff" + strings.Join([]string{"+++", "version=1", "[budget.run]", "requests=0", "+++", "show 5", ""}, newline)
		body, c, err := Extract(src)
		if err != nil || c == nil || *c.Budget.Run.Requests != 0 {
			t.Fatalf("extract: %v %+v", err, c)
		}
		if strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")[5] != "show 5" {
			t.Fatalf("body moved: %q", body)
		}
	}
	for _, src := range []string{"+++\nversion=1", "+++\nversion=1\n[editor]\n+++", "+++\nversion=1\n[editor]\nassistance='off'\n+++"} {
		if _, _, err := Extract(src); err == nil {
			t.Fatalf("bad header accepted: %q", src)
		}
	}
	src := "show \"+++\"\n"
	body, c, err := Extract(src)
	if err != nil || c != nil || body != src {
		t.Fatal("ordinary source changed")
	}
}

func TestLoadNearestProjectAndGlobal(t *testing.T) {
	global, root := t.TempDir(), t.TempDir()
	t.Setenv("SOS_CONFIG_HOME", global)
	child := filepath.Join(root, "child")
	deep := filepath.Join(child, "deep")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	for path, src := range map[string]string{
		filepath.Join(global, "config.toml"): "version=1\n[budget.run]\nrequests=10",
		filepath.Join(root, "sos.toml"):      "version=1\n[budget.run]\nrequests=0",
		filepath.Join(child, "sos.toml"):     "version=1\n[budget.run]\nrequests=5",
	} {
		if err := os.WriteFile(path, []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	layers, err := Load(deep)
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 2 {
		t.Fatalf("layers: %+v", layers)
	}
	got, err := Resolve(layers...)
	if err != nil || got.Requests != 5 {
		t.Fatalf("nearest config: %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(child, "sos.toml"), []byte("version=1\ntypo=3"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(deep); err == nil {
		t.Fatal("invalid nearest config ignored")
	}
}

func FuzzExtract(f *testing.F) {
	f.Add("+++\nversion=1\n+++\nshow 5\n")
	f.Add("show \"hello\"\n")
	f.Fuzz(func(t *testing.T, src string) {
		body, c, err := Extract(src)
		if err == nil && c != nil && strings.Count(body, "\n") != strings.Count(src, "\n") {
			t.Fatal("changed line count")
		}
	})
}
