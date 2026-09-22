// Package sosbuild produces standalone SysOneScript artifacts: a generated main
// program plus the interpreter sources, compiled by the Go toolchain. This is
// honest embedded-interpreter packaging, not a lowering backend.
//
// Development builds use one complete source checkout; released trimpath
// builds use one embedded snapshot including config code and module metadata.
// BuildOptions permits explicit interpreter source overrides for tooling.
package sosbuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/template"

	"github.com/DonaldMurillo/system-one-playground/sos"
)

// standaloneModulePath is the module path of generated standalone programs;
// mainTemplate's sos import must match it.
const standaloneModulePath = "sosstandalone"

// Target selects the artifact platform.
type Target string

const (
	TargetNative      Target = "native"
	TargetWasmBrowser Target = "wasm-browser"
	TargetWasmWasi    Target = "wasm-wasi"
)

// BuildOptions describe one standalone build.
type BuildOptions struct {
	// Dir is the project directory for configuration; defaults to the current directory.
	Dir string
	// Program is the parsed, checked program to embed.
	Program *sos.Program
	// Resolution pins a previously reviewed interpretation.
	Resolution *sos.Analysis
	// OnAnalysis observes completed analysis, including usage on failure.
	OnAnalysis func(*sos.Analysis, error)
	// Name is the script path; its base names diagnostics in the artifact.
	Name string
	// Output is the artifact path.
	Output string
	// Target defaults to TargetNative.
	Target Target
	// GoBinary defaults to "go".
	GoBinary string
	// SosFS and TypesafeFS supply interpreter sources, overriding asset
	// directories and development-layout discovery.
	SosFS      fs.FS
	TypesafeFS fs.FS
}

// Build compiles a standalone artifact embedding opts.Program. Building
// requires the Go toolchain; running the artifact does not. No environment
// values or .env content are included: the artifact reads its environment at
// runtime (SOS_MODEL, SOS_MAX_CALLS, SOS_TIMEOUT, SOS_RECORD, SOS_REPLAY,
// and .env beside the invocation for non-browser targets).
func Build(ctx context.Context, opts BuildOptions) error {
	target := opts.Target
	if target == "" {
		target = TargetNative
	}
	switch target {
	case TargetNative, TargetWasmBrowser, TargetWasmWasi:
	default:
		return fmt.Errorf("unknown target %q", target)
	}
	if opts.Program == nil {
		return errors.New("no program")
	}
	if opts.Output == "" {
		return errors.New("output path required")
	}
	// Reject already-known host incompatibilities before any paid analysis.
	if err := ValidateTarget(opts.Program, target); err != nil {
		return err
	}
	dir := opts.Dir
	var err error
	if dir == "" {
		dir, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	policy, err := sos.EffectiveConfig(opts.Program.Source, dir)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	analysisCtx := ctx
	if policy.Timeout > 0 {
		var cancel context.CancelFunc
		analysisCtx, cancel = context.WithTimeout(ctx, policy.Timeout)
		defer cancel()
	}
	budget, err := sos.NewRequestBudget(policy.Requests, nil)
	if err != nil {
		return err
	}
	analysis, err := sos.Analyze(analysisCtx, opts.Program.Source, sos.AnalyzeOptions{
		Config: policy, Budget: budget,
		Bucket: sos.BudgetInterpretation, Saved: opts.Resolution, Locked: opts.Resolution != nil,
		Modules: opts.Program.Modules,
	})
	if opts.OnAnalysis != nil {
		opts.OnAnalysis(analysis, err)
	}
	if err != nil {
		return fmt.Errorf("interpretation: %w", err)
	}
	if len(analysis.Diagnostics) > 0 {
		return fmt.Errorf("interpretation: %s", analysis.Diagnostics[0].Message)
	}
	canonical, ds := sos.ParseWithVocabulary(analysis.Canonical, opts.Program.Modules)
	if len(ds) > 0 {
		return fmt.Errorf("line %d: %s", ds[0].Line, ds[0].Message)
	}
	if err := ValidateTarget(canonical, target); err != nil {
		return err
	}
	analysisJSON, err := json.Marshal(analysis)
	if err != nil {
		return err
	}
	packagedPolicy := policy
	packagedPolicy.Origins = nil
	policyJSON, err := json.Marshal(packagedPolicy)
	if err != nil {
		return err
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return err
	}
	graph := opts.Program.Modules.Graph()
	finalOutput := output
	bundled := false
	for _, external := range graph.External {
		bundled = bundled || external.DistributionMode == "bundled"
	}
	if info, statErr := os.Stat(finalOutput); statErr == nil {
		if !bundled && info.IsDir() {
			return fmt.Errorf("output path %s is a directory", finalOutput)
		}
		if bundled && !info.IsDir() {
			return fmt.Errorf("bundled output path %s is not a directory", finalOutput)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	stageParent := filepath.Dir(finalOutput)
	if err := os.MkdirAll(stageParent, 0o755); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(stageParent, "."+filepath.Base(finalOutput)+"-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	var bundleStage string
	if bundled {
		bundleStage = filepath.Join(stageDir, "bundle")
		if err := os.MkdirAll(bundleStage, 0o755); err != nil {
			return err
		}
		output = filepath.Join(bundleStage, filepath.Base(finalOutput))
		if runtime.GOOS == "windows" && filepath.Ext(output) == "" {
			output += ".exe"
		}
		if err := prepareExternalBundle(graph, bundleStage, filepath.Base(output)); err != nil {
			return err
		}
	} else {
		output = filepath.Join(stageDir, filepath.Base(finalOutput))
	}
	normalizeGraphForDistribution(graph, opts.Dir)
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	sosFS, typesafeFS, configFS, mod, sum, err := resolveSnapshot()
	if err != nil {
		return err
	}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "sosbuild-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	mod = bytes.ReplaceAll(mod, []byte("module github.com/DonaldMurillo/system-one-playground"), []byte("module "+standaloneModulePath))
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), mod, 0o644); err != nil {
		return err
	}
	rewrite := func(src string) string {
		return strings.ReplaceAll(src, `"github.com/DonaldMurillo/system-one-playground/`, `"`+standaloneModulePath+"/")
	}
	if err := os.WriteFile(filepath.Join(tmp, "go.sum"), sum, 0644); err != nil {
		return err
	}
	if err := copyGoPackage(filepath.Join(tmp, "sosconfig"), configFS, rewrite); err != nil {
		return err
	}
	if err := copyGoPackage(filepath.Join(tmp, "sos"), sosFS, rewrite); err != nil {
		return fmt.Errorf("sos sources: %w", err)
	}
	if err := copyGoPackage(filepath.Join(tmp, "typesafe"), typesafeFS, rewrite); err != nil {
		return fmt.Errorf("typesafe sources: %w", err)
	}
	semcore, err := compilerSupportSources("internal/semcore")
	if err != nil {
		return err
	}
	if err := copySupportTree(filepath.Join(tmp, "internal", "semcore"), semcore, rewrite); err != nil {
		return err
	}
	var mainSrc bytes.Buffer
	if err := mainTemplate.Execute(&mainSrc, mainData{
		Source:     opts.Program.Source,
		Policy:     string(policyJSON),
		Resolution: string(analysisJSON),
		Modules:    string(graphJSON),
		Name:       filepath.Base(opts.Name),
		Browser:    target == TargetWasmBrowser,
		NoEnv:      target != TargetNative,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), mainSrc.Bytes(), 0o644); err != nil {
		return err
	}
	goBin := opts.GoBinary
	if goBin == "" {
		goBin = "go"
	}
	if _, err := exec.LookPath(goBin); err != nil {
		return errors.New("go toolchain required to build standalone artifacts")
	}
	cmd := exec.CommandContext(ctx, goBin, "build", "-trimpath", "-buildvcs=false", "-o", output, ".")
	cmd.Dir = tmp
	cmd.Env = buildEnv(target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %w\n%s", target, err, out)
	}
	if target == TargetWasmBrowser {
		if err := writeBrowserHost(filepath.Dir(output), filepath.Base(output), goBin); err != nil {
			return err
		}
	}
	if bundled {
		return publishStagedPaths([]stagedPath{{bundleStage, finalOutput}})
	}
	paths := []stagedPath{{output, finalOutput}}
	if target == TargetWasmBrowser {
		for _, name := range []string{"wasm_exec.js", "index.html"} {
			staged := filepath.Join(filepath.Dir(output), name)
			if _, err := os.Stat(staged); err == nil {
				paths = append(paths, stagedPath{staged, filepath.Join(filepath.Dir(finalOutput), name)})
			}
		}
	}
	return publishStagedPaths(paths)
}

type stagedPath struct{ staged, destination string }

// publishStagedPaths replaces each destination atomically only after every
// build output has been materialized. Backups are copies, not renames, so a
// crash can never leave a previously published destination temporarily absent.
func publishStagedPaths(paths []stagedPath) error {
	type movedPath struct {
		destination, backup string
		published           bool
	}
	moved := make([]movedPath, 0, len(paths))
	rollback := func() error {
		var problems []string
		for i := len(moved) - 1; i >= 0; i-- {
			item := moved[i]
			if item.published && item.backup != "" {
				if info, err := os.Stat(item.backup); err == nil && info.IsDir() {
					if err := os.RemoveAll(item.destination); err != nil {
						problems = append(problems, fmt.Sprintf("remove %s: %v", item.destination, err))
						continue
					}
					if err := os.Rename(item.backup, item.destination); err != nil {
						problems = append(problems, fmt.Sprintf("restore %s: %v", item.destination, err))
					}
					continue
				}
				if err := atomicReplace(item.backup, item.destination); err != nil {
					problems = append(problems, fmt.Sprintf("restore %s: %v", item.destination, err))
				}
			} else if item.published {
				if err := os.Remove(item.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
					problems = append(problems, fmt.Sprintf("remove %s: %v", item.destination, err))
				}
			}
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		return nil
	}
	fail := func(err error) error {
		if rollbackErr := rollback(); rollbackErr != nil {
			return fmt.Errorf("%w; rollback failed: %v", err, rollbackErr)
		}
		return err
	}
	for _, path := range paths {
		item := movedPath{destination: path.destination}
		if info, err := os.Stat(path.destination); err == nil {
			placeholder, err := os.CreateTemp(filepath.Dir(path.destination), ".sosbuild-backup-")
			if err != nil {
				return fail(err)
			}
			item.backup = placeholder.Name()
			if info.IsDir() {
				_ = placeholder.Close()
				_ = os.Remove(item.backup)
				if err := os.Rename(path.destination, item.backup); err != nil {
					return fail(err)
				}
				moved = append(moved, item)
				if err := os.Rename(path.staged, path.destination); err != nil {
					return fail(err)
				}
				moved[len(moved)-1].published = true
				continue
			}
			source, err := os.Open(path.destination)
			if err == nil {
				_, err = io.Copy(placeholder, source)
				_ = source.Close()
			}
			if err == nil {
				err = placeholder.Chmod(info.Mode())
			}
			if err == nil {
				err = placeholder.Sync()
			}
			if closeErr := placeholder.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(item.backup)
				return fail(err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fail(err)
		}
		moved = append(moved, item)
		if err := atomicReplace(path.staged, path.destination); err != nil {
			return fail(err)
		}
		moved[len(moved)-1].published = true
	}
	for _, item := range moved {
		if item.backup != "" {
			if err := os.RemoveAll(item.backup); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove build backup: %w", err)
			}
		}
	}
	return nil
}

func normalizeGraphForDistribution(graph *sos.ModuleGraph, root string) {
	root, _ = filepath.Abs(root)
	keys := map[string]string{}
	modules := map[string]*sos.ModuleSpec{}
	for i := range graph.Modules {
		modules[graph.Modules[i].Key] = &graph.Modules[i]
	}
	memo, visiting := map[string]string{}, map[string]bool{}
	var semanticKey func(string) string
	semanticKey = func(key string) string {
		if value := memo[key]; value != "" {
			return value
		}
		module := modules[key]
		if module == nil {
			return key
		}
		if visiting[key] {
			return "cycle:" + module.Name
		}
		visiting[key] = true
		type stableImport struct{ Alias, Target string }
		type stableFile struct {
			Name, Source string
			Imports      map[string]stableImport
		}
		stable := struct {
			Name  string
			Files []stableFile
		}{Name: module.Name}
		for _, file := range module.Files {
			item := stableFile{Name: file.Name, Source: file.Source}
			if len(file.Imports) > 0 {
				item.Imports = map[string]stableImport{}
				for name, edge := range file.Imports {
					item.Imports[name] = stableImport{Alias: edge.Alias, Target: semanticKey(edge.Key)}
				}
			}
			stable.Files = append(stable.Files, item)
		}
		delete(visiting, key)
		encoded, _ := json.Marshal(stable)
		sum := sha256.Sum256(encoded)
		memo[key] = fmt.Sprintf("module:%x", sum[:16])
		return memo[key]
	}
	for i := range graph.Modules {
		module := &graph.Modules[i]
		relative, err := filepath.Rel(root, module.Key)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			keys[module.Key] = "project:" + filepath.ToSlash(relative)
		} else {
			keys[module.Key] = semanticKey(module.Key)
		}
	}
	rewriteEdge := func(edge sos.ModuleEdge) sos.ModuleEdge {
		if key, ok := keys[edge.Key]; ok {
			edge.Key = key
		}
		return edge
	}
	for name, edge := range graph.Entry {
		graph.Entry[name] = rewriteEdge(edge)
	}
	for i := range graph.Modules {
		module := &graph.Modules[i]
		module.Key = keys[module.Key]
		for j := range module.Files {
			for name, edge := range module.Files[j].Imports {
				module.Files[j].Imports[name] = rewriteEdge(edge)
			}
		}
	}
	for i := range graph.Libraries {
		library := &graph.Libraries[i]
		if key, ok := keys[library.Key]; ok {
			library.Key = key
		}
		library.Origin = ""
	}
	sort.Slice(graph.Modules, func(i, j int) bool { return graph.Modules[i].Key < graph.Modules[j].Key })
	sort.Slice(graph.Libraries, func(i, j int) bool { return graph.Libraries[i].Path < graph.Libraries[j].Path })
}

type bundleManifest struct {
	Schema           int            `json:"schema"`
	Application      string         `json:"application"`
	Entrypoint       string         `json:"entrypoint"`
	SOSVersion       string         `json:"sosVersion"`
	PluginProtocol   string         `json:"pluginProtocol"`
	DefinitionSchema int            `json:"definitionSchema"`
	Target           string         `json:"target"`
	Modules          []bundleModule `json:"modules"`
}
type bundleModule struct {
	Path               string                       `json:"path"`
	Version            string                       `json:"version"`
	DefinitionDigest   string                       `json:"definitionDigest"`
	Artifact           string                       `json:"artifact"`
	SHA256             string                       `json:"sha256"`
	Runtime            string                       `json:"runtime"`
	Distribution       string                       `json:"distribution"`
	Protocol           string                       `json:"protocol,omitempty"`
	Capabilities       sos.ExternalCapabilitiesSpec `json:"capabilities"`
	Effects            []string                     `json:"effects,omitempty"`
	VersionRequirement string                       `json:"versionRequirement,omitempty"`
	Arguments          []string                     `json:"arguments,omitempty"`
}

func prepareExternalBundle(graph *sos.ModuleGraph, dir, application string) error {
	osName := runtime.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	target := osName + "-" + runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		target = osName + "-x64"
	}
	manifest := bundleManifest{Schema: 1, Application: application, Entrypoint: application, SOSVersion: sos.Version, PluginProtocol: "sos-plugin/1", DefinitionSchema: 1, Target: target, Modules: []bundleModule{}}
	for i := range graph.External {
		spec := &graph.External[i]
		if spec.DistributionMode != "bundled" {
			manifest.Modules = append(manifest.Modules, bundleModule{Path: strings.TrimPrefix(spec.Key, "external:"), Version: spec.Version, VersionRequirement: spec.VersionRequirement, Arguments: append([]string(nil), spec.DistributionArgs...), DefinitionDigest: spec.Digest, Runtime: spec.RuntimeKind, Distribution: spec.DistributionMode, Protocol: spec.Protocol, Capabilities: spec.Capabilities, Effects: append([]string(nil), spec.Effects...)})
			continue
		}
		var selected *sos.ExternalArtifactSpec
		for j := range spec.Artifacts {
			if spec.Artifacts[j].Target == target {
				selected = &spec.Artifacts[j]
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("external module %s has no bundled artifact for %s", spec.Key, target)
		}
		data, err := os.ReadFile(selected.Path)
		if err != nil {
			return fmt.Errorf("external module %s artifact: %w", spec.Key, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(data))
		expected := strings.TrimPrefix(selected.SHA256, "sha256:")
		if expected == "" || !strings.EqualFold(sum, expected) {
			return fmt.Errorf("external module %s artifact checksum mismatch", spec.Key)
		}
		safe := strings.NewReplacer("/", "-", "\\", "-", ":", "-", ".", "-").Replace(strings.TrimPrefix(spec.Key, "external:"))
		identity := sha256.Sum256([]byte(spec.Key))
		safe += fmt.Sprintf("-%x", identity[:16])
		moduleDir := filepath.Join(dir, "modules", safe)
		if err := os.MkdirAll(moduleDir, 0755); err != nil {
			return err
		}
		name := filepath.Base(selected.Path)
		destination := filepath.Join(moduleDir, name)
		mode := os.FileMode(0755)
		if info, e := os.Stat(selected.Path); e == nil {
			mode = info.Mode().Perm() | 0100
		}
		if err := os.WriteFile(destination, data, mode); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(moduleDir, "module.sos.toml"), []byte(spec.Definition), 0644); err != nil {
			return err
		}
		rel := filepath.ToSlash(filepath.Join("modules", safe, name))
		spec.BundleProgram = rel
		spec.BundleSHA256 = "sha256:" + sum
		manifest.Modules = append(manifest.Modules, bundleModule{Path: strings.TrimPrefix(spec.Key, "external:"), Version: spec.Version, VersionRequirement: spec.VersionRequirement, Arguments: append([]string(nil), spec.DistributionArgs...), DefinitionDigest: spec.Digest, Artifact: rel, SHA256: "sha256:" + sum, Runtime: spec.RuntimeKind, Distribution: "bundled", Protocol: spec.Protocol, Capabilities: spec.Capabilities, Effects: append([]string(nil), spec.Effects...)})
	}
	sort.Slice(manifest.Modules, func(i, j int) bool { return manifest.Modules[i].Path < manifest.Modules[j].Path })
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644)
}

// buildEnv forces the target platform regardless of ambient GOOS/GOARCH.
func buildEnv(target Target) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GOOS=") || strings.HasPrefix(kv, "GOARCH=") || strings.HasPrefix(kv, "CGO_ENABLED=") {
			continue
		}
		env = append(env, kv)
	}
	switch target {
	case TargetWasmBrowser:
		env = append(env, "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	case TargetWasmWasi:
		env = append(env, "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	}
	return env
}

func copyGoPackage(dst string, src fs.FS, rewrite func(string) string) error {
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	copied := false
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := fs.ReadFile(src, e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), []byte(rewrite(string(data))), 0o644); err != nil {
			return err
		}
		copied = true
	}
	if !copied {
		return errors.New("no Go sources found")
	}
	return nil
}

func dirHasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".go" && !strings.HasSuffix(e.Name(), "_test.go") {
			return true
		}
	}
	return false
}

func packageDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(file)
}

// writeBrowserHost copies the Go wasm_exec.js matching the built module and
// an index.html host page beside the artifact.
func writeBrowserHost(dir, wasmName, goBin string) error {
	out, err := exec.Command(goBin, "env", "GOROOT").Output()
	if err != nil {
		return fmt.Errorf("locating Go WASM support: %w", err)
	}
	goroot := strings.TrimSpace(string(out))

	execJS, err := os.ReadFile(filepath.Join(goroot, "lib", "wasm", "wasm_exec.js"))
	if err != nil {
		return fmt.Errorf("reading wasm_exec.js: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), execJS, 0o644); err != nil {
		return err
	}
	title := strings.TrimSuffix(wasmName, filepath.Ext(wasmName))
	filename, _ := json.Marshal(wasmName)
	page := fmt.Sprintf(indexHTML, html.EscapeString(title), filename)
	return os.WriteFile(filepath.Join(dir, "index.html"), []byte(page), 0o644)
}

const indexHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>%s</title>
</head>
<body>
<pre id="output"></pre>
<script src="wasm_exec.js"></script>
<script>
const originalLog = console.log;
console.log = (...args) => { document.getElementById("output").textContent += args.join(" ") + "\n"; originalLog(...args); };
const go = new Go();
// Go's matching wasm_exec runtime can retain a scheduled deadline after main
// exits. This host owns the runtime and clears those callbacks at shutdown.
go.exit = (code) => {
  for (const timer of go._scheduledTimeouts.values()) clearTimeout(timer);
  go._scheduledTimeouts.clear();
  if (code !== 0) console.error("sos: exit " + code);
};
WebAssembly.instantiateStreaming(fetch(%s), go.importObject)
  .then((result) => go.run(result.instance))
  .catch((err) => console.error("sos: " + err));
</script>
</body>
</html>
`

type mainData struct {
	Resolution string
	Policy     string
	Source     string
	Modules    string
	Name       string
	Browser    bool
	NoEnv      bool
}

var mainTemplate = template.Must(template.New("main").Parse(`// Code generated by sos build. This artifact embeds the SysOneScript program
// {{.Name}} and executes it on the reference interpreter; errors still refer
// to the original source lines. Runtime knobs come from the environment:
// SOS_MODEL, SOS_MAX_CALLS, SOS_TIMEOUT (duration or seconds),
// SOS_RECORD, SOS_REPLAY{{if not .Browser}}, and .env beside the invocation{{end}}.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"encoding/json"
 "errors"
 "sosstandalone/sosconfig"
 "sosstandalone/sos"
)

const programName = {{printf "%q" .Name}}

const programSource = {{printf "%q" .Source}}
const policySource = {{printf "%q" .Policy}}
const resolutionSource = {{printf "%q" .Resolution}}
const modulesSource = {{printf "%q" .Modules}}

func main() { os.Exit(run()) }

func run() int {
	var resolution sos.Analysis
 if err := json.Unmarshal([]byte(resolutionSource), &resolution); err != nil { fmt.Fprintln(os.Stderr,err); return 1 }
 var modules sos.ModuleGraph
 if err := json.Unmarshal([]byte(modulesSource), &modules); err != nil { fmt.Fprintln(os.Stderr,err); return 1 }
 program, diags := sos.LoadProgramFromGraph(programName, programSource, &modules)
	if len(diags) > 0 {
		printDiagnostics(diags)
		return 1
	}
	selection, err := sos.SelectCommand(program, os.Args[1:])
	if err == nil && selection.Help {
		fmt.Fprint(os.Stdout, selection.Usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sos: %v\n", err)
		if selection != nil { fmt.Fprint(os.Stderr, selection.Usage) }
		return 2
	}
	if err := loadEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "sos: %v\n", err)
		return 1
	}
	ctx := context.Background()
	d, durationErr := envDuration("SOS_TIMEOUT")
 if durationErr != nil { fmt.Fprintln(os.Stderr,durationErr); return 2 }
 if d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	var policy sosconfig.Effective
 if err := json.Unmarshal([]byte(policySource), &policy); err != nil { fmt.Fprintln(os.Stderr,err); return 1 }
 if raw, present := os.LookupEnv("SOS_MAX_CALLS"); present {
  n, err := strconv.Atoi(raw)
  if err != nil || n < 0 { fmt.Fprintln(os.Stderr,"invalid SOS_MAX_CALLS: expected a nonnegative integer"); return 2 }
  if n < policy.Requests { policy.Requests = n }
 }
 options := sos.Options{
 Config: &policy,
 Resolution: &resolution, Locked: true,
		Dir:      workingDir(),
		Args:     selection.Values,
        CommandPath: selection.Path,
		Stdin: os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Model:    os.Getenv("SOS_MODEL"),
		Record:   os.Getenv("SOS_RECORD"),
		Replay:   os.Getenv("SOS_REPLAY"),
		OnTrace:  traceLine,
	}
	result, err := sos.Run(ctx, program, options)
 var budgetErr *sos.BudgetError
 if result != nil && (result.Usage.TotalAdmitted > 0 || errors.As(err,&budgetErr)) {
  usage := result.Usage.Buckets[sos.BudgetRuntime]
  fmt.Fprintf(os.Stderr,"sos: usage requests=%d/%d inputTokens=%d unresolved=%d\n",result.Usage.TotalAdmitted,result.Usage.TotalLimit,usage.ReportedInputTokens,usage.Unresolved)
 }
 var exit interface{ExitCode() int}
 if errors.As(err,&exit){
  var stopped *sos.StopError
  if errors.As(err,&stopped){ fmt.Fprintf(os.Stderr,"sos: %s\n",stopped.Message) }
  return exit.ExitCode()
 }
	if err != nil {
		failure := sos.FailureValue(err)
		if kind, ok := failure["kind"].(string); ok && kind != "runtime" {
			message, _ := failure["message"].(string)
			payload, _ := json.Marshal(failure)
			fmt.Fprintf(os.Stderr, "sos: run: failure %s: %s (%s)\n", kind, message, payload)
		} else {
			fmt.Fprintf(os.Stderr, "sos: %v\n", err)
		}
		return 1
	}
	return 0
}

func workingDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func printDiagnostics(diags []sos.Diagnostic) {
	for _, d := range diags {
		fmt.Fprintf(os.Stderr, "%s:%d:%d: %s\n", programName, d.Line, d.Column, d.Message)
	}
}

func traceLine(t sos.Trace) {
	fmt.Fprintf(os.Stderr, "sos: trace line=%d model=%s %dms inputTokens=%d replay=%v\n",
		t.Line, t.Model, t.Milliseconds, t.InputTokens, t.Replay)
}

func envDuration(name string) (time.Duration,error) {
 raw, present := os.LookupEnv(name)
 if !present { return 0,nil }
 d, err := time.ParseDuration(raw)
 if err != nil {
  if _, e := strconv.ParseInt(raw,10,64); e == nil { d,err = time.ParseDuration(raw+"s") }
 }
 if err != nil || d <= 0 { return 0,fmt.Errorf("invalid %s: expected a positive duration",name) }
 return d,nil
}

{{if .NoEnv}}// WASM builds do not load local credentials; .env loading is a no-op.
func loadEnv() error { return nil }
{{else}}func loadEnv() error {
	return sos.LoadEnv(".env")
}
{{end}}`))

// resolveSnapshot never combines live interpreter code with archived config
// code. A development checkout supplies every input; a released CLI uses its
// single embedded snapshot. Explicit caller FS overrides remain intentional.
func resolveSnapshot() (fs.FS, fs.FS, fs.FS, []byte, []byte, error) {
	root := filepath.Dir(filepath.Dir(packageDir()))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); filepath.IsAbs(packageDir()) && err == nil {
		for _, name := range []string{"sos", "typesafe", "sosconfig"} {
			if !dirHasGoFiles(filepath.Join(root, name)) {
				return nil, nil, nil, nil, nil, fmt.Errorf("incomplete compiler source snapshot: missing %s", name)
			}
		}
		mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
		if err != nil {
			return nil, nil, nil, nil, nil, err
		}
		return os.DirFS(filepath.Join(root, "sos")), os.DirFS(filepath.Join(root, "typesafe")), os.DirFS(filepath.Join(root, "sosconfig")), mod, sum, nil
	}
	j, t, err := bundledSources()
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	c, mod, sum, err := bundledConfig()
	return j, t, c, mod, sum, err
}

func copySupportTree(dst string, src fs.FS, rewrite func(string) string) error {
	return fs.WalkDir(src, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".go") {
			data = []byte(rewrite(string(data)))
		}
		return os.WriteFile(target, data, 0644)
	})
}
func compilerSupportSources(pkg string) (fs.FS, error) {
	root := filepath.Dir(filepath.Dir(packageDir()))
	if filepath.IsAbs(packageDir()) && dirHasGoFiles(filepath.Join(root, pkg)) {
		return os.DirFS(filepath.Join(root, pkg)), nil
	}
	return bundledTree(pkg)
}
