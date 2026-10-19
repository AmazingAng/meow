# Notification failure safety — evidence

2026-10-20, Asia/Shanghai. Tier 3: identity isolation and durable reply races.

User authorized the preceding scenario inventory with “好的 补齐吧”. The
[detailed spec](notification-failure-safety-spec.md) was written autonomously;
separate approval of that file was not obtained. Independent verification was
not performed. This report describes executed checks, not a proof of all iOS
scheduling or physical behavior.

## Changes

- Direct notifications now require the current device ID. Old/unidentified
  direct notifications, stale relay bindings and missing credentials produce a
  generic alert without question actions, destination metadata, badge or thread.
  A legacy direct notification without device identity can no longer act on the
  currently selected server. Existing current direct pushes remain supported.
- Display checks expiry for direct as well as hydrated relay questions.
- Preserve a notification action's identity through delegate and AppModel. The
  reply actor checks it again before saving or capturing its sender, so a late
  A action cannot enter a resumed B queue. Old refusal results are checked again
  before local feedback is scheduled.
- The existing network fallback, verified-content preservation after auxiliary
  failures, explicit keep/discard choice, cancellation and idempotency remain.
  No new dependencies, public APIs, invitation gates or blanket switch bans.

## Reproduce and final results

```sh
python3 ios/scripts/verify_notification_failures.py --output /tmp/meow-failure-fresh --full
```

The output directory must be new. Actual final run:
`dist/notification-failures/final-3`. [Machine results](notification-failure-results.json).
Source SHA-256: `821e822abf0b737f1149405ae0cc134900d0e9b7bd94df6cbd77ed355ca2ef68`.
`state_hash()` in the entry point hashes sorted Git-listed tracked/untracked
non-ignored inputs, excludes `docs/verification/` to avoid report cycles, records
deletions, and requires the same hash after execution. The existing dirty tree
was preserved on `codex/notification-failure-safety`; no commit/reset/push.

Tools: Xcode 26.6 (17F113), Apple Swift 6.3.3, Go 1.27.2, Python standard library.

| Layer | Actual result |
| --- | --- |
| Native macOS XCTest using production shared sources | 41 passed, zero failures/skips; 41 passed again after mutant restoration |
| Full Go suite, uncached, race detection, shuffled order | 156 test/subtest pass events; zero failures/skips |
| Go static checks | `go vet ./...` passed |
| iOS SDK type/build check | Entire app, extension and test targets: `build-for-testing` passed, generic real-device destination, unsigned; no installation |
| Changed executable lines in shared notification/queue files | 24/24 covered: content 17, queue 6, delivery 1; one stored-property declaration is nonexecutable |
| Coverage checker controls | Missing input and zero execution counts both rejected for their intended reason |
| Behavioral mutation | 7/7 rejected by actual XCTest assertion failures; fresh compilation per mutant and byte-for-byte restoration |
| Format/syntax | Python compilation and `git diff --check` passed; `gofmt -l` emitted no file |
| Real HTTP/SQLite execution | Successful response deliberately lost after commit; same key returns original resolved time; different key gets 409; SQLite contains exactly one response |

Coverage explicitly **does not measure AppModel or AppDelegate wiring at runtime**.
Those wrappers are compiled with the iOS SDK and await physical confirmation.
Native execution does not run an iOS app, NSE process, lock-screen action or
simulator. The harness copies the original 11 ReplyQueueTests verbatim and adds
the new tests; UIKit-dependent model/session/UI tests are compile-only.

## Scenario mapping

All names below refer to tests actually executed in the final run.

| Scenario | Executed test | Status |
| --- | --- | --- |
| HTTP 401/404/5xx and malformed JSON retain only safe recovery destination | `testHTTPAndMalformedOriginResponsesKeepSafeRecoveryDestination` | pass |
| Offline, connection refused, timeout, TLS error; recovery hydrates again | `testNetworkAndTLSFailuresAreSafeAndRecoveryHydratesAgain` | pass |
| Extension expiration and late fetch complete once | `testExtensionExpiryReturnsExactlyOnceAndIgnoresLateFetch` | pass |
| Counts/receipt errors retain verified content | `testCountsAndReceiptFailuresKeepVerifiedContent`; `testSuccessfulFetchSurvivesReceiptFailureAndUpdatesBadge` | pass |
| Wrong returned ID cannot report receipt or provide actions | `testWrongOriginIDNeverReportsReceipt`; `testHydrateUsesOnlyAuthoritativeContent` | pass |
| Old direct IDs, missing ID/credentials and delayed old relay binding | `testOldDirectAndUnidentifiedNotificationsHaveNoActionsOrDestination`; `testMissingIdentityNeverDisplaysOldContent`; `testDelayedOldRelayAndDirectNeverFetchOrReportToNewIdentity` | pass |
| Switch while fetch/counts suspended | `testIdentityChangeDuringFetchCannotDisplayOldQuestion`; `testSwitchDuringCountsDropsOldBodyBadgeAndReceipt` | pass |
| Expiry during extension work, both direct and relay | `testDirectExpiryIsRecheckedAtDisplay`; `testQuestionExpiringDuringReceiptCannotKeepActionsAtDisplay` | pass |
| Old action enters queue after B has resumed | `testDelayedActionCannotEnterResumedNewServerQueue` | pass |
| Lost acknowledgment replays saved submission after restart | `testCommittedResponseWithLostAcknowledgmentReplaysSameKeyAfterRestart`; `TestReplyCommittedWithLostAcknowledgmentIsReplayedExactlyOnce` | pass |
| Permanent refusal ends retry; 429/5xx retain key | `testPermanentHTTPRefusalsStopAndTemporaryFailuresRetainOriginalKey` | pass |
| Duplicate concurrent sends and conflicting answers | `testConcurrentDuplicateUsesSameKeyAndConflictingAnswerIsNotSent` | pass |
| Corrupt/unwritable storage and failed discard/cleanup | `testUnwritableQueueNeverSendsAndCorruptFileIsPreserved`; `testFailedDiscardPreservesDurableAnswerAndSenderCanRetry`; original `testFailedQueueCleanupRetainsKeyUntilItCanBePersisted` | pass |
| Discard isolates late success/error and stops old batches | `testDiscardStopsOldFlushAndIgnoresLateSuccessOrFailure`; original queue regressions | pass |
| Keep existing expiry, permission, privacy and routing constraints | Full Go suite and native existing content/queue tests; signed archive/IPA inspection | pass within these test boundaries |
| End-to-end iOS action wiring, generic alert UI and physical A -> B behavior | [Physical plan](notification-failure-physical-plan.md) | unverified |

The seven mutants remove the direct-device requirement, accept an old relay
binding, expose stale direct content, permit expired relay actions, bypass the
queue identity check, accept old-epoch results, and ignore extension identity
changes. Each mutant modifies only a disposable source copy, removes SwiftPM's
build cache, compiles the changed file, and must produce completed failing test
cases. A compiler error is not accepted as a kill.

## Distribution and physical boundary

TestFlight **0.1.0 (6)** uploaded successfully at **00:48:54 +08:00**; Apple
reported the package processing. [Build record](notification-failure-build6.json).
Upload success is not installation readiness or a physical pass.

Both archive and exported IPA passed `ios/scripts/verify_distribution.py`:
correct build, valid signatures, production APNs, Time Sensitive entitlement,
App Group and both privacy manifests. Both bundles retain the exact provided
`kitten10.wav` bytes as `meow.wav`, SHA-256
`995a156c4cf66ac2ad0766c894a147f6032ea8efa6bf20668a38310615911773`.

No simulator was booted and the phone's TestFlight installation was not replaced.
The testing API and gateway remain running; no physical fault has been injected
in this session. Build 6 was subsequently installed and physically tested. Generic fallback was visible, but notification interaction crashed; the physical flow failed. See [crash diagnosis and fix](notification-callback-crash-evidence.md) and [physical results](notification-failure-build6-physical-results.json). Build 7 must be physically retested.
Historical build-2 offline/expiry and build-3 Focus evidence remain historical,
not passes attributed to build 6.

## Limits and honest notes

- No independent agent verification, Swift linter, property-testing framework,
  OS scheduling stress or iOS runtime coverage of the model/delegate was run.
  These are unverified/substituted layers, not implicit passes. Existing timed
  and data-driven fault tests cover only the declared cases. No new parser,
  serialization format or dependencies were added, so no dependency audit was
  needed for this change.
- Xcode emits the existing AppIntents metadata-extraction notices because the
  app does not link AppIntents; do not interpret compile success as zero notices.
- Old notifications can remain visible, and previously accepted APNs pushes can
  arrive after route revocation. Checks prevent their use of a new identity;
  cancellation cannot undo an old-server answer already committed.
- This run does not establish APNs reliability, foreground/background/force-quit
  scheduling, token rotation, public HTTPS or cellular access, nor UI behavior
  when iOS bypasses the extension. The visible generic relay APNs fallback and
  callback identity checks remain the defense in that case.
- Behavioral RED was observed for four content cases and the late-action queue
  case. The queue API seam existed without enforcement for that RED. The first
  runner assumed SwiftPM's XML covered Darwin XCTest, then was corrected to
  validate completed XCTest log cases and both All/Selected-tests suite endings.
  The original failing logs are preserved in `dist/notification-failures/`;
  [RED summary](notification-failure-red-results.json) separates that tooling issue.
- The first lost-ACK fixture selected HTTP 200 only; the actual API returns 201
  for a newly recorded reply. It was corrected to lose any successful 2xx
  response before rerunning. An initial direct-device mutant was equivalent
  because another existing check still rejected mismatched device IDs; it was
  replaced with the real missing-device-ID regression. No passing assertion was
  weakened to accommodate either issue.
