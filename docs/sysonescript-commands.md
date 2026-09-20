# Commands, inputs, and actions

Status: implemented in 0.2.0. See the
[language reference](sysonescript-language.md) and [tickets walkthrough](sysonescript-tickets.md).

## The three concepts

- A **command** is a public terminal entry point. It declares inputs and a body.
- An **input** is a named, typed value supplied by the person invoking a command.
- An **action** is reusable functionality with parameters and an optional result.

Commands handle the terminal interface and call actions. Actions do not parse
terminal arguments. Command declarations and input parsing remain deterministic. The implemented
semantic pipeline may interpret permitted body sentences and introduce declared
criterion judgments under policy. See the [CLI roadmap](sysonescript-cli-roadmap.md)
for the current implementation sequence; no new dependency is assumed.

## Surface

The example combines collection and file sentences with nested commands and
`describe` help text. Files use `.sos`; the runner is `sos`.

```text
command tickets:
  describe "Inspect and organize support tickets"
  option source as file default "tickets.json"

  command list:
    describe "List open tickets"
    switch urgent default off
    option limit as integer default 20

    read source as json called tickets
    keep tickets where status is "open"
    when urgent:
      keep tickets where jev:
        ask "The customer needs immediate help"
        using message
        accept probability at least 0.85
        on uncertain discard
        on failure stop with "Could not evaluate tickets"
    take first limit items from tickets called selected
    show selected as table with id, status, message

  command export:
    describe "Export all tickets as JSON"
    argument destination as file

    read source as json called tickets
    save tickets as json in destination
```

Packaged as an executable named `tickets`, the interface is:

```sh
tickets list
tickets list --urgent --limit 5
tickets --source backlog.json list
tickets list --source backlog.json
tickets export exported.json
tickets --help
tickets list --help
```

The root command name describes the application; it is not repeated as an
argument. The script runner passes everything after `--` through the
same argument parser as the packaged executable.

## Command rules

1. A CLI script has one root command. A command is either a group containing
   child commands or a leaf containing executable statements, never both.
2. Only the selected leaf body executes. There are no implicit parent setup
   hooks. Shared setup belongs in an action called by each leaf.
3. Parent options and switches are inherited. Shadowing an inherited input or
   duplicating a sibling command name is a diagnostic.
4. Groups cannot declare positional arguments. This keeps child command names
   unambiguous. Leaf positional arguments bind in declaration order.
5. Invoking a group without a child prints its help and succeeds. An unknown
   child or unexpected argument is a usage error.
6. CLI scripts cannot also contain executable top-level statements. Plain
   scripts remain supported, and top-level action declarations are allowed.

This deliberately replaces the old metadata-only command block plus top-level
execution model. The project is early enough to rework syntax and internal APIs:
update examples, tests, tooling, and documentation together. Do not add a legacy
parser or a file-level version switch. Reject incompatible saved interpretations
and regenerate them under the new compiler semantics.

## Input rules

- `argument` is positional and required. Optional positional arguments and
  variadic inputs are deferred.
- `option` takes a value. Without a default it is required. Defaults must be
  literals compatible with the declared type.
- `switch` is boolean, defaulting to off. `--urgent` enables it and
  `--urgent=false` explicitly disables it.
- Value options accept `--limit 5` and `--limit=5`. Short aliases and bundled
  short flags are deferred. Repeated inputs are usage errors.
- An inherited option may occur before or after the selected child name.
  Child-specific options must follow that child's name.
- A script-level `--` ends option parsing; subsequent tokens are positional
  values, even if they begin with a dash.
- Retain the current types: text, number, integer, boolean, duration, file,
  and folder. File/folder values are paths, not promises of existence; an
  output file may not exist yet. File operations enforce their own conditions.
- Resolve the command and validate its inputs before running its body. Invalid
  inputs cannot trigger user file writes or Jev calls.
- Reserve `--help`. Help succeeds without required input values or a provider
  key and never executes a command body.

Declarations generate help containing usage, descriptions, child commands,
types, defaults, and required markers. Input descriptions can be added as a
separate grammar extension; do not invent ad hoc trailing text now.

## Action rules

Keep the implemented action grammar:

```text
to double with value:
  return value * 2

call double with 7 called answer
show answer
```

Actions must be declared before use, take comma-separated arguments, and use
local bindings. Assignment inside an action does not mutate the caller's
bindings. File writes and provider calls are still effects, not isolated local
changes. Do not introduce implicit parameter capture from command inputs:
pass those values explicitly. Named arguments, parameter types, imports, and
module visibility are later extensions.

## Output and failure

Keep ordinary output on stdout and diagnostics on stderr so commands compose
with shell redirection. Successful completion returns 0, usage errors return
2, and execution failures return 1. Define platform-specific interruption
behavior separately. Explicit user-selected exit codes and reading stdin are
deferred. Existing write-replacement and Jev failure policies still apply;
commands do not make effects transactional.

## Verification checkpoints

1. Replace the command parser/AST and update examples and offline diagnostics.
   Support plain scripts explicitly; rework action internals where the new model needs it.
2. Share command selection, input validation, and help generation between the
   script runner and generated executables.
3. Bind only the selected leaf's effective inputs and execute its body.
4. Add command selection to Studio and nested-command understanding to LSP.
5. Verify through CLI end-to-end tests: both option placements, defaults,
   switches, positional values after `--`, group help, missing/unknown inputs,
   duplicate declarations, selected-body-only execution, and validation before
   effects. Run equivalent cases against a packaged executable.

The executable is `sos`. Modules, stdin pipelines, additional
providers, and new effects should build on this contract rather than changing
how commands and actions are selected.
