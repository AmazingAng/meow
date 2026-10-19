# Compact inbox / TestFlight 0.1.0 (9) — 2026-10-20

## UI changes

- Use the native inline navigation bar instead of the large title, persistent search field and separate filter row.
- The inbox title opens a menu containing All / Unread / Pending, unread/pending counts, and Mark all as read. The envelope toolbar button is removed. The selected non-default filter remains visible beside the title.
- Search is a trailing navigation button. It expands an input field only while searching; Cancel clears the query and returns to the full list.
- Remove the Other notifications heading, including the wasted heading space when no questions are pending.
- Reduce the pending summary to one line; retain urgency ordering, the pending shortcut, Dynamic Type and accessibility labels.

## Verification scope

The main app, notification extension, unit-test target and UI-test target compiled with the real iOS SDK using build-for-testing. Existing menu interaction tests were updated, and a focused UI flow covers compact layout, searching, cancelling search, mark-all-read and switching back from an empty unread view. These UI tests were compiled but not executed in this turn. No simulator was booted and no installed phone app was replaced. No new screenshot or physical-device layout pass is claimed. Previous UI screenshots/results represent build 8 or earlier and do not verify this layout.

The initial archive failed because a SwiftUI @Bindable declaration was accidentally removed during cleanup. It was restored before the successful Release archive. This failed attempt is retained in dist/testflight/0.1.0-9/archive-first-attempt.log; only the subsequent successful archive is used for export.

Final archive and source/resource manifest are under dist/testflight/0.1.0-9/. JSON localization validation and git diff --check passed. AppModel, API, reply persistence and server logic were unchanged; no prior model/Go test counts are presented as fresh UI execution.

Archive and exported IPA verification passed, including production APNs signing, App Group, privacy manifests, Time Sensitive entitlement and the original kitten sound in both bundles. At 2026-10-20T04:23:49+08:00, Xcode reported Upload succeeded / EXPORT SUCCEEDED and returned exit 0. Apple reported the package is processing; processing completion, tester availability and phone installation are not inferred from upload success. Upload status is recorded in dist/testflight/0.1.0-9/status.json. Physical verification should check a Chinese inbox with no pending questions, all three menu filters, search/cancel, and the pending shortcut, on regular and large text sizes.
