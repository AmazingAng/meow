# Notification interaction crash: TestFlight build 6

2026-10-20, Asia/Shanghai. Continues the authorized notification-failure work.
The spec amendment was autonomous; separate approval was not obtained.

## Physical failure and diagnosis

The user confirmed a generic fallback notification during the origin outage,
then reported Face ID unlock followed by a TestFlight crash prompt. This is a
failed end-to-end flow, not a successful physical fallback check. See
`notification-failure-build6-physical-results.json`.

`devicectl` copied these Meow crash logs without deleting them from the phone:

- `Retired/Meow-2026-10-20-005904.ips`
- `Retired/Meow-2026-10-20-010005.ips`
- `Retired/Meow-2026-10-20-010028.ips`

The local copies remain under ignored
`dist/notification-failures/crash-build6/`; raw reports are not committed.
All three identify `blue.meow.app` version 0.1.0 build 6 and the same executable
UUID `CE10EC82-9D76-3AAF-9627-491573CF586D`. `dwarfdump --uuid` confirms the
build-6 archive dSYM matches that UUID. `atos` resolves the Meow fault frame to
`@objc closure #1 in AppDelegate.userNotificationCenter(_:didReceive:)`.

Each abort is on a `com.apple.root.user-initiated-qos.cooperative` thread, with
the last-exception trace through `NSAssertionHandler` and
`-[UIApplication _performBlockAfterCATransactionCommitSynchronizes:]`, followed
by scene snapshot/state-restoration methods. This identifies the generated
async-to-Objective-C notification completion bridge running UIKit restoration
on a worker. A matching crash also precedes the outage, so origin unavailability
is not required to trigger it.

## Change

Replace both async notification delegate entry points with the SDK's explicit
completion-handler methods. `NotificationCallback.run` schedules handling and
the system completion together on MainActor, including ignored/stale inputs.
Foreground presentation completes before launching the inbox refresh, so a slow
API cannot hold the presentation callback. Existing binding, identity and expiry
guards are retained.

The header contract was checked in the installed iPhoneOS 26.5 SDK's
`UserNotifications.framework/Headers/UNUserNotificationCenter.h`.
No dependency, credential, server API, permission or simulator changes.

## Regression boundary

The new three callback tests start from a real background dispatch queue and
check MainActor handling, suspension/resumption, main-thread completion, returned
presentation options and XCTest over-fulfillment. Before the MainActor executor
was added to the callback helper, all three failed on the completion-thread
assertion: [RED results](notification-callback-red-results.json). This helper RED
reproduces the bridge's executor failure mechanism; the physical IPS reports
are the reproduction of the original app crash.

The tests do not instantiate an iOS `UNNotificationResponse` or execute UIKit
scene restoration. Compilation checks the actual delegate wiring; physical
TestFlight retest is required to confirm the repaired end-to-end behavior.
Do not turn a native macOS test pass into a physical pass.

## Reproduce

```sh
python3 -B ios/scripts/verify_notification_failures.py --output /tmp/meow-build7-fresh --full
```

The runner includes the earlier identity/queue regressions plus callback tests,
requires all four changed shared files in coverage, and adds a mutant that
removes the callback's MainActor executor. All mutation runs rebuild disposable
source copies and require assertion failures, not compilation errors.

Final fresh run: **44 native XCTest cases passed**, then passed again after mutation restoration; **156 Go test/subtest passes** with race detection, shuffle and no cache; `go vet` and complete iOS test-target compilation passed. **8/8 behavioral mutants** were rejected, including all three thread tests failing when MainActor is removed. Changed executable shared lines: **30/30**, including callback **6/6**. Two bad coverage controls were rejected. Source SHA-256: `1e9d9539bb4ec03166c3668b8bdb7cccdfac950cf6e7904754b5c3bfc767ea1a`. [Machine results](notification-callback-results.json).

Build 7 archive and distribution IPA have valid signatures, production APNs, both privacy manifests and unchanged supplied meow audio. Build 7 uploaded successfully at **2026-10-20 01:09:02 +08:00**, exit 0 with `Upload succeeded` and `EXPORT SUCCEEDED`; Apple reported processing. [Upload status](notification-callback-build7.json) is separate from installation and physical confirmation. Swift linter, property-testing framework, independent verification and UIKit runtime coverage were not run; compilation and explicit physical retest are not substitutes for those unmeasured layers.

The failed
build-6 physical record is retained. No simulator was booted, and no debug/test
app replaced the user's TestFlight installation.

## Build-7 physical retest

The phone installation was independently read as 0.1.0 build 7 via devicectl. The user confirmed TF7-OPEN-01 opens full detail from the lock screen without crashing. During an 18.11-second origin outage, production gateway/APNs replay produced only generic text and no approval/reply controls, confirmed by the user. The origin recovered automatically. The user then opened the generic notification, saw TF7-FALLBACK-01 in full, and approved without a crash. The actual CLI returned exit 0 with `approve`; SQLite contains exactly one response. These are physical passes for the described flows, on LAN. [Physical record](notification-failure-build7-physical-results.json). Server-switch and queue scenarios subsequently passed the listed physical checks: stale A actions were ignored, B control approval returned once, keeping the queue recovered the original reply once after foreground resume, and discard plus return to A/relaunch left zero discarded responses on either server. The final A notification opened in full without a crash. B was stopped after verification; A and the gateway remain healthy. [Full physical evidence](notification-failure-build7-physical-evidence.md). Immediate retry while the app is suspended was not established.
