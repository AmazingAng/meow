# Cursor generation snapshot evidence

Spec: [cursor-generation-spec.md](cursor-generation-spec.md).
Spec approval: not obtained (autonomous run). Independent verification: not
performed. This is implementation-authored executable evidence, not proof of a
complete specification or a live Cursor/APNs delivery test.

## Reproduce

```sh
python3 -B scripts/verify_cursor_generations.py --output dist/cursor-snapshot-check
```

Use a new output directory. No dependency installation is required.
Toolchain: `go version go1.27.2 darwin/arm64`; Python standard library.
Baseline for changed executable blocks: `f7ae005`.
The JSON result contains exact hashes for the Go sources, tests and verifier.

## Final checks

- Full race suite with shuffled order: 548 passing test cases including subtests; no failures or skips.
- Compiler/build and `go vet`: passed. `staticcheck` is not installed and was not run.
- Go formatting and installer regression suite (9 tests): passed.
- New snapshot module: 117/117 covered statements.
- All changed executable blocks: 153/153 covered statements. Both 100% gates exit nonzero on a known-zero-coverage negative control.
- Identity fuzzing: 20520 executions in the final 3-second run, passed.
- Race/stress: 16 simultaneous first-stop writers, repeated 20 times; passed.
- Four isolated source-overlay mutants killed by behavioral tests: generation
  key removed; registration scope removed; frozen record overwritten; captured
  body dropped. The pure identity property kills the first two; atomicity and
  body correctness rely on their dedicated contract tests.
- Real CLI: 10 separate hook processes, 6 local HTTP submissions and 4 distinct
  notifications. Replays preserved the first body and delivery key. Capture sent
  no requests; a missing snapshot stayed generic after a late capture. No actual
  phone notification was sent.

## Contract mapping

| Behavior / failure | Executable check |
| --- | --- |
| Snapshot rather than a mutable transcript; frozen replay across later turns | TestCursorGenerationSnapshotsAndReplay |
| Missing snapshot stays generic after late capture | TestCursorMissingSnapshotFreezesGenericNotification |
| Server and credential/conversation isolation | TestCursorSnapshotScopeIncludesServer; TestCursorSnapshotIsolatedByConversationAndRegistration |
| Unambiguous identity tuples, including separators | TestCursorSnapshotIdentityUnambiguous; FuzzCursorSnapshotIdentity |
| No-summary and ignored stop statuses | TestCursorNoSummaryAndIgnoredStops |
| Paired hooks, idempotent reinstall/uninstall, preservation of foreign hooks | TestCursorSetupCapturesResponseAndPreservesForeignHooks; TestCursorHooksPairedRoundTrip |
| Partial writes, corrupt/oversized/unreadable state and publication failure | TestCursorSnapshotAtomicFailuresLeaveNoPartialRecord; TestCursorSnapshotPreparationErrorsDoNotSend; TestCursorSnapshotRejectsInvalidAndUnreadableFiles |
| Private permissions, expiry and unrelated file preservation | TestCursorSnapshotFilesPrivateBoundedAndExpired |
| Concurrent publish and concurrent unlink during cleanup | TestCursorConcurrentStopsFreezeOnePayload; TestCursorSnapshotDirectoryFaults |
| Sanitized errors and silent capture failure | TestCursorInvalidCaptureDiagnosticIsPrivate |

## Failure history and limits

Three new end-to-end behavioral regressions failed before the implementation.
The concurrency test then exposed an entry-stat/unlink race during cleanup;
missing files are now tolerated. The adversarial tuple test exposed ambiguous
separator concatenation; cache keys now hash a JSON string tuple. Both fixes
were followed by the final complete verification above.

State is a private local plaintext snapshot, not a persistent send queue.
Snapshots expire after 24 hours to match server idempotency retention, and are
removed on the next Cursor hook; idle installations run no cleanup daemon.
After this window, or after explicit state deletion, replay history is unavailable
and a stop may be generic. Filesystem failure to freeze a delivery suppresses
that submission and logs a sanitized error. No new packages, production config,
accounts, installed agent hooks, server or iOS behavior were changed by this CLI
repair. Users must upgrade the CLI and rerun `meow setup cursor` to install the
response-capture hook. Physical-device/installed-Cursor verification remains to
be done after that installation.
