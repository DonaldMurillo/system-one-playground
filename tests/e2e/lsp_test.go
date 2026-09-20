package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/DonaldMurillo/system-one-playground/internal/soslsp"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

const testURI = "file:///tmp/triage.sos"

const invalidSource = "make\nfrobnicate the widget wildly\n"

// Canonical sentence-grammar constructions from the language sketch.
const validSource = "command triage:\n  argument source as folder\n  make reports as empty list\n  show reports as table\n"

func encode(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return frameBody(body)
}

func frameBody(body []byte) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	return buf.Bytes()
}

func request(id any, method string, params any) []byte {
	return encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func notification(method string, params any) []byte {
	return encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// runServer feeds input to Serve and returns every framed output message in
// order plus the Serve return value.
func runServer(t *testing.T, input []byte) ([]map[string]any, error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srvErr := make(chan error, 1)
	go func() {
		err := soslsp.Serve(inR, outW)
		outW.Close()
		srvErr <- err
	}()
	go func() {
		inW.Write(input)
		inW.Close()
	}()
	raw := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(outR)
		outR.Close()
		raw <- b
	}()
	err := <-srvErr
	messages := parseFrames(t, <-raw)
	return messages, err
}

func parseFrames(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	r := bufio.NewReader(bytes.NewReader(data))
	for {
		if _, err := r.Peek(1); err == io.EOF {
			break
		}
		contentLength := -1
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("truncated header: %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if i := strings.IndexByte(line, ':'); i >= 0 && strings.EqualFold(strings.TrimSpace(line[:i]), "content-length") {
				fmt.Sscanf(strings.TrimSpace(line[i+1:]), "%d", &contentLength)
			}
		}
		if contentLength < 0 {
			t.Fatalf("frame without Content-Length in output: %q", data)
		}
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatalf("truncated body: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("unmarshalable output frame %q: %v", body, err)
		}
		out = append(out, m)
	}
	return out
}

func getMap(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("message %v has no %q object", m, key)
	}
	return v
}

func TestServeProtocolSession(t *testing.T) {
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{"capabilities": map[string]any{}}))
	input.Write(notification("initialized", map[string]any{}))
	input.Write(notification("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": testURI, "languageId": "sos", "version": 1, "text": invalidSource},
	}))
	input.Write(notification("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": testURI, "version": 3},
		"contentChanges": []map[string]any{{"text": validSource}},
	}))
	// Stale edit: version 2 arrives after version 3 and must be ignored.
	input.Write(notification("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": testURI, "version": 2},
		"contentChanges": []map[string]any{{"text": invalidSource}},
	}))
	input.Write(request(2, "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": testURI}, "position": map[string]any{"line": 0, "character": 0},
	}))
	input.Write(request(3, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": testURI}, "position": map[string]any{"line": 2, "character": 5},
	}))
	input.Write(request(4, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": testURI}, "position": map[string]any{"line": 3, "character": 7},
	}))
	input.Write(request(5, "textDocument/formatting", map[string]any{
		"textDocument": map[string]any{"uri": testURI}, "options": map[string]any{"tabSize": 2, "insertSpaces": true},
	}))
	input.Write(frameBody([]byte("{not json")))
	input.Write(request(6, "textDocument/unknown", map[string]any{}))
	input.Write(notification("some/unknownNotification", map[string]any{}))
	input.Write(request(7, "shutdown", nil))
	input.Write(notification("exit", nil))

	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	// Responses and publish notifications, in server processing order.
	if len(messages) != 10 {
		t.Fatalf("got %d messages, want 10: %+v", len(messages), messages)
	}

	// initialize
	if got := messages[0]["id"]; got != float64(1) {
		t.Fatalf("first message id = %v, want 1", got)
	}
	caps := getMap(t, getMap(t, messages[0], "result"), "capabilities")
	if caps["hoverProvider"] != true || caps["definitionProvider"] != true || caps["documentFormattingProvider"] != true {
		t.Fatalf("missing capabilities: %+v", caps)
	}
	sync := getMap(t, caps, "textDocumentSync")
	if sync["change"] != float64(1) || sync["openClose"] != true {
		t.Fatalf("textDocumentSync must advertise full sync: %+v", sync)
	}

	// publishDiagnostics after opening invalid text
	pub1 := messages[1]
	if pub1["method"] != "textDocument/publishDiagnostics" {
		t.Fatalf("message 1 is %v, want publishDiagnostics", pub1["method"])
	}
	p1 := getMap(t, pub1, "params")
	if p1["uri"] != testURI || p1["version"] != float64(1) {
		t.Fatalf("first publish params wrong: %+v", p1)
	}
	diags1, ok := p1["diagnostics"].([]any)
	if !ok || len(diags1) == 0 {
		t.Fatalf("invalid source must produce at least one diagnostic: %+v", p1)
	}
	d1 := getMap(t, diags1[0].(map[string]any), "range")
	if _, ok := d1["start"]; !ok {
		t.Fatalf("diagnostic range missing start: %+v", diags1[0])
	}

	// publishDiagnostics for version 3 with valid text; the stale version 2
	// edit produced no third publish.
	pub2 := messages[2]
	if pub2["method"] != "textDocument/publishDiagnostics" {
		t.Fatalf("message 2 is %v, want publishDiagnostics", pub2["method"])
	}
	p2 := getMap(t, pub2, "params")
	if p2["version"] != float64(3) {
		t.Fatalf("second publish version = %v, want 3", p2["version"])
	}
	if diags, _ := p2["diagnostics"].([]any); len(diags) != 0 {
		t.Fatalf("valid source must be clean, got %+v", diags)
	}

	// completion: every canonical phrase
	comp := messages[3]
	if comp["id"] != float64(2) {
		t.Fatalf("message 3 id = %v, want 2", comp["id"])
	}
	items, ok := comp["result"].([]any)
	if !ok {
		t.Fatalf("completion result must be an array: %+v", comp)
	}
	canonical := map[string]bool{}
	for _, kw := range sos.Keywords() {
		canonical[kw] = true
	}
	if len(items) != len(canonical) {
		t.Fatalf("completion returned %d items, want %d canonical phrases", len(items), len(canonical))
	}
	for _, it := range items {
		item := it.(map[string]any)
		if !canonical[item["label"].(string)] {
			t.Fatalf("non canonical completion label %q", item["label"])
		}
		if item["kind"] != float64(14) {
			t.Fatalf("completion kind = %v, want Keyword(14)", item["kind"])
		}
	}

	// hover over the make statement
	hover := messages[4]
	contents := getMap(t, getMap(t, hover, "result"), "contents")
	value, _ := contents["value"].(string)
	if !strings.Contains(value, "make reports as empty list") {
		t.Fatalf("hover value %q must contain the statement text", value)
	}

	// definition of reports -> the make binding on line index 2
	def := messages[5]
	loc := getMap(t, getMap(t, def, "result"), "range") // works for single location
	_ = loc
	res := getMap(t, def, "result")
	rng := getMap(t, res, "range")
	start := getMap(t, rng, "start")
	end := getMap(t, rng, "end")
	if start["line"] != float64(2) || end["line"] != float64(2) {
		t.Fatalf("definition must land on line 2 (make binding): %+v", rng)
	}
	if start["character"] != float64(7) || end["character"] != float64(14) {
		t.Fatalf("definition must cover \"reports\": %+v", rng)
	}
	if res["uri"] != testURI {
		t.Fatalf("definition uri = %v", res["uri"])
	}

	// formatting: one full text edit matching the core formatter
	fmtMsg := messages[6]
	edits, ok := fmtMsg["result"].([]any)
	if !ok {
		t.Fatalf("formatting result must be an array: %+v", fmtMsg)
	}
	expected, _ := sos.Format(validSource)
	if expected == validSource {
		if len(edits) != 0 {
			t.Fatalf("already formatted source must yield no edits: %+v", edits)
		}
	} else {
		if len(edits) != 1 {
			t.Fatalf("unformatted source must yield one edit: %+v", edits)
		}
		edit := edits[0].(map[string]any)
		if edit["newText"] != expected {
			t.Fatalf("edit text %q must equal core Format output %q", edit["newText"], expected)
		}
	}

	// parse error for the malformed frame
	parseErr := messages[7]
	if parseErr["id"] != nil {
		t.Fatalf("parse error response id must be null: %+v", parseErr)
	}
	if getMap(t, parseErr, "error")["code"] != float64(-32700) {
		t.Fatalf("malformed JSON must yield -32700: %+v", parseErr)
	}

	// unknown request -> -32601
	unknown := messages[8]
	if getMap(t, unknown, "error")["code"] != float64(-32601) {
		t.Fatalf("unknown method must yield -32601: %+v", unknown)
	}

	// shutdown -> null result
	shutdown := messages[9]
	if shutdown["id"] != float64(7) || shutdown["result"] != nil {
		t.Fatalf("shutdown must return null result: %+v", shutdown)
	}
	if _, hasErr := shutdown["error"]; hasErr {
		t.Fatalf("shutdown must not be an error: %+v", shutdown)
	}
}

func TestServeCompletionFiltersByPrefix(t *testing.T) {
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{}))
	input.Write(notification("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": testURI, "languageId": "sos", "version": 1, "text": validSource},
	}))
	input.Write(request(2, "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": testURI}, "position": map[string]any{"line": 2, "character": 6},
	}))
	input.Write(request(3, "shutdown", nil))
	input.Write(notification("exit", nil))

	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(messages))
	}
	items, ok := messages[2]["result"].([]any)
	if !ok {
		t.Fatalf("completion result must be an array: %+v", messages[2])
	}
	// Two indentation spaces plus four letters place the cursor after "make".
	for _, it := range items {
		label := it.(map[string]any)["label"].(string)
		if !strings.HasPrefix(strings.ToLower(label), "make") {
			t.Fatalf("completion label %q does not match typed prefix", label)
		}
	}
	if len(items) == 0 {
		t.Skipf("core keywords contain no phrase starting with \"make\"; prefix filter exercised with empty result")
	}
}

func TestServeColorsAndHintsInterpolationExpressions(t *testing.T) {
	source := "make total 2\nshow \"Value {total plus 1 times 2}\"\n"
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{}))
	input.Write(notification("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": testURI, "languageId": "sos", "version": 1, "text": source},
	}))
	input.Write(request(2, "textDocument/semanticTokens/full", map[string]any{"textDocument": map[string]any{"uri": testURI}}))
	input.Write(request(3, "textDocument/inlayHint", map[string]any{"textDocument": map[string]any{"uri": testURI}}))
	input.Write(request(4, "shutdown", nil))
	input.Write(notification("exit", nil))

	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	byID := func(id float64) map[string]any {
		for _, message := range messages {
			if message["id"] == id {
				return message
			}
		}
		t.Fatalf("response %v missing: %+v", id, messages)
		return nil
	}
	data := getMap(t, byID(2), "result")["data"].([]any)
	type key struct{ line, start, kind int }
	seen := map[key]bool{}
	line, start := 0, 0
	for i := 0; i < len(data); i += 5 {
		deltaLine, deltaStart := int(data[i].(float64)), int(data[i+1].(float64))
		line += deltaLine
		if deltaLine == 0 {
			start += deltaStart
		} else {
			start = deltaStart
		}
		seen[key{line, start, int(data[i+3].(float64))}] = true
	}
	for _, want := range []key{{1, 13, 1}, {1, 19, 11}, {1, 24, 7}, {1, 26, 11}, {1, 32, 7}} {
		if !seen[want] {
			t.Fatalf("semantic token %+v missing from %v", want, seen)
		}
	}
	hints := byID(3)["result"].([]any)
	found := false
	for _, raw := range hints {
		hint := raw.(map[string]any)
		position := hint["position"].(map[string]any)
		if position["line"] == float64(1) && position["character"] == float64(18) && hint["label"] == ": number" {
			found = true
		}
	}
	if !found {
		t.Fatalf("interpolation type hint missing: %+v", hints)
	}
}

func TestServeDidCloseClearsDiagnostics(t *testing.T) {
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{}))
	input.Write(notification("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": testURI, "languageId": "sos", "version": 1, "text": invalidSource},
	}))
	input.Write(notification("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": testURI},
	}))
	input.Write(request(2, "shutdown", nil))
	input.Write(notification("exit", nil))

	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(messages))
	}
	clear := messages[2]
	if clear["method"] != "textDocument/publishDiagnostics" {
		t.Fatalf("didClose must publish cleared diagnostics: %+v", clear)
	}
	params := getMap(t, clear, "params")
	if diags, _ := params["diagnostics"].([]any); len(diags) != 0 {
		t.Fatalf("didClose must clear diagnostics: %+v", params)
	}
}

func TestServeRejectsOversizedFrame(t *testing.T) {
	input := []byte("Content-Length: 99999999\r\n\r\n")
	_, err := runServer(t, input)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized frame must fail with frame limit error, got %v", err)
	}
}

func TestServeRejectsFrameWithoutContentLength(t *testing.T) {
	input := []byte("Content-Type: application/vscode-jsonrpc\r\n\r\n{}")
	_, err := runServer(t, input)
	if err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("frame without Content-Length must fail, got %v", err)
	}
}

func TestServeExitBeforeShutdownIsError(t *testing.T) {
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{}))
	input.Write(notification("exit", nil))
	_, err := runServer(t, input.Bytes())
	if err == nil || !strings.Contains(err.Error(), "before shutdown") {
		t.Fatalf("exit before shutdown must error, got %v", err)
	}
}

func TestServeEOFWithoutExitReturnsNil(t *testing.T) {
	var input bytes.Buffer
	input.Write(request(1, "initialize", map[string]any{}))
	messages, err := runServer(t, input.Bytes())
	if err != nil {
		t.Fatalf("closed input must end Serve cleanly: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want the initialize response only", len(messages))
	}
}
