## 2026-09-21 - Stream lifecycle boundaries
- Scope: sysonescript-streams
- Trigger: Adding owned streams across interpreter and external modules.
- Approach: Keep one pull-based `streamSource`, validate before launch, and close newly owned handles at every scope exit.
- Evidence: `go test ./sos ./tests/e2e -count=1`; fixtures live in `examples/sos/streams/`.
- Next time: Extend stream behavior through the shared source, ownership, deadline, and typed-failure paths instead of adding adapter-specific lifecycle logic.
- Status: active
