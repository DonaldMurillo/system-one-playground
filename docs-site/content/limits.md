# Status and limitations

This site describes the 0.6 implementation. Earlier design proposals are not promises of supported syntax.

| Surface | Available | Current boundary |
| --- | --- | --- |
| Language | Canonical sentences, values, collections, actions, schemas and commands | Dynamic runtime values; not a complete static type system |
| Jev | Explicit judgments, declared criteria, constrained semantic interpretation | Supported operation registry; not arbitrary code generation |
| Packages | Local modules, multi-file packages, text/JSON/list/record libraries, filesystem/process primitives, vocabulary aliases | No remote dependency resolver |
| Streams | Single-owner bounded streams, cancellation, materialization, bounded flow-control transformations and handlers, Python/Node stdio producers, live stream panels | Detached tasks, implicit infinite queues, and unbounded keyed state remain intentionally unsupported |
| Stream inspection | Live stream panels over a loopback control channel authenticated by a `SOS_STREAM_TOKEN` environment token | Frames over 8 MiB and connections beyond 16 are rejected; a 2048-event lifecycle queue may drop events under extreme burst (counted and reported; snapshots remain authoritative) |
| Parallel maps | Bounded isolated workers, ordered results, shared limits, structured failures | No nested maps or worker stdin; external effects remain shared |
| Native build | Executable with interpreter and resolved source graph | Not direct machine-code lowering of each sentence |
| WASM | Pure browser and WASI programs | No live Jev host adapter; browser filesystem operations unavailable |
| Studio | Projects, file tree, settings, hints, interpretation and traces | CLI/MCP operate on disk, not another window's unsaved buffer |
| Budgets | Request counts and timeout limits, reported usage | No persistent account-wide spending cap; missing usage is unknown |
| Editor assistance | On-demand analysis | Automatic assistance currently behaves as on-demand |
| JavaScript | Browser WASM wrapper | No plain JavaScript emitter |

Stream policies bound five independent dimensions: pending items, concurrent
and waiting handlers, keyed state, aggregate partial-batch items, and retained
bytes/timers. Keyed batch aggregate capacity is derived from its declared batch
size and key count, so every language-level policy has a finite admission bound.

## Three meanings of “understood”

**Language syntax** is recognized deterministically by the parser and checker.

**Semantic interpretation** asks Jev to select among supported meanings, then validates the resolved operation. An unresolved phrase is pending analysis, not automatically invalid syntax.

**Runtime judgment** evaluates actual input data under a question, criterion or scoring scale. It can happen inside otherwise canonical code. A saved interpretation is not a recording of runtime judgments.

## Execution and credentials

Run trusted scripts. Project file APIs restrict editor paths, but script execution is not a sandbox. Scripts can write files and use provider credentials when policy permits. Recordings and traces can contain inputs and questions; review them before sharing.

## Distribution status

The project uses the MIT license. Public repository identity, release packaging and hosting must be finalized before a public launch. There is no verified public binary download or package installation command yet.
