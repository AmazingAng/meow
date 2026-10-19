# Wake gateway — evidence (2026-10-19, Asia/Shanghai)

Historical evidence for the superseded invitation implementation. For current automatic enrollment behavior and screenshots, see [remove-temporary-gates-evidence.md](remove-temporary-gates-evidence.md). The results and screenshots linked below describe the old source snapshot only.

Implemented on `codex/wake-gateway`, from a clean `main`, without commits or changes to the existing private deployment, DNS or physical phone installation.

- Spec: [wake-gateway-spec.md](wake-gateway-spec.md). Spec approval: **not obtained (autonomous run)**. The user authorized continuing the work; the detailed executable spec was not separately approved. Confidence is therefore limited by the author's specification and tests.
- Source state (script-defined SHA256, excluding verification reports/artifacts): `cd15284245a08c09e742125428c4f2a33299c54fe400bcd2997d5161bfd44839`. Initial/final hashes matched during the final fresh run. The work remains uncommitted; this is a content hash, not a commit identity.
- Results: [wake-gateway-results.json](wake-gateway-results.json). Full local logs, coverage profiles and `.xcresult` bundles: `/tmp/meow-gateway-final-v3/`.
- Toolchain: Go 1.27.2; Xcode 26.6 (17F113); XcodeGen 2.46.0; simulator iOS 26.2, iPhone 17 Pro.
- No new dependencies; `go.mod` and `go.sum` unchanged.

## Reproduce

```sh
python3 scripts/verify_gateway.py --simulator-id AF7E516E-5A30-4CE1-ABCB-45D117CD076C --mutations
```

Requires the existing Go/Xcode/XcodeGen/Docker Compose installations. The UI harness erases **only** a dedicated simulator whose name starts with `Meow Gateway`. Use a new empty output directory if passing `--output`.

## Final fresh run

| Layer | Actual result |
| --- | --- |
| Full Go suite | **141 test items passed**, zero failures/skips; `-race -shuffle=on -count=1 -coverpkg=./...` |
| Static checks | `go vet ./...`, Go formatting and final `git diff --check` passed |
| iOS unit suite | **46 passed**, zero failed/skipped; includes the existing reply queue and session regression tests |
| iOS UI | **1 passed**, zero failed/skipped; wrong invitation → correct invitation → reconnect without invitation → change server → observed revocation |
| Fault injection | **5/5 behavioral mutants killed**: owner/wake credential confusion, unknown content fields, HTTP 202 treated as acceptance, quota overrun, no-op revocation |
| Checker negative controls | **5/5 rejected**: empty/failing/skipped Go results, zero coverage, missing coverage inputs |
| Property testing | Bounded native Go fuzzing of notification identifiers; positive and negative validation invariants; see `go-fuzz.log` |
| Real execution | Built and ran the native originating API; actual iOS app in simulator enrolled through a disposable HTTP gateway boundary, stored its binding and revoked it on server switch |
| Compose | Both relay Compose and merged operator gateway Compose passed `docker compose ... config --quiet`; no deployment performed |
| Secrets/dependencies | No APNs private key or live mobile credential read/used; no packages added; origin snapshot contains wake capability but no invitation or gateway management secret |
| Visual QA | Opened final simulator screenshots; standard sheet layout, gateway URL and invitation visible, no system banner obscuring the final invitation screenshot |

Coverage gates fail below **80% per named core file**, or on missing/inconsistent inputs. These are scoped file measurements, **not full changed-line or branch coverage**:

| Go core file | Covered |
| --- | --- |
| `server/internal/api/gateway.go` | 84/93 statements |
| `server/internal/push/gateway.go` | 42/46 statements |
| `server/internal/store/gateway.go` | 36/39 statements |

| Shared Swift core | Covered |
| --- | --- |
| `Gateway.swift` | 12/13 executable lines |
| `NotificationDelivery.swift` | 112/114 executable lines |
| `NotificationContent.swift` | 96/100 executable lines |

Uncovered code includes some database/transport/encoding failures and the Swift redirect delegate callback. The gateway/session UI paths are exercised by simulator tests but no complete changed-line coverage gate is established for the app model, SwiftUI screens or command startup. A green scoped gate must not be read as complete coverage of those paths.

## Contract → checks

| Behavior / invariant | Evidence |
| --- | --- |
| Invitation-only enrollment and input validation | `TestGatewayInvitationAndValidation`, `TestGatewayHealthMalformedInputAndImmutableBinding` |
| Separate owner/wake credentials; cross-route isolation | `TestGatewayWakePrivacyAndIsolation`, `TestGatewayManagementAndRevocation`, `TestGatewayEnrollmentCapacityAndRouteIsolation` |
| Gateway contains no content, original URL or mobile credential | Captured APNs payload and negative extra-field tests in `TestGatewayWakePrivacyAndIsolation`, `TestGatewayEndToEndDispatchAndReply` |
| Failed/queued gateway responses never become APNs acceptance | `TestGatewayAPNsFailureNeverAccepted`, `TestGatewayClientExplicitAcceptance`, end-to-end persisted dispatcher failure |
| Durable quota under concurrent load | `TestGatewayConcurrentQuota`, `TestGatewayMigrationBackupAndRevocation` |
| Hash-only gateway credentials, revoke after restart | Snapshot byte check in management test; reopen/revocation store test |
| v1 upgrade preserves device, notification, question, answer and idempotency | `TestGatewayMigrationBackupAndRevocation` |
| Mobile binding requires mdt, remains per-device | `TestGatewayMobileBindingRequiresDevice` plus existing API authorization suite |
| Fixed HTTPS gateway; redirects refused | Go URL and redirect tests; app URL validation tests; URLSession redirect delegate implemented but its callback not independently exercised |
| Fallback cannot offer approval; resolved/expired/custom choices handled | `GatewayContentTests` |
| Extension expiry and late fetch call handler once | `testExtensionExpiryReturnsExactlyOnceAndIgnoresLateFetch` |
| Identity change during fetch cannot show or answer an old question | `testIdentityChangeDuringFetchCannotDisplayOldQuestion`, binding/action tests |
| Expiry rechecked at final display | `testQuestionExpiringDuringReceiptCannotKeepActionsAtDisplay` |
| Receipt-report failure preserves already verified content | `testSuccessfulFetchSurvivesReceiptFailureAndUpdatesBadge` |
| App preserves enrollment across API upload failure | `testBindingRetryReusesEnrollmentAndSeparatesCredentials` |
| Late enrollment after server switch is revoked | `testLateEnrollmentIsRevokedAfterChangingServers` |
| Older direct server / older install remain usable | `testLegacyServer404KeepsDirectPushAndLearnsDeviceID`, direct receipt test |
| Replies keep idempotency and fail closed on expiry | Existing Go/Swift suites, end-to-end repeated response test; no gateway response endpoint exists |
| Default direct delivery remains | Legacy compatibility test, existing APNs tests; native binary builds. Full default command startup is not instrumented by this suite |

## Screenshots

- [Invitation](wake-gateway-screenshots/gateway-invitation.png)
- [Connected settings](wake-gateway-screenshots/gateway-connected.png)
- [After server change](wake-gateway-screenshots/gateway-server-changed.png)

The UI test uses a mock APNs registration token and a network-boundary gateway fixture. Its invitation and owner credentials are disposable. It validates app enrollment and binding, not notification permission or actual APNs delivery. The real gateway handler is tested separately through HTTP integration tests with a simulated APNs boundary.

## Limits and honest notes

- **Independent verification: not performed.** No fresh-context verifier/sub-agent was used. This is author-produced evidence, not independent security review.
- Physical-device APNs → extension scheduling → lock-screen approval/Face ID for this **new route** remains unverified. No Apple service was contacted during verification.
- No public domain/ACME, VPN, production gateway, container runtime end-to-end or public enrollment was deployed/tested. Compose checks establish configuration validity only.
- Invited pilot users are trusted to enroll their APNs tokens. Cryptographic device ownership/attestation and public enrollment abuse controls remain a public-release gate; the pilot invitation must not be embedded in an app.
- Server changes revoke on a best-effort basis. Offline revocation may fail; binding checks still block old notifications from acting on a new server identity. Already in-flight gateway requests can finish after revocation.
- Ambiguous network failure can duplicate alerts; response idempotency still prevents duplicate resolution. APNs acceptance and extension receipt do not prove user-visible display.
- Initial unsigned simulator builds failed existing Keychain tests. Re-running with ad-hoc simulator signing restored the required Keychain behavior; no assertions were weakened.
- A held-enrollment test initially raced automatic discovery; its setup now completes discovery before holding the enrollment. Assertions remain unchanged.
- Intermediate local command-generation/compilation failures were repaired before this final fresh run. The final run had no skipped tests, compiler warnings, failing gates or source changes.
- The implementation also fixed the older-install Device ID migration issue discovered during review; its regression test was observed failing before the fix.
