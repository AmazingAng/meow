# Inbox read and swipe action evidence

Historical verification for commit `8726548`, before the 2026-10-29 removal of the header read button. The header-related claims and ten-function coverage below describe that original revision; the appended correction records the current UI contract.

Contract: [inbox-actions-spec.md](inbox-actions-spec.md). Spec approval: not obtained (autonomous run). Independent verification: not performed. Tests and checkers were authored in this task; their results support the stated behaviors and do not constitute independent proof.

## Reproduce

From the repository root on macOS with Xcode, XcodeGen and the iOS simulator runtime installed:

```sh
python3 -B ios/scripts/verify_inbox_actions.py --output /tmp/meow-inbox-actions-check
git diff --check
```

Choose a fresh output directory. The verifier uses local disposable HTTP fixtures, intercepted `.invalid` hosts, disposable SQLite files and freshly created simulators. It records Xcode version and SHA-256 hashes for the application/test/verifier inputs. No production notifications, messages, credentials or servers are used. No dependencies were added. Server, CLI, gateway and wire API are unchanged.

Recorded tools: XcodeGen 2.46.0 and Python 3.14.3. Xcode and simulator versions are included with the final results below.

## Behavior mapping

| Contract | Evidence |
| --- | --- |
| Read/delete the complete ID snapshot, including more than one page, and exclude new arrivals/unmatched messages | `testReadSelectionIncludesUndisplayedMatchesButNeverOtherScopesOrNewArrivals`; `testSelectedDeletionIncludesAllPagesAndRetainsFailuresAndNewArrivals` |
| Keep partial successes and retry failures; typed cache-expiry 404 succeeds, unsupported endpoint 404 does not | The selected-deletion tests above and `testSelectedDeletionRejectsForeignScopeAndUnsupportedEndpoint`; existing `testMatchingReadSnapshotsIncludeUndisplayedMessagesAndPartialFailuresRollBack` |
| Bound concurrency, reject duplicate delete submissions and discard late results after server changes | `testSelectedDeletionIsBoundedDeduplicatedAndStopsAfterServerSwitch`; existing stale-read/deletion/session regression tests |
| Batch local read state and SQLite persistence rather than saving the full inbox for every target | `testThousandsOfReadTargetsPersistTogetherBeforeNetworkFinishes`: 3000 messages are locally read and persisted while only the first four network requests are held; all 3000 targets eventually complete |
| A fast second group visit is not silently dropped | `testAnotherGroupVisitWaitsForActiveReadWithoutLosingItsSnapshot` |
| HTTP 429 delays are respected and persistent rate limiting is bounded | `testSelectedReadAndDeleteRespectRetryAfter`; `testPersistentRateLimitStopsAfterThreeAttemptsAndPreservesMessages`, covering both read and delete |
| Entering a group reads its current snapshot once | `test26GroupEntryMarksItsMessagesRead` |
| New arrivals survive refresh and returning from a detail page; they can be explicitly read/deleted by swipe | `test28GroupVisitDoesNotReadNewArrivalOnRefreshOrDetailReturn` |
| Group swipe read/delete, confirmation/cancellation, removal after confirmation, header read action | `test27InboxReadHeaderAndGroupSwipeActions` |
| Message swipe read/delete; search-scoped deletion preserves unmatched messages and the group | `test29MessageSwipeAndSearchGroupDeletionKeepUnmatchedHistory` |
| Search inherited on entry only reads matches, including returning/restarting | `test21GroupSearchReadsOnlyMatchingMessagesAndModeSurvivesRestart` |
| Header stays compact, left-aligned and retains four view options | `test08CompactHeaderSearchAndViewMenu`; `test25PlainHeaderKeepsViewNamesAndLeadingAlignment` |
| Chinese actions, large Dynamic Type and native confirmation; compact dark appearance | `test30ReadActionsLargeChinese` on normal and iPhone SE 3 simulators; exported screenshots |
| Preserve legacy read-only questions, session grouping, cache-independent history and key reset | Complete existing unit suite; no protocol, notification-extension or dependency changes |

## Final results

- Final fresh run: **248 unit tests and eight isolated UI tests passed, zero failures or skips**. An additional **one UI test passed** on iPhone SE 3 in dark mode at maximum Chinese accessibility text size: nine UI runs in total.
- Five of five deliberate behavioral mutants were killed by their named assertion tests: wrong registration scope, broadened selection, removing failed deletions, updating only the first four read targets, and ignoring the server's retry delay. The unmodified baseline passed all 248 unit tests before injection; source was restored before final unit/UI execution.
- Coverage: **130/131 executable lines** across the ten named model functions. Every function passed its 80% gate; the retry helper was 11/12, with its final fallthrough return unexecuted. This is statement/function coverage, not branch or whole-app coverage. The zero-coverage negative control was rejected.
- Xcode **26.6 (17F113)** and iOS simulator **26.5 (23F77)**. Whitespace, Python syntax and localization JSON checks passed. Final application/test/verifier source hashes are in `dist/inbox-actions/results.json`.
- Inspected exported swipe and confirmation screenshots in normal English, maximum-size Chinese, and compact dark appearance. Native alerts retain visible actions and scroll their body when needed. The header remains on one line; no extra inbox header row was added.
- Compact reports, logs, summaries and screenshots are retained locally under `dist/inbox-actions/`. Full temporary DerivedData and result bundles are removed after extraction. No application or test source was edited after this final run.

## Failures and corrections

- The initial RED run had 240 passing and six failing cases: four new model tests and the two new UI flows. The model stubs performed no read/delete operation, and the previous interface lacked swipe actions/automatic read. The first automatic-read UI check also exposed a missing `total` field in the test fixture statistics; it was added before the green run.
- A separate RED test reproduced a real fast-navigation defect: while the first group was being marked read, entering a second group lost the second operation. The test observed one request instead of two. The model now waits for the active operation while retaining the second visit's original scoped ID snapshot.
- The first combined UI run passed all 244 then-existing model tests and the inherited-search UI case, but two later cases failed. The HTTP fixture reset unread flags while the same simulator retained local durable read states. Each final UI scenario now owns a fresh simulator and HTTP origin. Assertions were retained, not relaxed; an isolated swipe run then passed all 246 model tests plus its UI case.
- Two previous UI expectations intentionally changed with the product contract: the header now has a read action, and a search inherited before group entry is already marked read when the group menu opens. The four-option view menu still excludes read/filter/selection actions.
- Inspecting the real server showed a five-per-second mobile-device limiter. Two further RED cases demonstrated missing 429 retries. The shared idempotent read/delete helper now honors `Retry-After` with at most three total attempts; exhausted targets retain their failure behavior. The verifier adds a fifth mutant that removes the waiting delay.
- The first new-arrival UI run did not pull far enough to trigger native refresh and timed out waiting for the new row. The test now uses a held drag past the refresh threshold, keeping the same new-row and unread assertions.
- One mutation run was rejected because the simulator refused to launch the test host (`Busy / Application failed preflight checks`). This was not counted as a killed mutant. The verifier now reboots its own disposable mutation simulator between installs, waits for boot completion, and still requires an actual named test assertion failure.
- The first reboot attempt also stopped when Xcode had already left the device shut down. The boot helper now checks its actual state and only requests shutdown when it is booted; unexpected states remain a hard failure.
- The first compact-screen run could not find the target group because maximum-size rows and scope-dependent tie ordering put it below the visible lazy-list region. The UI test now scrolls to reveal that group before performing the same existence, swipe and confirmation assertions. Application behavior and the assertions were not relaxed.

## Limits and skipped layers

- The isolated tree is based on `dfb4f8d`, without the main checkout's unrelated untracked `web/` and ignored outputs. The landed sources must match the report hashes. No TestFlight upload or real-device verification is included.
- The coverage gate measures ten named AppModel selection/read/delete functions at 80% minimum each. It does not claim SwiftUI callback or branch coverage. A zero-coverage negative control checks that the gate rejects its known-bad input. The manual mutants are deliberately wrong behavior and must fail their named runtime test; a compiler failure cannot count as a kill.
- Swift/Xcode compilation supplies type checking; whitespace, Python syntax and localization JSON are checked. There is no repository Swift lint tool. Existing unrelated unused-result/compiler metadata warnings and test-fixture diagnostics (semaphore QoS and SQLite files unlinked during teardown) are not evidence of a new production warning, and were not hidden by deleting assertions.
- No new parser or wire format warrants an added property-testing dependency. Existing generated-range/grouping tests run with the unit suite; the 3000-target test adds scale and persistence observation rather than an arbitrary performance deadline. Randomized-order whole-suite testing and independent review were not performed. Final UI cases are isolated to remove the demonstrated order dependence.
- Read changes are optimistic and failures roll back after the batch finishes. Network work uses at most four requests concurrently; large groups still require one existing API request per target. Automatic read runs once per group visit, so changing the search while staying inside a group does not silently read more messages.
- This does not add a durable network transaction/outbox. Termination after a remote delete but before local commit, or after optimistic read persistence but before a failed response, is not covered by these tests. Existing local-storage failures remain visible through the model's storage-error mechanism. System notification-center cleanup and physical-device badge delivery are outside this app-inbox change.

## Header correction — 2026-10-29

The user removed the header read button from the design. The header now has only view switching, trash and search. The unused `unreadInboxSelection` getter was removed; batch mutation, scope, persistence and retry code are unchanged. The 3000-target test uses the existing generic selection helper and still asserts complete local persistence and the final read state. Header-specific UI expectations now require the button to be absent; group swipe and confirmation assertions remain.

Targeted verification passed: **248 unit tests and three UI tests, zero failures/skips**. Inspected the normal English header and maximum-size Chinese iPhone SE 3 dark-mode screenshots. The new run did not repeat the previous mutation gauntlet or claim its old ten-function coverage for this revision. Current compact evidence is retained locally in `dist/inbox-header-no-read/`; no server, CLI or TestFlight release was made.

Reproduce with fresh output paths:

```sh
python3 -B ios/scripts/verify_ui_optimization.py --output /tmp/header-check/compact --test-case test08CompactHeaderSearchAndViewMenu
python3 -B ios/scripts/verify_ui_optimization.py --output /tmp/header-check/swipe --ui-only --test-case test27InboxReadHeaderAndGroupSwipeActions
python3 -B ios/scripts/verify_ui_optimization.py --output /tmp/header-check/small-dark --ui-only --test-case test30ReadActionsLargeChinese --device-type com.apple.CoreSimulator.SimDeviceType.iPhone-SE-3rd-generation --appearance dark
```
