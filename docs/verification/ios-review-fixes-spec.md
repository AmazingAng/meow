# iOS review fixes — executable scope

Spec approval: not obtained (autonomous run). The user authorized fixing verified, necessary findings and simulator testing/screenshots; no extra approval gate is imposed.

Isolation: copied the 1.6 MB repository to /tmp/meow-ios-fixes-work because the original has no Git repository. Implement and test there, then copy only changed files back and rerun validation. No git init, commits, new dependencies, or external services. Tests use existing XCTest, URLProtocol, Xcode, Go and a dedicated simulator; local Go service uses temporary SQLite files.

## Failure model / acceptance criteria

1. Disk failure must never look like a saved reply. A queue whose path cannot be written returns a visible storage failure, does not send, and does not retain an unsaved item. Existing persisted replies and their idempotency keys survive retry/relaunch. A failed deletion must leave retryable durable state rather than permit a silent server switch.
2. V0 has one active server. Switching/resetting is refused with a clear message while queued or in-flight replies exist; no reply is deleted by a server switch. The queue is paused atomically while switching and late old-session network results cannot repopulate the new session. This deliberately avoids introducing multi-account history or persisting bearer tokens in queue files.
3. While registration is in flight, server selection/reset is refused; a successful response belongs to the server that issued it. Switching resumes only after registration finishes or fails.
4. Completing onboarding starts the first inbox load without permission prompt, test notification, push, or app background/foreground transition. A UI test follows Get started → Not now → Done and verifies a loaded empty inbox, rather than an endless spinner.
5. Simulator UI tests exercise the real local API: onboarding, inbox, full question detail, text reply and the resulting server answer. Save screenshots without live credentials (no screenshot of the key page). APNs transport and Face ID on hardware are outside simulator evidence.

## Invariants

Keep the server HTTP API and question authorization/expiry rules unchanged. Preserve existing 9 iOS unit tests and Go race suite. Replies retain a stable idempotency key across retries. Queue files contain no new credentials. No app install/reset on the user's existing simulator: create a dedicated simulator.

## Planned files

Implementation: ios/Meow/App/ReplyQueue.swift, ios/Meow/App/AppModel.swift, ios/Meow/Views/OnboardingView.swift, ios/Meow/Views/SettingsView.swift (and first-load view only if needed).
Tests: ios/MeowTests/MeowTests.swift, ios/MeowUITests/AnswerFlowUITests.swift.
Reproducibility: ios/scripts/verify_review_fixes.py, docs/verification/ios-review-fixes-evidence.md, docs/verification/screenshots/*.png; this spec.

Gauntlet: failing regression tests before fixes, complete iOS unit suite and targeted real-API UI suite; compiler/static checks via Xcode; Go race suite; 3 manual behavior mutants; coverage report and explicit limits; inspect simulator screenshots. Record actual results, source-tree hash, versions, and skipped layers. No independent subagent review requested.

UI verification refinement: additionally simulate a temporary response failure, attempt a server switch, verify the blocking explanation is visible, then restart and confirm the original saved answer reaches the original server. Correct the UI test's expected resolved headline to the existing “You answered” copy; the answer and removed-controls assertions remain mandatory.
