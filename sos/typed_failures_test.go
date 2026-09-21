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

func TestTypedFailureCaptureSuccessfulNoResultAction(t *testing.T) {
	source := `to ping:
  finish
capture ping called outcome
`
	result, err := Run(context.Background(), mustParse(t, source), Options{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := result.Variables["outcome"].(map[string]any)
	if outcome["succeeded"] != true || outcome["value"] != nil || outcome["failure"] != nil {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestTypedFailureCapturePreservesNativeResult(t *testing.T) {
	dir := t.TempDir()
	source := `import "std/text" as text
capture text.upper with "hello" called outcome
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
	if outcome["succeeded"] != true || outcome["value"] != "HELLO" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestExportedActionRequiresExportedFailure(t *testing.T) {
	dir := t.TempDir()
	mod := filepath.Join(dir, "service")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "service.sos"), []byte(`package service
export fetch
define failure Missing:
to fetch may fail with Missing:
  fail Missing with "missing"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), `import "./service" as service
show "loaded"
`)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "exposes failure Missing") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestModuleFailurePayloadCanUseImportedRecordAndReportsModuleFrame(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	service := filepath.Join(dir, "service")
	for _, path := range []string{models, service} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(models, "models.sos"), []byte(`package models
export Address
define Address:
  city as text
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service, "service.sos"), []byte(`package service
import "../models" as models
export Missing
export fetch
define failure Missing:
  address as Address
to fetch may fail with Missing:
  fail Missing with "missing":
    address from {city: "Paris"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), `import "./service" as service
call service.fetch
`)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir, SourcePath: filepath.Join(dir, "main.sos")})
	if err == nil || result.Failure["kind"] != "Missing" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	frames, ok := result.Failure["frames"].([]map[string]any)
	if !ok || len(frames) == 0 || !strings.Contains(frames[0]["path"].(string), "service") {
		t.Fatalf("frames = %#v", result.Failure["frames"])
	}
}

func TestModuleFailureTraversalUsesModuleAndActionIdentity(t *testing.T) {
	dir := t.TempDir()
	dep := filepath.Join(dir, "dep")
	service := filepath.Join(dir, "service")
	for _, path := range []string{dep, service} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dep, "dep.sos"), []byte(`package dep
export Missing
export fetch
define failure Missing:
to fetch may fail with Missing:
  fail Missing with "missing"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service, "service.sos"), []byte(`package service
import "../dep" as dep
export fetch
to fetch may fail with Missing:
  call dep.fetch
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), `import "./service" as service
show "loaded"
`)
	if len(diagnostics) == 0 || !strings.Contains(fmt.Sprint(diagnostics), "exposes failure Missing") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestModuleLocalCallValidatesResultInCalleeTypeScope(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	service := filepath.Join(dir, "service")
	for _, path := range []string{models, service} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(models, "models.sos"), []byte(`package models
export Address
define Address:
  city as text
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service, "producer.sos"), []byte(`package service
import "../models" as models
to producer returning Address:
  finish with {city: "Paris"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service, "consumer.sos"), []byte(`package service
export consume
to consume:
  call producer called address
  show city of address
  finish
`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), `import "./service" as service
call service.consume
`)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if _, err := Run(context.Background(), p, Options{Dir: dir, SourcePath: filepath.Join(dir, "main.sos")}); err != nil {
		t.Fatal(err)
	}
}

func TestNestedModuleFailureFramesUseDefiningFile(t *testing.T) {
	dir := t.TempDir()
	service := filepath.Join(dir, "service")
	if err := os.MkdirAll(service, 0o755); err != nil {
		t.Fatal(err)
	}
	moduleFile := filepath.Join(service, "service.sos")
	if err := os.WriteFile(moduleFile, []byte(`package service
export Missing
export outer
define failure Missing:
to inner may fail with Missing:
  fail Missing with "missing"
to outer may fail with Missing:
  call inner
`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), `import "./service" as service
call service.outer
`)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir, SourcePath: filepath.Join(dir, "main.sos")})
	if err == nil {
		t.Fatal("expected typed failure")
	}
	frames := result.Failure["frames"].([]map[string]any)
	for _, frame := range frames {
		if frame["kind"] == "action" || frame["kind"] == "fail" {
			if !strings.HasSuffix(frame["path"].(string), filepath.Join("service", "service.sos")) {
				t.Fatalf("frame path = %#v, want defining file %q; frames=%#v", frame["path"], moduleFile, frames)
			}
		}
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

func TestTypedFailureCaptureRejectsUnknownResultFields(t *testing.T) {
	for _, source := range []string{
		`define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
capture fetch called outcome
when succeeded of outcome:
  show mystery of outcome
`,
		`define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
capture fetch called outcome
when succeeded of outcome:
  show "ok"
otherwise:
  show mystery of outcome
`,
	} {
		diagnostics := Check(source)
		if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "Result has no field mystery") {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
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

func TestTypedFailureHandlerBindingsAreLocalAndCannotCollide(t *testing.T) {
	collision := `define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
make problem "outer"
call fetch called value
  on failure Missing called problem:
    recover with "cached"
`
	diagnostics := Check(collision)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "collides") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	local := `define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
call fetch called value
  on failure Missing called problem:
    recover with "cached"
`
	result, err := Run(context.Background(), mustParse(t, local), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := result.Variables["problem"]; leaked {
		t.Fatalf("handler binding leaked: %#v", result.Variables)
	}
}

func TestTypedFailureRejectsImpossibleHandler(t *testing.T) {
	source := `define failure Missing:
define failure Invalid:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
to caller returning text:
  call fetch called value
    on failure Invalid:
      recover with "invalid"
    on failure Missing:
      recover with "cached"
  finish with value
`
	diagnostics := Check(source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "impossible") {
		t.Fatalf("diagnostics = %+v", diagnostics)
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
	if diagnostics := Check(source); len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "caller.recovery must be integer; received text") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestTypedFailureCanProduceImportedFailureAndBindMissingOptionalField(t *testing.T) {
	dir := t.TempDir()
	errorsDir := filepath.Join(dir, "errors")
	if err := os.MkdirAll(errorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(errorsDir, "errors.sos"), []byte(`package errors
export Missing
define failure Missing:
  retry_after as optional duration
`), 0o644); err != nil {
		t.Fatal(err)
	}
	source := `import "./errors"
to fetch returning duration may fail with Missing:
  fail Missing with "missing"
to caller returning duration:
  call fetch called value
    on failure Missing using retry_after:
      when retry_after is null:
        recover with 0s
      recover with retry_after
  finish with value
call caller called delay
`
	p, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	result, err := Run(context.Background(), p, Options{Dir: dir})
	if err != nil || result.Variables["delay"] == nil {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestTypedResultCheckerUsesParameterTypes(t *testing.T) {
	source := `to convert with value as text returning integer:
  finish with value
`
	if diagnostics := Check(source); len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "convert.result must be integer; received text") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestResolvedSentenceWrongArityGetsActionableDiagnostic(t *testing.T) {
	dir := t.TempDir()
	_, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), `import "std/text"
upper called result
`)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "upper expects 1 argument(s)") {
		t.Fatalf("diagnostics = %+v", diagnostics)
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

func TestTypedFailureMetadataExcludesRecoveredAndReplacedFailures(t *testing.T) {
	source := `define failure Missing:
define failure Replacement:
to leaf returning text may fail with Missing:
  fail Missing with "missing"
to recovered returning text:
  call leaf called value
    on failure Missing:
      recover with "cached"
  finish with value
to transformed returning text may fail with Replacement:
  call leaf called value
    on failure Missing:
      fail Replacement with "replacement"
  finish with value
`
	p := mustParse(t, source)
	if diagnostics := Check(source); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	metadata := p.ActionMetadata()
	byName := map[string][]string{}
	for _, action := range metadata {
		byName[action.Name] = action.PossibleFailures
	}
	if len(byName["recovered"]) != 0 {
		t.Fatalf("recovered failures = %#v", byName["recovered"])
	}
	if got := byName["transformed"]; len(got) != 1 || got[0] != "Replacement" {
		t.Fatalf("transformed failures = %#v", got)
	}
}

func TestTypedFailureImportedNameCollisionIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	for _, module := range []string{"alpha", "beta"} {
		moduleDir := filepath.Join(dir, module)
		if err := os.MkdirAll(moduleDir, 0o755); err != nil {
			t.Fatal(err)
		}
		source := "package " + module + "\nexport Missing\ndefine failure Missing:\n"
		if err := writeTestFile(filepath.Join(moduleDir, module+".sos"), source); err != nil {
			t.Fatal(err)
		}
	}
	source := "import \"./beta\" as beta\nimport \"./alpha\" as alpha\n"
	_, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "module alpha (imported as alpha)") || !strings.Contains(diagnostics[0].Message, "module beta (imported as beta)") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestTypedFailureModuleContractRetainsImports(t *testing.T) {
	dir := t.TempDir()
	dep := filepath.Join(dir, "dep")
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(dep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(dep, "dep.sos"), `package dep
export Missing
export fetch
define failure Missing:
to fetch returning text may fail with Missing:
  fail Missing with "missing"
`); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(lib, "lib.sos"), `package lib
import "../dep" as dep
export caller
to caller returning text:
  call dep.fetch called value
  finish with value
`); err != nil {
		t.Fatal(err)
	}
	_, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), `import "./lib" as lib
`)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "may pass Missing") {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestParallelMapRejectsBareFinishValue(t *testing.T) {
	source := `make values [1]
map each value in values with at most 1 running called results:
  finish
`
	_, err := Run(context.Background(), mustParse(t, source), Options{})
	if err == nil || !strings.Contains(err.Error(), "finished without a value") {
		t.Fatalf("error = %v", err)
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
