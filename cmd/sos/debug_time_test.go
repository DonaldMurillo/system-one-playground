package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"testing"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestDebuggerTreatsClientCancellationAsCleanStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !debugRunStoppedByClient(ctx, context.Canceled) || !debugRunStoppedByClient(ctx, errors.Join(context.Canceled, errors.New("stream closed"))) {
		t.Fatal("client cancellation was reported as an application failure")
	}
	if debugRunStoppedByClient(context.Background(), context.Canceled) || debugRunStoppedByClient(ctx, errors.New("invalid script")) {
		t.Fatal("unrelated application failure was suppressed")
	}
}

func newTestDAPServer(out *bytes.Buffer) *dapServer {
	server := &dapServer{in: bufio.NewReader(strings.NewReader("")), out: out, errOut: io.Discard, seq: 1}
	server.session = newDebugSession(server)
	return server
}

func dapResponseBody(t *testing.T, out *bytes.Buffer, seq int) map[string]any {
	data := out.Bytes()
	for {
		start := bytes.IndexByte(data, '{')
		if start < 0 {
			break
		}
		reader := bytes.NewReader(data[start:])
		dec := json.NewDecoder(reader)
		var message map[string]any
		if err := dec.Decode(&message); err != nil {
			break
		}
		consumed := (len(data) - start) - reader.Len()
		data = data[start+consumed:]
		if message["request_seq"] == float64(seq) {
			return message
		}
	}
	t.Fatalf("no response for request %d", seq)
	return nil
}

func TestDebugCustomRequestsExposeTimeObservability(t *testing.T) {
	var out bytes.Buffer
	server := newTestDAPServer(&out)
	if err := server.handle(dapRequest{Seq: 1, Command: "sos/capabilities"}); err != nil {
		t.Fatal(err)
	}
	caps := dapResponseBody(t, &out, 1)["body"].(map[string]any)
	if caps["calendarSchedules"] == nil || caps["clocks"] == nil {
		t.Fatalf("capability diagnostics incomplete: %#v", caps)
	}

	if err := server.handle(dapRequest{Seq: 2, Command: "sos/streams"}); err != nil {
		t.Fatal(err)
	}
	body := dapResponseBody(t, &out, 2)["body"].(map[string]any)
	streams, _ := body["streams"].([]any)
	if streams == nil || len(streams) != 0 {
		t.Fatalf("streams before launch must be an empty list, got %#v", body["streams"])
	}
}

func TestDebugStopStreamTargetsOneStreamOnlyWithActiveRun(t *testing.T) {
	var out bytes.Buffer
	server := newTestDAPServer(&out)
	err := server.handle(dapRequest{Seq: 1, Command: "sos/stopStream", Arguments: json.RawMessage(`{"id":"s1"}`)})
	if err == nil || !strings.Contains(err.Error(), "no run is active") {
		t.Fatalf("stop without a run must fail clearly, got %v", err)
	}

	// With a live controller an unknown id is a targeted miss; the adapter
	// reports it instead of stopping anything else.
	server.streamMu.Lock()
	server.streamCtl = sos.NewStreamController()
	server.streamMu.Unlock()
	err = server.handle(dapRequest{Seq: 2, Command: "sos/stopStream", Arguments: json.RawMessage(`{"id":"missing"}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown stream missing") {
		t.Fatalf("unknown stream stop must report the target, got %v", err)
	}
	if err := server.handle(dapRequest{Seq: 3, Command: "sos/stopStream", Arguments: json.RawMessage(`{"id":"  "}`)}); err == nil {
		t.Fatal("blank stream id must be rejected")
	}
}
