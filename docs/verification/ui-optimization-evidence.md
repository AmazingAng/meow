# UI review and implementation — 2026-10-20

Follow-up: the pagination, stale pending membership/count and unavailable CLI release link findings are fixed and verified in [UI review fixes](ui-review-fixes-evidence.md). The original implementation evidence below is historical.

## Recovery re-review

No new blocking finding in the three previously reported P2 fixes:

- Permanent queued-reply refusals are durably recorded and visible in Settings and question details; storage failures retain the original reply.
- Same-server re-registration is reachable from Settings and confirms the credential/inbox consequences before reset. Cancellation retains the session.
- Failed optimistic reads roll back and explain the failure; newer successful reads, answers, counts and sessions are protected from stale callbacks.

Fresh recovery harness: **86 passed, 0 failed**, recorded at `/tmp/meow-recheck-ui-20261020/results.json`. This run did not repeat the optional live-server integration. The earlier recovery report remains separate historical evidence.

## UI changes

- Inbox separates open questions from other notifications, puts the nearest expiry first, and keeps read-but-unanswered questions prominent. The summary links to the pending filter. Ordering applies to loaded pages, not an unseen server-wide result set.
- Notification titles lead; source/time become secondary metadata. Accessibility font sizes use a filter menu, stacked metadata and wrapping titles. Pending terminology is consistent in Simplified Chinese.
- Settings begins with server, last inbox sync, notification permission and push registration. Connection checks and notification testing are easier to find. Registration status explicitly does not assert actual push delivery.
- Long choice questions have a persistent jump to the answer controls. Free-text replies retain the keyboard-aware composer.
- Onboarding shows four-step progress, supports returning to earlier steps, and includes server-address and CLI-installation help. The existing server-change confirmation remains in use.
- All 34 added catalog strings have Simplified Chinese translations.

## Verification

The disposable HTTP fixture and simulator runner are in `ios/scripts/verify_ui_optimization.py`. It regenerates the Xcode project, uses a signed simulator build, exports screenshots and results, and removes its simulator/server afterward.

- iPhone 17 Pro, iOS 26.5: **88 unit tests + 5 UI flows passed**, no skips (`dist/ui-optimization/final/results.json`). Unit tests include the recovery suite and two new expiry/grouping tests.
- iPhone SE 3, iOS 26.5: flows 1–4 passed with the final app layout (`compact-verified`); Settings subsequently passed a focused rerun (`compact-settings`) after the test's scrolling helper was corrected. The full suite was not repeated after that test-helper-only fix. All five flows are therefore covered across those runs, not a claimed clean five-test single run. [Machine results and source hashes](ui-optimization-results.json) preserve the intermediate failure and final recheck.
- UI flows cover onboarding/help/back, priority ordering and filter navigation, maximum accessibility font size in Chinese, long-question rejection plus keyboard reply, and Settings test notification plus re-registration cancellation.
- `git diff --check` and localization JSON validation passed.

An existing storage-message assertion was made locale-independent by comparing with its localized expected message. The behavior assertion was not removed.

## Visual inspection and limits

Screenshots are in [ui-optimization-screenshots](ui-optimization-screenshots/). Inspection caught an oversized Settings status row from the nested icon label; the final value uses text and has a row-height regression assertion. Small-screen automation also needed bounded scrolling because clipped SwiftUI children could report themselves as hittable and full-screen swipes could overshoot a target.

The fixture exercises the real app UI and HTTP client with disposable data. It does not prove live APNs delivery, production-server behavior, VoiceOver traversal or physical-device rendering. No TestFlight upload or production configuration change was made. Existing unrelated working-tree changes were preserved.

To reproduce (fresh output directories):

```sh
python3 -B ios/scripts/verify_ui_optimization.py --output dist/ui-optimization/reproduce
python3 -B ios/scripts/verify_ui_optimization.py --output dist/ui-optimization/reproduce-small --device-type com.apple.CoreSimulator.SimDeviceType.iPhone-SE-3rd-generation --ui-only
```
