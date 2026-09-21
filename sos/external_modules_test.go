package sos

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

type writeBuffer struct{ bytes.Buffer }

func (w *writeBuffer) Close() error { return nil }

func protocolClient(response string) *stdioClient {
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/module"
	return &stdioClient{
		in:         &writeBuffer{},
		out:        bufio.NewReader(strings.NewReader(response)),
		nextID:     1,
		definition: definition,
	}
}

func TestStdioProtocolRejectsMalformedAndMismatchedResponses(t *testing.T) {
	for name, response := range map[string]string{
		"non-json":      "not json\n",
		"wrong-id":      `{"jsonrpc":"2.0","id":2,"result":null}` + "\n",
		"wrong-version": `{"jsonrpc":"1.0","id":1,"result":null}` + "\n",
		"invalid-utf8":  string([]byte{'{', 0xff, '}', '\n'}),
	} {
		t.Run(name, func(t *testing.T) {
			client := protocolClient(response)
			if err := client.call(context.Background(), "initialize", map[string]any{}, nil); err == nil {
				t.Fatal("expected protocol error")
			}
		})
	}
}

func TestExternalDefinitionRejectsInvalidTimeouts(t *testing.T) {
	_, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/timeout"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="run"
timeout="eventually"
[action.command]
program="tool"
`))
	if err == nil || !strings.Contains(err.Error(), "positive duration") {
		t.Fatalf("invalid timeout error = %v", err)
	}
}

func TestExternalDefinitionRejectsInvalidCommandBindings(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/bindings"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="run"
[[action.parameter]]
name="root"
type="text"
[action.command]
program="tool"
arguments=["${missing}"]
working_directory_parameter="root"
`))
	if err == nil {
		err = definition.ValidateInterface()
	}
	if err == nil || (!strings.Contains(err.Error(), "unknown parameter") && !strings.Contains(err.Error(), "folder parameter")) {
		t.Fatalf("invalid command binding error = %v", err)
	}
}

func TestExternalDefinitionDigestIgnoresFormattingAndComments(t *testing.T) {
	compact := `schema=1
[module]
path="test/digest"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
secrets=["B", "A"]
[[action]]
name="run"
effects=["write", "read"]
[action.command]
program="tool"
exit_codes=[2, 1]
`
	formatted := strings.Replace(compact, "schema=1", "# comment\nschema = 1", 1)
	formatted = strings.Replace(formatted, `secrets=["B", "A"]`, `secrets=["A", "B"]`, 1)
	formatted = strings.Replace(formatted, `effects=["write", "read"]`, `effects=["read", "write"]`, 1)
	formatted = strings.Replace(formatted, `exit_codes=[2, 1]`, `exit_codes=[1, 2]`, 1)
	left, err := decodeExternalModuleDefinition("left.toml", []byte(compact))
	if err != nil {
		t.Fatal(err)
	}
	right, err := decodeExternalModuleDefinition("right.toml", []byte(formatted))
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest() != right.Digest() {
		t.Fatalf("semantic digest changed with formatting: %s != %s", left.Digest(), right.Digest())
	}
}

func TestExternalDefinitionDigestNormalizesRuntimeDefaults(t *testing.T) {
	implicit := `schema=1
[module]
path="test/defaults"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="run"
[action.command]
program="tool"
`
	explicit := strings.Replace(implicit, `kind="command"`, "kind=\"command\"\nmax_in_flight=1\nworking_directory=\"${workspace}\"\nstartup_timeout=\"10s\"\nshutdown_timeout=\"1s\"", 1)
	explicit = strings.Replace(explicit, "process=true", "process=true\nfilesystem=\"none\"", 1)
	explicit = strings.Replace(explicit, `program="tool"`, "program=\"tool\"\nstdin=\"none\"\nstdout=\"text\"\nstderr=\"diagnostic\"\nexit_codes=[0]", 1)
	left, err := decodeExternalModuleDefinition("left.toml", []byte(implicit))
	if err != nil {
		t.Fatal(err)
	}
	right, err := decodeExternalModuleDefinition("right.toml", []byte(explicit))
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest() != right.Digest() {
		t.Fatalf("default forms changed digest: %s != %s", left.Digest(), right.Digest())
	}
}

func TestStdioProtocolRejectsOversizedResponse(t *testing.T) {
	response := strings.Repeat(" ", (1<<20)+1) + "\n"
	client := protocolClient(response)
	if err := client.call(context.Background(), "initialize", nil, nil); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("error = %v", err)
	}
}

func TestStdioProtocolRejectsOversizedRequest(t *testing.T) {
	client := protocolClient(`{"jsonrpc":"2.0","id":1,"result":null}` + "\n")
	err := client.call(context.Background(), "invoke", map[string]any{"value": strings.Repeat("x", 1<<20)}, nil)
	if err == nil || !strings.Contains(err.Error(), "request exceeds 1 MiB") {
		t.Fatalf("error = %v", err)
	}
}

func TestExternalVersionRequirements(t *testing.T) {
	if got := firstSemanticVersion("tool v1.12.3-beta\n"); got != "1.12.3-beta" {
		t.Fatalf("version = %q", got)
	}
	for requirement, want := range map[string]bool{
		">=1.12.0": true,
		">1.12.3":  false,
		"<2.0.0":   true,
		"=1.12.3":  true,
		">=1.12.3": false,
	} {
		version := "1.12.3"
		if requirement == ">=1.12.3" && !want {
			version = "1.12.3-beta.1"
		}
		if got := versionSatisfies(version, requirement); got != want {
			t.Errorf("versionSatisfies(%s, %q) = %v", version, requirement, got)
		}
	}
	if versionSatisfies("1.2.3", ">=garbage") {
		t.Fatal("malformed requirements must not match")
	}
}

var _ io.Closer = (*writeBuffer)(nil)

type captureDebugger struct {
	frames    []DebugFrame
	snapshots []map[string]any
}

func (d *captureDebugger) BeforeStatement(_ context.Context, frame DebugFrame, _ []DebugFrame, snapshot map[string]any) error {
	d.frames = append(d.frames, frame)
	d.snapshots = append(d.snapshots, snapshot)
	if frame.Kind == "external" {
		return errors.New("debug stop")
	}
	return nil
}

func TestExternalCallExposesOpaqueDebuggerFrame(t *testing.T) {
	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "modules", "demo")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := `schema=1
[module]
path="local/demo"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="echo"
[action.command]
program="never-launched"
arguments=[]
`
	definitionPath := filepath.Join(moduleDir, "module.sos.toml")
	if err := os.WriteFile(definitionPath, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sos.toml"), []byte("version=1\n[external]\nprocess=true\n[[module.external]]\npath=\"local/demo\"\ndefinition=\"modules/demo/module.sos.toml\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := "import \"local/demo\" as demo\ncall demo.echo\n"
	program, diagnostics := LoadProgram(filepath.Join(dir, "main.sos"), source)
	if len(diagnostics) > 0 {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	debugger := &captureDebugger{}
	policy := sosconfig.Effective{ExternalProcess: true}
	_, err := Run(context.Background(), program, Options{Dir: dir, Config: &policy, Debugger: debugger, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "debug stop") {
		t.Fatalf("run error = %v", err)
	}
	if len(debugger.frames) == 0 {
		t.Fatal("external debugger frame was not emitted")
	}
	frame := debugger.frames[len(debugger.frames)-1]
	if frame.Kind != "external" || frame.Name != "demo.echo" || frame.Path != definitionPath || strings.Contains(frame.Text, "argument") {
		t.Fatalf("external frame = %+v", frame)
	}
	if snapshot := debugger.snapshots[len(debugger.snapshots)-1]; len(snapshot) != 0 {
		t.Fatalf("external debugger snapshot leaked variables: %+v", snapshot)
	}
}

func TestExternalAuthorizationUsesMinimalRedactedEnvironment(t *testing.T) {
	t.Setenv("SOS_TEST_PLUGIN_SECRET", "never-print-this-value")
	t.Setenv("SOS_TEST_UNRELATED", "must-not-leak")
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/module"
	definition.Capabilities.Process = true
	definition.Capabilities.Secrets = []string{"SOS_TEST_PLUGIN_SECRET"}
	policy := sosconfig.Effective{ExternalProcess: true, ExternalSecrets: []string{"SOS_TEST_PLUGIN_SECRET"}}
	environment, err := definition.authorize(Options{Config: &policy})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "SOS_TEST_PLUGIN_SECRET=never-print-this-value") || strings.Contains(joined, "SOS_TEST_UNRELATED") {
		t.Fatalf("unexpected environment names: %q", joined)
	}
	policy.ExternalSecrets = nil
	_, err = definition.authorize(Options{Config: &policy})
	if err == nil || strings.Contains(err.Error(), "never-print-this-value") {
		t.Fatalf("authorization error leaked secret: %v", err)
	}
}

func TestExternalNamedTypesBecomeClosedRuntimeDefinitions(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/records"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["plugin"]
[runtime.requires]
python=">=3.11"
[capabilities]
process=true
[[type]]
name="Person"
[[type.field]]
name="name"
type="text"
[[action]]
name="lookup"
effects=["process"]
targets=["native"]
[action.result]
type="Person"
`))
	if err != nil {
		t.Fatal(err)
	}
	module, err := definition.module()
	if err != nil {
		t.Fatal(err)
	}
	if module.Definitions["Person"] == nil || !typeMatchesRef(map[string]any{"name": "Ada"}, TypeRef{Name: "Person"}, module.Definitions) {
		t.Fatalf("named type was not available for runtime validation: %+v", module.Definitions)
	}
	if !slices.Equal(module.Native["lookup"].Effects, []string{"process"}) {
		t.Fatalf("external effects were not preserved: %+v", module.Native["lookup"].Effects)
	}
	if typeMatchesRef(map[string]any{"name": "Ada", "extra": true}, TypeRef{Name: "Person"}, module.Definitions) {
		t.Fatal("named external records must remain closed")
	}
}

func TestExternalDurationBoundaryUsesCanonicalStrings(t *testing.T) {
	typ := TypeRef{Name: "duration"}
	encoded := externalEncodeValue(1500*time.Millisecond, typ, nil)
	if encoded != "1.5s" {
		t.Fatalf("encoded duration = %#v", encoded)
	}
	decoded, err := externalDecodeValue("1.5s", typ, nil)
	if err != nil || decoded != 1500*time.Millisecond {
		t.Fatalf("decoded duration = %#v, %v", decoded, err)
	}
}

func TestExternalTimestampAndIntegerBoundaryConversion(t *testing.T) {
	timestamp, err := externalDecodeValue("2026-09-21T12:00:00Z", TypeRef{Name: "timestamp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := timestamp.(time.Time); !ok {
		t.Fatalf("timestamp was not converted: %#v", timestamp)
	}
	if _, err := externalDecodeValue(json.Number("9007199254740993"), TypeRef{Name: "integer"}, nil); err == nil {
		t.Fatal("unsafe JSON integer precision was accepted")
	}
}

func TestExternalAnyRejectsUnsafeNestedInteger(t *testing.T) {
	_, err := externalDecodeResult(map[string]any{"nested": []any{json.Number("9007199254740993")}}, "any", nil)
	if err == nil || !strings.Contains(err.Error(), "exact JSON range") {
		t.Fatalf("unsafe any result error = %v", err)
	}
}

func TestExternalAnyRejectsUnsafeExponentInteger(t *testing.T) {
	_, err := externalDecodeResult(json.Number("9007199254740993e0"), "any", nil)
	if err == nil || !strings.Contains(err.Error(), "exact JSON range") {
		t.Fatalf("unsafe exponent result error = %v", err)
	}
}

func TestQueuedStdioCallObservesCancellation(t *testing.T) {
	client := &stdioClient{}
	client.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.call(ctx, "invoke", nil, nil)
	client.mu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queued call error = %v", err)
	}
}

func TestStdioAdmissionRejectsAlreadyCanceledContext(t *testing.T) {
	client := &stdioClient{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.call(ctx, "invoke", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("client admission error = %v", err)
	}
	definition := &ExternalModuleDefinition{clientsGate: make(chan struct{}, 1)}
	definition.clientsGate <- struct{}{}
	if err := definition.lockClients(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("definition admission error = %v", err)
	}
}

func TestActionHeadersAcceptFileAndFolderResults(t *testing.T) {
	for _, resultType := range []string{"file", "folder", "optional file", "list of folder"} {
		if _, err := parseActionDecl("to locate returning " + resultType + ":"); err != nil {
			t.Fatalf("%s result rejected: %v", resultType, err)
		}
	}
}

func TestExternalInterfaceAcceptsPathTypesAndRejectsInvalidFailureFields(t *testing.T) {
	valid := `schema=1
[module]
path="test/paths"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="run"
[[action.parameter]]
name="root"
type="folder"
[action.command]
program="tool"
`
	definition, err := decodeExternalModuleDefinition("paths.toml", []byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if err := definition.ValidateInterface(); err != nil {
		t.Fatalf("folder parameter rejected: %v", err)
	}
	invalid := strings.Replace(valid, "[[action]]", "[[failure]]\nname=\"Broken\"\n[[failure.field]]\nname=\"detail\"\ntype=\"MissingType\"\n[[action]]", 1)
	definition, err = decodeExternalModuleDefinition("failure.toml", []byte(invalid))
	if err == nil {
		err = definition.ValidateInterface()
	}
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("invalid failure type error = %v", err)
	}
}

func TestCloneValuePreservesTemporalValues(t *testing.T) {
	timestamp := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	value := map[string]any{"duration": 1500 * time.Millisecond, "timestamp": timestamp}
	cloned, err := cloneValue(value)
	if err != nil {
		t.Fatal(err)
	}
	object := cloned.(map[string]any)
	if object["duration"] != 1500*time.Millisecond || !object["timestamp"].(time.Time).Equal(timestamp) {
		t.Fatalf("clone changed temporal values: %#v", object)
	}
}

func TestExternalSessionKeysAreDistinct(t *testing.T) {
	left, right := newExternalSessionKey(), newExternalSessionKey()
	if left == right || left.id == right.id {
		t.Fatalf("session keys are not unique: %+v %+v", left, right)
	}
}

func TestModuleGraphReturnsDetachedImportMaps(t *testing.T) {
	entry := map[string]ModuleEdge{"lib": {Key: "/source/lib"}}
	imports := map[string]ModuleEdge{"dep": {Key: "/source/dep"}}
	table := &ModuleTable{entryRefs: entry, Order: []string{"/source/lib"}, ByKey: map[string]*Module{
		"/source/lib": {Name: "lib", Files: []ModuleFile{{Name: "lib.sos", Source: "", refs: imports}}},
	}}
	graph := table.Graph()
	graph.Entry["lib"] = ModuleEdge{Key: "changed"}
	graph.Modules[0].Files[0].Imports["dep"] = ModuleEdge{Key: "changed"}
	if entry["lib"].Key != "/source/lib" || imports["dep"].Key != "/source/dep" {
		t.Fatal("graph mutation leaked into the parsed module table")
	}
}

func TestOptionalExternalFailureFieldMayBeOmitted(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/failure"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[failure]]
name="Missing"
[[failure.field]]
name="detail"
type="optional text"
[[action]]
name="run"
failures=["Missing"]
[action.command]
program="tool"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := definition.validateFailurePayload("Missing", map[string]any{}); err != nil {
		t.Fatalf("optional payload field was required: %v", err)
	}
}

func TestStdioStderrIsBoundedAndRedacted(t *testing.T) {
	t.Setenv("SOS_PLUGIN_SECRET", "super-secret")
	definition := &ExternalModuleDefinition{}
	definition.Capabilities.Secrets = []string{"SOS_PLUGIN_SECRET"}
	captured := &externalOutput{limit: 16}
	_, _ = captured.Write([]byte("super-secret and more output"))
	var sink bytes.Buffer
	client := &stdioClient{definition: definition, stderr: captured, stderrSink: &sink}
	client.flushStderr()
	if strings.Contains(sink.String(), "super-secret") || !strings.Contains(sink.String(), "[REDACTED]") || !strings.Contains(sink.String(), "truncated") {
		t.Fatalf("stderr was not safely reported: %q", sink.String())
	}
}

func TestStdioStderrRedactsSecretsAcrossTruncationBoundary(t *testing.T) {
	t.Setenv("SOS_PLUGIN_SECRET", "boundary-secret")
	captured := newExternalOutput([]string{"SOS_PLUGIN_SECRET"})
	prefix := strings.Repeat("x", (1<<20)-4)
	_, _ = captured.Write([]byte(prefix + "boundary-secret" + "tail"))
	value := captured.redacted([]string{"SOS_PLUGIN_SECRET"})
	if strings.Contains(value, "boun") || strings.Contains(value, "boundary-secret") {
		t.Fatalf("stderr leaked a secret fragment at the boundary")
	}
}

func TestExternalRedactionIncludesObjectKeys(t *testing.T) {
	t.Setenv("SOS_PLUGIN_SECRET", "secret-key")
	redacted := redactExternalValue(map[string]any{"secret-key": "safe"}, []string{"SOS_PLUGIN_SECRET"}).(map[string]any)
	if _, leaked := redacted["secret-key"]; leaked {
		t.Fatal("secret object key was not redacted")
	}
	if redacted["[REDACTED]"] != "safe" {
		t.Fatalf("redacted object = %#v", redacted)
	}
}
