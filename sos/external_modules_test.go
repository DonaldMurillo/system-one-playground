package sos

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

type writeBuffer struct{ bytes.Buffer }

func (w *writeBuffer) Close() error { return nil }

type blockingWriteCloser struct {
	closed chan struct{}
	once   sync.Once
}

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	<-w.closed
	return 0, io.ErrClosedPipe
}
func (w *blockingWriteCloser) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

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

func TestExternalDefinitionAcceptsReadableParameterCommandBindings(t *testing.T) {
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
arguments=["${parameter.root}"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := definition.ValidateInterface(); err != nil {
		t.Fatal(err)
	}
	if got := commandParameterName("${parameter.root}"); got != "root" {
		t.Fatalf("parameter name=%q", got)
	}
}

func TestExternalDefinitionRejectsStreamStdinModeWithoutParameter(t *testing.T) {
	_, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/stdin-stream"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="events"
[action.result]
type="stream of text"
[action.command]
program="tool"
stdin="json"
stdout="json-lines"
`))
	if err == nil || !strings.Contains(err.Error(), "stdin mode requires stdin_parameter") {
		t.Fatalf("invalid streaming stdin binding error = %v", err)
	}
}

func TestExternalWorkingDirectoryParameterResolvesFromWorkspace(t *testing.T) {
	workspace := t.TempDir()
	subdir := filepath.Join(workspace, "nested")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/cwd"
	action := ExternalAction{Name: "events", Parameters: []ExternalField{{Name: "root", Type: "folder"}}}
	action.Command.WorkingDirectoryParameter = "root"
	policy := sosconfig.Effective{ExternalFilesystem: "workspace"}
	resolved, err := definition.resolveActionWorkingDirectory(Options{Dir: workspace, Config: &policy}, action, map[string]any{"root": "nested"})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(subdir)
	if resolved != want {
		t.Fatalf("relative cwd = %q, want %q", resolved, want)
	}
	outside := t.TempDir()
	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := definition.resolveActionWorkingDirectory(Options{Dir: workspace, Config: &policy}, action, map[string]any{"root": "escape"}); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("symlink escape error = %v", err)
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

func TestStdioStreamProtocolOpensConsumesAndReplenishesCredit(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/streams"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["plugin"]
[capabilities]
process=true
[[type]]
name="Event"
[[type.field]]
name="message"
type="text"
[[action]]
name="follow"
[action.result]
type="stream of Event"
`))
	if err != nil {
		t.Fatal(err)
	}
	responses := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"Event"}}`,
		`{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":0,"value":{"message":"started"}}}`,
		`{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":0}}`,
	}, "\n") + "\n"
	writes := &writeBuffer{}
	client := protocolClient(responses)
	client.in, client.definition = writes, definition
	stream, err := client.openStream(context.Background(), "follow", map[string]any{}, "Event")
	if err != nil {
		t.Fatal(err)
	}
	writtenBeforeMetrics := writes.Len()
	handle := newStreamHandle(TypeRef{Name: "Event"}, "events.follow", stream)
	metrics := handle.DebugStreamState()
	if metrics["itemsBuffered"] != 0 || metrics["creditAvailable"] != 1 || writes.Len() != writtenBeforeMetrics {
		t.Fatalf("non-consuming metrics = %#v", metrics)
	}
	item, ok, err := stream.next(context.Background())
	if err != nil || !ok || item.(map[string]any)["message"] != "started" {
		t.Fatalf("first item = %#v, %v, %v", item, ok, err)
	}
	if _, ok, err := stream.next(context.Background()); err != nil || ok {
		t.Fatalf("end = ok %v, err %v", ok, err)
	}
	lines := strings.Split(strings.TrimSpace(writes.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"method":"stream.open"`) || !strings.Contains(lines[0], `"credit":1`) || !strings.Contains(lines[1], `"method":"stream.credit"`) {
		t.Fatalf("protocol writes = %q", writes.String())
	}
}

func TestStdioStreamProtocolRejectsSequenceCreditAndTypeViolations(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/streams"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["plugin"]
[capabilities]
process=true
[[action]]
name="follow"
[action.result]
type="stream of integer"
`))
	if err != nil {
		t.Fatal(err)
	}
	for name, notification := range map[string]string{
		"sequence": `{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":1,"value":1}}`,
		"type":     `{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":0,"value":"wrong"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			responses := `{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"integer"}}` + "\n" + notification + "\n"
			client := protocolClient(responses)
			client.definition = definition
			stream, err := client.openStream(context.Background(), "follow", nil, "integer")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := stream.next(context.Background()); err == nil {
				t.Fatal("expected protocol violation")
			}
		})
	}
}

func TestStdioStreamProtocolRejectsBufferedItemsBeyondCredit(t *testing.T) {
	responses := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"integer"}}`,
		`{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":0,"value":1}}`,
		`{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":1,"value":2}}`,
	}, "\n") + "\n"
	client := protocolClient(responses)
	stream, err := client.openStream(context.Background(), "follow", nil, "integer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := stream.next(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeded its stream credit") || !client.poisoned.Load() {
		t.Fatalf("over-credit error=%v poisoned=%v", err, client.poisoned.Load())
	}
}

func TestStdioStreamProtocolConvertsDeclaredTerminalFailure(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/streams"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["plugin"]
[capabilities]
process=true
[[failure]]
name="Disconnected"
[[failure.field]]
name="service"
type="text"
[[action]]
name="follow"
failures=["Disconnected"]
[action.result]
type="stream of text"
`))
	if err != nil {
		t.Fatal(err)
	}
	responses := `{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"stream.error","params":{"streamId":"s1","failure":{"kind":"Disconnected","message":"lost","retryable":true,"payload":{"service":"payments"}}}}` + "\n"
	client := protocolClient(responses)
	client.definition = definition
	stream, err := client.openStream(context.Background(), "follow", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = stream.next(context.Background())
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "Disconnected" || failure.value["phase"] != "stream" || failure.value["service"] != "payments" {
		t.Fatalf("terminal failure = %#v, %v", failure, err)
	}
}

func TestStdioStreamProtocolAllowsOmittedEmptyFailurePayload(t *testing.T) {
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/streams"
	definition.Failures = []ExternalType{{Name: "Stopped", Fields: []ExternalField{{Name: "detail", Type: "optional text"}}}}
	definition.Actions = []ExternalAction{{Name: "follow", Failures: []string{"Stopped"}}}
	responses := `{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"stream.error","params":{"streamId":"s1","failure":{"kind":"Stopped","message":"done"}}}` + "\n"
	client := protocolClient(responses)
	client.definition = definition
	stream, err := client.openStream(context.Background(), "follow", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = stream.next(context.Background())
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "Stopped" {
		t.Fatalf("omitted payload terminal failure = %#v, %v", failure, err)
	}
	caught := FailureValue(err)
	if caught["kind"] != "Stopped" || caught["phase"] != "stream" || caught["message"] != "done" {
		t.Fatalf("caught failure value = %#v", caught)
	}
}

func TestStdioStreamInvalidFailureEnvelopePoisonsSession(t *testing.T) {
	for name, failureJSON := range map[string]string{
		"undeclared-kind": `{"kind":"Other","message":"bad"}`,
		"invalid-payload": `{"kind":"Stopped","message":"bad","payload":{"code":"wrong"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			definition := &ExternalModuleDefinition{}
			definition.Module.Path = "test/streams"
			definition.Failures = []ExternalType{{Name: "Stopped", Fields: []ExternalField{{Name: "code", Type: "integer"}}}}
			definition.Actions = []ExternalAction{{Name: "follow", Failures: []string{"Stopped"}}}
			responses := `{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}` + "\n" +
				`{"jsonrpc":"2.0","method":"stream.error","params":{"streamId":"s1","failure":` + failureJSON + `}}` + "\n"
			client := protocolClient(responses)
			client.definition = definition
			stream, err := client.openStream(context.Background(), "follow", nil, "text")
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = stream.next(context.Background())
			var declared *typedFailure
			if err == nil || errors.As(err, &declared) || !client.poisoned.Load() {
				t.Fatalf("invalid envelope error=%v declared=%v poisoned=%v", err, declared, client.poisoned.Load())
			}
		})
	}
}

func TestStdioStreamProtocolCancelWaitsForTerminalAcknowledgement(t *testing.T) {
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/streams"
	definition.Actions = []ExternalAction{{Name: "follow"}}
	responses := `{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":-1}}` + "\n"
	writes := &writeBuffer{}
	client := protocolClient(responses)
	client.in, client.definition = writes, definition
	stream, err := client.openStream(context.Background(), "follow", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := stream.cancel(context.Background()); err != nil {
		t.Fatalf("cleanup cancellation must be idempotent: %v", err)
	}
	if !strings.Contains(writes.String(), `"method":"stream.cancel"`) {
		t.Fatalf("protocol writes = %q", writes.String())
	}
}

func TestStdioStreamToolingStopInterruptsBlockedReadWithoutPoisoningSession(t *testing.T) {
	reader, writer := io.Pipe()
	writes := &writeBuffer{}
	client := &stdioClient{in: writes, out: bufio.NewReader(reader)}
	client.mu.Lock()
	stream := &stdioProtocolStream{client: client, id: "s1", action: "follow", itemType: TypeRef{Name: "text"}, stopped: make(chan struct{}), shutdown: time.Second}
	result := make(chan error, 1)
	go func() {
		_, more, err := stream.next(context.Background())
		if more {
			err = fmt.Errorf("unexpected item")
		}
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := stream.requestStreamStop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writes.String(), `"method":"stream.cancel"`) {
		t.Fatalf("writes=%q", writes.String())
	}
	if _, err := io.WriteString(writer, `{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":-1}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tooling stop did not unblock the stream read")
	}
	if client.poisoned.Load() {
		t.Fatal("graceful tooling stop poisoned the reusable plugin session")
	}
}

func TestStdioStreamToolingStopBoundsABlockedCancelWrite(t *testing.T) {
	writer := &blockingWriteCloser{closed: make(chan struct{})}
	client := &stdioClient{in: writer, out: bufio.NewReader(strings.NewReader(""))}
	stream := &stdioProtocolStream{client: client, id: "s1", action: "follow", itemType: TypeRef{Name: "text"}, stopped: make(chan struct{}), shutdown: 20 * time.Millisecond}
	started := time.Now()
	err := stream.requestStreamStop()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("blocked cancel error=%v duration=%v", err, time.Since(started))
	}
}

func TestStdioStreamProtocolCancelDiscardsAlreadyCreditedItem(t *testing.T) {
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/streams"
	definition.Actions = []ExternalAction{{Name: "follow"}}
	responses := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}`,
		`{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":0,"value":"buffered"}}`,
		`{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":0}}`,
	}, "\n") + "\n"
	client := protocolClient(responses)
	client.definition = definition
	stream, err := client.openStream(context.Background(), "follow", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStdioStreamProtocolWriteObservesDeadline(t *testing.T) {
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/blocked"
	writer := &blockingWriteCloser{closed: make(chan struct{})}
	client := &stdioClient{in: writer, out: bufio.NewReader(strings.NewReader("")), nextID: 1, definition: definition}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.openStream(ctx, "follow", nil, "text")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("blocked write error=%v duration=%v", err, time.Since(started))
	}
}

func TestStdioStreamOpenSendsDeadlineMetadata(t *testing.T) {
	writes := &writeBuffer{}
	client := protocolClient(`{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}` + "\n")
	client.in = writes
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := client.openStream(ctx, "follow", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.finish(false)
	if !strings.Contains(writes.String(), `"deadline":"`) {
		t.Fatalf("stream.open omitted deadline metadata: %s", writes.String())
	}
}

func TestStdioStreamTerminalPoisonsTrailingProtocolFrames(t *testing.T) {
	responses := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}`,
		`{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":-1}}`,
		`{"jsonrpc":"2.0","method":"stream.item","params":{"streamId":"s1","sequence":0,"value":"late"}}`,
	}, "\n") + "\n"
	client := protocolClient(responses)
	stream, err := client.openStream(context.Background(), "follow", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	if _, more, err := stream.next(context.Background()); err == nil || more || !strings.Contains(err.Error(), "after stream s1 ended") || !client.poisoned.Load() {
		t.Fatalf("terminal = more %v err %v poisoned=%v", more, err, client.poisoned.Load())
	}
}

func TestStdioStreamNormalTerminalAllowsSessionReuse(t *testing.T) {
	responses := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"streamId":"s1","itemType":"text"}}`,
		`{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":-1}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"value":"reused"}}`,
	}, "\n") + "\n"
	client := protocolClient(responses)
	stream, err := client.openStream(context.Background(), "follow", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	if _, more, err := stream.next(context.Background()); err != nil || more {
		t.Fatalf("terminal = more %v err %v", more, err)
	}
	var result struct {
		Value string `json:"value"`
	}
	if err := client.call(context.Background(), "invoke", nil, &result); err != nil || result.Value != "reused" {
		t.Fatalf("session reuse result=%+v err=%v", result, err)
	}
}

func TestStdioModuleOpensSecondStreamWithoutWaitingForFirst(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	definition, err := LoadExternalModuleDefinition(filepath.Join("..", "examples", "sos", "streams", "modules", "events", "module.sos.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var action ExternalAction
	for _, candidate := range definition.Actions {
		if candidate.Name == "infinite" {
			action = candidate
		}
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	session := newExternalSessionKey()
	defer definition.closeSession(session)
	opts := Options{Dir: t.TempDir(), Config: &policy, Stderr: io.Discard, externalSession: session}
	first, err := definition.openStdioStream(context.Background(), opts, action, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := definition.openStdioStream(ctx, opts, action, nil)
	if err != nil {
		_ = first.cancel(context.Background())
		t.Fatalf("second open blocked behind first stream: %v", err)
	}
	if err := second.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStdioStreamDeclaredOpenFailureAllowsSessionReuse(t *testing.T) {
	definition := &ExternalModuleDefinition{}
	definition.Module.Path = "test/streams"
	definition.Failures = []ExternalType{{Name: "Unavailable"}}
	definition.Actions = []ExternalAction{{Name: "follow", Failures: []string{"Unavailable"}}}
	definition.Actions[0].Result.Type = "stream of text"
	definition.clientsGate = make(chan struct{}, 1)
	definition.clientsGate <- struct{}{}
	definition.clients = map[*externalSessionKey]*stdioClient{}
	responses := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"nope","data":{"kind":"Unavailable"}}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"value":"reused"}}`,
	}, "\n") + "\n"
	client := protocolClient(responses)
	client.definition = definition
	session := newExternalSessionKey()
	definition.clients[session] = client
	if _, err := definition.openStdioStream(context.Background(), Options{externalSession: session}, definition.Actions[0], nil); err == nil {
		t.Fatal("expected declared open failure")
	} else {
		var failure *typedFailure
		if !errors.As(err, &failure) || failure.kind != "Unavailable" || client.poisoned.Load() {
			t.Fatalf("declared open failure=%v poisoned=%v", err, client.poisoned.Load())
		}
	}
	var result struct {
		Value string `json:"value"`
	}
	if err := client.call(context.Background(), "invoke", nil, &result); err != nil || result.Value != "reused" {
		t.Fatalf("reuse after open failure result=%+v err=%v", result, err)
	}
}

func TestStdioInvokeIncludesActionDeadlineContext(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/deadline"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["plugin"]
[capabilities]
process=true
[[action]]
name="lookup"
timeout="1s"
[action.result]
type="text"
`))
	if err != nil {
		t.Fatal(err)
	}
	writes := &writeBuffer{}
	client := protocolClient(`{"jsonrpc":"2.0","id":1,"result":{"value":"ok"}}` + "\n")
	client.in, client.definition = writes, definition
	session := newExternalSessionKey()
	definition.clients[session] = client
	result, err := definition.invokeStdio(context.Background(), Options{Dir: "/workspace", externalSession: session}, definition.Actions[0], nil)
	if err != nil || result != "ok" {
		t.Fatalf("invoke result=%v err=%v", result, err)
	}
	if !strings.Contains(writes.String(), `"context":{"deadline":"`) || !strings.Contains(writes.String(), `"workingDirectory":"/workspace"`) {
		t.Fatalf("invoke request omitted deadline context: %s", writes.String())
	}
}

func TestCommandAdapterStreamJSONLinesLifecycle(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	for name, test := range map[string]struct {
		code    string
		items   []int64
		errorIs string
		failure string
	}{
		"finite":  {code: `print("1"); print("2")`, items: []int64{1, 2}},
		"invalid": {code: `print("not-json")`, errorIs: "invalid JSON line", failure: "StreamDecodeFailure"},
		"partial": {code: `import sys; sys.stdout.write("1")`, errorIs: "partial final JSON line", failure: "StreamDecodeFailure"},
		"type":    {code: `import json; print(json.dumps("wrong"))`, errorIs: "must be integer", failure: "StreamDecodeFailure"},
		"exit":    {code: `import sys; print("1"); sys.exit(4)`, items: []int64{1}, errorIs: "exited 4", failure: "ProcessFailure"},
	} {
		t.Run(name, func(t *testing.T) {
			definition, err := decodeExternalModuleDefinition(filepath.Join(t.TempDir(), "module.sos.toml"), []byte(fmt.Sprintf(`schema=1
[module]
path="test/command-stream"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="events"
[action.result]
type="stream of integer"
[action.command]
program="python3"
arguments=["-c", %q]
stdout="json-lines"
stderr="diagnostic"
`, test.code)))
			if err != nil {
				t.Fatal(err)
			}
			if test.failure != "" {
				definition.Failures = append(definition.Failures, ExternalType{Name: test.failure})
				definition.Actions[0].Failures = append(definition.Actions[0].Failures, test.failure)
			}
			policy := sosconfig.Effective{ExternalProcess: true}
			source, err := definition.openCommandStream(context.Background(), Options{Dir: t.TempDir(), Config: &policy, Stderr: io.Discard}, definition.Actions[0], nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []int64
			for {
				item, more, nextErr := source.next(context.Background())
				if nextErr != nil {
					err = nextErr
					break
				}
				if !more {
					break
				}
				got = append(got, item.(int64))
			}
			if !slices.Equal(got, test.items) {
				t.Fatalf("items = %v, want %v", got, test.items)
			}
			if test.errorIs == "" && err != nil {
				t.Fatal(err)
			}
			if test.errorIs != "" && (err == nil || !strings.Contains(err.Error(), test.errorIs)) {
				t.Fatalf("error = %v, want %q", err, test.errorIs)
			}
			if test.failure != "" {
				var failure *typedFailure
				if !errors.As(err, &failure) || failure.kind != test.failure || failure.value["phase"] != "stream" {
					t.Fatalf("typed terminal failure = %#v, %v", failure, err)
				}
			}
		})
	}
}

func TestCommandAdapterStreamCancellationKillsProcess(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	definition, err := decodeExternalModuleDefinition(filepath.Join(t.TempDir(), "module.sos.toml"), []byte(`schema=1
[module]
path="test/command-cancel"
version="1.0.0"
[runtime]
kind="command"
shutdown_timeout="100ms"
[capabilities]
process=true
[[action]]
name="events"
[action.result]
type="stream of integer"
[action.command]
program="python3"
	arguments=["-c", "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); print(1, flush=True); time.sleep(60)"]
stdout="json-lines"
`))
	if err != nil {
		t.Fatal(err)
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	source, err := definition.openCommandStream(context.Background(), Options{Dir: t.TempDir(), Config: &policy, Stderr: io.Discard}, definition.Actions[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, more, err := source.next(context.Background()); err != nil || !more {
		t.Fatalf("first item: more=%v err=%v", more, err)
	}
	started := time.Now()
	if err := source.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("cancellation exceeded shutdown deadline: %v", time.Since(started))
	}
}

func TestCommandAdapterStreamCancellationAllowsCooperativeCleanup(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("Windows has no safe os/exec cooperative tree signal")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "stopped")
	script := filepath.Join(dir, "producer.py")
	sourceCode := fmt.Sprintf("import signal,sys,time\ndef stop(*_):\n open(%q, 'w').write('stopped')\n sys.exit(0)\nsignal.signal(signal.SIGTERM, stop)\nprint(1, flush=True)\ntime.sleep(60)\n", marker)
	if err := os.WriteFile(script, []byte(sourceCode), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := decodeExternalModuleDefinition(filepath.Join(dir, "module.sos.toml"), []byte(fmt.Sprintf(`schema=1
[module]
path="test/command-cooperative"
version="1.0.0"
[runtime]
kind="command"
shutdown_timeout="1s"
[capabilities]
process=true
[[action]]
name="events"
[action.result]
type="stream of integer"
[action.command]
program="python3"
arguments=[%q]
stdout="json-lines"
`, script)))
	if err != nil {
		t.Fatal(err)
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	source, err := definition.openCommandStream(context.Background(), Options{Dir: dir, Config: &policy, Stderr: io.Discard}, definition.Actions[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, more, err := source.next(context.Background()); err != nil || !more {
		t.Fatalf("first item: more=%v err=%v", more, err)
	}
	if err := source.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "stopped" {
		t.Fatalf("cooperative cleanup marker=%q err=%v", data, err)
	}
}

func TestCommandAdapterStreamActionTimeoutAppliesToLifetime(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	definition, err := decodeExternalModuleDefinition(filepath.Join(t.TempDir(), "module.sos.toml"), []byte(`schema=1
[module]
path="test/command-timeout"
version="1.0.0"
[runtime]
kind="command"
shutdown_timeout="100ms"
[capabilities]
process=true
[[action]]
name="events"
timeout="30ms"
failures=["StreamTimeout"]
[action.result]
type="stream of integer"
[action.command]
program="python3"
arguments=["-c", "import time; time.sleep(60)"]
stdout="json-lines"
[[failure]]
name="StreamTimeout"
`))
	if err != nil {
		t.Fatal(err)
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	source, err := definition.openCommandStream(context.Background(), Options{Dir: t.TempDir(), Config: &policy, Stderr: io.Discard}, definition.Actions[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, _, err = source.next(context.Background())
	var failure *typedFailure
	if !errors.As(err, &failure) || failure.kind != "StreamTimeout" || time.Since(started) > time.Second {
		t.Fatalf("action timeout error=%v duration=%v", err, time.Since(started))
	}
}

func TestCommandStreamDeadlineStopsUnconsumedProducer(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	definition, err := decodeExternalModuleDefinition(filepath.Join(t.TempDir(), "module.sos.toml"), []byte(`schema=1
[module]
path="test/unconsumed-command-timeout"
version="1.0.0"
[runtime]
kind="command"
shutdown_timeout="50ms"
[capabilities]
process=true
[[action]]
name="events"
timeout="30ms"
failures=["StreamTimeout"]
[action.result]
type="stream of integer"
[action.command]
program="python3"
arguments=["-c", "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(60)"]
stdout="json-lines"
[[failure]]
name="StreamTimeout"
`))
	if err != nil {
		t.Fatal(err)
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	source, err := definition.openCommandStream(context.Background(), Options{Dir: t.TempDir(), Config: &policy, Stderr: io.Discard}, definition.Actions[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := source.(*commandStreamSource)
	select {
	case <-stream.stopped:
	case <-time.After(time.Second):
		t.Fatal("unconsumed command producer did not stop at deadline")
	}
	if !stream.done.Load() || stream.cmd.ProcessState == nil {
		t.Fatalf("unconsumed command producer survived deadline: done=%v state=%v", stream.done.Load(), stream.cmd.ProcessState)
	}
	if _, more, err := source.next(context.Background()); more {
		t.Fatalf("deferred command timeout unexpectedly produced an item")
	} else {
		var failure *typedFailure
		if !errors.As(err, &failure) || failure.kind != "StreamTimeout" {
			t.Fatalf("deferred command timeout err=%v failure=%#v", err, failure)
		}
	}
	if _, more, err := source.next(context.Background()); more || err != nil {
		t.Fatalf("command timeout was delivered more than once: more=%v err=%v", more, err)
	}
}

func TestStdioStreamDeadlineStopsUnconsumedProducer(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	definition, err := LoadExternalModuleDefinition(filepath.Join("..", "examples", "sos", "streams", "modules", "events", "module.sos.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var action ExternalAction
	for _, candidate := range definition.Actions {
		if candidate.Name == "infinite" {
			action = candidate
		}
	}
	// The deadline should exercise an already-running producer, not race
	// Python process startup on a loaded Windows CI runner.
	action.Timeout = "1s"
	policy := sosconfig.Effective{ExternalProcess: true}
	session := newExternalSessionKey()
	defer definition.closeSession(session)
	source, err := definition.openStdioStream(context.Background(), Options{Dir: t.TempDir(), Config: &policy, Stderr: io.Discard, externalSession: session}, action, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := source.(*stdioProtocolStream)
	deadline := time.Now().Add(3 * time.Second)
	for !stream.done.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !stream.done.Load() || stream.client.activeStream.Load() {
		t.Fatalf("unconsumed stdio producer survived deadline: done=%v active=%v", stream.done.Load(), stream.client.activeStream.Load())
	}
	if _, more, err := source.next(context.Background()); more || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deferred stdio timeout more=%v err=%v", more, err)
	}
	if _, more, err := source.next(context.Background()); more || err != nil {
		t.Fatalf("stdio timeout was delivered more than once: more=%v err=%v", more, err)
	}
}

func TestStdioStreamEndAtDeadlineReturnsTimeoutNotCleanEOF(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	client := &stdioClient{
		in:  &writeBuffer{},
		out: bufio.NewReader(strings.NewReader(`{"jsonrpc":"2.0","method":"stream.end","params":{"streamId":"s1","lastSequence":-1}}` + "\n")),
	}
	client.mu.Lock() // openStream transfers this response boundary to the stream.
	stream := &stdioProtocolStream{
		client: client, id: "s1", action: "events", itemType: TypeRef{Name: "integer"},
		deadline: deadline, now: func() time.Time { return deadline }, stopped: make(chan struct{}),
		release: func(bool) {},
	}
	_, more, err := stream.next(context.Background())
	if more || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("terminal deadline race returned more=%v err=%v", more, err)
	}
	if _, more, err := stream.next(context.Background()); more || err != nil {
		t.Fatalf("terminal timeout was not one-shot: more=%v err=%v", more, err)
	}
}

func TestCommandStreamEOFAtDeadlineReturnsTimeoutNotCleanEOF(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("test fixture uses the POSIX true command")
	}
	deadline := time.Now().Add(time.Hour)
	cmd := exec.Command("true")
	configureProcessTree(cmd)
	if err := startProcessTree(cmd); err != nil {
		t.Fatal(err)
	}
	stream := &commandStreamSource{
		cmd: cmd, out: bufio.NewReader(strings.NewReader("")), stderr: newExternalOutput(nil),
		deadline: deadline, now: func() time.Time { return deadline }, lifecycleCancel: func() {},
		stopped: make(chan struct{}),
	}
	_, more, err := stream.next(context.Background())
	if more || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("terminal deadline race returned more=%v err=%v", more, err)
	}
	if _, more, err := stream.next(context.Background()); more || err != nil {
		t.Fatalf("terminal timeout was not one-shot: more=%v err=%v", more, err)
	}
}

func TestExternalProbeHonorsCanceledContextBeforeLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runExternalProbe(ctx, t.TempDir(), os.Environ(), "definitely-not-a-real-executable")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled probe error=%v", err)
	}
}

func TestStdioInitializeIncludesHostTarget(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "target")
	plugin := filepath.Join(dir, "plugin.py")
	code := fmt.Sprintf("import json,sys\nr=json.loads(sys.stdin.readline()); p=r['params']; open(%q,'w').write(p['host']['target']); print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':{'protocol':p['protocol'],'module':p['module'],'version':p['version'],'definitionDigest':p['definitionDigest']}}),flush=True)\n", marker)
	if err := os.WriteFile(plugin, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := decodeExternalModuleDefinition(filepath.Join(dir, "module.sos.toml"), []byte(fmt.Sprintf(`schema=1
[module]
path="test/target"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["python3", %q]
[capabilities]
process=true
`, plugin)))
	if err != nil {
		t.Fatal(err)
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	client, err := definition.startStdio(context.Background(), Options{Dir: dir, Config: &policy, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	client.terminate()
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != currentExternalTarget() {
		t.Fatalf("initialize host target=%q err=%v", data, err)
	}
}

func TestCanceledContextDoesNotLaunchExternalProcesses(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	policy := sosconfig.Effective{ExternalProcess: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	commandDefinition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(fmt.Sprintf(`schema=1
[module]
path="test/canceled-command"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="run"
[action.command]
program="sh"
arguments=["-c", %q]
`, "touch "+marker)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandDefinition.runCommand(ctx, Options{Dir: t.TempDir(), Config: &policy}, commandDefinition.Actions[0], nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command error=%v", err)
	}
	stdioDefinition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(fmt.Sprintf(`schema=1
[module]
path="test/canceled-stdio"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["sh", "-c", %q]
[capabilities]
process=true
`, "touch "+marker)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdioDefinition.startStdio(ctx, Options{Dir: t.TempDir(), Config: &policy}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stdio error=%v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pre-canceled context launched process: %v", err)
	}
}

func TestOrdinaryCommandJSONLinesAcceptsLargeLine(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/large-line"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[action]]
name="run"
[action.result]
type="list of text"
[action.command]
program="python3"
arguments=["-c", "import json; print(json.dumps('x' * 70000))"]
stdout="json-lines"
`))
	if err != nil {
		t.Fatal(err)
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	value, err := definition.runCommand(context.Background(), Options{Dir: t.TempDir(), Config: &policy}, definition.Actions[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	items := value.([]any)
	if len(items) != 1 || len(items[0].(string)) != 70000 {
		t.Fatalf("large JSON line result shape=%d", len(items))
	}
}

func TestStdioStreamCancellationDeadlineKillsUnresponsivePlugin(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	dir := t.TempDir()
	plugin := filepath.Join(dir, "plugin.py")
	if err := os.WriteFile(plugin, []byte(`import json, sys, time
init = json.loads(sys.stdin.readline())
p = init["params"]
print(json.dumps({"jsonrpc":"2.0","id":init["id"],"result":{"protocol":p["protocol"],"module":p["module"],"version":p["version"],"definitionDigest":p["definitionDigest"]}}), flush=True)
opened = json.loads(sys.stdin.readline())
print(json.dumps({"jsonrpc":"2.0","id":opened["id"],"result":{"streamId":"stuck","itemType":"text"}}), flush=True)
sys.stdin.readline()
time.sleep(60)
`), 0o644); err != nil {
		t.Fatal(err)
	}
	definition, err := decodeExternalModuleDefinition(filepath.Join(dir, "module.sos.toml"), []byte(fmt.Sprintf(`schema=1
[module]
path="test/stuck"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["python3", %q]
shutdown_timeout="50ms"
[capabilities]
process=true
[[action]]
name="follow"
[action.result]
type="stream of text"
`, plugin)))
	if err != nil {
		t.Fatal(err)
	}
	policy := sosconfig.Effective{ExternalProcess: true}
	session := newExternalSessionKey()
	source, err := definition.openStdioStream(context.Background(), Options{Dir: dir, Config: &policy, Stderr: io.Discard, externalSession: session}, definition.Actions[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = source.cancel(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("unresponsive plugin cancellation took %v", time.Since(started))
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

func TestExternalStreamingActionRetainsDeclarationMetadata(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/streams"
version="1.0.0"
[runtime]
kind="stdio"
protocol="sos-plugin/1"
command=["plugin"]
[capabilities]
process=true
[[action]]
name="follow"
[action.result]
type="stream of text"
`))
	if err != nil {
		t.Fatal(err)
	}
	module, err := definition.module()
	if err != nil {
		t.Fatal(err)
	}
	decl, err := parseActionDecl(module.Actions["follow"].Text)
	if err != nil || !decl.Streaming || decl.StreamItem.Name != "text" {
		t.Fatalf("external streaming declaration = %+v, %v", decl, err)
	}
}

func TestExternalStreamOpenValidatesArgumentsAndTargetsBeforeLaunch(t *testing.T) {
	definition, err := decodeExternalModuleDefinition("module.sos.toml", []byte(`schema=1
[module]
path="test/validation"
version="1.0.0"
[runtime]
kind="command"
[capabilities]
process=true
[[type]]
name="Filter"
[[type.field]]
name="name"
type="text"
[[action]]
name="follow"
targets=["never-this-target"]
[[action.parameter]]
name="filter"
type="Filter"
[action.result]
type="stream of text"
[action.command]
program="must-not-launch"
stdout="json-lines"
`))
	if err != nil {
		t.Fatal(err)
	}
	module, err := definition.module()
	if err != nil {
		t.Fatal(err)
	}
	runner := &runtime{ctx: context.Background(), env: map[string]any{"bad": map[string]any{"name": "ok", "extra": true}}, imports: map[string]*Module{"ext": module}}
	if _, err := runner.openExternalStream("ext.follow", "events", 1, []string{"bad"}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("target restriction was not enforced before launch: %v", err)
	}
	definition.Actions[0].Targets = nil
	module, err = definition.module()
	if err != nil {
		t.Fatal(err)
	}
	runner.imports["ext"] = module
	if _, err := runner.openExternalStream("ext.follow", "events", 1, []string{"bad"}); err == nil || !strings.Contains(err.Error(), "argument filter must be Filter") {
		t.Fatalf("record argument was not safely validated before launch: %v", err)
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
