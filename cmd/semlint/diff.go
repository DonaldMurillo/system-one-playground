package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/internal/semcore"
)

type ChangedLines = semcore.ChangedLines
type LineRange = semcore.LineRange

var ParseUnifiedDiff = semcore.ParseUnifiedDiff

// DiffFromGit runs git diff against a ref and parses the result. An empty ref
// means the working tree against HEAD, which is what you want while editing.
func DiffFromGit(ref, dir string) (ChangedLines, error) {
	root, err := gitRoot(dir)
	if err != nil {
		return nil, err
	}
	args := []string{"diff", "--unified=0", "--no-color", "--no-ext-diff"}
	if ref != "" {
		args = append(args, ref)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	changed, err := ParseUnifiedDiff(strings.NewReader(string(out)), root)
	if err != nil {
		return nil, err
	}
	// Uncommitted new files are untracked, so git diff never mentions them.
	// Treat each as changed in full, or a new file would be reviewed by nobody.
	if ref == "" {
		untracked, err := gitUntracked(root)
		if err == nil {
			for _, p := range untracked {
				if !lintable(p) {
					continue
				}
				if n := countLines(p); n > 0 {
					changed[p] = []LineRange{{Start: 1, End: n}}
				}
			}
		}
	}
	return changed, nil
}

func gitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitUntracked(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, filepath.Join(root, l))
		}
	}
	return paths, nil
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		n++
	}
	return n
}

// LoadDiff resolves the -diff flag: a path, or "-" for standard input.
func LoadDiff(path, root string) (ChangedLines, error) {
	if path == "-" {
		return ParseUnifiedDiff(os.Stdin, root)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseUnifiedDiff(f, root)
}
