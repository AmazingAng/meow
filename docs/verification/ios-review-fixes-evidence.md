# iOS review fixes — verification evidence

Date: 2026-10-17 (Asia/Shanghai). Spec: [ios-review-fixes-spec.md](ios-review-fixes-spec.md).
Spec approval: not obtained (autonomous run); the user authorized fixing verified issues and simulator tests/screenshots. No Git initialization, commits, new dependencies, or production API changes.

Source-tree SHA-256: `d4a21354fa7c9126146addfb84f6828d5ccdb2a5fdd5e0498e4c04c21dde9959`. The verification script hashes sorted relative paths and contents of Swift/Go/Python/project/scheme files. Isolated and landed trees produced the same hash before and after verification. Documents, screenshots, and build artifacts are excluded from the code hash. Work was developed in a copied checkout, then only reviewed files were copied into the original workspace and retested there.

## Reproduce

Create a dedicated iPhone 17 Pro / iOS 26.5 simulator named `Meow Fix Verification` (the script refuses everyday simulators). With its UDID:

```sh
python3 ios/scripts/verify_review_fixes.py --simulator-id <UDID> --output /tmp/meow-review-new-run --mutations
```

The output directory must not already exist. Uses Xcode local simulator signing (`CODE_SIGN_IDENTITY=-`), not developer-account signing. Installs the test app and resets only the named disposable simulator's app/keychain. Starts a temporary Go service and local forwarding proxy, keeps registration secrets only in memory, creates questions via the actual API, and stops the service after the run. No app data on the user's existing simulator is reset.

Tool versions: Xcode 26.6; Build version 17F113; go version go1.27.2 darwin/arm64; Python standard library only.

## Results on the final source

- `go test -race ./...`: passed (see runner's go-test.log).
- iOS XCTest: **18 unit tests, 0 failures, 0 skipped**.
- Simulator UI: **6 tests, 0 failures, 0 skipped** on iPhone 17 Pro / iOS 26.5.
- Exact text answer, `reject`, and `sqlite` verified by reading the resulting question through the real server API.
- A temporary HTTP 503 retains a reply; a server-switch attempt is refused; after restart the original answer reaches the original server.
- **10 named screenshots** exported; representative inbox, question controls/results, pending reply, and server-switch error screens were visually inspected. Only deliberate screenshots are copied here; automatic recordings that traverse the key page remain outside the repository.
- **5/5 behavior mutants killed** on the identical source hash in the isolated checkout: permit unsaved sends; allow switching with pending replies; allow server changes during registration; accept stale inbox responses; omit first load. Every mutant executed its named test and failed assertions, rather than merely failing compilation. The runner restores and hashes the cloned source after each mutant; the original source is never mutated.
- Python validation runner syntax checked with `python3 -m py_compile ios/scripts/verify_review_fixes.py`.

Detailed final counts, versions, source hash, and coverage: [ios-review-fixes-results.json](ios-review-fixes-results.json).

## Acceptance mapping

| Behavior | Executable evidence |
| --- | --- |
| Never claim a reply is saved if writing fails; never send an unsaved idempotency key | `ReplyQueueTests.testUnwritableQueueDoesNotSendOrClaimPersistence` |
| Persist and replay exactly the same key after relaunch | `testOfflineReplyIsPersistedAndRetriedWithSameKey`; real-API offline/restart check |
| Do not overwrite a corrupt queue | `testCorruptQueueIsNotSilentlyOverwritten` |
| Keep durable retry state when cleanup fails | `testFailedQueueCleanupRetainsKeyUntilItCanBePersisted` |
| Preserve pending replies/credentials on refused switch | `SessionRegressionTests.testServerSwitchPreservesPendingReplyAndIdentity`; `OfflineReplyUITests.testSavedReplyPreventsChangingServer` |
| Atomically pause sends; refuse switching while duplicate HTTP sends remain in flight | `testPausedQueueCannotSendUntilResumed`; `testSwitchCannotPauseWhileDuplicateSendIsStillInFlight` |
| Lock the registration destination | `testServerCannotChangeDuringRegistration` |
| Ignore old inbox results after reset | `testOldInboxResponseCannotRepopulateAfterSwitch` |
| Load inbox without permission/test push/scene transition | `testFinishOnboardingLoadsInboxWithoutSceneTransition`; `OnboardingRegressionUITests.testSkipPermissionAndFinishLoadsInbox` |
| Keep existing expiry/auth/API behavior | Original 9 XCTest cases retained; unchanged Go code; Go race suite |

## Red runs and checker limitations

The original signed app produced **9 passes and 5 failing regression tests** before the fixes; see [ios-review-fixes-red-results.json](ios-review-fixes-red-results.json). Initial unsigned simulator attempts also failed Keychain access; those failures were not treated as evidence of an app defect. Local simulator signing resolved the harness issue. The first UI choice test expected “Answered” instead of the existing detail headline “You answered”; the expectation was corrected to the actual UI contract, retaining both the exact server-answer check and the disappearing-controls assertion. The server-switch UI test additionally scrolls to the offscreen settings row before tapping it.

An early storage mutant survived because the test created an unreadable path before initializing the queue, exercising the constructor's fail-closed check instead of the write failure. The fixture now becomes unwritable after initialization; unchanged assertions catch the save-path mutant. The runner correctly rejected the surviving mutant rather than claiming a kill. Its disposable-simulator guard was also tested against the user's existing simulator and rejected it before resetting anything.

Xcode compilation supplies static type checking. No repository Swift lint/format configuration or additional linter dependency was introduced. File-level coverage was collected; ReplyQueue.swift unit coverage is 222/513 executable lines. **A changed-line/branch threshold was not enforced**, so this report does not claim every new guard/error branch is exercised. Existing date/JSON round-trip tests remain; no new parser or serialization format was introduced, so no separate property-testing library was added. Full positive suites ran in both isolated and landed directories; randomized-order execution was not configured. No quantitative complexity gate, supply-chain audit (dependencies unchanged), or independent subagent verification was performed.

## Practical limits

V0 continues to have one active server. Saved/in-flight replies must finish before changing servers; no discard-queue UI or multi-server account storage was added. A corrupt/unreadable queue fails closed and is not silently replaced. APNs transport, actual lock-screen actions, device suspension budgets, and hardware Face ID/Touch ID still require device testing. The simulator tests cover app detail actions and local-network failures, not guaranteed background delivery.
