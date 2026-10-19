# Notification failure safety

Tier 3: notification identity isolation, cancellation and durable replies.

Scope: the abnormal scenarios listed in the preceding conversation, authorized
by “好的 补齐吧”. This file records executable details autonomously; separate
approval of this file was not obtained. Do not claim independent verification.

## Setup

- Work on `codex/notification-failure-safety`, preserving the inherited dirty
  working tree; no commits, resets or pushes. A fresh worktree would omit the
  existing untracked gateway implementation and verification files.
- No new dependencies or installed tools. Use Swift/Xcode, Python and Go.
- Add `ios/MeowTests/NotificationFailureTests.swift`,
  `ios/MeowTests/ReplyFailureTests.swift`,
  `ios/scripts/verify_notification_failures.py` and this spec/evidence pair.
- Execute shared notification/queue XCTest cases natively on macOS, compiling
  the actual shared sources, with a disposable generated Swift package. Compile
  the entire iOS test target for a real device. Do not boot a simulator or
  replace the user's TestFlight app with a test/debug app.
- Real phone fault injection must use the actual production gateway and user's
  installed app; retain explicit pending status until the user confirms visible
  behavior. Restore any temporarily stopped test API. Any fix requiring a new
  app build needs a new TestFlight build before claiming physical verification.

## Failure model and executable scenarios

1. Origin network/TLS/timeouts, HTTP 401/404/5xx, malformed JSON and mismatched
   notification IDs: no verified question, category, reply fields or delivered
   receipt. The retained safe notification ID can be opened after recovery.
2. Extension expiration before fetch returns: handler once; a late successful
   fetch cannot add actions or report receipt.
3. Counts/receipt failure after successful fetch: retain authoritative content;
   expiry is checked again before display.
4. An old relay binding or direct device ID, missing identity, or a direct
   notification without device ID: no fetch, report, navigation or response using
   the new identity; display is generic and has no actions or old thread/badge.
   Current direct device IDs and current relay bindings continue working.
5. A -> B while fetch or receipt is suspended: late work cannot display A's
   content or mutate B's badge. Delayed A wakes are rejected even if revocation
   fails. Re-registration with a new binding/device is treated the same way.
6. A notification action validated on A but queued after switching/resuming on
   B: the original identity must be checked at the queue serialization boundary;
   no request or persisted reply is created for B. The action's identity must be
   passed from delegate through AppModel, not recaptured as B.
7. A response committed but acknowledgment lost: persist/reload/retry the same
   idempotency key; one authoritative result. HTTP 401/404/409/410 are terminal;
   HTTP 429/5xx/network failures remain queued. Queue corruption/write/cleanup
   failures never claim unsaved work is durable.
8. Concurrent identical submissions retain one key; a conflicting answer cannot
   replace it. Discard/pause isolates late responses and old flush batches.
9. Pending replies retain the existing explicit keep/discard choice. Failed
   discard preserves the old session. Cancellation cannot undo a response
   already committed on A.

## Invariants and validation

- No new server-switch prohibition, invitation gate, dependency, public API or
  APNs credential changes. Low priority stays silent; bundled meow sound stays.
- Run behavioral RED tests before fixes; for pre-existing protections use
  targeted mutants. Mutations must be restored byte-for-byte, isolated from the
  real phone and services. Record actual test failures, not compiler failures.
- The reproducible entry point records source hashes/tool versions, native
  XCTest results, changed-line coverage, mutation controls, full Go tests/vet,
  and real-device SDK build. Fail closed on missing coverage or test evidence.
- Honest limits: macOS execution does not demonstrate iOS extension scheduling,
  lock-screen UI or production APNs. Physical checks remain a separate record.

## Revisions

- Initial contract, before implementation edits. Older direct notifications
  lacking device identity will show a generic alert and cannot open/answer via
  the current session; this deliberately closes an unsafe legacy exception.
- Add `server/internal/api/response_failure_test.go`: exercise the real HTTP API
  and SQLite with a lost successful response, then replay the same key and
  inspect the single persisted response. TestFlight build 6 contains the iOS
  fixes; uploading does not by itself establish physical verification.
- Native verification also copies the original `ReplyQueueTests` declarations
  verbatim from `MeowTests.swift`; no assertions are changed. UIKit-dependent
  model/session/UI tests remain compile-only until physical testing.
- Verification artifacts also include
  `docs/verification/notification-failure-changed-lines.json`,
  `docs/verification/notification-failure-results.json`,
  `docs/verification/notification-failure-red-results.json`,
  `docs/verification/notification-failure-build6.json` and
  `docs/verification/notification-failure-physical-plan.md`. They contain test
  metadata and source/line mappings, no live credentials.
- Build-6 physical testing found a real crash on notification interaction:
  three matching reports symbolicate to the generated async `didReceive`
  completion bridge, called from a cooperative background thread into UIKit
  state restoration. Add `ios/Shared/NotificationCallback.swift` and
  `ios/MeowTests/NotificationCallbackTests.swift`. Replace both async delegate
  bridges with explicit completion-handler methods; handle responses and finish
  on MainActor, including missing/stale metadata and empty actions. Keep prior
  identity/expiry checks unchanged. Test background entry, suspension, exactly
  one completion and presentation return values. A targeted mutant must detect
  background completion. Compile and archive/upload TestFlight build 7; physical
  retest is required. Do not relabel build-6 fallback flow as passed.
