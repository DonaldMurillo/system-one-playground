# External modules over stdio and commands

Status: implemented for native targets. Strict offline definitions, typed
interfaces and failures, persistent stdio plugins, shell-free command adapters,
capability authorization, minimal secret environments, deterministic locked
directory bundles, runtime checksum verification, doctor/version probes,
redacted traces, opaque debugger frames, CLI/MCP commands, Studio, and VS Code
project controls are shipped. Browser and WASI builds reject process modules.
Archive output, plugin source debugging, and automatic dependency installation
remain intentionally out of scope.

This proposal lets SysOneScript import modules implemented in Python, Node.js,
Go, Rust, native executables, or existing command-line tools. A strict,
versioned TOML definition describes the SOS interface and process contract. The
language transforms that definition into the same module model used by SOS
packages and the standard library.

The interface is available to the checker and editors without starting a
process. Runtime execution begins only when an exported action is called.

## Goals

- Make external modules feel like ordinary SOS imports.
- Keep the plugin protocol language-neutral and small.
- Generate one SOS interface from an authoritative TOML definition.
- Support persistent stdio plugins and one-process-per-call CLI adapters.
- Describe types, actions, effects, capabilities, and target support offline.
- Preserve cancellation, deadlines, output limits, tracing, and typed errors.
- Invoke programs directly, never through shell interpolation.
- Enforce host policy before an external process starts.

## Non-goals

The first version does not provide an in-process ABI, shared libraries, remote
plugins, a package registry, dependency installation, cross-run daemons,
plugin-to-host callbacks, or automatic trust. It does not infer an interface by
scraping `--help` output and does not embed an expression language in TOML.

## Project and language experience

The project registers a definition in `sos.toml`:

```toml
version = 1

[[module.external]]
path = "example.com/acme/weather"
definition = "modules/weather/module.sos.toml"
```

SOS imports it normally:

```sos
import "example.com/acme/weather" as weather

call weather.current with "San Juan" called report
show "It is {temperature of report} degrees"
```

Authors can inspect the transformed interface without launching the plugin:

```text
sos module check modules/weather/module.sos.toml
sos module describe example.com/acme/weather
sos module generate modules/weather/module.sos.toml
```

`generate` emits deterministic, reviewable SOS interface source for docs and
distribution. The generated source describes the interface; it is not the
runtime implementation and is not required for imports.

## One interface, two runtime kinds

The definition chooses exactly one runtime kind.

### Persistent stdio plugin

Purpose-built plugins use `kind = "stdio"`. SOS starts the executable lazily,
performs a handshake, and reuses it during one run.

```toml
schema = 1

[module]
path = "example.com/acme/weather"
version = "1.2.0"
description = "Weather observations"

[runtime]
kind = "stdio"
protocol = "sos-plugin/1"
command = ["python3", "-m", "acme_weather"]
working_directory = "${definition_dir}"
max_in_flight = 1
startup_timeout = "5s"
shutdown_timeout = "2s"

[runtime.requires]
python = ">=3.11"

[capabilities]
network = true
filesystem = "none"
process = true
secrets = ["WEATHER_API_KEY"]

[[type]]
name = "WeatherReport"

[[type.field]]
name = "temperature"
type = "number"

[[type.field]]
name = "summary"
type = "text"

[[type.field]]
name = "observed_at"
type = "timestamp"

[[action]]
name = "current"
description = "Returns the latest observation for a city."
effects = ["network"]
timeout = "10s"

[[action.parameter]]
name = "city"
type = "text"

[action.result]
type = "WeatherReport"
```

### Existing command adapter

Existing CLIs use `kind = "command"`. Each call starts a process directly;
there is no shell and the CLI does not implement the plugin protocol.

```toml
schema = 1

[module]
path = "local/git"
version = "1.0.0"
description = "Checked Git operations"

[runtime]
kind = "command"

[capabilities]
network = false
filesystem = "workspace-read"
process = true

[[action]]
name = "status"
description = "Reads repository status."
effects = ["process", "filesystem-read"]
timeout = "5s"

[[action.parameter]]
name = "root"
type = "folder"

[action.result]
type = "text"

[action.command]
program = "git"
arguments = ["status", "--porcelain=v2", "--branch"]
working_directory_parameter = "root"
stdout = "text"
stderr = "diagnostic"
exit_codes = [0]
```

Command definitions are adapters, not shell programs. Arguments are arrays and
parameter substitutions occupy complete argument elements. Complex output
parsing belongs in a stdio plugin rather than a second language inside TOML.

## Definition model

The conventional filename is `module.sos.toml`. It contains:

- `schema`: the definition format version;
- `module`: import path, semantic version, description, and metadata;
- `runtime`: exactly one launch strategy and its limits;
- `capabilities`: required host access;
- zero or more named record `type` declarations; and
- one or more exported `action` declarations.

Unknown keys are errors. Names and types follow SOS rules. Declaration order is
preserved for documentation, positional arguments, and generated interfaces.

### Types and transformation

The ABI supports `any`, `text`, `number`, `integer`, `boolean`, `timestamp`,
`duration`, `file`, `folder`, `optional TYPE`, `list of TYPE`, and named records
declared in the definition.

Values cross stdio as JSON. Timestamps use RFC3339 with an offset, durations use
the canonical SOS duration string, numbers are finite, and integers must remain
exactly representable in the supported JSON range. Paths are text values plus
host validation; receiving a path does not grant filesystem access.

TOML records become SOS definitions:

```sos
define WeatherReport:
  temperature as number
  summary as text
  observed_at as timestamp

to current with city as text:
  # external implementation: example.com/acme/weather
```

Named records use the closed structural validation from the named-record spec.
The generated action body is descriptive and cannot execute independently.

### Actions

Every action declares a unique name, ordered parameters, one result type or
`none`, documentation, effects, required capabilities, supported targets, and
optional limits that may only tighten host policy.

SOS arguments remain positional and become a name-keyed JSON object at the
protocol boundary. The host validates arguments before launch/invocation and
validates results before returning them to SOS.

## Stdio protocol

The protocol is JSON-RPC 2.0 over UTF-8 newline-delimited JSON. Each line is one
message; newlines in values are escaped. Stdout is protocol-only and human logs
go to stderr.

The host bounds message size and total output. Invalid UTF-8, non-JSON stdout,
oversized messages, duplicate responses, unknown IDs, and responses after
cancellation are protocol errors.

### Lifecycle

1. Resolve and validate TOML without starting a process.
2. On the first call, evaluate capabilities and launch policy.
3. Start the process with pipes and a minimal environment.
4. Exchange `initialize`; verify protocol, identity, version, and digest.
5. Send serialized `invoke` requests. Schema 1 accepts `max_in_flight = 1`
   (or omission, which defaults to one); larger values are rejected until
   multiplexed response handling is specified.
6. Forward cancellation and enforce the host deadline.
7. Send `shutdown` at run completion, close stdin, and wait briefly.
8. Kill the process tree when startup, cancellation, or shutdown limits expire.

There is no automatic retry because calls may have effects. A crash fails every
in-flight call. A later call may restart the process only after the old process
cannot produce a response, and the restart is recorded in the trace.

### Initialize

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":"sos-plugin/1","module":"example.com/acme/weather","version":"1.2.0","definitionDigest":"sha256:...","host":{"sosVersion":"0.3.0","target":"darwin-arm64"},"limits":{"maxMessageBytes":1048576,"maxInFlight":1}}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"protocol":"sos-plugin/1","module":"example.com/acme/weather","version":"1.2.0","definitionDigest":"sha256:..."}}
```

TOML is authoritative. A plugin cannot add actions or request capabilities in
the handshake. Any identity, version, or digest mismatch fails before invoke.

### Invoke and errors

```json
{"jsonrpc":"2.0","id":2,"method":"invoke","params":{"action":"current","arguments":{"city":"San Juan"},"context":{"requestId":"run-...","deadline":"2026-09-20T20:30:00Z","workingDirectory":"/workspace"}}}
```

```json
{"jsonrpc":"2.0","id":2,"result":{"value":{"temperature":29.5,"summary":"Clear","observed_at":"2026-09-20T20:20:00-04:00"}}}
```

```json
{"jsonrpc":"2.0","id":2,"error":{"code":-32010,"message":"weather service unavailable","data":{"kind":"upstream_unavailable","retryable":true}}}
```

`retryable` informs scripts and users; the host still does not retry. Error data
is size-limited, validated as SOS data, and redacted before persistence.

### Cancellation and shutdown

```json
{"jsonrpc":"2.0","method":"cancel","params":{"id":2}}
{"jsonrpc":"2.0","id":3,"method":"shutdown","params":{}}
```

The plugin should stop cancelled work and answer the original request with a
cancellation error. Host deadlines remain authoritative and may terminate the
process. Shutdown cannot extend the run deadline.

## Command adapter contract

- `program` is one executable name or path, never a shell string.
- `arguments` is an array. `${parameter.name}` may replace a complete element;
  substring interpolation is rejected in schema 1.
- List-to-repeated-argument mappings require explicit descriptors; joining into
  command text is never implicit.
- Working directory is fixed, `${workspace}`, `${definition_dir}`, or a
  validated `folder` parameter allowed by policy.
- The environment is minimal. Definitions list inherited variable names and
  symbolic secret mappings, never secret values.
- stdin is `none`, `text`, `json`, or one declared parameter.
- stdout is `none`, `text`, `json`, `json-lines`, or a named host transform.
- stderr is `diagnostic`, `discard`, or `merge` and is not a result by default.
- Accepted exit codes are explicit and default to `[0]`.
- stdout, stderr, duration, and the process tree are bounded by host policy.

Built-in transforms are versioned host features, not arbitrary code. Schema 1
starts with identity text/JSON and JSON-lines. Only specifically tested formats
may be added. Anything complex should use a stdio plugin.

## Discovery and resolution

Resolution is explicit, local, and deterministic:

1. Normalize the requested module path.
2. Find its exact configured definition.
3. Parse with unknown-key rejection.
4. Validate names, types, actions, effects, runtime, and capabilities.
5. Hash normalized interface and launch behavior.
6. Expose the transformed module to the checker and editors.

No executable runs during discovery, completion, hover, formatting, checking,
or docs generation. Opening a project cannot start a plugin. Conflicts between
SOS packages, standard modules, and external modules are errors; there is no
precedence fallback.

## Distribution, executables, and portability

An SOS application and its external modules form one versioned distribution.
Every external module chooses a distribution mode independently from its runtime
kind. `stdio` versus `command` defines how SOS communicates with the program;
`bundled` versus `external` defines how that program reaches the destination.

### Bundled artifacts

`bundled` is the recommended production mode. The definition names a prebuilt,
checksummed executable for every supported target:

```toml
[distribution]
mode = "bundled"
arguments = ["--stdio"] # optional production-only launcher arguments

[[distribution.artifact]]
target = "darwin-arm64"
path = "dist/weather-darwin-arm64"
sha256 = "..."

[[distribution.artifact]]
target = "linux-x64"
path = "dist/weather-linux-x64"
sha256 = "..."

[[distribution.artifact]]
target = "win32-x64"
path = "dist/weather-win32-x64.exe"
sha256 = "..."
```

For the current native host, `sos build app.sos` selects exactly one matching artifact,
verifies its digest before copying, and emits a relocatable directory bundle:

```text
my-app/
|-- my-app
|-- manifest.json
`-- modules/
    `-- example.com-acme-weather/
        |-- module.sos.toml
        `-- weather
```

The generated application resolves the module relative to its own executable or
bundle root. It never searches PATH for a module declared as bundled. Runtime
startup verifies the packaged artifact against the locked manifest before
launching it.

`manifest.json` records at least:

- application, SOS, definition-schema, and plugin-protocol versions;
- target operating system and architecture;
- every module path and semantic version;
- normalized definition digest;
- selected artifact path and checksum;
- executable entrypoint;
- capabilities and effects; and
- whether the artifact is bundled, external, or unsupported.

The manifest uses stable ordering and normalized paths so the same inputs produce
the same bytes. Signing and notarization operate on the complete emitted bundle,
after SOS has assembled and verified it.

### External system programs

`external` is for deliberate host dependencies such as Git, Docker, or FFmpeg:

```toml
[distribution]
mode = "external"
program = "git"
version_command = ["git", "--version"]
version_requirement = ">=2.40"
install_documentation = "https://git-scm.com/downloads"
```

The application does not include the program. Build records the requirement;
installation, `sos module doctor`, and startup report a missing or incompatible
executable with its resolved path and detected version. Bare names resolve only
through the host's approved PATH snapshot. Absolute paths run only when policy
permits them.

External mode is smaller but intentionally less portable. It must not silently
fall back to a bundled artifact or another program with a similar name.

### Embedded single-file artifacts

An `embedded` mode is reserved for a later release. It would place target plugin
payloads inside the generated application, extract the selected artifact into a
private content-addressed cache, verify its digest, set executable permissions,
and launch it from there.

Embedded mode is deferred because extraction interacts with code signing,
notarization, Windows antivirus, macOS quarantine, no-exec temporary filesystems,
permissions, cleanup, and concurrent launches. Directory/archive bundles are
the reliable first implementation. The schema must reject `embedded` until all
of those behaviors are specified and tested.

### Development and production launchers

Definitions may use a convenient source runtime during development while
requiring a standalone artifact for distribution:

```toml
[runtime.development]
command = ["python3", "-m", "acme_weather"]

[runtime.production]
artifact = "weather"
```

Development commands follow the normal runtime prerequisite and PATH rules.
Production builds never package raw Python or Node source merely hoping the
destination has a compatible runtime. Module authors first create standalone
target artifacts using their ecosystem tools—for example PyInstaller/Nuitka for
Python, Node SEA/Bun compile for Node, and normal cross-builds for Go or Rust.
SysOneScript verifies and bundles the resulting executable; it does not become a
Python, Node, Go, or Rust build system.

SOS may later provide explicit scaffolding or CI recipes, but `sos build` does
not execute arbitrary module build hooks. Artifact production is a separate,
visible CI step.

### Existing CLIs and OS facilities

Command adapters also declare `bundled` or `external`. Bundling a CLI is allowed
only when the exact per-target binary and checksum are present and the author is
responsible for redistribution rights. SOS never copies the result of `which`,
`where`, or PATH resolution into a release.

OS facilities normally remain external requirements. Target-specific command
configuration may describe genuine platform differences, but every supported
target must still expose the same validated SOS action interface.

### Build matrix and failures

A release pipeline is intentionally staged:

```text
plugin ecosystem build
        -> target plugin executables
        -> sos module check
        -> sos build --target TARGET
        -> app + definitions + plugins + locked manifest
        -> archive, sign, and publish
```

CI normally builds `darwin-arm64`, `darwin-x64`, `linux-arm64`, `linux-x64`,
`win32-arm64`, and `win32-x64`. A target build fails before packaging when any
bundled module lacks a matching artifact, has the wrong checksum, requests an
unsupported capability, or cannot expose the same SOS interface on that target.
Cross-compilation never attempts to execute the target artifact.

`${definition_dir}` and `${workspace}` remain the only host path substitutions
in schema 1. In a standalone artifact, `${definition_dir}` means the bundled
module artifact directory for bundled modules and the executable's directory
for external-mode modules; source-machine paths are never embedded. Missing
development runtimes produce setup diagnostics; SOS does not install them.

Browser WASM and the current WASI target cannot launch process-backed modules;
both reject external modules at build time. A future WASI host-process bridge
would require a separately specified explicit capability.

## Capabilities and secrets

Definitions declare requirements; host policy authorizes them. Declarations do
not grant access. Initial capabilities are network, filesystem (`none`,
`workspace-read`, `workspace`, or `explicit`), process, and symbolic secrets.

Effective access is the intersection of definition requirements, project
policy, user/editor policy, target support, and run policy. Missing permission
fails before startup and names the module and capability.

Capability declarations are an authorization and audit boundary, not an OS
sandbox. Native plugins run as the current user and must be trusted like any
other executable. `filesystem = "none"` means the project did not authorize or
intend filesystem use; it cannot revoke permissions already held by a malicious
native process. Sandboxed untrusted plugins require a separately designed host.

Secret values come from approved host secret storage. They are passed only by
explicit mappings and never appear in TOML, generated interfaces, arguments,
traces, recordings, or diagnostics. Children receive no provider credential,
full parent environment, or unrelated secret by default. Jev access is not
implicitly delegated.

## Effects and recordings

Actions declare `pure`, `filesystem-read`, `filesystem-write`, `network`,
`process`, `clock`, `random`, and/or `secret`. A `pure` action cannot belong to a
runtime requesting effectful capabilities. Effects inform policy, docs, builds,
and warnings; declaration alone is not a sandbox.

Version 1 does not automatically record external calls. Jev recording remains
separate. Replay that reaches an external action executes it normally or is
denied by policy. A future format must bind module identity, definition digest,
action, validated arguments, result/error, effects, and redaction policy.

## Errors and tooling

Failures include module/version, action, source frame, safe message, stable kind,
and phase: resolve, authorize, start, initialize, invoke, decode, validate,
cancel, shutdown, or exit. Exit status/signal and plugin retryability are
included when relevant.

Representative diagnostics:

```text
module example.com/acme/weather requires Python >=3.11; python3 was not found
module local/git requests workspace filesystem access, denied by project policy
weather.current returned temperature as text; expected number
weather plugin wrote non-protocol data to stdout during invoke
git.status exceeded its 5s action timeout
external module local/git is unavailable in browser WASM builds
```

Studio and VS Code provide offline SOS import/action completion, type/effect
information, navigation to the TOML definition, a module status panel, explicit
`Check module`, `Diagnose runtime`, and `Open definition` actions, plus a
redacted protocol trace. `Check module` is the authoritative strict TOML and
interface validator; the extension does not replace a general TOML editor.
Debugger frames identify the external module/action but treat the child as
opaque in schema 1.

Editors never start plugins automatically. **Diagnose runtime** is explicit; it
performs initialize followed by shutdown for stdio plugins and dependency/path
checks for command adapters without invoking an action.

## CLI surface

```text
sos module check DEFINITION
sos module describe MODULE_PATH
sos module doctor MODULE_PATH
sos module generate DEFINITION
```

`check` validates the definition and complete typed interface entirely offline;
executable/dependency existence belongs to `doctor`. `describe`
prints the transformed interface. `doctor` explicitly handshakes a stdio plugin
or validates command launch policy without invoking an action. `generate` emits
stable source and the definition digest. The direct CLI is intentionally
human-readable: `check` and `doctor` print one success line or a diagnostic to
stderr, `describe` prints SOS interface source, and `generate` appends a
`# definition digest: sha256:...` line. Automation that needs stable fields uses
`sysone mcp` (`external_modules`, `external_module_check`, and
`external_module_doctor`) and reads each tool's `structuredContent` object
instead of parsing CLI prose.

## Streaming actions

For stdio plugins, an external action whose result type is `stream of TYPE` opens through
`stream.open` rather than `invoke`. The request includes an initial positive
item credit. A successful response returns the stream ID and item type; later
`stream.item`, `stream.end`, and `stream.error` notifications carry the
producer's ordered values and one terminal outcome. The host replenishes
capacity with `stream.credit` only as downstream space becomes available.

`stream.cancel` is acknowledged within the module cancellation deadline. The
host closes or kills an unresponsive process tree. Unknown IDs, sequence gaps
or duplicates, invalid item shapes, sends beyond credit, and any item after a
terminal message are protocol violations. Progress stays on the diagnostic
channel and is never converted into a stream item. See [Streams and long-running
operations](sysonescript-streams.md) for SOS ownership and consumption rules.
Command adapters expose a `stream of TYPE` result with `stdout = "json-lines"`.
Every complete line is decoded and validated as one item; malformed JSON,
invalid item types, partial final lines, and rejected exit codes terminate the
stream. Cancellation kills the complete process tree after the configured grace
period. OS pipe pressure and the host's bounded decoder queue provide
backpressure; command adapters do not use stdio-plugin credit messages.

Command streams recognize three conventional typed terminal failures, but only
when the action declares the exact name and the corresponding failure
definition accepts an empty payload:

- `StreamDecodeFailure`: malformed JSON-lines output, a partial final line, or
  an item that fails declared-type validation;
- `ProcessFailure`: stdout read failure or an exit status outside the accepted
  exit-code set; and
- `StreamTimeout`: the action deadline expires while producing the stream.

Without a compatible declaration, the same condition is returned as an
ordinary terminal runtime error. The adapter does not invent payload fields,
and items delivered before the terminal failure remain visible.

## Versioning

Definition `schema`, stdio `protocol`, and author-facing module `version` are
separate. The normalized digest binds interface and launch behavior. A handshake
with another digest fails rather than assuming semantic-version compatibility.

Adding an optional action parameter is still breaking because SOS calls are
positional and omitted arguments are not part of this proposal.

## Verification matrix

Coverage must include:

- Python and Node fixture plugins implementing the same interface;
- handshake success and identity/version/digest mismatches;
- typed arguments/results, nested records, lists, optionals, and nulls;
- malformed JSON, stdout noise, large/partial messages, duplicate/unknown IDs,
  delayed responses, crashes, and responses after cancellation;
- deadlines, graceful shutdown, forced process-tree termination, restarts, and
  absence of automatic effect retries;
- sequential and explicitly concurrent calls;
- shell-free command arguments, stdin/stdout/stderr, exits, and limits;
- denial before startup and secret redaction;
- missing runtimes/tools/platform artifacts;
- deterministic directory bundles and locked-manifest reproduction;
- checksum failures, missing target artifacts, and prohibition on PATH capture;
- development-runtime selection versus production-artifact selection;
- external dependency version diagnostics;
- deterministic module conflict handling;
- native external/bundled modes and browser/WASI rejection;
- completion, hover, navigation, TOML diagnostics, status UI, traces, debugger,
  CLI, and MCP output; and
- unchanged SOS-package and standard-library imports.

Tests use local fixtures and no network. Process-tree termination runs on Linux,
macOS, and Windows.

## Delivered implementation sequence

1. Add strict TOML schema validation and transformation into the module model.
2. Integrate offline resolution, checking, vocabulary, docs, and editor support.
3. Add the shared typed JSON boundary and structured failures.
4. Implement stdio lifecycle, limits, cancellation, and fixture plugins.
5. Implement the shell-free command adapter and minimal transforms.
6. Add capability policy and secret mappings.
7. Add CLI/MCP commands, Studio, VS Code, and debugger surfaces.
8. Add deterministic directory packaging, locked manifests, external
   dependency checks, and explicit WASM/WASI compatibility checks.
9. Run cross-platform process, Go, frontend, extension, build, lint, and E2E
   verification.

## Accepted design direction

- TOML is authoritative and transforms into an ordinary SOS module interface.
- Authored plugins use JSON-RPC 2.0 over newline-delimited stdio.
- Existing CLIs use a separate shell-free command adapter.
- Interface discovery is offline and starts nothing.
- Plugins start lazily and live for one SOS run.
- The host validates arguments and results.
- Effectful calls are never retried automatically.
- Modules declare capabilities; host policy grants them.
- External runtimes are diagnosed, not automatically installed.
- Deterministic directory bundles with exact per-target artifacts and checksums
  are the first self-contained distribution format; archive wrapping is deferred.
- System dependencies remain explicitly external; builds never capture PATH
  executables implicitly.
- Python and Node source runtimes are convenient for development, while release
  bundles contain prebuilt standalone artifacts.
- Embedded single-file plugin payloads are deferred until extraction, signing,
  quarantine, antivirus, and cache semantics are designed and tested.
- Browser WASM rejects process modules until a host bridge is separately
  designed.
