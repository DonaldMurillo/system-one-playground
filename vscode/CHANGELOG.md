# Changelog

## Unreleased

## 0.5.1

- First published 0.5-line build; the protected 0.5.0 tag failed release verification before any packages were published.
- Adds a live Streams view, per-stream stop controls, and a dedicated lifecycle output channel for run and debug sessions.
- Keeps stream inspection non-consuming and reports runtime-owned counters, credit, producer identity, terminal state, and failures.
- Adds the initial filesystem, HTTP, and time standard-library surfaces with runnable examples, shared runtime support, and editor integration. The broader specifications remain in progress.
- Keeps long-running project runs and HTTP services visible and stoppable from VS Code; isolates malformed HTTP requests so the listener can continue.
- Moves Project and Streams actions below their headings, and removes finished run sessions from the live Streams view while retaining lifecycle output.

## 0.4.0

- Adds typed external command and persistent stdio modules with capability policy, diagnostics, generated interfaces, and project controls.
- Adds checksummed external artifacts to relocatable standalone application bundles.
- Expands typed results and failures through runtime, build manifests, language tooling, and debugging.
- Expands and reorganizes runnable examples into self-contained project folders.

## 0.3.0

- Adds semantic interpretation batching, memoization, and canonicalization.
- Adds named records, typed failures, richer language tooling, and debugger hardening.
- Aligns the bundled CLI, extension, updater, and release artifacts on one version.
- Adds packaged-VSIX activation and runtime-content release gates.

## 0.2.0

- add the SysOneScript project Activity Bar panel and folder/file actions
- add secure Jev token setup through VS Code SecretStorage
- add native DAP debugging with breakpoints, stepping, locals, and evaluation
- add deterministic project Run, Check, Build, Debug, helpers, and visible output
- classify `plus`, `times`, `+`, and `*` as language operators, including inside interpolation
- make the Activity Bar a project-control dashboard instead of a file-system mirror
- add the S/1 Marketplace badge, Activity Bar glyph, and additive `.sos` file icon
- add a native walkthrough for project controls, debugging, Jev, and CLI setup
- add checksum-verified standalone CLI installers and `sysone update`
- detect and report version drift between VS Code and terminal CLIs

## 0.1.0

- Initial SysOneScript language support for VS Code.
- Added diagnostics, completion, hover, definitions, formatting, semantic
  highlighting, folding, inlay hints, code actions and CodeLens.
- Added explicit document analysis through the SysOneScript language server.
