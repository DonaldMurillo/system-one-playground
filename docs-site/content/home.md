# System One Playground

One repository for typed Jev judgments, semantic developer tools, a readable scripting language and its editor. Pick the layer you want to use.

| Component | Start here | What it does |
| --- | --- | --- |
| Go client | [Use the client](/client) | Ask typed questions against text or structured state; inspect answers, errors and usage. |
| semlint | [Lint with semantic rules](/semlint) | Find candidate code sites deterministically, then ask focused questions about meaning. |
| SysOneScript | [Write a script](/docs/getting-started) | Use readable sentences, local packages, commands and optional Jev judgments. |
| Studio | [Open the workbench](/studio) | Browse project files, inspect hints and decisions, configure credentials and build CLIs. |

## Try it without an API key

From a source checkout with Go 1.25 or newer:

```sh
go run ./cmd/sos run examples/sos/collections.sos
go run ./cmd/semlint -sites-only cmd/semlint/fixtures
```

The first command runs a script; the second inspects source without sending it to
Jev. Continue with [SysOneScript setup](/docs/getting-started), [Studio](/studio)
or [your first Go API request](/client).

## Explore the rest

The [gate, playground and experiments](/tools) cover agent tool-call policy, executable API demonstrations and measurement harnesses. The [repository CLI walkthrough](/docs/repository-cli) ties the language, packages, Studio and Jev together in one project.

## Shared foundation, different entry points

The `typesafe` Go client speaks to the TypeSafe System One API. semlint and the gate apply judgments to developer workflows. SysOneScript exposes judgment and interpretation inside a language; Studio provides its interactive workspace.

These tools can make paid API requests. Start with offline examples or semlint's site inspection, then configure credentials for live judgments. Request limits and reported usage are not account-wide monetary spending caps.

## From source

Use Go 1.25 or newer for the repository tools. This documentation site uses a separate Go 1.27 module. Clone `https://github.com/DonaldMurillo/system-one-playground.git` to try the tools. The Go client is available at `github.com/DonaldMurillo/system-one-playground/typesafe`. This preview provides source builds, not prebuilt installers.

MIT licensed repository code; third-party components retain their own licenses. The client here is for **TypeSafe**, not Typesense search. This is the repository's documentation, not the provider's official API reference.
