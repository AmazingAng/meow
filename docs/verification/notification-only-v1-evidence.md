# Notification-only V1

Date: 2026-10-20

Meow V1 delivers agent notifications. Decisions and replies stay in the agent on the computer.

## Behavior

- The inbox offers All and Unread. Search, local history and unread state remain available.
- Legacy questions display their options as text and retain historical answers. The detail screen has no reply field, answer buttons or approval controls.
- All notification categories, including legacy question categories, register with no actions. The app ignores callbacks from old reply/approval actions.
- Foreground activation loads previously saved reply records without submitting them. Settings exposes those records for copying.
- The CLI rejects `ask` and `wait` before making a request. Setup's generated agent skill uses `send --alert` for attention requests and directs users to respond on their computer.
- Server question endpoints and underlying compatibility code remain available. No production service, installed CLI or local agent configuration was changed.

## Verification

Results:

- `go test ./...`: passed.
- `go vet ./...`: passed.
- iOS model/unit tests: 133 passed, zero failures (`dist/notification-only-v1/final/test.log`).
- Final simulator UI run: 8 passed, zero failures or skips (`dist/notification-only-v1/ui-verified/results.json`), iPhone 17 Pro / iOS 26.5.
- Reviewed screenshots for the inbox, read-only options and Chinese maximum accessibility text size. No clipped options or reply controls were present.
- `git diff --check`: passed.

Commands:

```sh
go test ./...
go vet ./...
python3 -B ios/scripts/verify_ui_optimization.py --output dist/notification-only-v1/final
python3 -B ios/scripts/verify_ui_optimization.py --output dist/notification-only-v1/ui-verified --ui-only
git diff --check
```

The iOS harness uses a disposable HTTP fixture and a signed simulator build. It checks notification categories, saved records, read-only question details, absence of response API calls, All/Unread filters, onboarding, settings, search, and Chinese accessibility text sizes.

Screenshots: [Inbox](notification-only-v1-screenshots/notification-only-inbox.png), [read-only options](notification-only-v1-screenshots/legacy-choice-read-only.png), [free-text question](notification-only-v1-screenshots/legacy-text-read-only.png), [historical answer](notification-only-v1-screenshots/historical-answer-read-only.png), [Chinese accessibility size](notification-only-v1-screenshots/inbox-large-zh.png).

## Scope and limits

No physical-device or live APNs delivery test was performed. The empty notification action sets are covered by unit tests; app controls are covered by simulator UI tests.

This change does not repair Codex completion-hook parsing or the existing 200-character summary limit. It changes the V1 interaction model.

During verification, a message-kind comparison caused a compile failure and was corrected to use the enum case. A new fixture counter initially shadowed `BaseHTTPRequestHandler.responses`; it was renamed and its HTTP endpoints were checked before rerunning UI tests. An older onboarding test expected “Skip for now” after the fixture's message had already been received; it now verifies “Done” and completes onboarding. The subsequent UI tests were rerun in a fresh simulator.
