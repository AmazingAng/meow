# Cursor generation-bound notifications

Spec approval: not obtained (autonomous run). The user authorized fixing the
reviewed PR #9 bug; this document records the implementation contract for review.

Use the existing Go/Python toolchain and dependencies. Work in the temporary
`dist/cursor-generation-fix/source` worktree; remove it after committing the fix.
Update PR #9, without merging or publishing a CLI release. The separate requested
inbox-menu change will have its own commit on main.

## Contract and failure model

1. Capture only `afterAgentResponse.text` under its exact conversation/generation
   identity. Capture never sends a notification. Never read transcript bodies.
2. Stop retains completed/result, error/failure and aborted/unknown/ignored
   semantics. Missing snapshots produce an empty body, never a different turn.
3. First stop freezes its complete notification and delivery key. Subsequent
   stops for the same identity/policy reuse them, even if the title, response,
   transcript, process or next generation changes. Concurrent stops converge.
4. State is isolated by server and sender credential; credentials and raw paths
   never appear in filenames or diagnostic logs. Files are private. Cached bodies
   and deliveries expire after 24 hours, matching server idempotency retention;
   opportunistic cleanup bounds retention. This is not a persistent send queue.
5. Invalid capture input, corrupt or unwritable delivery state fails silently
   with sanitized diagnostics and no send. A missing/corrupt reply snapshot may
   produce a generic notification. No-summary records no response body.
6. Setup installs response capture plus stop; no-summary installs only stop.
   Reinstall/uninstall remove only exact Meow-owned commands, preserve foreign
   hooks and use the existing backup mechanism. Adapters stay pure and existing
   public adapter signatures and other agents' behavior remain unchanged.
7. New behavioral tests must be red on the old implementation. Verify process
   restarts, generation ordering, concurrent first writes, frozen replay,
   server/key isolation, retention, malformed/private state and setup migration.

## Verification

Run focused regressions, full randomized race suite, vet/build/gofmt, installer
checks, fuzz/property checks, changed-line coverage with a failing gate, at least
three behavioral mutants, and a real CLI/local-origin exercise. Record tools,
source state, failed controls, coverage limits and any skipped layer in evidence.
No physical-device push, account/key changes or installed-agent configuration
edits are authorized by this repair.
