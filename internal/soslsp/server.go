// Package soslsp implements a stdio Language Server Protocol server for
// SysOneScript on top of the shared analysis core in package sos. Editor
// analysis is offline: no model or network requests are made unless a client
// explicitly invokes the custom sos/analyze method, which enforces the
// effective editor policy and request budgets.
package soslsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DonaldMurillo/system-one-playground/internal/sossyntax"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

const (
	// maxFrameBytes bounds a single JSON-RPC body.
	maxFrameBytes = 8 << 20
	// maxHeaderLine bounds one framing header line.
	maxHeaderLine = 4096
)

const (
	codeParseError           = -32700
	codeInvalidRequest       = -32600
	codeMethodNotFound       = -32601
	codeInvalidParams        = -32602
	codeInternalError        = -32603
	codeServerNotInitialized = -32002
)

// Serve reads Content-Length framed JSON-RPC messages from r and writes
// responses and notifications to w, one message at a time, until the client
// sends exit or closes r.
func Serve(r io.Reader, w io.Writer) error {
	s := &server{out: w, docs: make(map[string]*document)}
	br := bufio.NewReaderSize(r, maxHeaderLine)
	for {
		body, err := readFrame(br)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if done, err := s.handleMessage(body); done {
			return err
		}
	}
}

type server struct {
	out           io.Writer
	initialized   bool
	shutdown      bool
	workspaceRoot string
	docs          map[string]*document
	// vocabCache shares one core vocabulary resolution per document text
	// across completion, hover, tokens, hints, and sos/vocabulary.
	vocabCache          map[string]*vocabCacheEntry
	index               *packageIndex
	interpretationCache *sos.InterpretationCache
}

// pkgIndex lazily builds the bounded local package index for the workspace.
// A missing workspace yields an empty index: standard library auto-imports
// keep working.
func (s *server) pkgIndex() *packageIndex {
	if s.index == nil {
		s.index = indexWorkspace(s.workspaceRoot)
	}
	return s.index
}

type document struct {
	syntax     *sossyntax.Document
	syntaxText string
	text       string
	version    int
	hasVersion bool
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

func (s *server) handleMessage(body []byte) (bool, error) {
	if len(body) == 0 {
		return false, nil
	}
	if startsWithJSONArray(body) {
		s.writeError(json.RawMessage("null"), codeInvalidRequest, "batch requests are not supported")
		return false, nil
	}
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		s.writeError(json.RawMessage("null"), codeParseError, "invalid JSON: "+err.Error())
		return false, nil
	}
	isRequest := len(msg.ID) > 0 && string(msg.ID) != "null"

	if msg.Method == "" {
		// A response to a server request; this server issues none. Ignore.
		return false, nil
	}
	if !s.initialized && msg.Method != "initialize" {
		if msg.Method == "exit" {
			return true, exitError(s.shutdown)
		}
		if isRequest {
			s.writeError(msg.ID, codeServerNotInitialized, "server not initialized")
		}
		return false, nil
	}
	switch msg.Method {
	case "sos/analyze":
		s.sosAnalyze(msg.ID, msg.Params)
	case "sos/vocabulary":
		s.sosVocabulary(msg.ID, msg.Params)
	case "initialize":
		s.initialized = true
		s.captureWorkspace(msg.Params)
		s.writeResult(msg.ID, initializeResult())
	case "shutdown":
		s.shutdown = true
		s.writeResult(msg.ID, nil)
	case "exit":
		return true, exitError(s.shutdown)
	case "initialized":
		// No state needed.
	case "textDocument/didOpen":
		s.didOpen(msg.Params)
	case "textDocument/didChange":
		s.didChange(msg.Params)
	case "textDocument/didClose":
		s.didClose(msg.Params)
	case "textDocument/foldingRange":
		s.writeResult(msg.ID, s.foldingRanges(msg.Params))
	case "textDocument/codeLens":
		s.writeResult(msg.ID, s.codeLens(msg.Params))
	case "textDocument/hover":
		s.writeResult(msg.ID, s.hover(msg.Params))
	case "textDocument/completion":
		s.writeResult(msg.ID, s.completion(msg.Params))
	case "textDocument/definition":
		s.writeResult(msg.ID, s.definition(msg.Params))
	case "textDocument/references":
		s.writeResult(msg.ID, s.references(msg.Params))
	case "textDocument/rename":
		s.writeResult(msg.ID, s.rename(msg.Params))
	case "textDocument/formatting":
		s.writeResult(msg.ID, s.formatting(msg.Params))
	case "textDocument/semanticTokens/full":
		s.writeResult(msg.ID, s.semanticTokens(msg.Params))
	case "textDocument/inlayHint":
		s.writeResult(msg.ID, s.inlayHints(msg.Params))
	case "textDocument/codeAction":
		s.writeResult(msg.ID, s.codeActions(msg.Params))
	default:
		if isRequest {
			s.writeError(msg.ID, codeMethodNotFound, "method not supported: "+msg.Method)
		}
		// Unknown notifications get no response.
	}
	return false, nil
}

// captureWorkspace records the workspace root declared at initialize time.
// Priority: rootUri, rootPath, first workspace folder, then the explicit
// initializationOptions.workspacePath used by the studio HTTP bridge. Only a
// real existing directory is accepted.
func (s *server) captureWorkspace(params json.RawMessage) {
	var p struct {
		RootURI          string `json:"rootUri"`
		RootPath         string `json:"rootPath"`
		WorkspaceFolders []struct {
			URI string `json:"uri"`
		} `json:"workspaceFolders"`
		InitializationOptions struct {
			WorkspacePath string `json:"workspacePath"`
		} `json:"initializationOptions"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	candidates := []string{uriToPath(p.RootURI), p.RootPath}
	if len(p.WorkspaceFolders) > 0 {
		candidates = append(candidates, uriToPath(p.WorkspaceFolders[0].URI))
	}
	candidates = append(candidates, p.InitializationOptions.WorkspacePath)
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			s.workspaceRoot = dir
			return
		}
	}
}

// uriToPath converts a file:// URI to a filesystem path; anything else, or a
// relative result, yields "" so it can never widen filesystem access.
func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return ""
	}
	path := strings.TrimPrefix(uri, "file://")
	if u, err := url.PathUnescape(path); err == nil {
		path = u
	}
	if !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}

func exitError(shutdownSeen bool) error {
	if shutdownSeen {
		return nil
	}
	return errors.New("soslsp: exit received before shutdown")
}

func initializeResult() map[string]any {
	return map[string]any{
		"capabilities": map[string]any{
			"positionEncoding": "utf-16",
			"textDocumentSync": map[string]any{
				"openClose": true,
				"change":    1, // full-text sync
			},
			"foldingRangeProvider":       true,
			"hoverProvider":              true,
			"completionProvider":         map[string]any{"triggerCharacters": []string{":", " "}},
			"definitionProvider":         true,
			"referencesProvider":         true,
			"renameProvider":             true,
			"documentFormattingProvider": true,
			"semanticTokensProvider": map[string]any{
				"legend": map[string]any{
					"tokenTypes":     tokenLegend,
					"tokenModifiers": []string{},
				},
				"full": true,
			},
			"inlayHintProvider":  true,
			"codeLensProvider":   map[string]any{"resolveProvider": false},
			"codeActionProvider": map[string]any{"codeActionKinds": []string{"quickfix"}},
		},
		"serverInfo": map[string]any{
			"name":    "soslsp",
			"version": sos.Version,
		},
	}
}

func (s *server) writeResult(id json.RawMessage, result any) {
	s.writeMessage(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeError(id json.RawMessage, code int, message string) {
	s.writeMessage(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	})
}

func (s *server) notify(method string, params any) {
	s.writeMessage(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *server) writeMessage(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(body))
	s.out.Write(body)
}

func startsWithJSONArray(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

// readFrame reads one Content-Length framed message body.
func readFrame(br *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := readHeaderLine(br)
		if err != nil {
			return nil, err
		}
		if line == "" {
			break
		}
		if i := strings.IndexByte(line, ':'); i >= 0 {
			name := strings.ToLower(strings.TrimSpace(line[:i]))
			if name == "content-length" {
				n, perr := strconv.Atoi(strings.TrimSpace(line[i+1:]))
				if perr != nil || n < 0 {
					return nil, fmt.Errorf("soslsp: invalid Content-Length %q", line[i+1:])
				}
				contentLength = n
			}
		}
	}
	if contentLength < 0 {
		return nil, errors.New("soslsp: missing Content-Length header")
	}
	if contentLength > maxFrameBytes {
		return nil, fmt.Errorf("soslsp: frame of %d bytes exceeds limit of %d bytes", contentLength, maxFrameBytes)
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(br, body); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return body, nil
}

func readHeaderLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		return "", errors.New("soslsp: header line too long")
	}
	if err != nil {
		if err == io.EOF && len(line) == 0 {
			return "", io.EOF
		}
		return "", fmt.Errorf("soslsp: truncated header: %w", err)
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}
