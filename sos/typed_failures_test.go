package sos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypedFailureDefinitionAndFinish(t *testing.T) {
	source := `define failure InvalidCity:
  city as text
to fetch with city as text returning text may fail with InvalidCity:
  when city is "":
    fail InvalidCity with "A city is required":
      city from city
  finish with city
call fetch with "Paris" called city
`
	p, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics = Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{})
	if err != nil || result.Variables["city"] != "Paris" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestTypedActionHeaderMayWrapBeforeFailureContract(t *testing.T) {
	source := `define failure Broken:
to fetch returning text
may fail with Broken:
  finish with "ok"
`
	p, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	declaration, err := parseActionDecl(p.Statements[1].Text)
	if err != nil || !declaration.HasResult || len(declaration.Failures) != 1 || declaration.Failures[0] != "Broken" {
		t.Fatalf("declaration = %#v, error = %v", declaration, err)
	}
}

func TestTypedFailureHandlerRecoveryAndPass(t *testing.T) {
	source := `define failure InvalidCity:
  city as text
to fetch with city as text returning text may fail with InvalidCity:
  fail InvalidCity with "A city is required":
    city from city
to recovered returning text:
  call fetch with "" called report
    on failure InvalidCity called problem:
      recover with "cached"
  finish with report
to passed returning text may fail with InvalidCity:
  call fetch with "" called report
    on failure InvalidCity:
      pass failure on
  finish with report
call recovered called cached
`
	if diagnostics := Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	p, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{})
	if err != nil || result.Variables["cached"] != "cached" {
		t.Fatalf("recovery result = %#v, error = %v", result, err)
	}

	passSource := strings.TrimSuffix(source, "call recovered called cached\n") + "call passed called result\n"
	if diagnostics := Check(passSource); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	passed, err := Run(context.Background(), mustParse(t, passSource), Options{})
	if err == nil || passed.Failure["kind"] != "InvalidCity" {
		t.Fatalf("pass result = %#v, error = %v", passed, err)
	}
	frames, ok := passed.Failure["frames"].([]map[string]any)
	if !ok || len(frames) < 2 {
		t.Fatalf("pass frames = %#v, want original and propagation frames", passed.Failure["frames"])
	}
}

func TestTypedFailureCapture(t *testing.T) {
	source := `define failure InvalidCity:
  city as text
to fetch with city as text returning text may fail with InvalidCity:
  fail InvalidCity with "A city is required":
    city from city
capture fetch with "" called outcome
`
	if diagnostics := Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), mustParse(t, source), Options{})
	if err != nil {
		t.Fatal(err)
	}
	outcome, ok := result.Variables["outcome"].(map[string]any)
	if !ok || outcome["succeeded"] != false {
		t.Fatalf("outcome = %#v", result.Variables["outcome"])
	}
	failure, ok := outcome["failure"].(map[string]any)
	if !ok || failure["kind"] != "InvalidCity" || failure["city"] != "" {
		t.Fatalf("failure = %#v", outcome["failure"])
	}
}

func TestTypedFailureCapturePreservesHiddenNameBinding(t *testing.T) {
	source := `define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
make __captured_value "sentinel"
capture fetch called outcome
show __captured_value
`
	result, err := Run(context.Background(), mustParse(t, source), Options{Stdout: &strings.Builder{}})
	if err != nil || result.Variables["__captured_value"] != "sentinel" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestTypedFailureCaptureNarrowsResultBranches(t *testing.T) {
	badSuccess := `define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
capture fetch called outcome
when succeeded of outcome:
  show failure of outcome
otherwise:
  show failure of outcome
`
	diagnostics := Check(badSuccess)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "unavailable in a successful branch") {
		t.Fatalf("success diagnostics = %+v", diagnostics)
	}
	badFailure := `define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
capture fetch called outcome
when succeeded of outcome:
  show value of outcome
otherwise:
  show value of outcome
`
	diagnostics = Check(badFailure)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "unavailable in a failed branch") {
		t.Fatalf("failure diagnostics = %+v", diagnostics)
	}
}

func TestTypedFailureContractsAreClosed(t *testing.T) {
	source := `define failure InvalidCity:
  city as text
define failure Other:
to fetch returning text may fail with InvalidCity:
  fail Other with "other"
`
	diagnostics := Check(source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "may pass Other") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestTypedFailureContractsRequireEverySuccessfulPathToFinish(t *testing.T) {
	source := `to maybe with flag as boolean returning text:
  when flag:
    finish with "ok"
`
	diagnostics := Check(source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "successful path") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestTypedFailureHandlersCannotFallThroughOrBeEmpty(t *testing.T) {
	fallthroughSource := `define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
to caller returning text:
  call fetch called value
    on failure Missing:
      when false:
        recover with "cached"
  finish with value
`
	diagnostics := Check(fallthroughSource)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "failure handler") {
		t.Fatalf("fallthrough diagnostics = %+v", diagnostics)
	}
	emptySource := `define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
to caller returning text:
  call fetch called value
    on failure Missing called problem
  finish with value
`
	diagnostics = Check(emptySource)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "failure handler") {
		t.Fatalf("empty diagnostics = %+v", diagnostics)
	}
}

func TestTypedFailurePassThroughRequiresCallerContract(t *testing.T) {
	source := `define failure Missing:
to leaf returning text may fail with Missing:
  fail Missing with "missing"
to caller returning text:
  call leaf called value
    on failure Missing:
      pass failure on
  finish with value
`
	diagnostics := Check(source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "may pass Missing") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestTypedFailureStopIsNotCatchable(t *testing.T) {
	source := `define failure Missing:
to fetch may fail with Missing:
  fail Missing with "missing"
call fetch
  on failure Missing:
    stop with "halt"
`
	var stop *StopError
	_, err := Run(context.Background(), mustParse(t, source), Options{})
	if !errors.As(err, &stop) || stop.Message != "halt" {
		t.Fatalf("error = %v, want StopError", err)
	}
}

func TestTypedFailureRecoveryRequiresResultShape(t *testing.T) {
	source := `define failure Missing:
to fetch returning integer may fail with Missing:
  fail Missing with "missing"
to caller returning integer:
  call fetch called value
    on failure Missing:
      recover with "not an integer"
  finish with value
call caller called value
`
	if diagnostics := Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	_, err := Run(context.Background(), mustParse(t, source), Options{})
	if err == nil || !strings.Contains(err.Error(), "recover with requires integer") {
		t.Fatalf("error = %v", err)
	}
}

func TestTypedFailureMetadataIncludesReachableFailures(t *testing.T) {
	source := `define failure InvalidCity:
  city as text
to leaf returning text may fail with InvalidCity:
  fail InvalidCity with "bad":
    city from ""
to root returning text may fail with InvalidCity:
  call leaf called value
  finish with value
`
	p := mustParse(t, source)
	if diagnostics := Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	metadata := p.ActionMetadata()
	if len(metadata) != 2 || metadata[1].Name != "root" || len(metadata[1].PossibleFailures) != 1 || metadata[1].PossibleFailures[0] != "InvalidCity" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(p.Failures) != 1 || p.Failures["InvalidCity"].Fields[0].Name != "city" {
		t.Fatalf("failures = %#v", p.Failures)
	}
}

func TestTypedFailurePackageExportAndRuntimeIdentity(t *testing.T) {
	dir := t.TempDir()
	packageDir := filepath.Join(dir, "weather")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(packageDir, "weather.sos"), `package weather
export InvalidCity
export fetch
define failure InvalidCity:
  city as text
to fetch with city as text returning text may fail with InvalidCity:
  fail InvalidCity with "bad city":
    city from city
`); err != nil {
		t.Fatal(err)
	}
	source := `import "./weather" as weather
capture weather.fetch with "" called outcome
`
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	outcome := result.Variables["outcome"].(map[string]any)
	failure := outcome["failure"].(map[string]any)
	if failure["kind"] != "InvalidCity" || failure["city"] != "" {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestTypedFailureNestedPackageScopeAndSentenceRecovery(t *testing.T) {
	dir := t.TempDir()
	packageDir := filepath.Join(dir, "weather")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(packageDir, "weather.sos"), `package weather
export fetch
export Missing
word retrieve of fetch
define failure Missing:
to fetch returning integer may fail with Missing:
  fail Missing with "missing"
to retrieve returning integer may fail with Missing:
  call fetch called value
  finish with value
`); err != nil {
		t.Fatal(err)
	}
	source := `import "./weather" as weather
weather.retrieve called value
  on failure Missing:
    recover with 7
show value
`
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir})
	if err != nil || fmt.Sprint(result.Variables["value"]) != "7" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestTypedFailureImportedContractsAreClosed(t *testing.T) {
	dir := t.TempDir()
	packageDir := filepath.Join(dir, "weather")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(packageDir, "weather.sos"), `package weather
export fetch
export Missing
define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
`); err != nil {
		t.Fatal(err)
	}
	source := `import "./weather" as weather
to caller returning text:
  call weather.fetch called value
  finish with value
`
	_, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "Missing") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func mustParse(t *testing.T, source string) *Program {
	t.Helper()
	p, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	return p
}
