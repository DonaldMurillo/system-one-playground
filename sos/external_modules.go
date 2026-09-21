package sos

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/DonaldMurillo/system-one-playground/sosconfig"
	"github.com/pelletier/go-toml/v2"
)

// ExternalModuleDefinition is the strict, offline contract transformed into
// an ordinary SOS module. Runtime processes are never started while parsing it.
type ExternalModuleDefinition struct {
	Schema int `toml:"schema"`
	Module struct {
		Path, Version, Description string
	} `toml:"module"`
	Runtime struct {
		Kind             string            `toml:"kind"`
		Protocol         string            `toml:"protocol"`
		Command          []string          `toml:"command"`
		WorkingDirectory string            `toml:"working_directory"`
		MaxInFlight      int               `toml:"max_in_flight"`
		StartupTimeout   string            `toml:"startup_timeout"`
		ShutdownTimeout  string            `toml:"shutdown_timeout"`
		Requires         map[string]string `toml:"requires"`
	} `toml:"runtime"`
	Capabilities struct {
		Network    bool     `toml:"network"`
		Filesystem string   `toml:"filesystem"`
		Process    bool     `toml:"process"`
		Secrets    []string `toml:"secrets"`
	} `toml:"capabilities"`
	Distribution struct {
		Mode                 string             `toml:"mode"`
		Program              string             `toml:"program"`
		Arguments            []string           `toml:"arguments"`
		VersionCommand       []string           `toml:"version_command"`
		VersionRequirement   string             `toml:"version_requirement"`
		InstallDocumentation string             `toml:"install_documentation"`
		Artifacts            []ExternalArtifact `toml:"artifact"`
	} `toml:"distribution"`
	Types          []ExternalType   `toml:"type"`
	Failures       []ExternalType   `toml:"failure"`
	Actions        []ExternalAction `toml:"action"`
	definitionPath string
	rawSource      string
	digest         string
	clientsGate    chan struct{}
	clients        map[*externalSessionKey]*stdioClient
	bundleProgram  string
	bundleSHA256   string
	embedded       bool
}

type ExternalArtifact struct{ Target, Path, SHA256 string }

type externalSessionKey struct{ id uint64 }

var nextExternalSessionID atomic.Uint64

func newExternalSessionKey() *externalSessionKey {
	return &externalSessionKey{id: nextExternalSessionID.Add(1)}
}

type ExternalType struct {
	Name   string          `toml:"name"`
	Fields []ExternalField `toml:"field"`
}

type ExternalField struct{ Name, Type string }

type ExternalAction struct {
	Name, Description, Timeout string
	Failures                   []string        `toml:"failures"`
	Effects                    []string        `toml:"effects"`
	Targets                    []string        `toml:"targets"`
	Parameters                 []ExternalField `toml:"parameter"`
	Result                     struct {
		Type string `toml:"type"`
	} `toml:"result"`
	Command struct {
		Program                   string   `toml:"program"`
		Arguments                 []string `toml:"arguments"`
		WorkingDirectoryParameter string   `toml:"working_directory_parameter"`
		StdinParameter            string   `toml:"stdin_parameter"`
		Stdin, Stdout, Stderr     string
		ExitCodes                 []int `toml:"exit_codes"`
	} `toml:"command"`
}

func LoadExternalModuleDefinition(path string) (*ExternalModuleDefinition, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeExternalModuleDefinition(path, b)
}

func decodeExternalModuleDefinition(path string, b []byte) (*ExternalModuleDefinition, error) {
	var d ExternalModuleDefinition
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("external module definition: %w", err)
	}
	d.definitionPath, _ = filepath.Abs(path)
	d.rawSource = string(b)
	if d.Schema != 1 {
		return nil, fmt.Errorf("external module definition: schema must be 1")
	}
	if d.Module.Path == "" || d.Module.Version == "" {
		return nil, fmt.Errorf("external module definition: module.path and module.version are required")
	}
	moduleCore := strings.SplitN(strings.TrimPrefix(d.Module.Version, "v"), "-", 2)[0]
	if _, ok := parseSemanticVersion(d.Module.Version); !ok || len(strings.Split(moduleCore, ".")) != 3 {
		return nil, fmt.Errorf("external module definition: module.version must be semantic version MAJOR.MINOR.PATCH")
	}
	for runtimeName, requirement := range d.Runtime.Requires {
		if _, _, ok := parseVersionRequirement(requirement); !ok {
			return nil, fmt.Errorf("external module definition: runtime requirement %s has invalid version %q", runtimeName, requirement)
		}
	}
	if d.Runtime.Kind != "command" && d.Runtime.Kind != "stdio" {
		return nil, fmt.Errorf("external module definition: runtime.kind must be command or stdio")
	}
	if d.Runtime.MaxInFlight == 0 {
		d.Runtime.MaxInFlight = 1
	}
	if d.Runtime.StartupTimeout == "" {
		d.Runtime.StartupTimeout = "10s"
	}
	if d.Runtime.ShutdownTimeout == "" {
		d.Runtime.ShutdownTimeout = "1s"
	}
	if d.Runtime.WorkingDirectory == "" {
		d.Runtime.WorkingDirectory = "${workspace}"
	}
	if d.Capabilities.Filesystem == "" {
		d.Capabilities.Filesystem = "none"
	}
	if !containsString([]string{"", "${workspace}", "${definition_dir}"}, d.Runtime.WorkingDirectory) {
		return nil, fmt.Errorf("external module definition: runtime.working_directory must be ${workspace} or ${definition_dir}")
	}
	if !d.Capabilities.Process {
		return nil, fmt.Errorf("external module definition: command runtime requires capabilities.process = true")
	}
	seen := map[string]bool{}
	for _, a := range d.Actions {
		if a.Name == "" || seen[a.Name] {
			return nil, fmt.Errorf("external module definition: action names must be nonempty and unique")
		}
		seen[a.Name] = true
		if d.Runtime.Kind == "command" && a.Command.Program == "" {
			return nil, fmt.Errorf("external module definition: action %s requires action.command.program", a.Name)
		}
		for _, arg := range a.Command.Arguments {
			if strings.Contains(arg, "${") && !(strings.HasPrefix(arg, "${") && strings.HasSuffix(arg, "}") && strings.Count(arg, "${") == 1) {
				return nil, fmt.Errorf("external module definition: action %s substitutions must occupy a complete argument", a.Name)
			}
		}
		parameterTypes := map[string]string{}
		for _, parameter := range a.Parameters {
			parameterTypes[parameter.Name] = parameter.Type
		}
		for _, arg := range a.Command.Arguments {
			if strings.HasPrefix(arg, "${") {
				name := strings.TrimSuffix(strings.TrimPrefix(arg, "${"), "}")
				if _, ok := parameterTypes[name]; !ok {
					return nil, fmt.Errorf("external module definition: action %s references unknown parameter %s", a.Name, name)
				}
			}
		}
		if a.Command.StdinParameter != "" {
			if _, ok := parameterTypes[a.Command.StdinParameter]; !ok {
				return nil, fmt.Errorf("external module definition: action %s stdin_parameter is not declared", a.Name)
			}
		}
		if a.Command.WorkingDirectoryParameter != "" && parameterTypes[a.Command.WorkingDirectoryParameter] != "folder" {
			return nil, fmt.Errorf("external module definition: action %s working_directory_parameter must name a folder parameter", a.Name)
		}
		if d.Runtime.Kind == "command" {
			if !containsString([]string{"", "none", "text", "json"}, a.Command.Stdin) {
				return nil, fmt.Errorf("external module definition: action %s has unsupported stdin mode %s", a.Name, a.Command.Stdin)
			}
			if !containsString([]string{"", "none", "text", "json", "json-lines"}, a.Command.Stdout) {
				return nil, fmt.Errorf("external module definition: action %s has unsupported stdout mode %s", a.Name, a.Command.Stdout)
			}
			if !containsString([]string{"", "diagnostic", "discard", "merge"}, a.Command.Stderr) {
				return nil, fmt.Errorf("external module definition: action %s has unsupported stderr mode %s", a.Name, a.Command.Stderr)
			}
		}
	}
	for i := range d.Actions {
		action := &d.Actions[i]
		if d.Runtime.Kind == "command" {
			if action.Command.Stdin == "" {
				if action.Command.StdinParameter == "" {
					action.Command.Stdin = "none"
				} else {
					action.Command.Stdin = "text"
				}
			}
			if action.Command.Stdout == "" {
				action.Command.Stdout = "text"
			}
			if action.Command.Stderr == "" {
				action.Command.Stderr = "diagnostic"
			}
			if len(action.Command.ExitCodes) == 0 {
				action.Command.ExitCodes = []int{0}
			}
		}
	}
	failureNames := map[string]bool{}
	for _, failure := range d.Failures {
		if failure.Name == "" || failureNames[failure.Name] {
			return nil, fmt.Errorf("external module definition: failure names must be nonempty and unique")
		}
		failureNames[failure.Name] = true
		reserved := map[string]bool{"kind": true, "message": true, "retryable": true, "status": true, "code": true, "frames": true, "module": true, "phase": true}
		for _, field := range failure.Fields {
			if reserved[field.Name] {
				return nil, fmt.Errorf("external module definition: failure %s uses reserved field %s", failure.Name, field.Name)
			}
		}
	}
	for _, action := range d.Actions {
		for _, name := range action.Failures {
			if !failureNames[name] {
				return nil, fmt.Errorf("external module definition: action %s declares unknown failure %s", action.Name, name)
			}
		}
	}
	if d.Runtime.Kind == "stdio" {
		if d.Runtime.Protocol != "sos-plugin/1" {
			return nil, fmt.Errorf("external module definition: stdio protocol must be sos-plugin/1")
		}
		if len(d.Runtime.Command) == 0 {
			return nil, fmt.Errorf("external module definition: stdio runtime requires command")
		}
		if d.Runtime.MaxInFlight < 0 || d.Runtime.MaxInFlight > 1 {
			return nil, fmt.Errorf("external module definition: runtime.max_in_flight currently supports only 1")
		}
	}
	if !containsString([]string{"", "none", "workspace-read", "workspace", "explicit"}, d.Capabilities.Filesystem) {
		return nil, fmt.Errorf("external module definition: unsupported filesystem capability %q", d.Capabilities.Filesystem)
	}
	for label, configured := range map[string]string{"runtime.startup_timeout": d.Runtime.StartupTimeout, "runtime.shutdown_timeout": d.Runtime.ShutdownTimeout} {
		if configured != "" {
			if duration, err := time.ParseDuration(configured); err != nil || duration <= 0 {
				return nil, fmt.Errorf("external module definition: %s must be a positive duration", label)
			}
		}
	}
	for _, action := range d.Actions {
		if action.Timeout != "" {
			if duration, err := time.ParseDuration(action.Timeout); err != nil || duration <= 0 {
				return nil, fmt.Errorf("external module definition: action %s timeout must be a positive duration", action.Name)
			}
		}
	}
	if d.Distribution.Mode == "" {
		d.Distribution.Mode = "external"
	}
	if d.Distribution.Mode != "external" && d.Distribution.Mode != "bundled" {
		return nil, fmt.Errorf("external module definition: distribution.mode must be external or bundled")
	}
	if d.Distribution.Mode == "bundled" && len(d.Distribution.Artifacts) == 0 {
		return nil, fmt.Errorf("external module definition: bundled distribution requires artifacts")
	}
	if d.Distribution.VersionRequirement != "" && len(d.Distribution.VersionCommand) == 0 {
		return nil, fmt.Errorf("external module definition: version_requirement requires version_command")
	}
	if d.Distribution.VersionRequirement != "" {
		if _, _, ok := parseVersionRequirement(d.Distribution.VersionRequirement); !ok {
			return nil, fmt.Errorf("external module definition: invalid version_requirement %q", d.Distribution.VersionRequirement)
		}
	}
	encodedDefinition, err := json.Marshal(&d)
	if err != nil {
		return nil, fmt.Errorf("external module definition: normalize: %w", err)
	}
	var normalizedDefinition ExternalModuleDefinition
	if err := json.Unmarshal(encodedDefinition, &normalizedDefinition); err != nil {
		return nil, fmt.Errorf("external module definition: normalize: %w", err)
	}
	sort.Strings(normalizedDefinition.Capabilities.Secrets)
	for i := range normalizedDefinition.Actions {
		sort.Strings(normalizedDefinition.Actions[i].Failures)
		sort.Strings(normalizedDefinition.Actions[i].Effects)
		sort.Strings(normalizedDefinition.Actions[i].Targets)
		sort.Ints(normalizedDefinition.Actions[i].Command.ExitCodes)
	}
	sort.Slice(normalizedDefinition.Distribution.Artifacts, func(i, j int) bool {
		return normalizedDefinition.Distribution.Artifacts[i].Target < normalizedDefinition.Distribution.Artifacts[j].Target
	})
	normalized, err := json.Marshal(&normalizedDefinition)
	if err != nil {
		return nil, fmt.Errorf("external module definition: normalize: %w", err)
	}
	sum := sha256.Sum256(normalized)
	d.digest = fmt.Sprintf("sha256:%x", sum[:])
	d.clients = map[*externalSessionKey]*stdioClient{}
	d.clientsGate = make(chan struct{}, 1)
	d.clientsGate <- struct{}{}
	return &d, nil
}

func (d *ExternalModuleDefinition) lockClients(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.clientsGate:
	}
	if err := ctx.Err(); err != nil {
		d.clientsGate <- struct{}{}
		return err
	}
	return nil
}

func (d *ExternalModuleDefinition) unlockClients() { d.clientsGate <- struct{}{} }

func (d *ExternalModuleDefinition) GenerateInterface() string {
	var b strings.Builder
	for _, typ := range d.Types {
		fmt.Fprintf(&b, "define %s:\n", typ.Name)
		for _, f := range typ.Fields {
			fmt.Fprintf(&b, "  %s as %s\n", f.Name, f.Type)
		}
	}
	for _, failure := range d.Failures {
		fmt.Fprintf(&b, "define failure %s:\n", failure.Name)
		for _, f := range failure.Fields {
			fmt.Fprintf(&b, "  %s as %s\n", f.Name, f.Type)
		}
	}
	for _, a := range d.Actions {
		fmt.Fprintf(&b, "to %s", a.Name)
		for i, p := range a.Parameters {
			if i == 0 {
				fmt.Fprintf(&b, " with ")
			} else {
				fmt.Fprintf(&b, ", ")
			}
			fmt.Fprintf(&b, "%s as %s", p.Name, p.Type)
		}
		if a.Result.Type != "" {
			fmt.Fprintf(&b, " returning %s", a.Result.Type)
		}
		if len(a.Failures) > 0 {
			fmt.Fprintf(&b, " may fail with %s", strings.Join(a.Failures, ", "))
		}
		b.WriteString(":\n")
		b.WriteString("  # implemented by external command\n")
	}
	return b.String()
}

func (d *ExternalModuleDefinition) Digest() string { return d.digest }

// ValidateInterface verifies every declared record, failure, action parameter,
// and result type without launching the external runtime.
func (d *ExternalModuleDefinition) ValidateInterface() error {
	_, err := d.module()
	return err
}

// DoctorExternalModule performs the explicitly requested launch preflight. It
// handshakes stdio plugins and only resolves command-adapter executables.
func DoctorExternalModule(ctx context.Context, dir, modulePath string) error {
	d, err := ResolveExternalModuleDefinition(dir, modulePath)
	if err != nil {
		return err
	}
	if err := d.ValidateInterface(); err != nil {
		return err
	}
	config, err := EffectiveConfig("", dir)
	if err != nil {
		return err
	}
	budget, _ := NewRequestBudget(config.Requests, nil)
	session := newExternalSessionKey()
	opts := Options{Dir: dir, Config: &config, Budget: budget, Stderr: io.Discard, externalSession: session}
	if _, err := d.authorize(opts); err != nil {
		return err
	}
	for runtimeName, requirement := range d.Runtime.Requires {
		program := runtimeName
		if len(d.Runtime.Command) > 0 && strings.HasPrefix(filepath.Base(d.Runtime.Command[0]), runtimeName) {
			program = d.Runtime.Command[0]
		}
		resolved, err := exec.LookPath(program)
		if err != nil {
			return fmt.Errorf("module %s requires %s %s; %s was not found", d.Module.Path, runtimeName, requirement, program)
		}
		probe := exec.CommandContext(ctx, resolved, "--version")
		probe.Env = []string{"PATH=" + os.Getenv("PATH")}
		output, err := probe.CombinedOutput()
		if err != nil {
			return fmt.Errorf("module %s %s version probe: %w", d.Module.Path, runtimeName, err)
		}
		version := firstSemanticVersion(string(output))
		if version == "" || !versionSatisfies(version, requirement) {
			return fmt.Errorf("module %s requires %s %s; found %s", d.Module.Path, runtimeName, requirement, version)
		}
	}
	if d.Distribution.Mode == "external" && len(d.Distribution.VersionCommand) > 0 {
		command := d.Distribution.VersionCommand
		probe := exec.CommandContext(ctx, command[0], command[1:]...)
		probe.Dir, probe.Env = dir, []string{"PATH=" + os.Getenv("PATH")}
		output, err := probe.CombinedOutput()
		if err != nil {
			return fmt.Errorf("module %s version probe: %w", d.Module.Path, err)
		}
		version := firstSemanticVersion(string(output))
		if version == "" {
			return fmt.Errorf("module %s version probe returned no semantic version", d.Module.Path)
		}
		if requirement := d.Distribution.VersionRequirement; requirement != "" && !versionSatisfies(version, requirement) {
			return fmt.Errorf("module %s version %s does not satisfy %s", d.Module.Path, version, requirement)
		}
	}
	if d.Runtime.Kind == "command" {
		for _, action := range d.Actions {
			if _, err := exec.LookPath(action.Command.Program); err != nil {
				return fmt.Errorf("module %s command %s: %w", d.Module.Path, action.Command.Program, err)
			}
		}
		return nil
	}
	client, err := d.startStdio(ctx, opts)
	if err != nil {
		return err
	}
	if err := d.lockClients(ctx); err != nil {
		client.terminate()
		return err
	}
	d.clients[session] = client
	d.unlockClients()
	d.closeSession(session)
	return nil
}

func firstSemanticVersion(value string) string {
	for _, field := range strings.Fields(value) {
		candidate := strings.Trim(strings.TrimPrefix(field, "v"), ",;()[]")
		parts := strings.SplitN(candidate, "-", 2)
		segments := strings.Split(parts[0], ".")
		if len(segments) < 2 || len(segments) > 3 {
			continue
		}
		valid := true
		for _, segment := range segments {
			if segment == "" || strings.Trim(segment, "0123456789") != "" {
				valid = false
			}
		}
		if valid {
			return candidate
		}
	}
	return ""
}

func versionSatisfies(version, requirement string) bool {
	operator, required, ok := parseVersionRequirement(requirement)
	if !ok {
		return false
	}
	compare := compareSemanticVersions(version, required)
	switch operator {
	case ">=":
		return compare >= 0
	case "<=":
		return compare <= 0
	case ">":
		return compare > 0
	case "<":
		return compare < 0
	default:
		return compare == 0
	}
}

func parseVersionRequirement(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	operator := "="
	for _, prefix := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(value, prefix) {
			operator, value = prefix, strings.TrimSpace(strings.TrimPrefix(value, prefix))
			break
		}
	}
	_, ok := parseSemanticVersion(value)
	return operator, value, ok
}

type semanticVersion struct {
	parts      [3]int
	prerelease []string
}

func parseSemanticVersion(value string) (semanticVersion, bool) {
	var parsed semanticVersion
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	mainAndPre := strings.SplitN(strings.SplitN(value, "+", 2)[0], "-", 2)
	segments := strings.Split(mainAndPre[0], ".")
	if len(segments) < 2 || len(segments) > 3 {
		return parsed, false
	}
	for i, segment := range segments {
		if segment == "" || strings.Trim(segment, "0123456789") != "" {
			return parsed, false
		}
		if len(segment) > 1 && strings.HasPrefix(segment, "0") {
			return semanticVersion{}, false
		}
		n, err := strconv.Atoi(segment)
		if err != nil {
			return parsed, false
		}
		parsed.parts[i] = n
	}
	if len(mainAndPre) == 2 {
		if mainAndPre[1] == "" {
			return parsed, false
		}
		parsed.prerelease = strings.Split(mainAndPre[1], ".")
		for _, identifier := range parsed.prerelease {
			if identifier == "" || strings.Trim(identifier, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-") != "" {
				return semanticVersion{}, false
			}
		}
	}
	return parsed, true
}

func compareSemanticVersions(left, right string) int {
	a, aOK := parseSemanticVersion(left)
	b, bOK := parseSemanticVersion(right)
	if !aOK || !bOK {
		return 0
	}
	for i := range a.parts {
		if a.parts[i] < b.parts[i] {
			return -1
		}
		if a.parts[i] > b.parts[i] {
			return 1
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) > 0 {
		return 1
	}
	if len(a.prerelease) > 0 && len(b.prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(a.prerelease) && i < len(b.prerelease); i++ {
		if a.prerelease[i] == b.prerelease[i] {
			continue
		}
		aNumber, aErr := strconv.Atoi(a.prerelease[i])
		bNumber, bErr := strconv.Atoi(b.prerelease[i])
		if aErr == nil && bErr == nil {
			if aNumber < bNumber {
				return -1
			}
			return 1
		}
		if aErr == nil {
			return -1
		}
		if bErr == nil {
			return 1
		}
		if a.prerelease[i] < b.prerelease[i] {
			return -1
		}
		return 1
	}
	if len(a.prerelease) < len(b.prerelease) {
		return -1
	}
	if len(a.prerelease) > len(b.prerelease) {
		return 1
	}
	return 0
}

func (d *ExternalModuleDefinition) module() (*Module, error) {
	m := &Module{Key: "external:" + d.Module.Path, Name: d.Module.Path, Actions: map[string]*Statement{}, Schemas: map[string]*Statement{}, Definitions: map[string]*RecordDef{}, Failures: map[string]*FailureDef{}, Native: map[string]NativeOp{}, Exports: map[string]bool{}, Words: map[string]string{}, external: d}
	for _, externalType := range d.Types {
		if externalType.Name == "" || m.Definitions[externalType.Name] != nil {
			return nil, fmt.Errorf("external type names must be nonempty and unique")
		}
		def := &RecordDef{Name: externalType.Name}
		for _, field := range externalType.Fields {
			typ, err := parseType(field.Type, true)
			if err != nil {
				return nil, fmt.Errorf("type %s field %s: %w", externalType.Name, field.Name, err)
			}
			def.Fields = append(def.Fields, RecordField{Name: field.Name, Type: typ})
		}
		m.Definitions[externalType.Name], m.Exports[externalType.Name] = def, true
	}
	for _, failure := range d.Failures {
		def := &FailureDef{Name: failure.Name}
		fieldNames := map[string]bool{}
		for _, field := range failure.Fields {
			if field.Name == "" || fieldNames[field.Name] {
				return nil, fmt.Errorf("failure %s field names must be nonempty and unique", failure.Name)
			}
			fieldNames[field.Name] = true
			typ, err := parseType(field.Type, true)
			if err != nil {
				return nil, fmt.Errorf("failure %s field %s: %w", failure.Name, field.Name, err)
			}
			if err := validateTypeRefs(typ, m.Definitions, map[string]bool{}); err != nil {
				return nil, fmt.Errorf("failure %s field %s: %w", failure.Name, field.Name, err)
			}
			def.Fields = append(def.Fields, RecordField{Name: field.Name, Type: typ})
		}
		m.Failures[failure.Name], m.Exports[failure.Name] = def, true
	}
	for _, a := range d.Actions {
		params := make([]NativeParam, 0, len(a.Parameters))
		for _, p := range a.Parameters {
			typ, err := parseType(p.Type, true)
			if err != nil {
				return nil, fmt.Errorf("action %s parameter %s: %w", a.Name, p.Name, err)
			}
			if err := validateTypeRefs(typ, m.Definitions, map[string]bool{}); err != nil {
				return nil, fmt.Errorf("action %s parameter %s: %w", a.Name, p.Name, err)
			}
			params = append(params, NativeParam{Name: p.Name, Type: p.Type})
		}
		if a.Result.Type != "" && a.Result.Type != "none" && a.Result.Type != "any" {
			typ, err := parseType(a.Result.Type, true)
			if err != nil {
				return nil, fmt.Errorf("action %s result: %w", a.Name, err)
			}
			if err := validateTypeRefs(typ, m.Definitions, map[string]bool{}); err != nil {
				return nil, fmt.Errorf("action %s result: %w", a.Name, err)
			}
		}
		action := a
		m.Native[a.Name] = NativeOp{Name: a.Name, Params: params, Result: a.Result.Type, Targets: append([]string(nil), a.Targets...), Effects: append([]string(nil), a.Effects...), Description: a.Description, PossibleFailures: append([]string(nil), a.Failures...), ContextFn: func(ctx context.Context, opts Options, args []any) (any, error) {
			if d.Runtime.Kind == "stdio" {
				return d.invokeStdio(ctx, opts, action, args)
			}
			return d.runCommand(ctx, opts, action, args)
		}}
		m.Exports[a.Name], m.Words[a.Name] = true, a.Name
	}
	if errors := validateRecordDefinitions(m.Definitions); len(errors) > 0 {
		return nil, fmt.Errorf("external module definitions: %s", strings.Join(errors, "; "))
	}
	return m, nil
}

func (d *ExternalModuleDefinition) runCommand(ctx context.Context, opts Options, a ExternalAction, values []any) (any, error) {
	environment, err := d.authorize(opts)
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	module, moduleErr := d.module()
	if moduleErr != nil {
		return nil, moduleErr
	}
	for i, p := range a.Parameters {
		typ, _ := parseType(p.Type, true)
		params[p.Name] = externalEncodeValue(values[i], typ, module.Definitions)
	}
	argv := make([]string, 0, len(a.Command.Arguments)+len(d.Distribution.Arguments))
	if d.bundleProgram != "" {
		argv = append(argv, d.Distribution.Arguments...)
	}
	for _, raw := range a.Command.Arguments {
		if strings.HasPrefix(raw, "${") {
			value := params[strings.TrimSuffix(strings.TrimPrefix(raw, "${"), "}")]
			if text, ok := value.(string); ok {
				raw = text
			} else {
				encoded, encodeErr := json.Marshal(value)
				if encodeErr != nil {
					return nil, fmt.Errorf("module %s command argument: %w", d.Module.Path, encodeErr)
				}
				raw = string(encoded)
			}
		}
		argv = append(argv, raw)
	}
	if a.Timeout != "" {
		duration, _ := time.ParseDuration(a.Timeout)
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	program := a.Command.Program
	if d.bundleProgram != "" {
		program, err = d.verifiedBundleProgram()
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(program, argv...)
	configureProcessTree(cmd)
	cmd.Env = environment
	cmd.Dir = opts.Dir
	if d.Runtime.WorkingDirectory == "${definition_dir}" {
		cmd.Dir = d.definitionDirectory()
	} else if d.Runtime.WorkingDirectory != "" && d.Runtime.WorkingDirectory != "${workspace}" {
		return nil, fmt.Errorf("module %s has unsupported working_directory %s", d.Module.Path, d.Runtime.WorkingDirectory)
	}
	if p := a.Command.WorkingDirectoryParameter; p != "" {
		declaredFolder := false
		for _, parameter := range a.Parameters {
			declaredFolder = declaredFolder || (parameter.Name == p && parameter.Type == "folder")
		}
		if !declaredFolder {
			return nil, fmt.Errorf("module %s working directory parameter %s must be declared as folder", d.Module.Path, p)
		}
		candidate, pathErr := filepath.Abs(fmt.Sprint(params[p]))
		if pathErr != nil {
			return nil, pathErr
		}
		workspace, _ := filepath.Abs(opts.Dir)
		resolvedCandidate, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr != nil {
			return nil, resolveErr
		}
		resolvedWorkspace, resolveErr := filepath.EvalSymlinks(workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if opts.Config.ExternalFilesystem != "explicit" && resolvedCandidate != resolvedWorkspace && !strings.HasPrefix(resolvedCandidate, resolvedWorkspace+string(filepath.Separator)) {
			return nil, fmt.Errorf("module %s working directory is outside the workspace", d.Module.Path)
		}
		cmd.Dir = resolvedCandidate
	}
	if parameter := a.Command.StdinParameter; parameter != "" {
		value, ok := params[parameter]
		if !ok {
			return nil, fmt.Errorf("module %s stdin parameter %s is not declared", d.Module.Path, parameter)
		}
		switch a.Command.Stdin {
		case "", "text":
			cmd.Stdin = strings.NewReader(fmt.Sprint(value))
		case "json":
			encoded, encodeErr := json.Marshal(value)
			if encodeErr != nil {
				return nil, encodeErr
			}
			cmd.Stdin = bytes.NewReader(append(encoded, '\n'))
		default:
			return nil, fmt.Errorf("module %s has unsupported stdin mode %s", d.Module.Path, a.Command.Stdin)
		}
	} else if a.Command.Stdin != "" && a.Command.Stdin != "none" {
		return nil, fmt.Errorf("module %s stdin mode requires stdin_parameter", d.Module.Path)
	}
	stdout, stderr := newExternalOutput(d.Capabilities.Secrets), newExternalOutput(d.Capabilities.Secrets)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Start(); err == nil {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			_ = killProcessTree(cmd)
			<-done
			return nil, ctx.Err()
		}
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("external command output exceeds 1 MiB")
	}
	stderrText := redactExternalOutput(stderr.String(), d.Capabilities.Secrets)
	switch a.Command.Stderr {
	case "", "diagnostic":
		if stderrText != "" && opts.Stderr != nil {
			_, _ = io.WriteString(opts.Stderr, stderrText)
		}
	case "discard":
	case "merge":
		_, _ = stdout.Write([]byte(stderrText))
		if stdout.exceeded {
			return nil, fmt.Errorf("external command output exceeds 1 MiB")
		}
	default:
		return nil, fmt.Errorf("module %s has unsupported stderr mode %s", d.Module.Path, a.Command.Stderr)
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			return nil, err
		}
	}
	allowed := a.Command.ExitCodes
	if len(allowed) == 0 {
		allowed = []int{0}
	}
	if !containsInt(allowed, code) {
		return nil, fmt.Errorf("external command exited %d", code)
	}
	decode := func(value any) (any, error) { return externalDecodeResult(value, a.Result.Type, module.Definitions) }
	switch a.Command.Stdout {
	case "none":
		return nil, nil
	case "json":
		var value any
		if err := decodeExternalJSON(stdout.Bytes(), &value); err != nil {
			return nil, fmt.Errorf("external command returned invalid JSON: %w", err)
		}
		return decode(value)
	case "json-lines":
		values := []any{}
		scanner := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
		for scanner.Scan() {
			var value any
			if err := decodeExternalJSON(scanner.Bytes(), &value); err != nil {
				return nil, fmt.Errorf("external command returned invalid JSON line: %w", err)
			}
			values = append(values, value)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return decode(values)
	case "", "text":
		return decode(strings.TrimSuffix(stdout.String(), "\n"))
	default:
		return nil, fmt.Errorf("module %s has unsupported stdout mode %s", d.Module.Path, a.Command.Stdout)
	}
}

func redactExternalOutput(value string, secretNames []string) string {
	for _, name := range secretNames {
		if secret, ok := os.LookupEnv(name); ok && secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func redactExternalValue(value any, secretNames []string) any {
	switch current := value.(type) {
	case string:
		return redactExternalOutput(current, secretNames)
	case []any:
		out := make([]any, len(current))
		for i, item := range current {
			out[i] = redactExternalValue(item, secretNames)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			out[redactExternalOutput(key, secretNames)] = redactExternalValue(item, secretNames)
		}
		return out
	default:
		return value
	}
}

type externalOutput struct {
	bytes.Buffer
	limit    int
	reportAt int
	exceeded bool
}

func newExternalOutput(secretNames []string) *externalOutput {
	const reportAt = 1 << 20
	lookahead := 0
	for _, name := range secretNames {
		if secret, ok := os.LookupEnv(name); ok && len(secret) > lookahead {
			lookahead = len(secret)
		}
	}
	return &externalOutput{limit: reportAt + lookahead, reportAt: reportAt}
}

func (b *externalOutput) Write(p []byte) (int, error) {
	original := len(p)
	if b.reportAt > 0 && b.Len()+len(p) > b.reportAt {
		b.exceeded = true
	}
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return original, nil
	}
	if len(p) > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}

func (b *externalOutput) redacted(secretNames []string) string {
	value := redactExternalOutput(b.String(), secretNames)
	if b.reportAt > 0 && len(value) > b.reportAt {
		value = value[:b.reportAt]
	}
	return value
}

func containsInt(values []int, value int) bool {
	sort.Ints(values)
	i := sort.SearchInts(values, value)
	return i < len(values) && values[i] == value
}

func currentExternalTarget() string {
	osName, arch := stdruntime.GOOS, stdruntime.GOARCH
	if osName == "windows" {
		osName = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	return osName + "-" + arch
}

type stdioClient struct {
	mu         sync.Mutex
	cmd        *exec.Cmd
	in         io.WriteCloser
	out        *bufio.Reader
	nextID     int
	definition *ExternalModuleDefinition
	ctx        context.Context
	deadline   time.Time
	stderr     *externalOutput
	stderrSink io.Writer
	stderrOnce sync.Once
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	} `json:"error"`
}

func (d *ExternalModuleDefinition) invokeStdio(ctx context.Context, opts Options, action ExternalAction, values []any) (any, error) {
	if action.Timeout != "" {
		duration, err := time.ParseDuration(action.Timeout)
		if err != nil {
			return nil, fmt.Errorf("module %s action %s timeout: %w", d.Module.Path, action.Name, err)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	if err := d.lockClients(ctx); err != nil {
		return nil, err
	}
	client := d.clients[opts.externalSession]
	if client == nil {
		var err error
		client, err = d.startStdio(ctx, opts)
		if err != nil {
			d.unlockClients()
			return nil, err
		}
		d.clients[opts.externalSession] = client
	}
	d.unlockClients()
	arguments := map[string]any{}
	module, err := d.module()
	if err != nil {
		return nil, err
	}
	for i, parameter := range action.Parameters {
		typ, _ := parseType(parameter.Type, true)
		arguments[parameter.Name] = externalEncodeValue(values[i], typ, module.Definitions)
	}
	var result struct {
		Value any `json:"value"`
	}
	if err := client.call(ctx, "invoke", map[string]any{"action": action.Name, "arguments": arguments, "context": map[string]any{"workingDirectory": opts.Dir}}, &result); err != nil {
		var declared *typedFailure
		if errors.As(err, &declared) {
			return nil, err
		}
		client.terminate()
		if lockErr := d.lockClients(context.Background()); lockErr != nil {
			return nil, lockErr
		}
		if d.clients[opts.externalSession] == client {
			delete(d.clients, opts.externalSession)
		}
		d.unlockClients()
		return nil, err
	}
	return externalDecodeResult(result.Value, action.Result.Type, module.Definitions)
}

func externalEncodeValue(value any, typ TypeRef, defs map[string]*RecordDef) any {
	if value == nil {
		return nil
	}
	if typ.Element != nil {
		items, _ := value.([]any)
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = externalEncodeValue(item, *typ.Element, defs)
		}
		return out
	}
	if typ.Name == "duration" {
		if duration, ok := value.(time.Duration); ok {
			return duration.String()
		}
	}
	if typ.Name == "timestamp" {
		if timestamp, ok := value.(time.Time); ok {
			return timestamp.Format(time.RFC3339Nano)
		}
	}
	if definition := defs[typ.Name]; definition != nil {
		object, _ := value.(map[string]any)
		out := make(map[string]any, len(object))
		fields := fieldsByName(definition)
		for name, item := range object {
			out[name] = externalEncodeValue(item, fields[name].Type, defs)
		}
		return out
	}
	return value
}

func externalDecodeResult(value any, typeName string, defs map[string]*RecordDef) (any, error) {
	if typeName == "" || typeName == "any" {
		return normalizeExternalAny(value)
	}
	if typeName == "none" {
		return value, nil
	}
	typ, err := parseType(typeName, true)
	if err != nil {
		return nil, err
	}
	decoded, err := externalDecodeValue(value, typ, defs)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func normalizeExternalAny(value any) (any, error) {
	switch current := value.(type) {
	case json.Number:
		if !strings.ContainsAny(current.String(), ".eE") {
			integer, err := current.Int64()
			if err != nil || integer < -(1<<53-1) || integer > 1<<53-1 {
				return nil, fmt.Errorf("integer is outside the exact JSON range")
			}
			return float64(integer), nil
		}
		number, err := current.Float64()
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("invalid finite number")
		}
		if math.Trunc(number) == number && (number < -(1<<53-1) || number > 1<<53-1) {
			return nil, fmt.Errorf("integer is outside the exact JSON range")
		}
		return number, nil
	case []any:
		out := make([]any, len(current))
		for i, item := range current {
			normalized, err := normalizeExternalAny(item)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			normalized, err := normalizeExternalAny(item)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

func externalDecodeValue(value any, typ TypeRef, defs map[string]*RecordDef) (any, error) {
	if value == nil && typ.Optional {
		return nil, nil
	}
	if typ.Element != nil {
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("expected %s", typ.String())
		}
		out := make([]any, len(items))
		for i, item := range items {
			decoded, err := externalDecodeValue(item, *typ.Element, defs)
			if err != nil {
				return nil, err
			}
			out[i] = decoded
		}
		return out, nil
	}
	if typ.Name == "duration" {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected duration string")
		}
		return time.ParseDuration(text)
	}
	if typ.Name == "timestamp" {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected RFC3339 timestamp string")
		}
		return time.Parse(time.RFC3339Nano, text)
	}
	if typ.Name == "integer" {
		if numberValue, ok := value.(json.Number); ok {
			integer, err := numberValue.Int64()
			if err != nil || integer > 1<<53-1 || integer < -(1<<53-1) {
				return nil, fmt.Errorf("integer is outside the exact JSON range")
			}
			return integer, nil
		}
	}
	if typ.Name == "any" {
		return normalizeExternalAny(value)
	}
	if typ.Name == "number" {
		if numberValue, ok := value.(json.Number); ok {
			number, err := numberValue.Float64()
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("invalid finite number")
			}
			return number, nil
		}
	}
	if definition := defs[typ.Name]; definition != nil {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected %s record", typ.Name)
		}
		out := make(map[string]any, len(object))
		fields := fieldsByName(definition)
		for name, item := range object {
			field, exists := fields[name]
			if !exists {
				return nil, fmt.Errorf("%s contains undeclared field %s", typ.Name, name)
			}
			decoded, err := externalDecodeValue(item, field.Type, defs)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", typ.Name, name, err)
			}
			out[name] = decoded
		}
		return out, nil
	}
	return value, nil
}

func (d *ExternalModuleDefinition) startStdio(ctx context.Context, opts Options) (*stdioClient, error) {
	environment, err := d.authorize(opts)
	if err != nil {
		return nil, err
	}
	command := append([]string(nil), d.Runtime.Command...)
	if d.bundleProgram != "" {
		program, verifyErr := d.verifiedBundleProgram()
		err = verifyErr
		if err != nil {
			return nil, err
		}
		command = append([]string{program}, d.Distribution.Arguments...)
	}
	// A plugin belongs to the whole run session. A worker-local context (for
	// example parallel map's join context) must not kill a process retained for
	// later calls in that same run; invoke cancellation still terminates it.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), command[0], command[1:]...)
	configureProcessTree(cmd)
	cmd.Dir = opts.Dir
	if d.Runtime.WorkingDirectory == "${definition_dir}" {
		cmd.Dir = d.definitionDirectory()
	}
	cmd.Env = environment
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	pluginStderr := newExternalOutput(d.Capabilities.Secrets)
	cmd.Stderr = pluginStderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("module %s start: %w", d.Module.Path, err)
	}
	deadline, _ := ctx.Deadline()
	client := &stdioClient{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 64<<10), nextID: 1, definition: d, ctx: ctx, deadline: deadline, stderr: pluginStderr, stderrSink: opts.Stderr}
	var initialized struct{ Protocol, Module, Version, DefinitionDigest string }
	params := map[string]any{"protocol": "sos-plugin/1", "module": d.Module.Path, "version": d.Module.Version, "definitionDigest": d.digest, "host": map[string]any{"sosVersion": Version}, "limits": map[string]any{"maxMessageBytes": 1 << 20, "maxInFlight": 1}}
	startupCtx, startupCancel, err := timeoutContext(ctx, d.Runtime.StartupTimeout, 10*time.Second)
	if err != nil {
		client.terminate()
		return nil, fmt.Errorf("module %s startup timeout: %w", d.Module.Path, err)
	}
	defer startupCancel()
	if err := client.call(startupCtx, "initialize", params, &initialized); err != nil {
		client.terminate()
		return nil, fmt.Errorf("module %s initialize: %w", d.Module.Path, err)
	}
	if initialized.Protocol != "sos-plugin/1" || initialized.Module != d.Module.Path || initialized.Version != d.Module.Version || initialized.DefinitionDigest != d.digest {
		client.terminate()
		return nil, fmt.Errorf("module %s initialize identity mismatch", d.Module.Path)
	}
	return client, nil
}

func timeoutContext(parent context.Context, configured string, fallback time.Duration) (context.Context, context.CancelFunc, error) {
	duration := fallback
	if configured != "" {
		parsed, err := time.ParseDuration(configured)
		if err != nil {
			return nil, nil, err
		}
		duration = parsed
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	return ctx, cancel, nil
}

func (d *ExternalModuleDefinition) resolveBundleProgram() string {
	executable, err := os.Executable()
	if err != nil {
		return d.bundleProgram
	}
	return filepath.Join(filepath.Dir(executable), filepath.FromSlash(d.bundleProgram))
}

func (d *ExternalModuleDefinition) definitionDirectory() string {
	if d.bundleProgram != "" {
		return filepath.Dir(d.resolveBundleProgram())
	}
	if d.embedded {
		if executable, err := os.Executable(); err == nil {
			return filepath.Dir(executable)
		}
	}
	return filepath.Dir(d.definitionPath)
}

func (d *ExternalModuleDefinition) verifiedBundleProgram() (string, error) {
	program := d.resolveBundleProgram()
	executable, executableErr := os.Executable()
	if executableErr != nil {
		return "", executableErr
	}
	manifestData, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "manifest.json"))
	if err != nil {
		return "", fmt.Errorf("module %s locked manifest: %w", d.Module.Path, err)
	}
	var manifest struct {
		Schema, DefinitionSchema                                    int
		Application, Entrypoint, SOSVersion, PluginProtocol, Target string
		Modules                                                     []struct {
			Path, Version, VersionRequirement, DefinitionDigest, Artifact, SHA256, Runtime, Distribution, Protocol string
			Arguments                                                                                              []string
			Capabilities                                                                                           ExternalCapabilitiesSpec
			Effects                                                                                                []string
		} `json:"modules"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil || manifest.Schema != 1 || manifest.DefinitionSchema != 1 || manifest.Application != filepath.Base(executable) || manifest.Entrypoint != filepath.Base(executable) || manifest.SOSVersion != Version || manifest.PluginProtocol != "sos-plugin/1" || manifest.Target != currentExternalTarget() {
		return "", fmt.Errorf("module %s locked manifest is invalid", d.Module.Path)
	}
	effects := map[string]bool{}
	for _, action := range d.Actions {
		for _, effect := range action.Effects {
			effects[effect] = true
		}
	}
	expectedEffects := make([]string, 0, len(effects))
	for effect := range effects {
		expectedEffects = append(expectedEffects, effect)
	}
	sort.Strings(expectedEffects)
	expectedCapabilities := ExternalCapabilitiesSpec{Process: d.Capabilities.Process, Network: d.Capabilities.Network, Filesystem: d.Capabilities.Filesystem, Secrets: append([]string(nil), d.Capabilities.Secrets...)}
	expectedCapabilitiesJSON, _ := json.Marshal(expectedCapabilities)
	locked := false
	for _, module := range manifest.Modules {
		capabilitiesJSON, _ := json.Marshal(module.Capabilities)
		if module.Path == d.Module.Path && module.Version == d.Module.Version && module.VersionRequirement == d.Distribution.VersionRequirement && slices.Equal(module.Arguments, d.Distribution.Arguments) && module.DefinitionDigest == d.digest && module.Artifact == filepath.ToSlash(d.bundleProgram) && strings.EqualFold(strings.TrimPrefix(module.SHA256, "sha256:"), strings.TrimPrefix(d.bundleSHA256, "sha256:")) && module.Runtime == d.Runtime.Kind && module.Distribution == "bundled" && module.Protocol == d.Runtime.Protocol && bytes.Equal(capabilitiesJSON, expectedCapabilitiesJSON) && slices.Equal(module.Effects, expectedEffects) {
			locked = true
			break
		}
	}
	if !locked {
		return "", fmt.Errorf("module %s is not locked by manifest", d.Module.Path)
	}
	data, err := os.ReadFile(program)
	if err != nil {
		return "", err
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	expected := strings.TrimPrefix(d.bundleSHA256, "sha256:")
	if expected == "" || !strings.EqualFold(sum, expected) {
		return "", fmt.Errorf("module %s bundled artifact checksum mismatch", d.Module.Path)
	}
	return program, nil
}

func (d *ExternalModuleDefinition) authorize(opts Options) ([]string, error) {
	if opts.Config == nil {
		return nil, fmt.Errorf("module %s cannot resolve external capability policy", d.Module.Path)
	}
	policy := opts.Config
	if d.Capabilities.Process && !policy.ExternalProcess {
		return nil, fmt.Errorf("module %s requests process capability, denied by project policy", d.Module.Path)
	}
	if d.Capabilities.Network && !policy.ExternalNetwork {
		return nil, fmt.Errorf("module %s requests network capability, denied by project policy", d.Module.Path)
	}
	ranks := map[string]int{"none": 0, "workspace-read": 1, "workspace": 2, "explicit": 3}
	requested := d.Capabilities.Filesystem
	if requested == "" {
		requested = "none"
	}
	if ranks[requested] > ranks[policy.ExternalFilesystem] {
		return nil, fmt.Errorf("module %s requests %s filesystem access, denied by project policy", d.Module.Path, requested)
	}
	allowed := map[string]bool{}
	for _, name := range policy.ExternalSecrets {
		allowed[name] = true
	}
	environment := []string{"PATH=" + os.Getenv("PATH")}
	for _, name := range d.Capabilities.Secrets {
		if !allowed[name] {
			return nil, fmt.Errorf("module %s requests secret %s, denied by project policy", d.Module.Path, name)
		}
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		} else {
			return nil, fmt.Errorf("module %s requires secret %s, but it is not configured", d.Module.Path, name)
		}
	}
	return environment, nil
}

func (c *stdioClient) call(ctx context.Context, method string, params any, result any) error {
	for !c.mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID
	c.nextID++
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return fmt.Errorf("plugin request encoding: %w", err)
	}
	if len(request)+1 > 1<<20 {
		return fmt.Errorf("plugin request exceeds 1 MiB")
	}
	var admissionMu sync.Mutex
	canceled := false
	stopCancellation := context.AfterFunc(ctx, func() {
		admissionMu.Lock()
		canceled = true
		admissionMu.Unlock()
	})
	defer stopCancellation()
	writeDone := make(chan error, 1)
	go func() {
		admissionMu.Lock()
		defer admissionMu.Unlock()
		if canceled || ctx.Err() != nil {
			writeDone <- ctx.Err()
			return
		}
		_, writeErr := c.in.Write(append(request, '\n'))
		writeDone <- writeErr
	}()
	select {
	case <-ctx.Done():
		c.terminate()
		<-writeDone
		return ctx.Err()
	case err := <-writeDone:
		if err != nil {
			return err
		}
	}
	type readResult struct {
		line []byte
		err  error
	}
	read := make(chan readResult, 1)
	go func() { line, err := readProtocolLine(c.out, 1<<20); read <- readResult{line, err} }()
	var incoming readResult
	select {
	case <-ctx.Done():
		if method == "invoke" {
			cancel, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "cancel", "params": map[string]any{"id": id}})
			cancelWritten := make(chan struct{}, 1)
			go func() {
				_, _ = c.in.Write(append(cancel, '\n'))
				cancelWritten <- struct{}{}
			}()
			select {
			case <-cancelWritten:
			case <-time.After(50 * time.Millisecond):
			}
		}
		// A canceled request has no safe response boundary in the line-oriented
		// protocol. Retire the process and join its sole response reader before
		// releasing the mutex so a later call can never consume this response.
		c.terminate()
		<-read
		return ctx.Err()
	case incoming = <-read:
	}
	if incoming.err != nil {
		return incoming.err
	}
	if !utf8.Valid(incoming.line) {
		return fmt.Errorf("plugin response is not valid UTF-8")
	}
	var response rpcResponse
	if err := decodeExternalJSON(incoming.line, &response); err != nil {
		return fmt.Errorf("plugin wrote non-protocol data: %w", err)
	}
	if response.JSONRPC != "2.0" || response.ID != id {
		return fmt.Errorf("plugin response id mismatch")
	}
	if response.Error != nil {
		kind, _ := response.Error.Data["kind"].(string)
		if kind != "" && containsString(c.definition.definitionFailureNames(method, params), kind) {
			message := redactExternalOutput(response.Error.Message, c.definition.Capabilities.Secrets)
			value := map[string]any{"kind": kind, "message": message, "retryable": false, "module": c.definition.Module.Path, "phase": "invoke"}
			if retryable, ok := response.Error.Data["retryable"].(bool); ok {
				value["retryable"] = retryable
			}
			payloadValue, payloadPresent := response.Error.Data["payload"]
			if payload, ok := payloadValue.(map[string]any); ok {
				if err := c.definition.validateFailurePayload(kind, payload); err != nil {
					return fmt.Errorf("plugin failure payload: %w", err)
				}
				for key, item := range payload {
					value[key] = redactExternalValue(item, c.definition.Capabilities.Secrets)
				}
			} else if payloadPresent {
				return fmt.Errorf("plugin failure payload must be an object")
			} else if err := c.definition.validateFailurePayload(kind, nil); err != nil {
				return fmt.Errorf("plugin failure payload: %w", err)
			}
			return &typedFailure{kind: kind, value: value}
		}
		return fmt.Errorf("plugin error %d: %s", response.Error.Code, redactExternalOutput(response.Error.Message, c.definition.Capabilities.Secrets))
	}
	if result != nil {
		return decodeExternalJSON(response.Result, result)
	}
	return nil
}

func decodeExternalJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func readProtocolLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, min(limit, 64<<10))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, fmt.Errorf("plugin response exceeds 1 MiB")
		}
		line = append(line, fragment...)
		if err == nil {
			return line, nil
		}
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

func (d *ExternalModuleDefinition) validateFailurePayload(kind string, payload map[string]any) error {
	module, err := d.module()
	if err != nil {
		return err
	}
	definition := module.Failures[kind]
	if definition == nil {
		return fmt.Errorf("unknown failure %s", kind)
	}
	fields := failureFieldsByName(definition)
	for name, field := range fields {
		value, ok := payload[name]
		if !ok && field.Type.Optional {
			continue
		}
		decoded, decodeErr := externalDecodeValue(value, field.Type, module.Definitions)
		if !ok || decodeErr != nil || !typeMatchesRef(decoded, field.Type, module.Definitions) {
			return fmt.Errorf("%s.%s must be %s", kind, name, field.Type.String())
		}
		payload[name] = decoded
	}
	for name := range payload {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("%s contains undeclared field %s", kind, name)
		}
	}
	return nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func (d *ExternalModuleDefinition) definitionFailureNames(method string, params any) []string {
	if method != "invoke" {
		return nil
	}
	values, _ := params.(map[string]any)
	actionName, _ := values["action"].(string)
	for _, action := range d.Actions {
		if action.Name == actionName {
			return action.Failures
		}
	}
	return nil
}

func (c *stdioClient) kill() {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = killProcessTree(c.cmd)
	}
}

func (c *stdioClient) terminate() {
	c.kill()
	if c.cmd != nil {
		_ = c.cmd.Wait()
	}
	c.flushStderr()
}

func (c *stdioClient) flushStderr() {
	c.stderrOnce.Do(func() {
		if c.stderr == nil || c.stderrSink == nil {
			return
		}
		value := c.stderr.redacted(c.definition.Capabilities.Secrets)
		if c.stderr.exceeded {
			value += "\n[SysOneScript: plugin stderr exceeded 1 MiB and was truncated]\n"
		}
		_, _ = io.WriteString(c.stderrSink, value)
	})
}

func (d *ExternalModuleDefinition) closeSession(key *externalSessionKey) {
	if err := d.lockClients(context.Background()); err != nil {
		return
	}
	client := d.clients[key]
	delete(d.clients, key)
	d.unlockClients()
	if client == nil {
		return
	}
	parent := context.Background()
	if !client.deadline.IsZero() {
		var deadlineCancel context.CancelFunc
		parent, deadlineCancel = context.WithDeadline(parent, client.deadline)
		defer deadlineCancel()
	}
	ctx, cancel, err := timeoutContext(parent, d.Runtime.ShutdownTimeout, time.Second)
	if err != nil {
		client.terminate()
		return
	}
	defer cancel()
	_ = client.call(ctx, "shutdown", map[string]any{}, nil)
	_ = client.in.Close()
	done := make(chan struct{})
	go func() { _ = client.cmd.Wait(); close(done) }()
	select {
	case <-done:
		client.flushStderr()
	case <-ctx.Done():
		client.kill()
		<-done
		client.flushStderr()
	}
}

// ResolveExternalModuleDefinition finds a registered definition without
// loading or starting its runtime.
func ResolveExternalModuleDefinition(dir, modulePath string) (*ExternalModuleDefinition, error) {
	layers, err := sosconfig.Load(dir)
	if err != nil {
		return nil, err
	}
	for i := len(layers) - 1; i >= 0; i-- {
		base := filepath.Dir(layers[i].Name)
		for _, registration := range layers[i].Config.Module.External {
			if registration.Path != modulePath {
				continue
			}
			path := registration.Definition
			if !filepath.IsAbs(path) {
				path = filepath.Join(base, path)
			}
			definition, err := LoadExternalModuleDefinition(path)
			if err != nil {
				return nil, err
			}
			if definition.Module.Path != modulePath {
				return nil, fmt.Errorf("definition declares module path %q", definition.Module.Path)
			}
			return definition, nil
		}
	}
	return nil, fmt.Errorf("external module %q is not registered", modulePath)
}
