//go:build !windows && !js && !wasip1

package sos

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
)

func TestCommandStreamCancelKillsTermIgnoringDescendant(t *testing.T) {
	definition, err := decodeExternalModuleDefinition(filepath.Join(t.TempDir(), "module.sos.toml"), []byte(`schema=1
[module]
path="test/process-tree"
version="1.0.0"
[runtime]
kind="command"
shutdown_timeout="200ms"
[capabilities]
process=true
[[action]]
name="events"
[action.result]
type="stream of integer"
[action.command]
program="sh"
arguments=["-c", "trap 'exit 0' TERM; (trap '' TERM; sleep 60) & child=$!; echo $child; wait"]
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
	item, more, err := source.next(context.Background())
	if err != nil || !more {
		t.Fatalf("first item more=%v err=%v", more, err)
	}
	childPID := int(item.(int64))
	if err := source.cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TERM-ignoring descendant %d survived cancellation: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
