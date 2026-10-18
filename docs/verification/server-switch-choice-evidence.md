# Server switching — verification evidence

2026-10-18 (Asia/Shanghai). Spec: [server-switch-choice-spec.md](server-switch-choice-spec.md).
Spec approval: not obtained (autonomous run). User explicitly authorized softening the restriction. Tier 3 checks focus on local data loss and asynchronous session isolation. No new dependencies, Git initialization, commits, or backend changes.

Source-tree SHA-256: `b529d0d609f17bce236f437fc96584b25914f60529a8acdf77f11c5c56d5dbba`. Hash includes sorted relative paths and contents of Swift, Go, Python, project/scheme, and string catalog files. The isolated copy and landed project match before and after verification. Only the changed files were copied back. Historical review evidence remains source-specific and its hard-block policy is superseded by this spec.

## Reproduce

```sh
python3 ios/scripts/verify_review_fixes.py --simulator-id <UDID> --output /tmp/meow-switch-new-run --mutations
```

Use a dedicated simulator named `Meow Fix Verification`; the script rejects everyday simulators. Output must be a new directory. The runner resets this dedicated simulator's app/keychain, starts a disposable Go service and forwarding proxy, and uses local simulator signing. Tools: Xcode 26.6 (17F113), iPhone 17 Pro / iOS 26.5, Go 1.27.2, Python standard library. Credentials are captured only in memory; deliberate screenshots exclude the registration key screen.

## Final results

- Landed and isolated source: **23 unit tests, 7 UI tests; zero failures or skips** in each positive run.
- `go test -race ./...`: passed in both directories (Go reused cached results; backend is unchanged).
- **10/10 manual behavior mutants detected** in the isolated source matching the landed hash. Every mutant executed its selected test and produced assertion failures with exit 65; compiler failures are rejected. Source restoration is checked after each mutant.
- Real API checks confirm text, approval rejection, and custom-choice answers.
- Keep waiting leaves the original registration intact. After app restart and recovery from HTTP 503, the exact saved answer reaches the original server.
- Discard switches to the new server and onboarding survives app relaunch. The actual App Group queue file is `[]`; the old server's question remains unanswered.
- **12 named screenshots** exported. Both choices and the switched/relaunched onboarding screen were visually inspected; the keyboard is hidden and both buttons are visible.
- Python runner syntax checked with `python3 -m py_compile ios/scripts/verify_review_fixes.py`.

Detailed final results and coverage: [server-switch-choice-results.json](server-switch-choice-results.json).
Screenshots: [choices](server-switch-choice-screenshots/server-switch-choices.png), [after discard and relaunch](server-switch-choice-screenshots/server-switched-after-discard.png).

## Acceptance mapping

| Behavior | Evidence |
| --- | --- |
| Preserve replies and identity unless discard is explicit | `testServerSwitchPreservesPendingReplyAndIdentity`; `OfflineReplyUITests.testKeepWaitingPreservesSavedReply`; real API restart check |
| Durably discard before switching; keep old identity if persistence fails | `testConfirmedDiscardAllowsSwitchWithOfflineReplies`; `testFailedDiscardPreservesServerAndDurableReply`; actual simulator queue-file check |
| Cancel requests without waiting for cancellation cooperation | `testDiscardDoesNotWaitForOrAcceptALateResponse`; `testDiscardCancelsSendAndStopsOldFlushAfterResume` |
| Ignore late success, temporary error, and permanent refusal | `testDiscardDoesNotWaitForOrAcceptALateResponse`; `testLateNetworkAndPermanentErrorsStayDiscarded` |
| Old flush must stop even after the new session resumes | `testDiscardCancelsSendAndStopsOldFlushAfterResume`; old-flush mutation |
| Warn that processed remote answers cannot be undone; display both choices | Both switch UI tests assert the warning and both buttons; screenshot inspection |
| Register on the new server after discard and retain it on relaunch | `DiscardReplyUITests.testConfirmedDiscardSwitchesServer`; screenshot shows `127.0.0.1:1` |
| Keep existing registration lock, stale-response rejection, expiry, idempotency, and ordinary answers | Existing 18 unit tests retained; all prior positive UI flows; unchanged Go suite |

The production sender captures server/token synchronously inside the queue actor before creating its network task. The old-flush test verifies that the previous batch cannot initiate another send after resume; token capture was also inspected in source, without a separate test that forces task scheduling between capture and send.

## Red runs and verification fixes

[Initial behavior run](server-switch-choice-red-results.json): 2 failing tests, 1 passing test. The failures reproduce explicit discard still being blocked and a late old result being accepted. Storage-failure preservation already passed through the previous hard block; its negative control now removes the durable discard write and is detected. The first attempt had an incorrectly constructed test fixture and did not compile; it is not counted as behavioral RED evidence.

The first actual UI run found that iOS 26's confirmation popover hides a cancel-role Keep waiting button. The test failed to find it. The implementation now gives this choice a normal visible action and dismisses the keyboard before showing the dialog. The UI assertion was retained and strengthened to require both choices and the remote-result warning. The old hard-block UI expectation was replaced because the user explicitly requested the new behavior; the default queue-preservation unit test remains unchanged.

The added cancellation, old-batch, and late-error tests initially passed on the implementation, then each was checked using deliberate behavior mutants. All are included in the final 10-mutant run.

## Limits and skipped layers

One active server remains the V0 model. Switching requires new registration and a new Device Key. Discarding local work cannot undo an answer already processed remotely. Corrupt/unreadable queues still fail closed in the ordinary switch flow. APNs, actual notification actions, hardware authentication, and background suspension were not tested here.

Xcode compilation supplies type checking. File-level unit coverage is ReplyQueue.swift 132/138 and AppModel.swift 242/522; per-run coverage is in the result JSON. A changed-line/branch coverage threshold was not enforced. No configured Swift linter, randomized execution, quantitative complexity gate, or independent agent verification was run. No new parser/serialization format or dependencies were introduced, so no separate property-testing library or supply-chain audit was added. The held-network and failing-filesystem tests constitute the targeted adversarial pass; they do not prove all scheduling or storage failure modes.
