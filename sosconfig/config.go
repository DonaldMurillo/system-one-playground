// Package sosconfig parses and resolves deterministic SysOneScript configuration.
// It never executes scripts or contacts a model.
package sosconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const maxBytes = 64 << 10

// Error identifies a configuration error's one-based source position.
// Extract adjusts TOML positions to the original script's header lines.
type Error struct {
	Line, Column int
	Message      string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d:%d: %s", e.Line, e.Column, e.Message) }

// Config is a partial versioned configuration. Nil requests means inherit;
// a pointer to zero denies requests. Empty optional strings mean omitted.
type Config struct {
	Version int `toml:"version"`
	Editor  struct {
		Assistance string `toml:"assistance"`
	} `toml:"editor"`
	Interpretation struct {
		Mode string `toml:"mode"`
	} `toml:"interpretation"`
	Runtime struct {
		Judgment string `toml:"judgment"`
	} `toml:"runtime"`
	Budget struct {
		Run struct {
			Requests *int   `toml:"requests"`
			Timeout  string `toml:"timeout"`
		} `toml:"run"`
	} `toml:"budget"`
	Module struct {
		Path     string           `toml:"path"`
		External []ExternalModule `toml:"external"`
	} `toml:"module"`
	Language struct {
		// Libraries is nil when absent (inherit) and an explicit empty
		// list when language.libraries = [] (disable inherited libraries).
		Libraries *[]Library `toml:"libraries"`
	} `toml:"language"`
	External struct {
		Process    *bool     `toml:"process"`
		Network    *bool     `toml:"network"`
		Filesystem string    `toml:"filesystem"`
		Secrets    *[]string `toml:"secrets"`
	} `toml:"external"`
}

// ExternalModule registers an offline module definition with a logical import
// path. Definition is resolved relative to the project sos.toml.
type ExternalModule struct {
	Path       string `toml:"path" json:"path"`
	Definition string `toml:"definition" json:"definition"`
}

// Library is one configured vocabulary import: a module path with an optional
// alias. Without as, the library's words are exposed bare; with as, calls must
// use the alias prefix. It is language surface, never policy: it cannot touch
// budgets or judgment.
type Library struct {
	Path string `toml:"path" json:"path"`
	As   string `toml:"as" json:"as,omitempty"`
}

// Layer identifies where a partial configuration came from.
type Layer struct {
	Name   string
	Config Config
}

// Effective contains resolved preferences, intersected ceilings, and origins.
// Timeout zero means no configured timeout, not an immediate deadline.
// Libraries is the resolved vocabulary import list: the last layer defining a
// list replaces earlier ones, and an explicit empty list disables inheritance.
type Effective struct {
	Editor             string            `json:"editor"`
	Interpretation     string            `json:"interpretation"`
	Runtime            string            `json:"runtime"`
	Requests           int               `json:"requests"`
	Timeout            time.Duration     `json:"timeout_ns"`
	Libraries          []Library         `json:"libraries,omitempty"`
	ExternalProcess    bool              `json:"externalProcess"`
	ExternalNetwork    bool              `json:"externalNetwork"`
	ExternalFilesystem string            `json:"externalFilesystem"`
	ExternalSecrets    []string          `json:"externalSecrets,omitempty"`
	Origins            map[string]string `json:"origins"`
}

// Parse validates a TOML document. file prohibits editor preferences even when
// its table is empty. Unknown fields, explicit empty enums, and duplicate keys
// fail rather than falling back to defaults.
func Parse(data []byte, file bool) (Config, error) {
	var cfg Config
	if len(data) > maxBytes {
		return cfg, fmt.Errorf("configuration exceeds 64 KiB")
	}
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var missing *toml.StrictMissingError
		if errors.As(err, &missing) && len(missing.Errors) > 0 {
			first := missing.Errors[0]
			line, col := first.Position()
			return cfg, &Error{line, col, "unknown configuration key " + strings.Join(first.Key(), ".")}
		}
		var decode *toml.DecodeError
		if errors.As(err, &decode) {
			line, col := decode.Position()
			return cfg, &Error{line, col, decode.Error()}
		}
		return cfg, err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return cfg, err
	}
	if file {
		if _, ok := raw["editor"]; ok {
			return cfg, located(data, "editor", "editor settings are not allowed in frontmatter")
		}
		if _, ok := raw["module"]; ok {
			return cfg, located(data, "module", "module settings are not allowed in frontmatter; put [module] in the project sos.toml")
		}
		if _, ok := raw["language"]; ok {
			return cfg, located(data, "language", "language settings are not allowed in frontmatter; put [language] in the project sos.toml")
		}
		if _, ok := raw["external"]; ok {
			return cfg, located(data, "external", "external capability settings are not allowed in frontmatter; put [external] in the project sos.toml")
		}
	}
	for section, key := range map[string]string{"editor": "assistance", "interpretation": "mode", "runtime": "judgment"} {
		if m, ok := raw[section].(map[string]any); ok {
			if v, present := m[key]; present && v == "" {
				return cfg, located(data, section+"."+key, fmt.Sprintf("%s.%s must not be empty", section, key))
			}
		}
	}
	if budget, ok := raw["budget"].(map[string]any); ok {
		if run, ok := budget["run"].(map[string]any); ok {
			if v, present := run["timeout"]; present && v == "" {
				return cfg, located(data, "budget.run.timeout", "budget.run.timeout must not be empty")
			}
		}
	}
	err := validate(cfg)
	var invalid *fieldError
	if errors.As(err, &invalid) {
		return cfg, located(data, invalid.key, invalid.message)
	}
	return cfg, err
}

func validate(c Config) error {
	if c.Version != 1 {
		return &fieldError{"version", "configuration version must be 1"}
	}
	for field, pair := range map[string][2]string{
		"editor.assistance":   {c.Editor.Assistance, "off,on-demand,automatic"},
		"interpretation.mode": {c.Interpretation.Mode, "canonical,assisted,semantic"},
		"runtime.judgment":    {c.Runtime.Judgment, "deny,explicit,semantic"},
	} {
		if pair[0] != "" && !slices.Contains(strings.Split(pair[1], ","), pair[0]) {
			return &fieldError{field, fmt.Sprintf("invalid %s %q", field, pair[0])}
		}
	}
	if c.Budget.Run.Requests != nil && *c.Budget.Run.Requests < 0 {
		return &fieldError{"budget.run.requests", "budget.run.requests must not be negative"}
	}
	if c.Budget.Run.Timeout != "" {
		d, err := time.ParseDuration(c.Budget.Run.Timeout)
		if err != nil || d <= 0 {
			return &fieldError{"budget.run.timeout", "budget.run.timeout must be a positive duration"}
		}
	}
	if c.Module.Path != "" && !validModulePath(c.Module.Path) {
		return &fieldError{"module.path", "module.path must be a logical module identity such as example.com/tools"}
	}
	for i, external := range c.Module.External {
		field := fmt.Sprintf("module.external[%d]", i)
		if !validModulePath(external.Path) {
			return &fieldError{field + ".path", field + ".path must be a logical module identity"}
		}
		if external.Definition == "" {
			return &fieldError{field + ".definition", field + ".definition must not be empty"}
		}
	}
	if c.External.Filesystem != "" && !slices.Contains([]string{"none", "workspace-read", "workspace", "explicit"}, c.External.Filesystem) {
		return &fieldError{"external.filesystem", "external.filesystem must be none, workspace-read, workspace, or explicit"}
	}
	if c.Language.Libraries != nil {
		if len(*c.Language.Libraries) > 64 {
			return &fieldError{"language.libraries", "language.libraries exceeds the 64 entry limit"}
		}
		for i, lib := range *c.Language.Libraries {
			field := fmt.Sprintf("language.libraries[%d]", i)
			if lib.Path == "" {
				return &fieldError{field + ".path", field + ".path must not be empty"}
			}
			if !validModulePath(lib.Path) {
				return &fieldError{field + ".path", field + ".path must be a logical module identity such as std/text"}
			}
			if lib.As != "" && !validWord(lib.As) {
				return &fieldError{field + ".as", field + ".as must be an identifier such as media"}
			}
		}
	}

	return nil
}

// validModulePath accepts slash-separated identifier parts, without dots
// required: "std/text" and "example.com/tools" are both identities.
func validModulePath(path string) bool {
	if len(path) > 512 || strings.HasPrefix(path, "/") || strings.Contains(path, ":") {
		return false
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
				return false
			}
		}
	}
	return true
}

// validWord accepts the identifier shape shared by aliases and vocabulary words.
func validWord(w string) bool {
	if w == "" {
		return false
	}
	for i, r := range w {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// Extract strips an optional +++ TOML header by replacing header lines with
// blank lines. This preserves body line numbers. Source without a header is
// returned unchanged. A UTF-8 BOM before the opening delimiter is accepted.
func Extract(source string) (string, *Config, error) {
	start := strings.TrimPrefix(source, "\ufeff")
	first, _, _ := strings.Cut(start, "\n")
	if strings.TrimSuffix(first, "\r") != "+++" {
		return start, nil, nil
	}
	lines := strings.Split(start, "\n")
	size := 0
	for i := 1; i < len(lines); i++ {
		size += len(lines[i]) + 1
		if size > maxBytes {
			return "", nil, fmt.Errorf("frontmatter exceeds 64 KiB")
		}
		if strings.TrimSuffix(lines[i], "\r") != "+++" {
			continue
		}
		cfg, err := Parse([]byte(strings.Join(lines[1:i], "\n")+"\n"), true)
		if err != nil {
			var positioned *Error
			if errors.As(err, &positioned) {
				return "", nil, &Error{positioned.Line + 1, positioned.Column, "frontmatter: " + positioned.Message}
			}
			return "", nil, fmt.Errorf("frontmatter: %w", err)
		}
		for j := 0; j <= i; j++ {
			lines[j] = ""
		}
		return strings.Join(lines, "\n"), &cfg, nil
	}
	return "", nil, fmt.Errorf("frontmatter is missing closing +++ delimiter")
}

// Resolve applies preferences in order and intersects all explicit ceilings.
// The default 100 requests only applies when no layer supplies a request cap.
// Runtime deny is a ceiling which subsequent layers cannot lift. Libraries
// replace wholesale: the last layer defining a list wins, and an explicit
// empty list disables inherited libraries.
func Resolve(layers ...Layer) (Effective, error) {
	e := Effective{Editor: "on-demand", Interpretation: "canonical", Runtime: "explicit", Requests: 100, ExternalFilesystem: "none", Origins: map[string]string{}}
	for _, key := range []string{"editor", "interpretation", "runtime", "requests", "timeout", "libraries"} {
		e.Origins[key] = "default"
	}
	hasRequests, hasTimeout, denied := false, false, false
	hasExternalProcess, hasExternalNetwork, hasExternalFilesystem, hasExternalSecrets := false, false, false, false
	filesystemRank := map[string]int{"none": 0, "workspace-read": 1, "workspace": 2, "explicit": 3}
	for _, layer := range layers {
		c := layer.Config
		if err := validate(c); err != nil {
			return e, fmt.Errorf("%s: %w", layer.Name, err)
		}
		if c.Editor.Assistance != "" {
			e.Editor = c.Editor.Assistance
			e.Origins["editor"] = layer.Name
		}
		if c.Interpretation.Mode != "" {
			e.Interpretation = c.Interpretation.Mode
			e.Origins["interpretation"] = layer.Name
		}
		if c.Runtime.Judgment != "" && !denied {
			e.Runtime = c.Runtime.Judgment
			e.Origins["runtime"] = layer.Name
			denied = e.Runtime == "deny"
		}
		if n := c.Budget.Run.Requests; n != nil && (!hasRequests || *n < e.Requests) {
			e.Requests = *n
			e.Origins["requests"] = layer.Name
			hasRequests = true
		}
		if c.Budget.Run.Timeout != "" {
			d, _ := time.ParseDuration(c.Budget.Run.Timeout)
			if !hasTimeout || d < e.Timeout {
				e.Timeout = d
				e.Origins["timeout"] = layer.Name
				hasTimeout = true
			}
		}
		if c.Language.Libraries != nil {
			e.Libraries = append([]Library(nil), *c.Language.Libraries...)
			e.Origins["libraries"] = layer.Name
		}
		if c.External.Process != nil {
			if !hasExternalProcess {
				e.ExternalProcess = *c.External.Process
			} else {
				e.ExternalProcess = e.ExternalProcess && *c.External.Process
			}
			hasExternalProcess = true
		}
		if c.External.Network != nil {
			if !hasExternalNetwork {
				e.ExternalNetwork = *c.External.Network
			} else {
				e.ExternalNetwork = e.ExternalNetwork && *c.External.Network
			}
			hasExternalNetwork = true
		}
		if c.External.Filesystem != "" {
			if !hasExternalFilesystem || filesystemRank[c.External.Filesystem] < filesystemRank[e.ExternalFilesystem] {
				e.ExternalFilesystem = c.External.Filesystem
			}
			hasExternalFilesystem = true
		}
		if c.External.Secrets != nil {
			if !hasExternalSecrets {
				e.ExternalSecrets = append([]string(nil), (*c.External.Secrets)...)
			} else {
				allowed := map[string]bool{}
				for _, name := range *c.External.Secrets {
					allowed[name] = true
				}
				e.ExternalSecrets = slices.DeleteFunc(e.ExternalSecrets, func(name string) bool { return !allowed[name] })
			}
			slices.Sort(e.ExternalSecrets)
			hasExternalSecrets = true
		}
	}
	return e, nil
}

func readConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// Load discovers global and nearest-project layers. SOS_CONFIG_HOME overrides
// the directory containing the global config.toml; no environment values are
// interpolated inside TOML. Project discovery starts at the working directory.
func Load(dir string) ([]Layer, error) {
	globalDir := os.Getenv("SOS_CONFIG_HOME")
	if globalDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		globalDir = filepath.Join(base, "sysonescript")
	}
	global := filepath.Join(globalDir, "config.toml")
	layers := []Layer{}
	c, err := readConfig(global)
	if err != nil {
		return nil, err
	}
	if c != nil {
		layers = append(layers, Layer{Name: global, Config: *c})
	}
	current, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for {
		name := filepath.Join(current, "sos.toml")
		c, err := readConfig(name)
		if err != nil {
			return nil, err
		}
		if c != nil {
			layers = append(layers, Layer{Name: name, Config: *c})
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return layers, nil
}

type fieldError struct{ key, message string }

func (e *fieldError) Error() string { return e.message }

// located uses the dependency's pinned TOML AST so quoted/dotted keys and
// inline tables get the same positions as ordinary table declarations.
func located(data []byte, key, message string) error {
	result := &Error{1, 1, message}
	set := func(offset uint32) {
		prefix := data[:int(offset)]
		result.Line = bytes.Count(prefix, []byte("\n")) + 1
		result.Column = len(prefix) - bytes.LastIndexByte(prefix, '\n')
	}
	keys := func(node *unstable.Node) []string {
		out := []string{}
		it := node.Key()
		for it.Next() {
			out = append(out, string(it.Node().Data))
		}
		return out
	}
	var visit func(*unstable.Node, []string) bool
	visit = func(node *unstable.Node, parent []string) bool {
		parts := append(append([]string{}, parent...), keys(node)...)
		if strings.Join(parts, ".") == key {
			it := node.Key()
			it.Next()
			set(it.Node().Raw.Offset)
			return true
		}
		if node.Kind == unstable.KeyValue && node.Value().Kind == unstable.InlineTable {
			it := node.Value().Children()
			for it.Next() {
				if visit(it.Node(), parts) {
					return true
				}
			}
		}
		return false
	}
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			if visit(node, nil) {
				return result
			}
			table = keys(node)
		case unstable.KeyValue:
			if visit(node, table) {
				return result
			}
		}
	}
	return result
}
