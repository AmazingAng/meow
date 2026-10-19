# SPEC — invitation-only wake gateway

Historical specification, superseded by [remove-temporary-gates-spec.md](remove-temporary-gates-spec.md). Invitation enrollment, the installation cap and loopback-only HTTP policy were removed at the user's request. Do not use this file as current product requirements.

- Tier 3: new device-scoped credentials, HTTP protocol, SQLite migration, concurrent dispatch and iOS extension.
- Autonomous run: proceed under the user's “继续吧”; no separate spec approval was obtained. Evidence must disclose this.
- Isolation: branch `codex/wake-gateway` from a clean `main`; no commits, existing services, DNS, real APNs credentials or installed phone app changed.
- Tools/dependencies: existing Go, Python, Xcode and XcodeGen only; no new packages.
- Verification files: `scripts/verify_gateway.py`, `docs/verification/wake-gateway-evidence.md`, Go gateway/store/API tests and `ios/MeowTests/GatewayTests.swift`. Generated logs/results live outside the checkout.

## Contract

1. Default mode stays direct APNs. Setting `MEOW_PUSH_GATEWAY_URL` selects relay mode; only that configured URL receives device-scoped wake credentials. HTTPS required except literal loopback for local tests. Never follow redirects carrying secrets.
2. A separate `meow-server gateway` process holds APNs credentials and a separate SQLite database. Enrollment requires an operator invitation (`MEOW_GATEWAY_ENROLLMENT_TOKEN`, private pilot only), rate limits, and bounded registration capacity. No public anonymous enrollment or embedded invitation in the distributed app.
3. App enrolls directly with APNs token/environment and a random binding ID. Gateway returns route ID, management secret and separate wake secret. Only hashes of those secrets are stored at the gateway. App keeps credentials in Keychain and sends only the wake secret/route/binding to its own server. The server must retain the outbound wake secret, an explicitly documented exception to hashing inbound authentication credentials.
4. Gateway accepts only route ID, notification ID, priority and expiry, authenticates the route's wake secret, rejects extra fields, bounds expiry to 24h and enforces a durable 60/minute per-route quota. Its APNs payload contains a fixed generic alert, mutable-content, event ID and binding ID; no source, body, title, choices, question ID, original URL or mobile credential.
5. HTTP 200 `accepted:true` follows actual APNs acceptance. Rejected/revoked/invalid credentials, APNs errors and transport failures never become accepted. Dispatcher preserves queued/accepted/failed/skipped meanings and retry behavior.
6. Management secret can update APNs token or revoke the route; wake secret cannot manage devices or answer questions. One route's credentials cannot access another route. Revocation survives restart.
7. Mobile push configuration and binding endpoints require `mdt_`, never a send-only `meow_`. Server stores binding transactionally; v1 databases migrate without losing devices/questions/idempotency records; online backups/reopen preserve routes.
8. App discovers relay configuration, explicitly requests the pilot invitation only when needed, persists enrollment before publishing its binding, reuses it for token updates/retries and revokes the old route on server change on a best-effort basis. Session generation prevents late enrollment/update results changing the new server identity.
9. Relay notification extension matches its binding to the current local identity, fetches only the stored server using `mdt_`, verifies returned notification ID and current identity, then reconstructs content from the server response. Pending, unexpired approval gets Approve/Reject; free text gets Reply; custom choices open the app. Fallback, timeout, mismatch, cancellation or resolved/expired question offers no approval/reply action. Handler runs at most once.
10. Replies go directly to the stored server using existing authentication, idempotency and expiry checks. Notification action handler re-fetches relay question state before sending, and rejects stale bindings after server changes. Direct notifications remain compatible.

## Failure model and checks

- Credential confusion/cross-device routing: HTTP negative tests, independent owner/wake secrets, hash-at-rest assertions, hostile JSON inputs.
- Content exfiltration/SSRF: fixed typed protocol, payload capture, reject extra fields, redirect tests, local-only stored server resolution.
- False acceptance: simulated APNs failures + dispatcher persistence integration tests.
- Concurrent quota overrun and restart bypass: Go race tests, parallel requests, reopen SQLite.
- Migration loss/partial writes: v1 fixture upgrade + backup/reopen tests.
- Extension expiry/stale identity and unsafe action: Swift unit tests for rendering/routing and session changes; simulator build/tests and local simulated pushes. Real APNs extension scheduling/Face ID remains a physical-device check.
- Existing API/reply contracts: full Go/Swift suites, shuffled Go tests, vet, format, bounded fuzzing, targeted mutation and coverage report with explicit misses (never claim complete coverage from a percentage alone).

## Must NOT

- Publish APNs key or invitation; change current private deployment or claim public readiness.
- Forward original message content or `mdt_` through the gateway; fetch arbitrary URLs from payload.
- Approve on timeout, failed fetch, stale binding or expired question.
- Treat gateway queueing as APNs acceptance or APNs acceptance as proof of display.
- Add FCM, third-party dependencies, a second user account system, or automatic deployment.

## Revisions

- Initial scope: invitation-only pilot because public enrollment/Apple device-attestation abuse protection is a separate release gate. This adds one invitation entry for pilot users, not the final public onboarding experience.

- Revision 1: relay actions use content already verified by the service extension, with an explicit hydration marker and local expiry/binding checks. They do not fetch a second time before queueing: that would prevent an offline lock-screen action from entering the existing durable reply queue. The receiving server remains authoritative for expiry, state and idempotency.

- Revision 2: final-display expiry is checked again after receipt/count requests. A fetch timeout keeps a generic alert; if verified content was already obtained and is still valid, extension expiry can hand that verified content to iOS. Core coverage gates require at least 80% for each of three new Go gateway files and three shared Swift files; this is a scoped minimum, not a claim of complete changed-line coverage.
