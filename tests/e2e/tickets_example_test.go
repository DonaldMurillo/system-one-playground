package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the published example through the real CLI, with all three commands
// passing data through files. The all criterion intentionally needs no provider.
func TestTicketsExampleFilePipeline(t *testing.T) {
	dir := t.TempDir()
	script, err := filepath.Abs("../../examples/sos/tickets/main.sos")
	if err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs("../../examples/sos/tickets/team-tickets.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"run", script, "--", "import", source, "--output", "tickets.json"},
		{"run", script, "--", "triage", "tickets.json", "--criterion", "all", "--output", "open.json"},
		{"run", script, "--", "report", "open.json", "--by", "team", "--output", "reports"},
	} {
		_, stderr, code := runCLI(t, dir, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr)
		}
		if strings.Contains(stderr, "sos: trace") {
			t.Fatalf("unexpected provider call: %s", stderr)
		}
	}
	var tickets []map[string]any
	data, err := os.ReadFile(filepath.Join(dir, "open.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &tickets); err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 3 {
		t.Fatalf("expected three open tickets, got %s", data)
	}
	for i, team := range []string{"billing", "platform"} {
		file := filepath.Join(dir, "reports", []string{"1.json", "2.json"}[i])
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Team  string `json:"team"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		if report.Team != team || report.Count != []int{2, 1}[i] {
			t.Fatalf("unexpected report: %s", data)
		}
	}
	// The example explicitly refuses replacement, preserving the prior report.
	_, stderr, code := runCLI(t, dir, "run", script, "--", "import", source, "--output", "tickets.json")
	if code != 1 {
		t.Fatalf("overwrite exit=%d stderr=%s", code, stderr)
	}
}
