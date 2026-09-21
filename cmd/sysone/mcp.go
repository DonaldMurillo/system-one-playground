package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/DonaldMurillo/system-one-playground/internal/studio"
	"github.com/DonaldMurillo/system-one-playground/sos"
)

type toolSpec struct {
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	InputSchema      map[string]any `json:"inputSchema"`
	Annotations      map[string]any `json:"annotations"`
	endpoint, action string
}

func mcpTools() []toolSpec {
	str := map[string]any{"type": "string"}
	tool := func(name, description, endpoint, action string, properties map[string]any, required []string, readOnly bool) toolSpec {
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			schema["required"] = required
		}
		return toolSpec{name, description, schema, map[string]any{"readOnlyHint": readOnly, "destructiveHint": !readOnly && action != "moduleDoctor", "openWorldHint": endpoint == "run" || endpoint == "analyze" || endpoint == "build" || action == "moduleDoctor"}, endpoint, action}
	}
	return []toolSpec{
		tool("project_tree", "List project files; secret files are excluded.", "project", "tree", map[string]any{}, nil, true),
		tool("project_read", "Read a project file and its revision for a subsequent write.", "project", "read", map[string]any{"path": str}, []string{"path"}, true),
		tool("project_write", "Create or update a file. Existing files require the exact revision from project_read; use empty revision for a new file.", "project", "write", map[string]any{"path": str, "source": str, "revision": str}, []string{"path", "source", "revision"}, false),
		tool("build", "Compile saved source into a new native executable. Requires Go; output must be a new project-relative path with an existing parent directory. May consume Jev interpretation budget.", "build", "", map[string]any{"path": str, "output": str}, []string{"path", "output"}, false),
		tool("project_mkdir", "Create a project directory.", "project", "mkdir", map[string]any{"path": str}, []string{"path"}, false),
		tool("environment_list", "List environment variable names and configuration status, never secret values.", "project", "environment", map[string]any{}, nil, true),
		tool("environment_set", "Set a project environment variable; its value is never returned.", "project", "setEnvironment", map[string]any{"name": str, "value": str}, []string{"name", "value"}, false),
		tool("project_settings", "Read settings or set the experience mode.", "project", "settings", map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"examples", "studio"}}}, nil, false),
		tool("external_modules", "List registered external modules without starting them.", "project", "externalModules", map[string]any{}, nil, true),
		tool("external_module_check", "Validate an external module definition and return its generated interface without starting it.", "project", "moduleCheck", map[string]any{"path": str}, []string{"path"}, true),
		tool("external_module_doctor", "Authorize, launch, and handshake an external module to verify runtime readiness.", "project", "moduleDoctor", map[string]any{"path": str}, []string{"path"}, false),
		tool("check", "Check source using its project-relative path and imports. Does not call Jev.", "check", "", map[string]any{"path": str, "source": str}, []string{"path", "source"}, true),
		tool("analyze", "Interpret source; may call Jev and consume the configured budget.", "analyze", "", map[string]any{"path": str, "source": str}, []string{"path", "source"}, false),
		tool("run", "Execute source in the project. Can write files, call Jev, and consume the configured budget. Returns output, decisions and usage.", "run", "", map[string]any{"path": str, "source": str, "args": map[string]any{"type": "object"}, "commandPath": map[string]any{"type": "array", "items": str}, "timeoutMs": map[string]any{"type": "integer", "minimum": 1}}, []string{"path", "source"}, false),
	}
}

func validateArguments(spec toolSpec, args map[string]any) error {
	properties := spec.InputSchema["properties"].(map[string]any)
	if required, ok := spec.InputSchema["required"].([]string); ok {
		for _, key := range required {
			if _, ok := args[key]; !ok {
				return fmt.Errorf("missing argument %s", key)
			}
		}
	}
	for key, value := range args {
		property, ok := properties[key].(map[string]any)
		if !ok {
			return fmt.Errorf("unknown argument %s", key)
		}
		valid := false
		switch property["type"] {
		case "string":
			_, valid = value.(string)
		case "object":
			_, valid = value.(map[string]any)
		case "integer":
			n, ok := value.(float64)
			valid = ok && n >= 1 && n == float64(int64(n))
		case "array":
			a, ok := value.([]any)
			valid = ok
			for _, v := range a {
				if _, ok := v.(string); !ok {
					valid = false
				}
			}
		}
		if !valid {
			return fmt.Errorf("invalid argument %s", key)
		}
		if values, ok := property["enum"].([]string); ok {
			valid = false
			for _, v := range values {
				if value == v {
					valid = true
				}
			}
			if !valid {
				return fmt.Errorf("invalid argument %s", key)
			}
		}
	}
	return nil
}

// serveMCP implements the 2025-06-18 stdio transport; stdout is JSON-RPC only.
func serveMCP(srv *studio.Server, input io.Reader, output io.Writer) int {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	encoder := json.NewEncoder(output)
	initialized := false
	ready := false
	for scanner.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		var result any
		code := 0
		message := ""
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			code = -32700
			message = "Parse error"
		} else if req.JSONRPC != "2.0" || req.Method == "" {
			code = -32600
			message = "Invalid Request"
		}
		if code == 0 && len(req.ID) == 0 {
			if req.Method == "notifications/initialized" && initialized {
				ready = true
			}
			continue
		}
		if code == 0 {
			switch req.Method {
			case "initialize":
				var params struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if initialized || json.Unmarshal(req.Params, &params) != nil || params.ProtocolVersion == "" {
					code = -32602
					message = "Invalid initialize parameters"
					break
				}
				initialized = true
				result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "sysone", "version": sos.Version}, "instructions": "Project paths are relative to this server's configured root. Read before replacing a file. Check before run; run and analyze can consume Jev budget. Environment values are never returned."}
			case "ping":
				result = map[string]any{}
			case "tools/list":
				if !ready {
					code = -32000
					message = "Initialize the session first"
				} else {
					result = map[string]any{"tools": mcpTools()}
				}
			case "tools/call":
				if !ready {
					code = -32000
					message = "Initialize the session first"
					break
				}
				var params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}
				if json.Unmarshal(req.Params, &params) != nil {
					code = -32602
					message = "Invalid tool parameters"
					break
				}
				var spec *toolSpec
				for _, candidate := range mcpTools() {
					if candidate.Name == params.Name {
						copy := candidate
						spec = &copy
						break
					}
				}
				if spec == nil {
					code = -32602
					message = "Unknown tool"
					break
				}
				if params.Arguments == nil {
					params.Arguments = map[string]any{}
				}
				if err := validateArguments(*spec, params.Arguments); err != nil {
					code = -32602
					message = err.Error()
					break
				}
				if spec.action != "" {
					params.Arguments["action"] = spec.action
				}
				response, bad, err := invoke(srv, spec.endpoint, params.Arguments)
				if err != nil {
					response = map[string]any{"error": err.Error()}
					bad = true
				}
				text, _ := json.Marshal(response)
				result = map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}, "structuredContent": response, "isError": bad}
			default:
				code = -32601
				message = "Method not found"
			}
		}
		id := req.ID
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		response := map[string]any{"jsonrpc": "2.0", "id": id}
		if code != 0 {
			response["error"] = map[string]any{"code": code, "message": message}
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return 1
		}
	}
	if err := scanner.Err(); err != nil {
		return 1
	}
	return 0
}
