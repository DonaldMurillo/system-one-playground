# Project provider environment

Studio stores per-project provider settings in the project root's `.env`.
The project API lists variable names, configured status, and origin; it never
returns their values. Saving settings replaces the file atomically with mode
0600 and clears the session's cached semantic analysis. The editor file API
rejects hidden files, so `.env` cannot be opened through the file tree.

Runs and semantic analysis copy these values into an invocation context using
`sos.WithEnvironment(ctx, values)`. This function copies its input map and does
not mutate process environment. The API key and base URL use a project value when
present and otherwise fall back to the launching process.

Model precedence is:

1. Explicit source or invocation/config model.
2. Project `TYPESAFE_DEFAULT_MODEL`.
3. Process `TYPESAFE_DEFAULT_MODEL`.
4. The provider's default model.

An explicitly empty project default suppresses the process default and selects
the provider default. Model selection occurs before runtime replay/cache identity
is computed and before fresh semantic analysis records its selected model.

Values are not added to execution results or traces. An explicit program can
still print data it reads, so this is secret-safe configuration handling, not
a sandbox for untrusted source code. The context environment currently configures
provider settings; it is not a general process-launch environment API.

The HTTP acceptance tests in `tests/e2e/project_workspace_test.go` exercise
project settings, provider key/model forwarding, explicit model precedence,
semantic analysis defaults, environment-driven cache invalidation, and absence
of credentials from normal responses and traces.
