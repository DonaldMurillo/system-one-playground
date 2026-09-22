package studio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/internal/soslsp"
)

// lspBridgeMethods are the editor methods the browser may invoke. Each
// request uses an immutable document snapshot and the real stdio handler.
var lspBridgeMethods = map[string]bool{
	"textDocument/foldingRange":        true,
	"textDocument/codeLens":            true,
	"textDocument/hover":               true,
	"textDocument/completion":          true,
	"textDocument/definition":          true,
	"textDocument/references":          true,
	"textDocument/rename":              true,
	"textDocument/formatting":          true,
	"textDocument/semanticTokens/full": true,
	"textDocument/inlayHint":           true,
	"textDocument/codeAction":          true,
	"sos/vocabulary":                   true,
}

// The browser transports LSP requests over an authenticated HTTP bridge.
// Each request uses an immutable document snapshot and the real stdio handler.
// The workspace context is the server's own session directory: the browser
// supplies document text only and can never widen filesystem access.
func (s *Server) handleLSP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string          `json:"path"`
		Source   string          `json:"source"`
		Method   string          `json:"method"`
		Position map[string]int  `json:"position"`
		Range    json.RawMessage `json:"range"`
		Context  json.RawMessage `json:"context"`
		Query    string          `json:"query"`
		Library  string          `json:"library"`
		NewName  string          `json:"newName"`
	}
	if !decodeBody(w, r, &req) || !checkSource(w, req.Source) {
		return
	}
	if !lspBridgeMethods[req.Method] {
		writeError(w, 400, "method", "unsupported editor method")
		return
	}
	// Range and context pass through only as validated JSON objects; the
	// handlers bound them to the snapshot document.
	if len(req.Range) > 0 && !isObjectJSON(req.Range) {
		writeError(w, 400, "range", "range must be an LSP range object")
		return
	}
	if len(req.Context) > 0 && !isObjectJSON(req.Context) {
		writeError(w, 400, "context", "context must be an LSP CodeActionContext object")
		return
	}
	params := map[string]any{}
	switch req.Method {
	case "textDocument/foldingRange", "textDocument/hover", "textDocument/completion", "textDocument/definition", "textDocument/references":
		params["position"] = req.Position
	case "textDocument/rename":
		params["position"] = req.Position
		params["newName"] = req.NewName
	case "textDocument/codeAction":
		if len(req.Range) == 0 {
			writeError(w, 400, "range", "codeAction requires a range")
			return
		}
		params["range"] = json.RawMessage(req.Range)
		if len(req.Context) > 0 {
			params["context"] = json.RawMessage(req.Context)
		} else {
			params["context"] = map[string]any{"diagnostics": []any{}}
		}
	case "textDocument/inlayHint":
		if len(req.Range) > 0 {
			params["range"] = json.RawMessage(req.Range)
		}
	case "sos/vocabulary":
		// The dictionary panel's filters. Plain strings by decodeBody's typed
		// fields; the server treats them as optional convenience filters.
		if req.Query != "" {
			params["query"] = req.Query
		}
		if req.Library != "" {
			params["library"] = req.Library
		}
	}
	var in, out bytes.Buffer
	frame := func(m any) {
		b, _ := json.Marshal(m)
		fmt.Fprintf(&in, "Content-Length: %d\r\n\r\n", len(b))
		in.Write(b)
	}
	filename, err := s.sourceFilename(req.Path)
	if err != nil {
		writeError(w, 400, "path", err.Error())
		return
	}
	uri := (&url.URL{Scheme: "file", Path: filename}).String()
	initParams := map[string]any{
		// The only workspace context: the trusted session directory. Local
		// package indexing is bounded to it; client input cannot change it.
		"initializationOptions": map[string]any{"workspacePath": s.Dir()},
	}
	frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": initParams})
	frame(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": req.Source}}})
	requestParams := map[string]any{"textDocument": map[string]any{"uri": uri}}
	for k, v := range params {
		requestParams[k] = v
	}
	frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": req.Method, "params": requestParams})
	frame(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "shutdown", "params": nil})
	frame(map[string]any{"jsonrpc": "2.0", "method": "exit", "params": nil})
	if e := soslsp.Serve(&in, &out); e != nil {
		writeError(w, 500, "lsp", e.Error())
		return
	}
	br := bufio.NewReader(&out)
	for {
		line, e := br.ReadString('\n')
		if e == io.EOF {
			break
		}
		if e != nil {
			writeError(w, 500, "lsp", e.Error())
			return
		}
		n, e := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:")))
		if e != nil {
			break
		}
		if _, e = br.ReadString('\n'); e != nil {
			break
		}
		b := make([]byte, n)
		if _, e = io.ReadFull(br, b); e != nil {
			break
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m["id"] == float64(2) {
			writeJSON(w, 200, m)
			return
		}
	}
	writeError(w, 500, "lsp", "missing language server response")
}

// isObjectJSON reports whether raw decodes to a JSON object.
func isObjectJSON(raw json.RawMessage) bool {
	var v map[string]any
	return json.Unmarshal(raw, &v) == nil
}
