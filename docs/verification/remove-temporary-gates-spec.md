# Remove temporary product gates

User instruction: remove the invitation scheme and similar temporary choices/extra conditions across the repository. This supersedes the invitation/cap/loopback-only parts of `wake-gateway-spec.md`; they are not release requirements.

- Tier 3: registration and existing credential boundaries. Autonomous run; no separate specification approval.
- Branch: `codex/remove-temporary-gates`, preserving the previous uncommitted work. No commits, public deployment, live service or actual device changes.
- Dependencies/installations: none. Reuse Go, Xcode, XcodeGen, Docker Compose and the verification harness. No new account/attestation/approval flow.
- Artifacts: this spec, `remove-temporary-gates-evidence.md`, existing gateway verification script/tests and screenshots, focused client/CLI regression tests.

## Acceptance

1. Valid APNs registration at the gateway succeeds without an invitation, bearer secret or extra user interaction. Registered routes still need their own independent management/wake credentials for later operations.
2. Enrollment has the same per-client-IP rate limiting as the existing anonymous mobile registration; different clients do not share one global quota. Proxy handling uses the operator's existing `MEOW_TRUST_PROXY` setting. No fixed 1,000-device installation cap.
3. iOS discovers the gateway and enrolls automatically after receiving its APNs token. No invitation prompt, gateway setup sheet or extra navigation step. Connection status/retry stays in ordinary notification settings. Existing durable enrollment retry, token refresh, stale-session checks, revocation, fallback and offline replies remain.
4. Gateway addresses support the same HTTP/HTTPS choices as existing self-hosted API addresses, including local Wi-Fi addresses and localhost. Reject URL credentials, query and fragments; never disable TLS certificate validation or follow secret-bearing redirects.
5. Remove the obsolete `meow dev register/inbox/reply` phone emulator from the shipping CLI and replace README quick start with the actual iOS flow. An old `meow dev ...` invocation must fail clearly without sending a notification by accident.
6. Remove the unconfigured `api.meow.blue` destination fallback. Client calls with no server fail locally; waiting does not retry a missing configuration forever. CLI setup/login use explicit flag, existing environment/config, or the existing interactive address prompt rather than contacting a placeholder host. Existing configured use remains valid.
7. Remove iOS's fabricated `server.invalid` API fallback; missing server/session returns to onboarding and makes no request to a dummy endpoint.
8. Keep real constraints: send-only/device credential separation, idempotency, fail-closed expiry, local reply durability, Face ID, APNs protocol limits, configurable quotas, config overwrite protection, and persistent hook executable requirement. Debug-only simulator fixtures are test support, not user access conditions.
9. Current docs, deployment config, translations, UI tests and verification harness match automatic enrollment. Prior evidence is labelled historical rather than presented as current behavior.

## Failure model / validation

- Accidentally opening management/response routes: existing negative HTTP authorization tests, route-isolation and revocation tests; targeted mutation.
- Removing a cap but leaving global throttling: distinct-IP and trusted/untrusted proxy tests; >1,000-route regression test.
- Silently leaking credentials to placeholder destinations: client transport spy and CLI config tests; Swift absent-session tests.
- Automatic registration races/duplicates: Swift failed-bind retry and late-enrollment/revocation tests; native HTTP integration.
- New user-facing steps surviving under a new name: simulator test verifies automatic binding with no invitation/control sheet, ordinary retry, and server change.
- Full Go race/shuffled suite, vet/format, fuzzing, existing scoped coverage gates, full iOS unit suite and dedicated simulator UI/screenshot run. Coverage remains scoped, not a claim that all changed lines or all platform behaviors are covered.

## Revisions

- Initial scope from review; no speculative replacement admission mechanism will be introduced.

- Review revision: CLI setup/doctor must recognize a bound gateway rather than require a direct APNs token. Add transport-neutral `push_registered` to whoami; old servers still use the existing APNs flag as compatibility fallback.
