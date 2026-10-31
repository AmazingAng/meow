# Gateway resource limits — specification

Tier 3: public HTTP admission and concurrent APNs delivery.

User authorization: “好的 你修复吧”, following the agreed IP-rate and global
concurrency protections. Detailed spec approval: not obtained (autonomous run).

## Setup

- Work on branch `codex/gateway-resource-limits` in the initially clean checkout;
  no additional checkout or application state is needed.
- No new dependencies or tools. Reuse the existing bounded token-bucket limiter,
  Go tests/race detector, `testing/quick`, Python verification conventions.
- Add `server/internal/api/gateway_limits_test.go`, command configuration tests,
  `scripts/verify_gateway_limits.py`, and an engineering evidence report.
- Use disposable databases and mock APNs boundaries only; never push to users.
- No release tag, production deployment, credential change, iOS or CLI update.

## Acceptance scenarios

1. All gateway HTTP requests share a per-client-IP bucket: burst 200, refill
   20 requests/second. A depleted bucket returns HTTP 429, JSON `rate_limited`
   and a positive integral `Retry-After` before reading a body or accessing
   SQLite/APNs. Changing routes, credentials, endpoint or HTTP method does not
   create a new bucket. Another client retains its own allowance.
2. IP selection follows the existing trusted-proxy behavior: last forwarded
   hop only when configured; spoofed forwarding headers otherwise do not help.
   Equivalent IPv4 and IPv4-mapped addresses use one key. IPv6 addresses within
   one /64 share a bucket to prevent trivial interface-address rotation.
3. Request allowance refills with time. Concurrent callers cannot overspend it;
   the existing limiter's bounded memory and reclamation remain unchanged.
4. Anonymous enrollment remains available, additionally retaining its existing
   10/hour burst-10 registration limit. Existing route credentials, validation,
   privacy, revocation and durable 60/minute route quota remain intact.
5. Gateway APNs delivery has a process-wide admission cap, default 32;
   `MEOW_GATEWAY_MAX_CONCURRENT_PUSHES` may set a positive whole number.
   Invalid values fail gateway startup; this setting does not affect API mode.
6. When all slots are occupied, another valid wake returns immediately with
   HTTP 429 `rate_limited` and `Retry-After: 1`. It does not call APNs, queue
   a goroutine waiting for a slot, or consume the durable route quota.
7. Unauthorized and invalid wakes do not hold an APNs slot. Slots are released
   after success, provider error, quota rejection and request cancellation.
   Cancellation is checked before admission; in-flight cancellation remains
   governed by the pusher's existing context contract.
8. Health, registration and management requests are not blocked by APNs slot
   exhaustion (their own IP/enrollment/authentication limits still apply).
9. Existing retry clients receive and honor `Retry-After`; server acceptance
   still requires actual provider acceptance. No wire/credential/schema change.

## Failure model and checks

| Failure | Check |
| --- | --- |
| New route/credential resets IP budget | Alternating real routes and invalid auth/paths against one handler |
| Spoofed forwarding or IPv6 address churn | Trusted/untrusted proxy cases and generated /64/IPv4 equivalence tests |
| Races overspend request allowance or slots | Frozen clock; blocked provider; concurrent callers; `-race` |
| Provider failure/cancellation leaks capacity | Error, cancellation, quota-full and subsequent successful wake |
| Busy rejection burns quota or delays health | Exhaust slots, inspect durable quota and health/management results |
| Invalid operator setting disables protection | Default, positive and malformed configuration tests |
| Compatibility regression | Full Go suite and existing gateway/dispatcher tests |
| Assertions/checker accidentally pass | Focused mutations and checker negative controls |

## Limits

IP and concurrency limits are per process. They do not claim protection from a
distributed flood, multi-node quota coordination, phone ownership attestation,
or a strict per-phone daily quota. Multiple self-hosted APIs behind one NAT share
the IP request bucket. Registration count remains uncapped globally. The Go
1.27.2 certificate advisory identified in the review is pre-existing and outside
this rate-limiting change.

## Revisions

- Initial spec: global admission is nonblocking; busy responses do not spend
  route quota. IPv6 /64 grouping and canonical IP keys prevent obvious aliases.
- Existing 80-way route-quota regression explicitly configures 80 global slots
  so its original exact 60 accepted / 20 rejected assertions continue measuring
  the durable quota. Default global-cap behavior is checked independently at 32.
