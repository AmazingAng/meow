# Remove temporary product gates — review and evidence

2026-10-19 · branch `codex/remove-temporary-gates` · uncommitted, preserving preceding gateway work.

## Changes from review

| Finding | Result |
| --- | --- |
| Invitation token, invitation input and separate gateway setup sheet | Removed. iOS registers automatically after APNs registration; ordinary settings retain status and retry. No replacement account/admission step. |
| Fixed 1,000-route installation cap and globally shared enrollment quota | Removed. Normal anonymous registration keeps per-client-IP 10/hour, burst 10, with the existing trusted-proxy setting. Management and wake operations keep device-scoped authentication. |
| HTTP gateways permitted only on literal loopback | Removed. LAN and other HTTP/HTTPS gateway addresses follow self-hosted API choices. HTTPS certificate checks and rejection of credential-bearing redirects remain. |
| Temporary CLI phone emulator | Removed `meow dev register/inbox/reply`; old invocations return usage without sending. README uses the actual iOS flow. |
| Empty client config silently contacts an unconfigured official address | Removed. Explicit flag/environment/config/address prompt supplies the server. A missing server fails locally, including long polling. |
| iOS fabricates a server.invalid API client | Removed. Missing server/session returns to onboarding; API access requires a real saved session. |
| CLI doctor/setup requires a direct APNs token in gateway mode | Fixed. Current transport registration is authoritative; only older API responses without that field fall back to the APNs flag. |
| Hook executable rejected whenever its path contains go-build | Narrowed to the actual Go temporary executable path pattern; normal install directories are accepted. |

Reviewed runtime guards/configuration across `server/`, `client/`, `cli/`, `ios/`, deployment files and release workflow, including defaults, artificial caps, unsupported paths, temporary comments and UI disabling conditions. Reference Bark/ntfy repositories were outside the change scope.

Retained conditions have concrete purposes: credential scope isolation, idempotency, fail-closed expiry, approval authentication, durable offline reply handling, protection against losing pending replies during server changes, APNs/platform limits, configurable operator quotas, normal per-IP/per-route rate limits, configuration overwrite/backups, and persistent hook executables. Debug-only simulator fixtures and network/protocol test doubles are verification support. No new user-facing authorization or release gate was added.

## Executed evidence

- Regression tests were observed failing before the corresponding fixes: anonymous registration/per-IP isolation, installation cap, phantom server requests, obsolete CLI emulator, transport-aware diagnostics, missing iOS server, automatic iOS enrollment, and valid hook paths. The obsolete invitation/cap test expectations changed because the user explicitly changed that contract; unrelated protections were retained.
- Final fresh run: **151 Go test items**, **47 iOS unit tests**, **1 iOS UI test**, all passed with zero failed/skipped tests. Go used race detection, shuffled order and no test cache. Static checks: vet, Go formatting and diff whitespace checks.
- **7/7 behavioral mutants killed**: global registration quota, placeholder destination, management/wake scope confusion, extra content fields, HTTP 202 acceptance, quota overrun, ineffective revocation. Sources were restored after each mutant.
- **5/5 checker negative controls rejected**; bounded native Go identifier fuzzing passed. Existing per-file 80% coverage gates passed (scoped statement/line coverage, not whole-repository or branch coverage):

| File | Covered/total |
| --- | --- |
| `server/internal/api/gateway.go` | 82/89 |
| `server/internal/push/gateway.go` | 40/45 |
| `server/internal/store/gateway.go` | 30/32 |
| `Gateway.swift` | 8/9 |
| `NotificationDelivery.swift` | 112/114 |
| `NotificationContent.swift` | 96/100 |

- Native Go originating API plus actual iOS app in the dedicated simulator exercised automatic enrollment, token update/retry and old route revocation. Gateway boundary was a disposable HTTP test server. Origin database snapshot contained the wake capability and no management credential.
- Both relay and merged gateway Compose configurations validated. No live deployment, DNS, private APNs keys, or installed physical phone changed.
- Screenshots opened and visually checked: [automatic enrollment/settings](remove-temporary-gates-screenshots/gateway-auto-connected.png), [server changed/onboarding](remove-temporary-gates-screenshots/gateway-server-changed.png). The simulator injects a test APNs token; its notification permission is unset. These screenshots do not establish real APNs delivery or permission behavior.

Machine-readable evidence: [results](remove-temporary-gates-results.json). Full logs, profiles and xcresult bundles: `/tmp/meow-remove-gates-final-v2`. Final initial/end source hashes matched: `c2f750bcbca3c5645adbb4cf96eb1ff3890d4a60f6d716237770d976f82b2f7a`. Verification reports/artifacts are excluded from that hash; intentional source deletion is included as a tombstone. This is a content identity, not a commit identity.

Reproduce with the existing toolchain:

```sh
python3 scripts/verify_gateway.py --simulator-id AF7E516E-5A30-4CE1-ABCB-45D117CD076C --mutations
```

The harness erases only a dedicated simulator whose name starts with Meow Gateway. Go/Xcode/XcodeGen/Docker Compose are existing installations; no new package was installed. Specification was executed autonomously without separate approval; no independent fresh-context verifier was used. Tests are author-written and scoped; gateway real-device APNs delivery/Face ID were not rerun. Earlier interrupted final-run attempt is superseded by the completed `final-v2` result above. Previous invitation evidence is explicitly historical.
