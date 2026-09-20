# Semantic lexicon pipeline

SysOneScript semantic interpretation is a retrieval and validation pipeline,
not unrestricted code generation:

1. Match source phrases against the compiled lexicon.
2. Retrieve only registered language or module definitions for those concepts.
3. Compose host-known constructions and reject malformed expressions.
4. Remove candidates that conflict with visible binding types and scope.
5. Lower locally when a familiar sentence shape leaves exactly one candidate.
6. For an unfamiliar sentence shape, ask Jev whether a host-composed candidate
   actually matches the prose—even when only one valid composition survives.
   For lexical ambiguity, ask Jev to choose among the bounded survivors. Both
   forms include an explicit reject option.
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

The semantic composer also recognizes the same concepts in unfamiliar clause
order. For example:

```sos
only show "adult" once age has gone beyond 18
```

The host retrieves `modifier.only`, `language.show`, `language.when`, and
`operator.greater_than`, validates the expressions and types, and offers only
the canonical `when age > 18` composition. Because the sentence shape itself is
not registered, Jev must accept that alignment or reject it. Jev cannot return
arbitrary source or introduce a definition absent from the candidate.

Use `sos explain` to inspect the canonical lowering and its `matches` records.
The exact program below resolves and runs offline:

```sos
make age 21
if age bigger 18 show "adult"
```

Studio opens the bundled `semantic-dictionary` example by default in Examples
mode. Choose **Analyze** to inspect phrase-to-definition matches, then **Run** to
see the lowered program execute without a Jev token. Select
`jev-language-composer` to exercise the bounded Jev grammatical-alignment path.
