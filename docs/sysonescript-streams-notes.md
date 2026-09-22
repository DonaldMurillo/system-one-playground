## 2026-09-21 - Stream lifecycle boundaries
- Scope: sysonescript-streams
- Trigger: Adding owned streams across interpreter and external modules.
- Approach: Keep one pull-based `streamSource`, validate before launch, and close newly owned handles at every scope exit.
- Evidence: `go test ./sos ./tests/e2e -count=1`; fixtures live in `examples/sos/streams/`.
- Next time: Extend stream behavior through the shared source, ownership, deadline, and typed-failure paths instead of adding adapter-specific lifecycle logic.
- Status: active

## 2026-09-22 - Derived stream policies and language lowering
- Scope: sysonescript-streams
- Trigger: Adding bounded timed transforms and concurrent handlers without disconnecting checked syntax from execution.
- Approach: Lower canonical sentences and `std/streams` aliases onto the same runtime constructors; keep retained bytes, timers, keys, admissions, cleanup, and obligations explicitly bounded.
- Evidence: `go test -race ./sos ./tests/e2e`; language regressions live in `sos/stream_language_lowering_test.go` and runtime boundary cases in `sos/streams_test.go`.
- Next time: Add a parsed-source E2E before exposing a stream construction in checker or editor metadata, then race cancellation, terminal delivery, and timer boundaries together.
- Status: active
