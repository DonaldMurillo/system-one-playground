package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectFolderPicker(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "empty", ".private"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-folder.txt"), []byte("private content"), 0600); err != nil {
		t.Fatal(err)
	}
	api := newWorkspaceAPI(t, root)
	listing := api.call("/api/project", map[string]any{"action": "browseFolders", "path": root}, 200)
	if listing["path"] != root || listing["parent"] != filepath.Dir(root) {
		t.Fatalf("wrong directory navigation: %v", listing)
	}
	folders := listing["folders"].([]any)
	if len(folders) != 2 || folders[0].(map[string]any)["name"] != "alpha" {
		t.Fatalf("wrong visible folders: %v", folders)
	}
	api.call("/api/project", map[string]any{"action": "browseFolders", "path": "relative"}, 400)
	api.call("/api/project", map[string]any{"action": "browseFolders", "path": filepath.Join(root, "not-a-folder.txt")}, 400)
	if api.server.Dir() != root {
		t.Fatal("browsing changed the active project")
	}
	target := filepath.Join(root, "empty")
	api.call("/api/project", map[string]any{"action": "openProject", "path": target}, 200)
	if api.server.Dir() != target {
		t.Fatal("selection did not open the project")
	}
	tree := api.call("/api/project", map[string]any{"action": "tree"}, 200)
	if len(tree["files"].([]any)) != 0 {
		t.Fatal("empty folder should be an empty project")
	}
}
