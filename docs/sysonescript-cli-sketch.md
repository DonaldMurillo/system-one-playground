# SysOneScript sentence grammar: a CLI stress test

Discussion sketch, 2026-09-19. This is proposed syntax, not an implemented CLI. It supersedes the conventional `let`/`function` surface in the initial design proposal. User direction: finite sentence constructions with meaningful modifiers and contextual synonyms; neither conventional programming with renamed keywords nor unrestricted English.

## Example: triage

Read JSONL logs, retain recent errors, group by service, optionally classify each service's errors using Jev, and write one report per service. Example invocation:

```sh
triage ./logs --since 2h --output ./reports --judge
```

```sos
command triage:
  argument source as folder
  option since as duration default 24h
  option output as folder default "./reports"
  switch judge default off

expect log-entry with:
  time as timestamp
  service as text
  level as text
  message as text

remember now called started
find files under source matching "*.jsonl" called logs
read each log in logs as lines of json into entries
  on failure stop with "Cannot read {log.path}"

require each entry in entries matches log-entry
keep entries where time >= started minus since
keep entries where level is "error"
sort entries by time ascending
group entries by service called services
sort services by key ascending

create folder output if missing
make reports as empty list

for each service in services numbered from 1:
  make category "unclassified"

  when judge:
    take first 50 items from service.items called sample
    classify sample by "Primary cause suggested by these errors" called assessment:
      "capacity": "Resource exhaustion or overload"
      "dependency": "An unavailable or failing external dependency"
      "application": "An internal application failure"
      "unknown": "Evidence does not support the other causes"
      on failure assign category "review"
      on success:
        when assessment.confidence >= 0.8:
          assign category assessment.value
        otherwise:
          assign category "review"

  make report with:
    service from service.key
    errors from count of service.items
    category from category
    examples from first 3 items of service.items

  append report to reports
  save report as json under output named "{number}.json"
    on existing stop
    on failure stop with "Cannot save report for {service.key}"

show reports as table with service, errors, category
show "{count of entries} errors across {count of services} services"
```

## Construction rules being tested

- A line is one construction; `:` opens an indented body. There are no optional prose words or arbitrary synonyms in this sketch.
- Declarations (`command`, `expect`) have fixed field constructions. `require` fails with a source line and data location when input violates the named schema. Schema names occupy an explicit type position.
- `called` names a result; `as` specifies a type or representation; `into` collects results. `read each ... into entries` concatenates parsed records in stable path/line order. A failed read aborts before downstream processing.
- `keep`, `sort`, and `group` operate on collections. `keep entries` replaces the named collection with its filtered result; `group ... called services` creates a new collection of `{key, items}` groups. Bare field names are allowed only in the collection's field position.
- `for each ... numbered from 1` binds the singular name and a scoped integer `number`. Sorting before numbering gives deterministic report names for an unchanged input snapshot.
- `make category value` initializes a name; `assign category value` updates the nearest existing binding. These are intentionally not global synonyms.
- `classify ... by ... called ...` declares a Choice question. Its quoted label clauses define options; reserved `on` clauses define failure/success behavior. `called assessment` binds a validated answer in the success branch.
- `on success` and `on failure` attach to the immediately preceding fallible operation at their specified indentation. `otherwise` attaches to the immediately preceding `when` at the same indentation. Failures do not manufacture a successful answer.
- `save ... as json under ... named ...` means filesystem output. `on existing stop` forbids replacement. `own` could later generate distinct destinations, but this example spells out the naming rule so its meaning is reviewable.

## Semantics and remaining issues

All counting, time comparisons, iteration, JSON handling, sorting, and file operations execute deterministically in Go. `classify` is the only model operation. Its criteria and confidence threshold are illustrative; confidence does not guarantee correctness. Sampling is explicit and limits what the model can infer; it does not bound total input memory.

For this example, input discovery is nonrecursive, sorted by path, and JSONL contains one record per nonblank line. Timestamps must contain an offset and normalize to UTC. `now` is captured once. Missing folders and invalid inputs stop with a nonzero exit; `create folder ... if missing` creates the output directory and otherwise leaves it intact, failing if the path is a file. A failure after earlier saves can leave partial output; this sketch does not promise a transaction. No matching errors yields an empty table and successful zero-count summary.

Before treating this as executable grammar, settle name shadowing, collection replacement, group record access, and indentation rules for operation handlers. Simplify those constructions before adding more synonyms. Resource budgets, streaming input, parallel judgment evaluation, replay, help generation, and packaging are next extensions, not silently supplied behavior.

## Review purpose

Use this example to judge whether the surface feels right. Then write a small grammar table mapping each accepted sentence form into typed core operations. Do not implement a parser for arbitrary English. An independent OMP review highlighted connector overloading, implicit references, modifier scope, and clause ordering. Adopt explicit result names with `called` and per-construction connector order. Defer implicit previous-result references and broad synonym sets; each needs a separate ambiguity test.
