# A file-based tickets CLI

The [example](/docs/tickets-source) defines one application with three
commands. Run these from the repository root, using fresh output paths:

```sh
bin/sos run examples/sos/tickets.sos -- --help
bin/sos run examples/sos/tickets.sos -- import examples/sos/team-tickets.json --output tickets.json
bin/sos run examples/sos/tickets.sos -- triage tickets.json --criterion all --output open.json
bin/sos run examples/sos/tickets.sos -- report open.json --by team --output ./ticket-reports
```

Import validates the ticket schema. Triage keeps open tickets; `all` requires
no provider call. The fixture produces three open tickets, then two reports:
`1.json` for billing (two tickets) and `2.json` for platform (one ticket).
Reports are sorted by team. The example refuses to replace existing files.

For semantic judgment, use `--criterion urgent` (the default). It uses the
real Jev client to evaluate each open ticket against the explicit outage/charge
question, accepting at probability 0.85 or higher. Uncertain tickets are discarded;
a provider failure stops the command. Your configured request/time ceilings
apply. This judgment needs credentials and is not guaranteed to return the same
classification on every run. The `all` path is deterministic.

Build once and invoke the same commands directly:

```sh
bin/sos build examples/sos/tickets.sos --output ./bin/tickets
bin/tickets --help
bin/tickets triage tickets.json --criterion all --output selected.json
```

The executable embeds the interpreter, source, interpretation, and effective
configuration. It can run from a different directory without the source tree;
data paths remain relative to its invocation directory. Help and invalid inputs
spend no Jev requests. Building resolves the application; runtime `urgent`
judgments still require the provider.

This first slice uses JSON files and bounded materialized collections. JSONL
stdin/stdout streams, imports, and persistent project budgets are later stages in
the [roadmap](/docs/limits).
