# Gateway resource limits — evidence

- Spec: [gateway-resource-limits-spec.md](gateway-resource-limits-spec.md).
- Tier 3. Detailed spec approval: not obtained (autonomous run); the user
  authorized the agreed IP and concurrency changes. No independent verifier
  was used; confidence is limited to the checks below, not a full security audit.
- Baseline: `63199e1a499aab1a406f7c523a3084ebf56b544f`.
- Final source SHA-256: `c1e0c9ea5fd4e09057452e0742c05d7b5519eec75f58c58ff2c6202b9c7da7ae`.
  The persisted verifier hashes Git-listed source inputs, including untracked
  new inputs and excluding this self-referential report and ignored build output.
- Toolchain: Go 1.27.2, Darwin ARM64; existing Python, Docker Compose and
  Gitleaks tools. No dependencies or tools installed.
- Final run artifacts: `dist/gateway-resource-limits-20261101-release/` (ignored).

Reproduce in a fresh output directory:

```sh
python3 -B scripts/verify_gateway_limits.py --output dist/gateway-limits-recheck
```

## Spec → tests

| Spec | Automated check | Result |
| --- | --- | --- |
| 1: shared IP budget, reject before body/DB | `TestGatewayLimitsAllRequestsBeforeBodyAndDatabase`, `TestGatewayLimitsInvalidCredentialsShareBudget`, `TestGatewayLimitsRoutesShareIPBudget` | Pass |
| 2: proxy trust, address aliases | `TestGatewayLimitsTrustedProxyAndAddressAliases`, `TestGatewayLimitsAddressProperties` | Pass |
| 3: refill, concurrent token accounting | `TestGatewayLimitsAllRequestsBeforeBodyAndDatabase`, `TestGatewayLimitsConcurrentIPAdmission`; existing limiter capacity/generated-accounting tests | Pass |
| 4: enrollment/auth/route quota compatibility | Existing `TestGatewayRegistrationIsAutomaticAndPerClient`, `TestGatewayWakePrivacyAndIsolation`, `TestGatewayManagementAndRevocation`, `TestGatewayConcurrentQuota` | Pass |
| 5: defaults, override, invalid setting | `TestGatewayLimitsGlobalPushAdmission`, `TestGatewayConcurrencyConfiguration`, `TestGatewayInvalidConcurrencyFailsBeforeStartup`; Compose override gate | Pass |
| 6: immediate busy result, no quota spend | `TestGatewayLimitsGlobalPushAdmission`, `TestGatewayLimitsLiveHTTP` | Pass |
| 7: release on success/error/quota/cancellation | `TestGatewayLimitsReleasePushSlot`, `TestGatewayLimitsCancelledAdmission` | Pass |
| 8: health/registration/management while busy | `TestGatewayLimitsGlobalPushAdmission` | Pass |
| 9: retry and acceptance contract | Existing `TestGatewayRetryAfterLocalHTTP`, `TestGatewayClientExplicitAcceptance`, `TestGatewayAPNsFailureNeverAccepted` | Pass |
| No credential/schema/product changes | Full Go suite; no SQL, iOS, CLI or module edits | Pass within Go scope |
| No production access | SQLite fixtures and simulated APNs only; real TCP check uses loopback | Pass |

## Final verification

| Layer | Result |
| --- | --- |
| Full Go tests | 634 passing test events including subtests, 0 failed/skipped; 31 gateway-limit/config cases |
| Race and suite order | Full suite uses `-race -shuffle=20261101 -count=1` |
| Compiler / lint / formatting | `go build ./...`, `go vet ./...`, changed Go formatting and `git diff --check` pass |
| Installer | 9 tests pass |
| Admission/config coverage | 31/31 selected changed/new executable statements covered; gate fails on misses or absent inputs |
| Property checks | 100 deterministic generated IPv6-prefix and IPv4-alias cases; existing token-accounting properties retained |
| Mutation | 6/6 applied mutants rejected by their named behavioral tests |
| Checker controls | Empty/skipped/failed test evidence and empty coverage all rejected: 4 controls |
| Real execution | Native gateway HTTP handler + SQLite over actual loopback TCP: full provider capacity returns 429 and `Retry-After: 1` |
| Compose | Explicit concurrency override `7` reaches the gateway service |
| Supply chain / secrets | No dependency diff; Gitleaks finds no secrets in changed/new files |
| Source stability | Before/after source hash identical; every expected verifier layer completed |

Mutants: ignore IP budget, overspend push slots, leak push slot, charge busy
requests route quota, trust spoofed forwarding, and allow IPv6 interface rotation.
Each was applied to a disposable source copy, run with uncached Go test results,
and required the intended behavioral test to fail. User source was not mutated.

## Honest limits and failures encountered

- Initial regressions failed as expected: changing routes/credentials avoided
  any shared gateway IP budget, and overflow reached the provider instead of
  returning 429. A panic-on-read body verified early rejection was absent.
- Initial configuration tests detected ignored settings. They passed only after
  parsing and early startup validation were implemented.
- The old 80-way route-quota test initially failed because the new independent
  global cap also rejected requests. Its fixture now has 80 slots; original
  exact 60-success/20-rejection assertions are unchanged. Separate tests assert
  the production default cap of 32 and an override of 2.
- Coverage excludes `main.run` daemon-construction/logging wiring, which would
  start APNs credential checks. Configuration parsing, early rejection, native
  handler operation and Compose binding are tested separately. No live APNs,
  production deployment, Docker runtime or phone delivery is claimed.
- Existing IPv4 limiter memory bounds remain covered by existing tests; this is
  not a multi-node or distributed-denial-of-service defense. IP allowances are
  shared behind NAT; IPv6 uses /64. Limits reset per process except durable
  per-route quotas. No phone-ownership attestation or daily phone quota added.
- No iOS tests: no iOS files changed. No separate staticcheck or independent
  review was run; the repository's compiler/vet/race checks were used.
- Dependencies are unchanged. The previously identified Go 1.27.2 certificate
  advisory remains outside this change; these results do not claim it is fixed.
- The verifier pins its comparison base so coverage and diff scanning remain
  reproducible after committing the fix. Independent verification: not performed.
