# SysOneScript: initial design proposal

Status: historical discussion draft, 2026-09-19. A 0.1 implementation now exists; see the [implemented reference](sysonescript-language.md). The [semantic interpretation design](sysonescript-semantics.md) supersedes this document's restriction of Jev to explicit runtime judgments. Syntax and architecture below are historical proposals. User direction: a GENERAL scripting language with readable, executable pseudocode; Go implementation; native binaries and WASM, with JavaScript a possible later target.

Surface syntax update: the user rejected the conventional syntax below. [Sentence grammar CLI sketch](sysonescript-cli-sketch.md) is the current language-surface exploration; the Go/backend architecture remains a proposal.

## Thesis

A general scripting language that reads like disciplined pseudocode and can perform useful work with no model calls. Semantic judgment is a first-class operation within that language. Go implements the language and runtime; Jev supplies Noul, Choice, and Score evaluations. The program controls execution and preserves uncertainty until an explicit decision.

Jev is the semantic foundation, not the parser or a machine-code backend. Its current API produces constrained answers rather than code or arbitrary text. Do not ask it to implement arithmetic, count items, resolve symbols, or interpret the grammar. Natural language belongs inside explicitly delimited questions and criteria.

## Layers

```text
.sos source
    -> lexer/parser -> source-mapped AST -> type/effect checking
    -> typed execution plan -> Go interpreter
                                 | pure computation
                                 | Jev judgment provider
                                 | permitted host operations

shared analysis core -> LSP -> editor clients
runtime events       -> CLI / desktop judgment inspector
```

The first execution plan can be a compact typed tree with judgment nodes. Use its interpreter as the reference semantics, then add a Go code-generation backend for native and WASM builds. Bytecode and a direct JavaScript emitter remain optional later backends. Shipping a compiler front end and interpreter does not require shipping a native-code compiler.

Jev can appear between deterministic stages: validate input -> ask independent questions -> apply policy -> assemble new state -> ask a dependent question -> emit output. Each question gets explicit state; there is no invisible conversation history. Later judgments consume previous answers as data, including their uncertainty, rather than treating model output as established fact.

## Proposed surface language

The leading syntax candidate uses readable keywords and explicit `end` blocks. It remains a fixed grammar: `for each`, `in`, `if`, `then`, and `end` have exact meanings. Arbitrary English synonyms are not executable syntax. Prefer one canonical spelling per operation and let formatting expose the structure.

A model-free program should feel complete:

```sos
use files
use text

function word_count(content: string) returns integer
  return length(text.words(content))
end

let paths = try files.list("./notes", pattern: "*.txt")
var total = 0

for each path in paths
  let content = try files.read_text(path)
  let count = word_count(content)
  set total = total + count
  print "{path}: {count} words"
end

print "Total: {total} words"
```

`let` is immutable; `var` declares a mutable local; `set` assigns to it. `try` propagates typed errors. Named arguments, interpolation, path ordering, and word tokenization need documented semantics. The file module should return a stable sorted listing by default. This is illustrative syntax, not a runnable file today.

The same language can then introduce Jev explicitly. Records and question declarations use braces as data syntax; control flow uses `end`. This separate example emits a routing recommendation:

```sos
input ticket: { message: string }

let assessment = try judge ticket {
  refund: noul "Does the customer explicitly request a refund?"
  team: choice "Which team should handle this request?" {
    billing: "Payments, charges, invoices, and refunds"
    technical: "Product failures and technical assistance"
    other: "Requests outside those categories"
  }
}

if assessment.refund.p_yes >= 0.90 then
  emit { route: "refund-review" }
else if assessment.team.confidence >= 0.80 then
  emit { route: assessment.team.value }
else
  emit { route: "manual-review" }
end
```

`try` propagates a typed runtime failure and ends the script with a nonzero exit status; it does not supply a fallback answer. Thresholds above illustrate syntax and require domain evaluation before use. The refund threshold is not the Choice confidence statistic.

One `judge` block defines one explicit batch over the same immutable state. Its fields cannot depend on each other; dependent questions require another block. Do not silently reorder or rebatch model calls as a compiler optimization. Exact batching belongs in recordings and evaluations.

Proposed result types:

| Expression | Result payload | Explicit decision |
| --- | --- | --- |
| `noul` | `NoulAnswer { p_yes: probability }` | Compare `p_yes` with a threshold |
| `choice` | `ChoiceAnswer<E> { value: E, probabilities, confidence }` | Inspect distribution/confidence, then use value |
| `score` | `ScoreAnswer<R> { expected: number, probabilities, confidence, rubric }` | Compare expected rubric position with a threshold |

The provider operation returns `Result<payload, JudgeError>`. Noul has no provider-supplied confidence field. Choice labels form a closed type inferred from the declared options. Score is an expected rubric position, not a precise measurement or an integer class.

No answer-to-boolean coercion: `if assessment.refund` is a type error. Comparisons yield ordinary booleans. The compiler can enforce explicit access and valid option names, but cannot prove a natural-language question is appropriate or a threshold calibrated. An explicit `other` option is still a model answer, not a substitute for transport failure or uncertainty handling.

Initial core: immutable `let`, mutable `var`/`set`, booleans, strings, integers, floating numbers, records, lists, dictionaries, closed choices, `if`, `for each`, `while`, functions, modules, `Result`, and explicit `try`/error matching. Execution budgets are runtime policy, not a ban on general loops. Standard modules begin with text, math, collections, JSON, files, and console; HTTP and process execution follow through host capabilities. Define checked integer overflow, division-by-zero errors, stable evaluation order, and no implicit string/number coercion. Avoid classes, inheritance, macros, and arbitrary Go imports in v0.

## Runtime contract

- Separate pure computation, `judge`, and host I/O effects. Checking, formatting, and LSP analysis never call Jev.
- Validate provider answers before exposing them: question IDs, answer kinds, labels, finite numbers, ranges, and distribution shape. Malformed or missing responses are errors, never zero-value answers.
- Typed failures distinguish authentication, timeout, cancellation, rate limiting, invalid response, budget exhaustion, and replay mismatch. Low confidence remains a successful answer.
- Use Go contexts for run cancellation and deadlines. Bound steps, loop iterations, concurrency, input size, and HTTP attempts, including retries. Cancellation cannot retract a request already processed remotely.
- Enforce call/token ceilings locally where possible. Report estimated money separately from provider-reported usage; do not promise an exact dollar ceiling without reservation/accounting support.
- Support arguments, console output, JSON, and filesystem scripting in the native first slice. Expose filesystem, network, or process capabilities through named host APIs and a run-level permission policy. Model output cannot create new capabilities.
- Treat a process boundary as crash isolation, not an OS security sandbox. Stronger isolation is separate work if running untrusted scripts.
- Record source/plan hash, language version, input snapshot or digest, requested and returned model identity, questions, batch shape, responses, attempts, usage, and source spans. Store sensitive state only under an explicit recording policy; never store API keys.
- Replay supplies recorded judgments and captured external inputs without network calls. A mismatched program, input, or call sequence fails. Replay tests program behavior; fresh live evaluations measure model behavior. Pinning a model name alone does not promise identical live answers.

## Delivery

One Go CLI executable exposes proposed commands `sos run`, `sos check`, `sos fmt`, `sos test`, `sos build`, and `sos lsp`. Running/checking do not require the Go toolchain on the end-user machine. Building through Go requires a compatible Go toolchain on the builder; consuming a native artifact does not.

| Path | Strategy | Contract |
| --- | --- | --- |
| `sos run app.sos` | Interpret the typed core | Fast development and reference behavior |
| `sos build app.sos --target native` | Typed core -> generated Go + SysOneScript runtime helpers -> Go compiler | Native executable, no SysOneScript install needed by recipient |
| `sos build app.sos --target wasm-browser` | Same Go backend targeting `js/wasm` | WASM plus matching Go JS support and browser host adapter |
| `sos build app.sos --target wasm-wasi` | Same backend targeting `wasip1/wasm` | A WASI host and its supported capabilities |
| Future `--target javascript` | Separate typed-core -> JavaScript emitter | JS runtime helpers and semantic conformance; not supplied by the Go compiler |

A temporary packaging prototype can embed the typed program into a compiled Go interpreter. Label that accurately: it is a standalone executable whose SysOneScript code is interpreted. The intended Go backend lowers program operations to generated Go with helpers that preserve SysOneScript semantics. Generated Go is an implementation artifact; errors and traces map back to `.sos` source spans.

Define language semantics independently of Go: integer width/overflow, division, Unicode string indexing, dictionary iteration, equality, error propagation, and evaluation order. A future JS backend must preserve them explicitly, including integers that exceed JavaScript Number precision. Avoid arbitrary Go imports; expose curated typed host bindings so portability is knowable.

Keep the compiler core and pure standard library portable. Browser and WASI are distinct target profiles: browsers do not offer ordinary local filesystem/process APIs, and WASI host support does not imply browser DOM or unrestricted HTTP support. Reject known unsupported capabilities at build time; report unavailable dynamically supplied capabilities as typed errors. Browser-side Jev calls need a host/provider service that keeps deployment credentials off the client. Pure scripts must work offline.

Before expanding backends, run the same conformance programs through the reference interpreter, native output, and a WASM host. Cover overflow, strings, loops/mutation, errors, output ordering, and fixture judgments. Record startup time and artifact size rather than assuming Go produces tiny WASM.

Reuse the local `typesafe/` package behind a provider interface. Its existing panic-based answer accessors and permissive JSON response decoding are unsuitable as the language boundary without validation. Do not expose Go panics to script authors. Use the existing live Go client and API key for the first prototype, per user direction. A fake provider is not a prerequisite; keep syntax/type checks offline and add recorded regression cases when useful.

Suggested eventual Go package boundaries: `syntax`, `analysis`, `plan`, `runtime`, `provider/typesafe`, `protocol/lsp`, and `cmd/sos`. These are design boundaries, not directories to scaffold before a vertical slice needs them.

## Language Server Protocol and desktop

The LSP shares parsing, name resolution, types, source locations, and diagnostics with the CLI. First support diagnostics, hover, completion, go-to-definition, and formatting. Preserve incomplete syntax and handle document versions, cancellation, Unicode position conversion, and stale result suppression.

Use stdio JSON-RPC for ordinary editor clients. Monaco needs a client/transport bridge to the Go language server; embedding Monaco alone does not supply SysOneScript intelligence. Use consistent document URIs and package its workers correctly. Keep run/stop/trace events separate from LSP, so runtime features do not contaminate editor analysis.

The tentative desktop host is Wails plus Monaco: Go backend, web UI, packaged assets. Confirm platform webview prerequisites and target OS packaging in a spike before committing. A packaged executable does not imply no OS runtime dependencies. Electron is an alternative if a bundled Chromium environment becomes essential; a Rust host adds a second systems toolchain and needs a concrete benefit here.

The desktop app should contain a small file list, editor, input fixture pane, Run/Stop controls, output/diagnostics pane, and a judgment inspector. The inspector connects source expressions to distributions, state, timing, usage, and replay. Keep credentials in the backend; execute scripts in a worker process to keep the UI responsive and terminate stuck runs. Use the same engine version across CLI, worker, and LSP.

## Build sequence and acceptance checks

1. **General language slice:** parse/check/run the file word-count example, with functions, loops, mutation, modules, and errors. Prove useful work offline without a Jev provider. Start parser recovery and source mapping now.
2. **Native build:** lower the typed core into Go and build a standalone executable. Compare output and failures against the interpreter using a shared conformance suite.
3. **Jev operations:** adapt the existing client, preserve typed uncertainty, and verify bounded retries, cancellation, malformed responses, recording, and offline replay.
4. **WASM slice:** execute pure/constrained-host versions of the same programs in one chosen WASM host; verify browser/WASI capability diagnostics. Select the first host before promising both.
5. **Language server:** exercise incomplete `.sos` edits through an LSP client; verify diagnostics, hover, completion, navigation, formatting, and stale document handling without model credentials.
6. **Desktop:** package Monaco, LSP transport, and a worker with Run/Stop, output and judgment inspection. Verify packaged launches on selected target systems.
7. **JavaScript decision:** add a direct emitter only if native JS interop or distribution needs justify another backend and its conformance burden.

The first demo runs the same readable general script through `sos run` and as a standalone binary. A second demo adds one judgment and replay. WASM and the optional desktop extend this foundation.

## Open decisions

- Confirm exact pseudocode syntax: proposed keyword-led `end` blocks, conventional expressions, and fixed module calls.
- Which first general script best represents the desired feel: file processing, HTTP/JSON automation, or developer tooling?
- Which WASM host comes first: browser or WASI?
- Which desktop OS comes first? “co base desktop app” is not yet interpreted as a specific framework requirement.
- Should a later pseudocode surface map only to known operations, or use a separate code-generating model to propose editable SysOneScript? Current Jev cannot generate that source.

## OMP design review synthesis

Two independent tool-disabled OMP advisers initially reviewed language semantics and compiler/editor architecture; a third review was requested after the user clarified general scripting and build targets. Their outputs are proposals, not API evidence.

- Adopt: shared compiler core, typed intermediate representation, reference interpreter, parser error recovery, explicit uncertainty, and offline replay. The clarified user scope makes a Go build backend a planned early milestone, superseding the earlier suggestion to defer native output.
- Decline: banning arithmetic because Jev struggles with it. Ordinary arithmetic belongs in the deterministic Go evaluator.
- Defer: probabilistic `and`/`or`, bounds calculus, and a mandatory decision type. These need a separate semantics proposal; comparing explicit probability fields avoids implied independence today.
- Do not adopt unsupported provider assumptions: restricted Score level counts, a universal refusal result, or categorical claims that batching corrupts answers. Match the documented API and evaluate the exact chosen batch configuration.
- Treat a browser-hosted Monaco prototype as an optional packaging experiment. Wails remains tentative; no framework or LSP client dependency has been selected or installed.
- Keep replay as a validated ordered recording, not simply a cache. Repeated calls and external inputs must remain distinguishable.
- The follow-up review preferred embedding the interpreter for standalone delivery. Keep that as a packaging shortcut, while retaining Go lowering as an early milestone for general scripting. Reject its suggestion that current Jev proposes source programs, and do not adopt its unmeasured WASM size estimates or claim that model latency dominates every general script. Backend conformance is the useful shared recommendation.

## Evidence

Official documentation checked 2026-09-19:

- [TypeSafe System One](https://docs.typesafe.ai/concepts/system-one): constrained typed judgments; no generated code or explanations.
- [TypeSafe confidence](https://docs.typesafe.ai/confidence): Choice/Score confidence and Noul distinction.
- [Jev 1.13 limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13): numeric precision and literal interpretation.
- [Language Server Protocol](https://microsoft.github.io/language-server-protocol/): reusable editor/server protocol.
- [Monaco](https://github.com/microsoft/monaco-editor): editor models, providers, workers, and integration boundaries.
- [Go WebAssembly](https://go.dev/wiki/WebAssembly) and [Go WASI](https://go.dev/blog/wasi): separate browser/JS and WASI targets and host requirements.
- [Wails](https://wails.io/docs/introduction/): Go/web desktop host and native webview use.

Local grounding: `typesafe/questions.go`, `typesafe/answers.go`, `typesafe/client.go`, and the existing semantic-linter experiments under `cmd/semlint/`.
