package studio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"github.com/DonaldMurillo/system-one-playground/sos"
	"unicode/utf8"
)

type projectRequest struct {
	Action   string `json:"action"`
	Path     string `json:"path"`
	Source   string `json:"source"`
	Revision string `json:"revision"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	Mode     string `json:"mode"`
}

// projectPath refuses traversal, hidden files, and symlinks. os.Root bounds all
// actual file I/O even if filesystem entries change after validation.
func projectPath(root *os.Root, name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || !filepath.IsLocal(name) {
		return "", fmt.Errorf("a project-relative file path is required")
	}
	parts := strings.Split(name, string(filepath.Separator))
	prefix := ""
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("hidden files belong in environment/settings, not the editor")
		}
		prefix = filepath.Join(prefix, part)
		st, err := root.Lstat(prefix)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlinks are not editable project files")
		}
	}
	return name, nil
}

func (s *Server) sourceFilename(name string) (string, error) {
	if name == "" {
		return filepath.Join(s.Dir(), "buffer.sos"), nil
	}
	dir := s.Dir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name, err = projectPath(root, name)
	if err != nil {
		return "", err
	}
	if filepath.Ext(name) != ".sos" {
		return "", fmt.Errorf("select a .sos file for language operations")
	}
	return filepath.Join(dir, name), nil
}

func hashSource(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func readProjectFile(root *os.Root, name string) ([]byte, error) {
	// Reject special files before opening: opening a FIFO for reading can block
	// indefinitely and hold the project's mutation lock.
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
	if len(b) > maxSourceBytes {
		return nil, fmt.Errorf("file exceeds 1 MiB")
	}
	return b, err
}
func atomicProjectWrite(root *os.Root, name string, b []byte, mode fs.FileMode) error {
	// Random temporary files avoid clobbering another writer's staging file.
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(name), ".sysone-"+hex.EncodeToString(suffix))
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return root.Rename(temp, name)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func projectEnvironment(root *os.Root) (map[string]string, error) {
	out := map[string]string{}
	st, err := root.Lstat(".env")
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf(".env must not be a symlink")
	}
	b, err := readProjectFile(root, ".env")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(strings.TrimPrefix(name, "export "))
		if !ok || !envName.MatchString(name) {
			return nil, fmt.Errorf("unsupported .env syntax; use NAME=value")
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			var decoded string
			if err := json.Unmarshal([]byte(value), &decoded); err != nil {
				return nil, fmt.Errorf("invalid quoted environment value")
			}
			value = decoded
		} else {
			value = strings.Trim(value, "'")
		}
		out[name] = value
	}
	return out, nil
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method", "POST required")
		return
	}
	var req projectRequest
	if !decodeBody(w, r, &req) {
		return
	}
	s.projectMu.Lock()
	defer s.projectMu.Unlock()
	if req.Action == "openProject" {
		if !filepath.IsAbs(req.Path) {
			writeError(w, 400, "path", "choose an absolute project directory")
			return
		}
		if err := s.SetDir(req.Path); err != nil {
			writeError(w, 400, "project", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"root": s.Dir()})
		return
	}
	if req.Action == "write" || req.Action == "mkdir" || req.Action == "setEnvironment" {
		s.mu.Lock()
		busy := s.cancel != nil || s.analysisCancel != nil
		s.mu.Unlock()
		if busy {
			writeError(w, 409, "busy", "stop active Run or Analyze before modifying the project")
			return
		}
	}
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		writeError(w, 400, "project", err.Error())
		return
	}
	defer root.Close()
	fail := func(err error) { writeError(w, 400, "project", err.Error()) }
	switch req.Action {
	case "tree":
		files := []map[string]any{}
		count := 0
		err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if path == "." {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "vendor" || d.Name() == "build" {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			count++
			if count > 4000 {
				return fmt.Errorf("project tree exceeds 4000 entries")
			}
			files = append(files, map[string]any{"path": filepath.ToSlash(path), "directory": d.IsDir()})
			return nil
		})
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"root": s.Dir(), "files": files})
	case "read", "write", "mkdir":
		name, e := projectPath(root, req.Path)
		if e != nil {
			fail(e)
			return
		}
		if req.Action == "mkdir" {
			if e = root.MkdirAll(name, 0755); e != nil {
				fail(e)
				return
			}
			writeJSON(w, 200, map[string]any{"path": req.Path})
			return
		}
		b, e := readProjectFile(root, name)
		if req.Action == "read" {
			if e != nil {
				fail(e)
				return
			}
			if !utf8.Valid(b) {
				fail(fmt.Errorf("file is not UTF-8 text"))
				return
			}
			writeJSON(w, 200, map[string]any{"path": req.Path, "source": string(b), "revision": hashSource(b)})
			return
		}
		if len(req.Source) > maxSourceBytes {
			fail(fmt.Errorf("file exceeds 1 MiB"))
			return
		}
		if e != nil && !os.IsNotExist(e) {
			fail(e)
			return
		}
		if (e == nil && req.Revision != hashSource(b)) || (os.IsNotExist(e) && req.Revision != "") {
			writeError(w, 409, "conflict", "file changed on disk; reopen before saving")
			return
		}
		mode := fs.FileMode(0644)
		if st, e := root.Stat(name); e == nil {
			mode = st.Mode().Perm()
		}
		if e = atomicProjectWrite(root, name, []byte(req.Source), mode); e != nil {
			fail(e)
			return
		}
		s.mu.Lock()
		s.analysisCache = nil
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"path": req.Path, "revision": hashSource([]byte(req.Source))})
	case "environment", "setEnvironment":
		values, e := projectEnvironment(root)
		if e != nil {
			fail(e)
			return
		}
		if req.Action == "setEnvironment" {
			if !envName.MatchString(req.Name) || strings.ContainsAny(req.Value, "\x00\r\n") {
				fail(fmt.Errorf("use a valid environment name and single-line value"))
				return
			}
			values[req.Name] = req.Value
			names := []string{}
			for name := range values {
				names = append(names, name)
			}
			sort.Strings(names)
			var data strings.Builder
			for _, name := range names {
				v, _ := json.Marshal(values[name])
				fmt.Fprintf(&data, "%s=%s\n", name, v)
			}
			if data.Len() > maxSourceBytes {
				fail(fmt.Errorf("environment file exceeds 1 MiB"))
				return
			}
			if e = atomicProjectWrite(root, ".env", []byte(data.String()), 0600); e != nil {
				fail(e)
				return
			}
			s.mu.Lock()
			s.analysisCache = nil
			s.mu.Unlock()
		}
		entries := []map[string]any{}
		names := map[string]bool{"TYPESAFE_API_KEY": true, "TYPESAFE_DEFAULT_MODEL": true}
		for name := range values {
			names[name] = true
		}
		ordered := []string{}
		for name := range names {
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		for _, name := range ordered {
			value, present := values[name]
			origin := "project"
			if !present {
				value = os.Getenv(name)
				origin = "process"
			}
			entries = append(entries, map[string]any{"name": name, "configured": value != "", "origin": origin})
		}
		writeJSON(w, 200, map[string]any{"variables": entries})
	case "settings":
		mode := "examples"
		if b, e := root.ReadFile(".sysone-studio.json"); e == nil {
			var setting map[string]string
			if json.Unmarshal(b, &setting) == nil && setting["mode"] == "studio" {
				mode = "studio"
			}
		}
		if req.Mode != "" {
			if req.Mode != "examples" && req.Mode != "studio" {
				fail(fmt.Errorf("mode must be examples or studio"))
				return
			}
			mode = req.Mode
			b, _ := json.Marshal(map[string]string{"mode": mode})
			if e := atomicProjectWrite(root, ".sysone-studio.json", b, 0600); e != nil {
				fail(e)
				return
			}
		}
		writeJSON(w, 200, map[string]any{"mode": mode})
	default:
		fail(fmt.Errorf("unknown project action"))
	}
}

func projectContext(ctx context.Context, dir string) (context.Context, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	values, err := projectEnvironment(root)
	if err != nil {
		return nil, err
	}
	return sos.WithEnvironment(ctx, values), nil
}
