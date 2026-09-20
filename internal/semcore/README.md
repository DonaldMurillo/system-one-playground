# Shared deterministic semlint core

Go semlint and SOS std/source share rule compilation, source-unit extraction,
cross-file indexing and diff parsing here. This package performs no model calls.
Extraction remains the existing language-aware lexical approximation; it is not
an AST parser, and cut context is explicitly disclosed in prepared states.

Prepare accepts the full scanned unit set so related-file context remains
available. Apply diff filtering to its returned units, not before building the
index. It preserves the Go linter's initial optional-context trimming order and
question instructions, including the abstention clause. The SOS caller owns
evaluation, threshold comparisons, retry policy, report filtering and exit code.

Changes to Go semlint's request construction should update Prepare and its
parity tests. Provider-triggered oversize retries remain caller policy; initial
context trimming is not a guarantee that a request fits the provider's limit.
Bundled rule JSON has one authoritative location: internal/semcore/rules.
Both Go semlint and SOS std/source embed these same files. Custom rule files
remain supported through the CLI and source.rules.
