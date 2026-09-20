package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDebugAdapterBreakpointsLocalsAndContinue(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", "make total 2\nassign total total + 3\nshow total\n")
	cmd := exec.Command(sosBin, "debug")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	messages := make(chan map[string]any, 32)
	readErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			message, err := readDAPMessage(reader)
			if err != nil {
				readErr <- err
				return
			}
			messages <- message
		}
	}()
	seq := 0
	send := func(command string, arguments any) {
		seq++
		payload, _ := json.Marshal(map[string]any{"type": "request", "seq": seq, "command": command, "arguments": arguments})
		if _, err := fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n%s", len(payload), payload); err != nil {
			t.Fatal(err)
		}
	}
	next := func(predicate func(map[string]any) bool) map[string]any {
		t.Helper()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case message := <-messages:
				if predicate(message) {
					return message
				}
			case err := <-readErr:
				t.Fatalf("DAP stream ended: %v", err)
			case <-timer.C:
				t.Fatal("timed out waiting for DAP message")
			}
		}
	}

	send("initialize", map[string]any{"clientID": "e2e", "adapterID": "sysonescript"})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "initialize"
	})
	send("launch", map[string]any{"program": script, "cwd": dir})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "launch"
	})
	send("setBreakpoints", map[string]any{"source": map[string]any{"path": script}, "breakpoints": []any{map[string]any{"line": 2}}})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "setBreakpoints"
	})
	send("configurationDone", map[string]any{})
	next(func(message map[string]any) bool { return message["type"] == "event" && message["event"] == "stopped" })

	send("scopes", map[string]any{"frameId": 1})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "scopes"
	})
	send("variables", map[string]any{"variablesReference": 1})
	variables := next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "variables"
	})
	encoded, _ := json.Marshal(variables)
	if !strings.Contains(string(encoded), "total") || !strings.Contains(string(encoded), "2") {
		t.Fatalf("locals did not expose total=2: %s", encoded)
	}
	send("evaluate", map[string]any{"frameId": 1, "expression": "total", "context": "hover"})
	evaluated := next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "evaluate"
	})
	if body, _ := evaluated["body"].(map[string]any); body["result"] != "2" {
		t.Fatalf("evaluate result = %#v", evaluated)
	}
	send("continue", map[string]any{"threadId": 1})
	next(func(message map[string]any) bool {
		return message["type"] == "event" && message["event"] == "terminated"
	})
}

func TestDebugAdapterStepInAndLogpoint(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "main.sos", "to double with value:\n  return value * 2\n\nmake total 2\ncall double with total called doubled\nshow doubled\n")
	cmd := exec.Command(sosBin, "debug")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	messages := make(chan map[string]any, 32)
	readErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			message, err := readDAPMessage(reader)
			if err != nil {
				readErr <- err
				return
			}
			messages <- message
		}
	}()
	seq := 0
	send := func(command string, arguments any) {
		seq++
		payload, _ := json.Marshal(map[string]any{"type": "request", "seq": seq, "command": command, "arguments": arguments})
		if _, err := fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n%s", len(payload), payload); err != nil {
			t.Fatal(err)
		}
	}
	next := func(predicate func(map[string]any) bool) map[string]any {
		t.Helper()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case message := <-messages:
				if predicate(message) {
					return message
				}
			case err := <-readErr:
				t.Fatalf("DAP stream ended: %v", err)
			case <-timer.C:
				t.Fatal("timed out waiting for DAP message")
			}
		}
	}

	send("initialize", map[string]any{"clientID": "e2e", "adapterID": "sysonescript"})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "initialize"
	})
	send("launch", map[string]any{"program": script, "cwd": dir})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "launch"
	})
	send("setBreakpoints", map[string]any{"source": map[string]any{"path": script}, "breakpoints": []any{map[string]any{"line": 5}}})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "setBreakpoints"
	})
	send("configurationDone", map[string]any{})
	next(func(message map[string]any) bool { return message["type"] == "event" && message["event"] == "stopped" })

	send("stepIn", map[string]any{"threadId": 1})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "stepIn"
	})
	stopped := next(func(message map[string]any) bool { return message["type"] == "event" && message["event"] == "stopped" })
	if body, _ := stopped["body"].(map[string]any); body["reason"] != "step" {
		t.Fatalf("stepIn stopped with unexpected reason: %s", mustJSON(stopped))
	}

	send("stackTrace", map[string]any{"threadId": 1})
	stack := next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "stackTrace"
	})
	if body, _ := stack["body"].(map[string]any); !strings.Contains(mustJSON(body), `"line":2`) {
		t.Fatalf("stepIn did not enter action body: %s", mustJSON(stack))
	}

	send("setBreakpoints", map[string]any{"source": map[string]any{"path": script}, "breakpoints": []any{map[string]any{"line": 6, "logMessage": "doubled={doubled}"}}})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "setBreakpoints"
	})
	send("continue", map[string]any{"threadId": 1})
	next(func(message map[string]any) bool {
		return message["type"] == "response" && message["command"] == "continue"
	})
	output := next(func(message map[string]any) bool {
		if message["type"] != "event" || message["event"] != "output" {
			return false
		}
		return strings.Contains(mustJSON(message), "doubled=4")
	})
	if !strings.Contains(mustJSON(output), "doubled=4") {
		t.Fatalf("logpoint output missing: %s", mustJSON(output))
	}
	next(func(message map[string]any) bool {
		return message["type"] == "event" && message["event"] == "terminated"
	})
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func readDAPMessage(reader *bufio.Reader) (map[string]any, error) {
	length := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "Content-Length" {
			return nil, fmt.Errorf("invalid DAP header %q", line)
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &length); err != nil {
			return nil, fmt.Errorf("invalid DAP length %q", value)
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	var message map[string]any
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, err
	}
	return message, nil
}

func TestDebugCommandAppearsInCLIHelp(t *testing.T) {
	stdout, _, code := runCLI(t, t.TempDir(), "help")
	if code != 0 || !strings.Contains(stdout, "debug") {
		t.Fatalf("help missing debug: code=%d output=%s", code, stdout)
	}
}
