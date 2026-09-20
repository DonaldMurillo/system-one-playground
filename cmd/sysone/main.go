// Command sysone is the language and project automation entry point.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/internal/studio"
)

const usage = `usage: sysone [--project DIR] COMMAND [arguments]

Language: run, check, build, fmt, explain, config, vocabulary, lsp, version
  These preserve the sos command arguments and output.
Workspace (JSON output):
  tree
  read PATH
  write PATH [--revision SHA256]      source from stdin; revision required to replace
  mkdir PATH
  env list
  env set NAME                       value from stdin, never echoed
  settings [examples|studio]
  analyze PATH                       Studio interpretation response
  api OPERATION                      JSON request from stdin (check/run/analyze/build/lsp/project)
  open [examples|studio]              launch Studio for this project
  mcp                                agent tools over newline JSON-RPC on stdio
`

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	root := "."
	// Global options are parsed only before the command: program arguments are preserved.
	if len(args) > 0 && (args[0] == "--project" || strings.HasPrefix(args[0], "--project=")) {
		if args[0] == "--project" {
			if len(args) < 2 {
				return fail(errors.New("--project requires a directory"))
			}
			root = args[1]
			args = args[2:]
		} else {
			root = strings.TrimPrefix(args[0], "--project=")
			args = args[1:]
		}
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return 0
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return fail(err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fail(errors.New("project must be an existing directory"))
	}
	command, rest := args[0], args[1:]
	switch command {
	case "run", "check", "build", "fmt", "explain", "config", "vocabulary", "lsp", "version":
		return delegate(root, "sos", args)
	}
	srv, err := studio.New(studio.Options{Dir: root})
	if err != nil {
		return fail(err)
	}
	if command == "mcp" {
		if len(rest) != 0 {
			return fail(errors.New("mcp takes no arguments"))
		}
		return serveMCP(srv, os.Stdin, os.Stdout)
	}
	body := map[string]any{}
	endpoint := "project"
	switch command {
	case "tree":
		if len(rest) != 0 {
			return fail(errors.New("tree takes no arguments"))
		}
		body["action"] = "tree"
	case "read", "mkdir", "analyze":
		if len(rest) != 1 {
			return fail(fmt.Errorf("%s requires PATH", command))
		}
		body["path"] = rest[0]
		if command == "analyze" {
			endpoint = "analyze"
			read, bad, e := invoke(srv, "project", map[string]any{"action": "read", "path": rest[0]})
			if e != nil {
				return fail(e)
			}
			if bad {
				json.NewEncoder(os.Stdout).Encode(read)
				return 1
			}
			body["source"] = read["source"]
		} else {
			body["action"] = command
		}
	case "write":
		if len(rest) != 1 && !(len(rest) == 3 && rest[1] == "--revision") {
			return fail(errors.New("write PATH [--revision SHA256] reads source from stdin"))
		}
		body["action"] = "write"
		body["path"] = rest[0]
		body["revision"] = ""
		if len(rest) == 3 {
			body["revision"] = rest[2]
		}
		data, e := readInput()
		if e != nil {
			return fail(e)
		}
		body["source"] = string(data)
	case "env":
		if len(rest) == 1 && rest[0] == "list" {
			body["action"] = "environment"
		} else if len(rest) == 2 && rest[0] == "set" {
			data, e := readInput()
			if e != nil {
				return fail(e)
			}
			body["action"] = "setEnvironment"
			body["name"] = rest[1]
			body["value"] = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		} else {
			return fail(errors.New("env list | env set NAME (value from stdin)"))
		}
	case "settings", "open":
		if len(rest) > 1 {
			return fail(errors.New("expected examples or studio"))
		}
		body["action"] = "settings"
		if len(rest) == 1 {
			if rest[0] != "examples" && rest[0] != "studio" {
				return fail(errors.New("mode must be examples or studio"))
			}
			body["mode"] = rest[0]
		}
	case "api":
		if len(rest) != 1 || !allowedEndpoint(rest[0]) {
			return fail(errors.New("api requires check, run, analyze, build, lsp, or project"))
		}
		endpoint = rest[0]
		data, e := readInput()
		if e != nil {
			return fail(e)
		}
		if e = json.Unmarshal(data, &body); e != nil || body == nil {
			return fail(errors.New("api input must be a JSON object"))
		}
	default:
		return fail(fmt.Errorf("unknown command %q; see sysone help", command))
	}
	result, bad, err := invoke(srv, endpoint, body)
	if err != nil {
		return fail(err)
	}
	if command == "open" {
		if bad {
			json.NewEncoder(os.Stdout).Encode(result)
			return 1
		}
		return delegate(root, "sos-studio", []string{"-dir", root})
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return fail(err)
	}
	if bad {
		return 1
	}
	return 0
}

func allowedEndpoint(s string) bool {
	switch s {
	case "project", "check", "run", "analyze", "build", "lsp":
		return true
	}
	return false
}
func readInput() ([]byte, error) {
	data, e := io.ReadAll(io.LimitReader(os.Stdin, 2*1024*1024+1))
	if len(data) > 2*1024*1024 {
		return nil, errors.New("input exceeds 2 MiB")
	}
	return data, e
}
func fail(e error) int { fmt.Fprintln(os.Stderr, "sysone:", e); return 1 }
func delegate(root, name string, args []string) int {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable, _ := os.Executable()
	binary := filepath.Join(filepath.Dir(executable), name)
	if _, err := os.Stat(binary); err != nil {
		var e error
		binary, e = exec.LookPath(name)
		if e != nil {
			return fail(fmt.Errorf("%s executable missing; install it alongside sysone or on PATH", name))
		}
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return fail(err)
	}
	return 0
}

// invoke shares Studio's authenticated boundary without opening a network listener.
func invoke(srv *studio.Server, endpoint string, body map[string]any) (map[string]any, bool, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, true, err
	}
	req := httptest.NewRequest("POST", "http://localhost/api/"+endpoint, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Studio-Token", srv.Token())
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, req)
	var result map[string]any
	if err = json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		return nil, true, fmt.Errorf("Studio %s returned HTTP %d without JSON", endpoint, recorder.Code)
	}
	bad := recorder.Code >= 400 || result["error"] != nil
	if diagnostics, ok := result["diagnostics"].([]any); ok {
		for _, v := range diagnostics {
			if d, ok := v.(map[string]any); ok && (d["severity"] == "error" || d["severity"] == float64(1)) {
				bad = true
			}
		}
	}
	return result, bad, nil
}
