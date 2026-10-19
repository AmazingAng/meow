# Frontend recovery verification

Scope: the three reviewed frontend recovery issues (silent permanent reply refusal, missing same-server re-registration, and optimistic reads with no failure recovery). The existing uncommitted changes remain intact on `codex/frontend-recovery`; no commit or push was made. No new dependency or public server API, no production credential/configuration changes.

Spec approval: **not obtained (autonomous run)**. The user authorized fixes; detailed scenarios were written autonomously in [the contract](frontend-recovery-spec.md). Independent verification: **not performed**. This report claims executable evidence within the stated scope, not independent assurance.

## Behavior mapping

| Contract | Regression evidence |
| --- | --- |
| Permanent refusal is visible | `testRetryRefusalReportsFailureInsteadOfSilentlyDisappearing` (behavioral RED before implementation) |
| Legacy queues, durable failure, no automatic resend, delete saved failure | `testLegacyQueueRefusalsSurviveRestartWithoutResending`; actual-server integration |
| Archive write failure preserves original bytes/key | `testFailureArchiveWriteErrorPreservesOriginalDurableReply` |
| Delete/reset write failure keeps records and explains storage | `testFailedRecordDeleteAndResetWriteErrorsKeepRecord` (extra storage-message RED before clarification fix) |
| Late refusal cannot replace concurrent success | `testLateRefusalCannotReplaceConcurrentAcceptedReply` |
| Later explicit success clears old failure | `testAcceptedNewSubmissionClearsOlderFailureForSameQuestion` |
| Same-server invalid-credential recovery, new credentials | `testSameServerResetAndRegistrationRecoversInvalidCredentials`; actual-server integration |
| Pending reply requires explicit discard | `testSameServerResetRequiresExplicitDiscardForPendingReply`; original session tests |
| Old failed records cannot cross registrations | `testFailedRecordsAreClearedByServerReset` |
| Read failure rolls back and explains | `testFailedReadRollsBackAndExplainsFailure` (behavioral RED before implementation) |
| Count rollback, coalesced taps, later retry succeeds | `testReadRollbackRestoresCountAndCoalescesDuplicateTaps` |
| Newer successful operations win | `testReadFailureCannotUndoMarkAllRead`, `testReadFailureCannotUndoAcceptedAnswer` |
| Late failure cannot change new session or double-count newer snapshot | `testReadFailureCannotChangeNewSession`, `testReadFailureDoesNotIncrementNewerServerCountSnapshot` |
| Unicode/escaping/long-answer/key round trips | `testFailureSerializationPreservesGeneratedAnswersAndKeys`: 64 deterministic generated cases; serialization/durability scope, not a full-state fuzzer |
| Existing identity, expiry, idempotency, callback/audio behavior | Original 70 unit tests included in native harness; all iOS targets compiled |

The user-visible settings button opens `ServerSheet(reregister: true)`. Same-URL Save in ordinary change mode remains a no-op. Confirmation explains new Device Key, previous inbox availability and pending-reply discard. Failed answers appear in Settings and question detail; the saved record may be copied or deleted. Mark-read failures use rollback and a toast rather than a second durable network queue. English and Simplified Chinese strings are supplied.

## Reproduce

```sh
python3 -B ios/scripts/verify_frontend_recovery.py --output /tmp/meow-frontend-recovery-fresh --full
```

Use a fresh output directory. Tool versions and source hash are recorded with final machine results. The runner builds the actual Go server, binds an ephemeral loopback port and isolated SQLite DB, and uses its real HTTP API for registration, delivery/read state, missing-question refusal and same-server registration recovery. Test credentials are created locally and are not printed. The server is stopped in `finally`.

The runner executes actual AppModel, ReplyQueue, API and shared logic on macOS. UIKit objects and notification-center calls are test boundaries; Keychain/App Group storage are isolated test boundaries. The notification authorization enum is mirrored because `.ephemeral` is unavailable on macOS. Sound-resource lookup uses the SwiftPM bundle. Original display helper declarations are reused verbatim. Business methods are not stubbed. Complete main app, extension, unit and UI test targets are compiled against the real iOS SDK; that is not iOS runtime execution.

## Verification status

Final fresh run: **87/87 native tests passed**, including the original 70 tests, 16 recovery regression tests and one real-server integration test. All 87 passed again after restoring the mutants. **7/7 manual behavioral mutants rejected**, with actual assertion failures after fresh compilation. Changed executable lines covered: **103/103** (AppModel 68/68, ReplyQueue 35/35; eight changed lines were not executable). SwiftUI bodies and platform adapters are explicitly outside this coverage claim. Both invalid coverage inputs were rejected, and removing the checker's defense made the negative-control harness fail.

Full iOS `build-for-testing` passed for the app, extension, unit and UI test targets. `git diff --check` passed and the localization catalog parses. Four existing-style AppIntents metadata-skipped notices are reported, because the targets have no AppIntents dependency; this is not a claim of zero compiler notices.

Source SHA-256: `89b1e7d3a5958e7914f2fc0621c9db2717c67d97e5505b06654d4a36371ccd95`. Xcode 26.6 (17F113), Swift 6.3.3, XcodeGen 2.46.0. [Final machine results](frontend-recovery-results.json). Logs are under `dist/frontend-recovery/final/`; the fresh-output command above reproduces them from repository sources.

A and the gateway remain healthy; the isolated integration server was terminated. The phone's existing TestFlight installation and its connection were not changed.

## Limits and failures encountered

- The first native setup failed compilation because iOS's ephemeral authorization case is unavailable on macOS. A platform-boundary enum resolved this; it was not counted as a behavioral RED. The two intended regressions subsequently failed on assertions.
- An early iOS build used the old generated project before regeneration finished and could not find the new view. The runner now generates the project synchronously before compiling; no app source change was needed for that setup failure.
- First changed-line coverage gate correctly rejected three unmeasured paths. Tests were strengthened to check inbox as well as detail state, reconstructed refusal metadata, and outstanding counts; no coverage threshold or behavior assertion was weakened.
- Native UI/framework adapters mean actual SwiftUI presentation, Keychain entitlements, system badge rendering and notification callbacks are not newly verified on a phone. **No simulator was booted, no phone app was replaced, and no TestFlight build was uploaded.** Existing build-7 physical passes are historical evidence for the preceding version, not this patch.
- No Swift linter is configured. Compilation supplies static type checking; whitespace is checked separately. No new dependency was introduced, so a dependency audit was not applicable. No server implementation changed; the real server was built and exercised, but the full Go suite was not repeated for this frontend-only patch.
- XCTest runs serially because existing session fixtures share configuration. Full native suite is repeated after mutant restoration; randomized full-suite order is not configured. The generated serialization corpus has a fixed seed. These are limits, not a claim of randomized-order or arbitrary-input proof.
- Manual mutants exercise the concrete failure feedback, durable archival, same-server reset, rollback, old-session guard, count revision and mark-all guard. Mutants execute only in disposable source copies, must freshly compile and fail actual assertions, and are restored byte-for-byte. The mutation score describes the suite, not every individual test or unmeasured SwiftUI control.
