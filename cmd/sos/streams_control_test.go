package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

func TestStreamControlClientAuthenticatesPublishesAndAnswersSnapshots(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverFrames := make(chan streamControlFrame, 3)
	serverConn := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		serverConn <- conn
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var frame streamControlFrame
			if json.Unmarshal(scanner.Bytes(), &frame) == nil {
				serverFrames <- frame
				if frame.Type == "hello" {
					_ = json.NewEncoder(conn).Encode(streamControlFrame{Type: "ready", Session: frame.Session})
				}
			}
		}
	}()

	controller := sos.NewStreamController()
	client, err := connectStreamControl(context.Background(), listener.Addr().String(), "secret", "run-1", controller)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	hello := <-serverFrames
	if hello.Type != "hello" || hello.Token != "secret" || hello.Session != "run-1" {
		t.Fatalf("hello=%#v", hello)
	}
	event := sos.StreamEvent{ID: "stream-1", Event: "opened", State: "open", Binding: "events"}
	client.event(event)
	if got := <-serverFrames; got.Event == nil || got.Event.ID != "stream-1" {
		t.Fatalf("event=%#v", got)
	}

	conn := <-serverConn
	if err := json.NewEncoder(conn).Encode(streamControlFrame{Type: "request", ID: "request-1", Method: "streams.snapshot"}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-serverFrames:
		if response.Type != "response" || response.ID != "request-1" || !response.OK {
			t.Fatalf("response=%#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for snapshot response")
	}
}

func TestStreamControlRejectsNonLoopbackAddresses(t *testing.T) {
	_, err := connectStreamControl(context.Background(), "192.0.2.10:1234", "secret", "run-1", sos.NewStreamController())
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunCLIStreamControlStopsOneProducerAndContinues(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	events := make(chan sos.StreamEvent, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		requested := false
		for scanner.Scan() {
			var frame streamControlFrame
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				continue
			}
			if frame.Type == "hello" {
				_ = json.NewEncoder(conn).Encode(streamControlFrame{Type: "ready", Session: frame.Session})
				continue
			}
			if frame.Event != nil {
				events <- *frame.Event
				if frame.Event.Event == "opened" && !requested {
					requested = true
					_ = json.NewEncoder(conn).Encode(streamControlFrame{Type: "request", ID: "stop-1", Method: "stream.stop", Stream: frame.Event.ID})
				}
			}
		}
	}()
	dir, err := filepath.Abs("../../examples/sos/streams")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(dir, ".tooling-stop-*.sos")
	if err != nil {
		t.Fatal(err)
	}
	fileName := file.Name()
	t.Cleanup(func() { _ = os.Remove(fileName) })
	const source = `import "example/streams" as events

stream events.infinite called incoming
for each event from incoming:
  show message of event

show "Continued after tooling stop"
`
	if _, err := file.WriteString(source); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	t.Setenv("SOS_STREAM_TOKEN", "secret")
	code := RunCLI([]string{"run", "--stream-control", listener.Addr().String(), "--stream-session", "test-run", filepath.Base(fileName)}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Continued after tooling stop") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	<-done
	close(events)
	seenOpened, seenStopped := false, false
	for event := range events {
		seenOpened = seenOpened || event.Event == "opened"
		seenStopped = seenStopped || event.Event == "stopped"
	}
	if !seenOpened || !seenStopped {
		t.Fatalf("opened=%v stopped=%v", seenOpened, seenStopped)
	}
}

func TestStreamControlCloseDoesNotWaitForAStalledInspector(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := &streamControlClient{
		conn:       clientConn,
		controller: sos.NewStreamController(),
		events:     make(chan sos.StreamEvent, 4),
		done:       make(chan struct{}),
	}
	go client.writeEvents()
	client.event(sos.StreamEvent{ID: "stream-1", Event: "opened", Reason: strings.Repeat("x", 1<<20)})
	time.Sleep(10 * time.Millisecond)
	closed := make(chan struct{})
	go func() {
		client.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close waited for a peer that was not reading")
	}
}

func TestStreamControlCloseWritesByeFrameWithDroppedCount(t *testing.T) {
	clientConn, serverPipe := net.Pipe()
	defer serverPipe.Close()
	client := &streamControlClient{
		conn:       clientConn,
		controller: sos.NewStreamController(),
		events:     make(chan sos.StreamEvent, 2),
		done:       make(chan struct{}),
	}
	lines := make(chan string, 4)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(serverPipe)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	// No writeEvents goroutine: with the queue capped at two, the third and
	// fourth events take the overflow path and increment the dropped counter.
	for range 4 {
		client.event(sos.StreamEvent{ID: "stream-1", Event: "opened"})
	}
	if got := client.Dropped(); got != 2 {
		t.Fatalf("dropped=%d want 2", got)
	}
	client.close()
	var bye struct {
		Type    string `json:"type"`
		Dropped int64  `json:"dropped"`
	}
	seen := false
	for line := range lines {
		var frame struct {
			Type    string `json:"type"`
			Dropped int64  `json:"dropped"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil {
			continue
		}
		if frame.Type == "bye" {
			bye = frame
			seen = true
		}
	}
	if !seen || bye.Dropped != 2 {
		t.Fatalf("bye frame seen=%v dropped=%d", seen, bye.Dropped)
	}
}

func TestRunCLIRejectsStreamTokenFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"run", "--stream-token", "secret", "script.sos"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "unknown flag --stream-token") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}
