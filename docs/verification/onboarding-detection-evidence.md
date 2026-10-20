# Onboarding receipt detection — build 12

The final onboarding page could wait indefinitely even though the computer's setup message was already in the inbox. Its asynchronous baseline fetch classified every message returned by that fetch as pre-existing. An early message, resuming onboarding, or a delayed/restarted baseline fetch could therefore exclude the very receipt the page was waiting for.

The page now reads an observable AppModel candidate from the complete, current device's saved inbox. It evaluates existing messages immediately and keeps synchronizing while waiting; it does not take a second baseline or depend on an array-change callback. Search, filters, and the first 50 visible messages do not restrict detection. An existing computer message is valid evidence for this registered device, including after navigating back or restarting unfinished onboarding.

Phone-initiated test IDs are excluded, saved with the hashed server/device scope, restored on relaunch, and removed by the existing completed server-reset flow. An in-flight phone test pauses detection until its returned ID is known; failed sends release that pause. These checks demonstrate a computer-to-server-to-app message path, not independent proof that APNs displayed a banner or played a sound.

Validation on 2026-10-20:

- 130 native XCTest cases passed with no failures. The existing macOS harness runs the actual AppModel, shared transport/models, reply queue and SQLite sources, with isolated UIKit/Keychain/notification-center boundaries.
- Eight new cases cover early receipt, an unchanged inbox refresh, an offline restart from saved messages, an empty inbox, phone-test exclusion across restart, a push arriving before the phone POST response, failed phone sends, full-history detection despite filtering/pagination, and server/device scope isolation.
- The UI regression now relaunches unfinished onboarding after a computer receipt and expects the same success without a second push. It was compiled, not executed.
- Generic iOS build-for-testing succeeded for the main app, notification extension, unit tests and UI tests. Release archive 0.1.0 (12) also succeeded.
- Distribution status is recorded separately in `onboarding-build12-status.json` when upload completes.

No simulator was started. No app was installed on a phone, and no physical onboarding interaction was automated. The colleague's exact UI flow remains a TestFlight recheck. No server or CLI release is required. Existing unrelated localization metadata edits were excluded from this change.

Local logs (ignored by Git): `dist/onboarding-detection/native.log`, `dist/onboarding-detection/ios-build.log`, and `dist/testflight/0.1.0-12/`.
