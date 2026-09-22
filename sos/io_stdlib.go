package sos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ExitError requests a process exit without treating the status as a provider or
// interpreter failure. Embedders decide whether to terminate their process.
type ExitError struct{ Code int }

func (e *ExitError) ExitCode() int { return e.Code }
func (e *ExitError) Error() string { return fmt.Sprintf("script requested exit status %d", e.Code) }

const maxIOBytes = 16 << 20

func ioPath(opts Options, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(opts.Dir, name)
}

func registerIO(module, name string, params []NativeParam, result, description, effect string, fn func(context.Context, Options, []any) (any, error)) {
	if stdRegistry[module] == nil {
		stdRegistry[module] = map[string]NativeOp{}
	}
	targets := []string{"native", "wasip1"}
	if effect == "process" {
		targets = []string{"native"}
	}
	stdRegistry[module][name] = NativeOp{Name: name, Params: params, ContextFn: fn, Targets: targets}
	stdDocs[module+"."+name] = stdDoc{result, description, []string{effect}}
}

func init() {
	registerIO("std/files", "exists", []NativeParam{{"path", "text"}}, "boolean", "Reports whether a path exists; permission and other filesystem errors fail rather than returning false.", "filesystem", func(ctx context.Context, opts Options, args []any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, err := os.Lstat(ioPath(opts, args[0].(string)))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return nil, err
		}
		return true, nil
	})

	registerIO("std/process", "run", []NativeParam{{"executable", "text"}, {"arguments", "list"}}, "record", "Runs an executable without a shell, with a 30-second deadline and 16 MiB per output stream. Returns stdout, stderr and status; nonzero exit status is a value.", "process", runProcess)

	registerIO("std/files", "discover", []NativeParam{{"root", "text"}, {"exclusions", "list"}}, "list", "Recursively lists sorted regular-file paths relative to root. Exclusions match slash-separated relative paths or any path component; symlinks are skipped.", "filesystem", discoverFiles)
	registerIO("std/files", "read", []NativeParam{{"path", "text"}}, "text", "Reads a regular text file (up to 16 MiB), relative to the execution directory.", "filesystem", func(ctx context.Context, opts Options, args []any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := ioPath(opts, args[0].(string))
		initial, err := os.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !initial.Mode().IsRegular() {
			return nil, fmt.Errorf("read requires a regular file, not a symlink or special file")
		}
		file, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("read requires a regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, maxIOBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxIOBytes {
			return nil, fmt.Errorf("file exceeds 16 MiB limit")
		}
		return string(data), ctx.Err()
	})
	registerIO("std/files", "write", []NativeParam{{"path", "text"}, {"text", "text"}}, "text", "Writes text to a regular file, replacing its contents. Parent directories must exist.", "filesystem", func(ctx context.Context, opts Options, args []any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := ioPath(opts, args[0].(string))
		data := args[1].(string)
		if len(data) > maxIOBytes {
			return nil, fmt.Errorf("text exceeds 16 MiB limit")
		}
		if info, err := os.Lstat(name); err == nil && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("write requires a regular file destination")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.WriteFile(name, []byte(data), 0644); err != nil {
			return nil, err
		}
		return args[0], nil
	})
	registerIO("std/io", "read", []NativeParam{{"maxBytes", "number"}}, "text", "Reads standard input up to the explicit byte limit (at most 16 MiB); oversized input fails.", "stdin", func(ctx context.Context, opts Options, args []any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, _ := number(args[0])
		if math.IsNaN(n) || n < 0 || n > maxIOBytes || math.Trunc(n) != n {
			return nil, fmt.Errorf("maxBytes must be an integer from 0 through %d", maxIOBytes)
		}
		if opts.Stdin == nil {
			return "", nil
		}
		data, err := io.ReadAll(io.LimitReader(opts.Stdin, int64(n)+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > int64(n) {
			return nil, fmt.Errorf("standard input exceeds maxBytes")
		}
		return string(data), ctx.Err()
	})
	registerIO("std/io", "error", []NativeParam{{"text", "text"}}, "text", "Writes text followed by a newline to standard error.", "stderr", func(ctx context.Context, opts Options, args []any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts.Stderr != nil {
			if _, err := fmt.Fprintln(opts.Stderr, args[0]); err != nil {
				return nil, err
			}
		}
		return args[0], nil
	})
	registerIO("std/io", "exit", []NativeParam{{"status", "number"}}, "any", "Stops the script with an integer exit status from 0 through 255. Embedders receive ExitError.", "exit", func(ctx context.Context, opts Options, args []any) (any, error) {
		n, _ := number(args[0])
		if math.IsNaN(n) || n < 0 || n > 255 || math.Trunc(n) != n {
			return nil, fmt.Errorf("status must be an integer from 0 through 255")
		}
		return nil, &ExitError{Code: int(n)}
	})
	stdRegistry["std/path"] = map[string]NativeOp{}
	for name, fn := range map[string]func(string) string{"base": filepath.Base, "extension": filepath.Ext, "directory": filepath.Dir, "clean": filepath.Clean} {
		stdRegistry["std/path"][name] = textOp(name, nil, fn)
		stdDocs["std/path."+name] = stdDoc{"text", "Returns the " + name + " of a filesystem path.", []string{"pure"}}
	}
	stdRegistry["std/path"]["join"] = NativeOp{Name: "join", Params: []NativeParam{{"base", "text"}, {"child", "text"}}, Fn: func(args []any) (any, error) { return filepath.Join(args[0].(string), args[1].(string)), nil }}
	stdDocs["std/path.join"] = stdDoc{"text", "Joins two filesystem path components.", []string{"pure"}}
	stdRegistry["std/path"]["relative"] = NativeOp{Name: "relative", Params: []NativeParam{{"base", "text"}, {"target", "text"}}, Fn: func(args []any) (any, error) { return filepath.Rel(args[0].(string), args[1].(string)) }}
	stdDocs["std/path.relative"] = stdDoc{"text", "Returns target relative to base, or an error when no relative path exists.", []string{"pure"}}
}

func discoverFiles(ctx context.Context, opts Options, args []any) (any, error) {
	root := ioPath(opts, args[0].(string))
	exclusions := args[1].([]any)
	patterns := make([]string, 0, len(exclusions))
	for _, v := range exclusions {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("exclusions must contain only text")
		}
		s = filepath.ToSlash(s)
		if _, err := path.Match(s, ""); err != nil {
			return nil, fmt.Errorf("invalid exclusion %q: %w", s, err)
		}
		patterns = append(patterns, s)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("discovery root must be a directory, not a symlink or file")
	}
	out := []any{}
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if name == root {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, pattern := range patterns {
			match, _ := path.Match(pattern, rel)
			if !strings.Contains(pattern, "/") {
				for _, part := range strings.Split(rel, "/") {
					m, _ := path.Match(pattern, part)
					match = match || m
				}
			}
			if match {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.Type().IsRegular() {
			out = append(out, rel)
			if len(out) > 100000 {
				return fmt.Errorf("discovery exceeds 100000 files")
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
	return out, err
}

// boundedOutput rejects oversized process output without retaining it in memory.
type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > maxIOBytes-b.Len() {
		return 0, fmt.Errorf("process output exceeds 16 MiB")
	}
	return b.Buffer.Write(p)
}
func runProcess(ctx context.Context, opts Options, args []any) (any, error) {
	argv := []string{}
	for _, v := range args[1].([]any) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("process arguments must contain only text")
		}
		argv = append(argv, s)
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(args[0].(string), argv...)
	// Bound Wait when an escaped descendant inherits the process pipes. The
	// process-group kill cannot reach a child that deliberately creates a new
	// session, but WaitDelay still closes the pipes after the leader exits.
	cmd.WaitDelay = time.Second
	configureProcessTree(cmd)
	cmd.Dir = opts.Dir
	var stdout, stderr boundedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := startProcessTree(cmd); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		releaseProcessTree(cmd)
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = killProcessTree(cmd)
		<-done
		if parentCtx.Err() == nil {
			return nil, &operationTimeoutError{"process operation timed out"}
		}
		return nil, ctx.Err()
	}
	status := 0
	if err != nil {
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			return nil, err
		}
		status = exited.ExitCode()
	}
	return map[string]any{"stdout": stdout.String(), "stderr": stderr.String(), "status": float64(status)}, nil
}
