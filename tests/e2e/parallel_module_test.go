package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Runs through the Studio HTTP boundary so `go test -race` instruments the
// runtime as well as the test. Imported nested declarations must stay local
// even when several worker invocations enter the same package simultaneously.
func TestParallelImportedDeclarationsAreIsolated(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "helper"), 0755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "helper"), "main.sos", `package helper
export work
to work with value:
  to nested with n:
    return n
  call nested with value called result
  return result
`)
	source := `import "./helper"
make items [1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16]
map each item in items with at most 8 running called results:
  call helper.work with item called result
  return result
show results
`
	writeScript(t, dir, "main.sos", source)
	api := newWorkspaceAPI(t, dir)
	for attempt := 0; attempt < 3; attempt++ {
		result := api.call("/api/run", map[string]any{"path": "main.sos", "source": source}, 200)
		if result["ok"] != true {
			t.Fatalf("run failed: %#v", result)
		}
		var values []int
		if err := json.Unmarshal([]byte(result["output"].(string)), &values); err != nil {
			t.Fatal(err)
		}
		if len(values) != 16 {
			t.Fatalf("wrong result: %v", values)
		}
		for i, v := range values {
			if v != i+1 {
				t.Fatalf("unordered or contaminated result: %v", values)
			}
		}
	}
}
