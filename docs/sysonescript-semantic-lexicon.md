# Semantic lexicon pipeline

SysOneScript semantic interpretation is a retrieval and validation pipeline,
not unrestricted code generation:

1. Match source phrases against the compiled lexicon.
2. Retrieve only registered language or module definitions for those concepts.
3. Compose host-known constructions and reject malformed expressions.
4. Remove candidates that conflict with visible binding types and scope.
5. Lower locally when exactly one candidate remains.
6. Ask Jev to choose from the bounded surviving candidates only when ambiguity
   remains, with an explicit reject option.
7. Run the canonical checker over the complete lowered program.

The reviewable source is `semantic/lexicon.json`. `go generate ./sos` runs
`cmd/semanticgen` and creates `sos/semantic_lexicon_generated.go`. Production
analysis reads the generated Go table once; it does not parse JSON, load a
dictionary database, or scan WordNet at runtime.

The initial mappings are project-authored. Open English WordNet is the intended
build-time source for broader synonym discovery, but imported terms must retain
provenance and license metadata and still be curated into SysOneScript concepts.
WordNet terms never become executable merely because they exist: each concept
must map to a registered language or imported-module definition.

Current dictionary-backed grammar supports one-line conditionals whose condition
is an ordered comparison and whose action is `show`, including aliases such as
`bigger`, `greater than`, `at least`, `less than`, `display`, and `print`.
Quoted regions are excluded from phrase matching. Known scalar types are used to
reject invalid ordered comparisons before any Jev request.

Use `sos explain` to inspect the canonical lowering and its `matches` records.
The exact program below resolves and runs offline:

```sos
make age 21
if age bigger 18 show "adult"
```
