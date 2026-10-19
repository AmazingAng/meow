# TestFlight 0.1.0 (7): physical failure checks

Status: build 6 failed on notification interaction despite visible generic fallback; that historical failure is retained. Build 7 passed notification opening, origin-outage fallback/recovery, A-to-B old-notification rejection and B control approval. Offline reply retention passed after foreground resume. Explicit discard, return to A and force-quit/reopen passed; both servers still had zero discarded responses after the final foreground check. B was then stopped; A and the gateway remain healthy. See notification-failure-build7-physical-results.json for observations. Automated checks and archive inspection are separate evidence.
Do not record a physical pass from APNs acceptance, extension receipt, a native
macOS XCTest pass, or successful TestFlight upload alone.

## Preconditions

- Install build 7 through TestFlight, retaining the existing registration.
- Phone and Mac share Wi-Fi; current origin is `http://192.168.31.235:8182`,
  gateway is `http://192.168.31.235:8181`. Confirm IP and both health checks.
- Read the private CLI config and routing credentials locally; never paste
  secrets into this report, command arguments, screenshots or chat.
- Faults affect only these testing processes/DBs. Match PID, binary and config
  before stopping anything. Always restore origin in a `finally` block; bound
  every outage, including when the phone observation is still pending.

## A. Origin unavailable, APNs reachable

1. Create a harmless pending approval question against the current origin.
   Confirm the ordinary notification arrives with full content.
2. Temporarily stop only origin 8182. Leave gateway 8181 and APNs available.
3. Replay the existing notification ID through the actual gateway push endpoint
   with the current route's push credential, normal priority and future expiry.
   This uses the existing production API, not a fake notification mode.
4. Restore origin within 20 seconds even if the user has not replied yet.
5. User checks the second notification: generic Meow message, no approval/reply
   controls. Record the visible result; APNs accepted alone is insufficient.
6. After recovery, opening the generic notification should load the actual
   question. An explicit user reply should reach the origin exactly once.

## B. A -> B with a previously hydrated notification

1. Prepare a second harmless pending A question; user sees its full content and
   leaves it in Notification Center without answering.
2. Start a separate B origin/DB with the same actual gateway. Switch via normal
   settings and register B. Keep A and its DB for observation.
3. Open/approve the old A notification. Verify no new question/response is
   created on B, and no reply is sent to A using B's identity. A remains pending.
4. A new B notification must still open and answer normally.
5. Record old-route revocation separately. If revoked, an old-route replay must
   be rejected by the gateway. This is not evidence that a delayed APNs wake
   reached the phone; local delayed-wake identity rejection has native coverage.
6. Let the user choose the final server, register it normally, and update only
   the isolated verification CLI config if its Device Key changed.

## C. Offline queue while changing server

1. Load a fresh B question, stop B temporarily, submit an answer, and observe
   waiting-to-send. Attempt switching to A; choose Cancel in the discard
   confirmation, then close the server editor.
2. Confirm B identity and saved reply remain. Restore B, resume the app in the
   foreground, and verify the original answer reaches B once. Background
   delivery latency is not established by this observation.
3. Repeat with a fresh B question; explicitly discard to switch. Confirm A opens
   onboarding, register A and update the private test CLI. After restoring B,
   force-quit/reopen Meow from a new A notification and check neither server
   receives the discarded answer. A commit before cancellation would require
   separate treatment; the observed B outage prevented a pre-discard commit.

## Other limits

Extension scheduling/termination is controlled by iOS. Automated held-fetch
tests establish once-only completion and late-result rejection; actual
extension termination, APNs token rotation, cellular/public HTTPS access and
pure phone airplane-mode behavior need separate observations. Ordinary Focus,
permissions and silent-mode settings also affect alerts and should not be
classified as origin-fetch failures.
