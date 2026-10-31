# Notification-only launch hardening

Tier 3 (credentials, persisted data and public API). Authorized by the user's
request to fix the remaining findings and open up the default 32-push cap.
Detailed spec approval: not obtained (autonomous run).

## Plan and scope

- Isolate on branch `codex/launch-hardening`; preserve existing local state.
- Use official Go 1.27.3 for module, Docker and CLI builds. Keep existing module
  dependency pins. No production deployment, tag, TestFlight upload or key reset.
- Add focused Go/Swift regressions, test-only legacy data fixtures, and a
  reproducible `scripts/verify_launch_hardening.py` with an evidence report.
- Reuse the standard library, current SQLite driver and existing iOS simulator
  harness. No new runtime dependencies. All verification uses disposable data.

## Acceptance

1. Go module/Docker builds require 1.27.3; official vulnerability scanning no
   longer reports GO-2026-6809. Rebuilt local CLI and server report that toolchain.
2. Gateway concurrency defaults to unlimited (`0`), with no extra 32-request
   rejection. Positive explicit operator caps remain supported; negative or
   malformed settings fail startup. Existing IP/enrollment/route limits remain.
   The existing 60/minute allowance is shared by normalized APNs token and
   environment, rather than reset by new routes, token-case aliases or client
   IPs. Schema v7 preserves active v6 route usage; revocation, restart and
   rotating away/back do not erase it. Expired quota identities are pruned.
3. New and rotated sender keys grant only `notify`. Old `ask` scope never
   authorizes reading legacy questions or answers. Only the owning `mdt_`
   credential reads history; another device gets 404. Existing notify keys
   continue sending without rotation. New requests for `ask` are rejected.
4. Create/answer/cancel/wait question endpoints return 410 `feature_retired`
   without changing stored history or queuing pushes, including with old keys.
   Delete their handlers, long-poll broker and unused execution paths. Preserve
   schema/history decoding and the expiry/retention needed for old records.
5. Database files, WAL/SHM and backup snapshots have at most 0600 permissions;
   newly created data directories have at most 0700. Tighten existing owned
   regular files on open. Do not chmod unrelated existing parent directories.
   Reject symlink/special-file database/backup targets and existing backups;
   permission/setup failures fail closed. Snapshot consistency is preserved.
6. Remove iOS answer/flush/network-retry execution and unused read/count/pending
   HTTP APIs. Earlier saved reply JSON still loads with text, IDs, timestamps
   and failures intact; it is copyable and explicitly deletable, never sent.
   Corrupt/unwritable storage must not silently lose records or switch identity.
   Retain reset confirmation and local-history/read-state isolation.
7. Remove APNs routes reported permanently invalid using a conditional delete
   that cannot erase a concurrently updated token. Do not expire healthy idle
   registrations merely for inactivity; that would silently break delivery.

## Failure model / validation

- Old keys regain read/write access: direct HTTP tests with seeded legacy rows.
- Cleanup destroys history/old text: migration, backup restore and native Swift
  saved-record/restart tests; no live phone or production data.
- File modes leak data or clobber symlinks/backups: real SQLite and filesystem
  tests with permissive umask, existing files, WAL/SHM, failures and symlinks.
- Gateway capacity regresses: >32 blocked provider calls admitted by default;
  explicit cap, busy retry, IP limit, provider error and cancellation regressions.
- Token invalidation races with updates: concurrent/conditional invalidation test.
- False verification: first observe failing tests, targeted mutation, race and
  shuffled full Go suite, Swift unit/UI runner, source hash and explicit skips.

## Compatibility boundary

Legacy approval clients must upgrade: remote approvals/answers intentionally
stop. Historical questions/answers and downloaded phone history remain readable.
Existing server read/count endpoints remain for older notification clients;
only unused iOS caller methods are removed. Previously published artifacts and
running services are unchanged until rebuilt/released/deployed.

## Revisions

- Initial: remove the default cap, preserve opt-in operator control, retire
  execution but preserve records. Invalid APNs routes are pruned only when
  upstream confirms the currently stored token is invalid.
- Review revision: the earlier IP-only mitigation did not fully close the
  multi-route quota bypass. Share the existing allowance durably across token
  identities, without reinstating the default global concurrency cap. A route
  changing before admission fails closed rather than charging another token.
  A token/environment update returns retryable 429 with Retry-After: 1; a
  revoked/missing route remains 401. This preserves normal delivery retries.
- Simulator revision: each UI case reinstalls only this runner's disposable
  app and resets that simulator's keychain. Fixture omission cannot reset local
  history or read state. Preserve original assertions, including unread counts.
  Removed execution-only tests are replaced with read/delete archive contracts;
  old store writers remain test-only fixtures for migration/history regressions.
- Bark reference: inspected the supplied `bark-server-master/apns/apns.go`.
  Its Push borrows then immediately returns the shared HTTP/2 client before
  the network call; the pool is not a fixed in-flight request semaphore.
  Meow likewise defaults to no application-level concurrency semaphore, while
  retaining its explicitly configured operator cap and public-gateway limits.
