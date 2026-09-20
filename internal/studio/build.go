package studio

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DonaldMurillo/system-one-playground/internal/sosbuild"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

// handleBuild compiles saved project source, then publishes a new native
// executable through the same confined filesystem boundary as project edits.
func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Output string `json:"output"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Path == "" || req.Output == "" {
		writeError(w, 400, "path", "path and output are required")
		return
	}
	filename, err := s.sourceFilename(req.Path)
	if err != nil {
		writeError(w, 400, "path", err.Error())
		return
	}
	dir := s.Dir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		writeError(w, 400, "project", err.Error())
		return
	}
	defer root.Close()
	sourcePath, err := projectPath(root, req.Path)
	if err != nil {
		writeError(w, 400, "path", err.Error())
		return
	}
	outputPath, err := projectPath(root, req.Output)
	if err != nil {
		writeError(w, 400, "path", err.Error())
		return
	}
	if _, err = root.Lstat(outputPath); err == nil {
		writeError(w, 409, "conflict", "output already exists; choose a new output path")
		return
	} else if !os.IsNotExist(err) {
		writeError(w, 400, "path", err.Error())
		return
	}
	source, err := readProjectFile(root, sourcePath)
	if err != nil {
		writeError(w, 400, "read", err.Error())
		return
	}
	// The directory must exist; callers can create it through project_mkdir.
	parent, err := root.Stat(filepath.Dir(outputPath))
	if err != nil || !parent.IsDir() {
		writeError(w, 400, "path", "output directory must already exist")
		return
	}
	program, diagnostics := sos.ResolveModules(filename, string(source))
	for _, d := range diagnostics {
		if !strings.HasPrefix(d.Message, "unknown construction:") {
			writeJSON(w, 400, map[string]any{"error": map[string]string{"kind": "parse", "message": "source did not parse"}, "diagnostics": diagnostics})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	ctx, err = projectContext(ctx, dir)
	if err != nil {
		writeError(w, 400, "environment", err.Error())
		return
	}
	staging, err := os.MkdirTemp("", "sysone-studio-build-")
	if err != nil {
		writeError(w, 500, "build", err.Error())
		return
	}
	defer os.RemoveAll(staging)
	artifact := filepath.Join(staging, "program")
	var analysis *sos.Analysis
	err = sosbuild.Build(ctx, sosbuild.BuildOptions{Dir: dir, Program: program, Name: filename, Output: artifact, Target: sosbuild.TargetNative, OnAnalysis: func(a *sos.Analysis, _ error) { analysis = a }})
	response := map[string]any{"path": req.Path, "output": req.Output, "target": "native", "analysis": analysis}
	if err != nil {
		response["error"] = map[string]string{"kind": "build", "message": err.Error()}
		writeJSON(w, 400, response)
		return
	}
	// O_EXCL checks again after compilation: another editor/agent may have
	// created this path while Go was running. Never replace an existing file.
	dst, err := root.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		writeError(w, 409, "conflict", "output could not be created; recheck the project tree")
		return
	}
	src, err := os.Open(artifact)
	if err == nil {
		_, err = io.Copy(dst, src)
		src.Close()
	}
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		root.Remove(outputPath)
		writeError(w, 500, "build", "could not save compiled output")
		return
	}
	response["ok"] = true
	writeJSON(w, 200, response)
}
