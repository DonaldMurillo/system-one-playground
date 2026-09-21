# SysOneScript examples

Every example is a self-contained folder with its entry point named `main.sos`.
Run commands from the example directory unless its README says otherwise.

## Language basics

- `collections`, `primitives`, and `standard-library` cover core values and APIs.
- `count`, `greet`, and `triage` are compact runnable programs.
- `packages` and `greet` demonstrate local imports.
- `tickets`, `team-report`, `urgent-filter`, and `urgent-tickets` process JSON data.

## Jev and semantic interpretation

- `jev-workflow` demonstrates an explicitly Jev-backed workflow.
- `semantic-gauntlet` pushes eight ambiguous sentences through semantic
  interpretation and exposes confidence, request use, and estimated cost.
- `vocabulary` and `vocabulary-project` demonstrate deterministic vocabulary
  matching and project-level vocabulary configuration.

## Projects and tooling

- `repo-assistant` is a multi-file Studio project.
- `semlint` is a complete repository scanner and calibration CLI.

## External modules

- `external-command` wraps an existing CLI without a shell.
- `external-stdio` implements the persistent JSON-RPC protocol with typed
  records, typed failures, secrets, process reuse, and deadline cleanup.
- `external-bundled` builds a checksummed external artifact into a relocatable
  standalone application and locked manifest.
