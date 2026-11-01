# Credential and gateway lifecycle hardening

Tier 3: credential persistence, public route lifetime and release supply chain.
Spec approval: not obtained (autonomous run); the user authorized the four
review fixes and corresponding skill changes. Independent verification: not performed.

## Setup and scope

- Work on branch `fix/security-lifecycle` from `14464b1`. This isolates commits;
  preserve and exclude existing App Store metadata, screenshot script and assets.
  No temporary worktree, runtime dependency, production mutation or iOS rebuild.
- Go 1.27.3 and existing SQLite dependency; pinned development-only
  govulncheck v1.9.0 and GoReleaser v2.18.3. Use temporary test databases,
  fake credentials, loopback HTTP and disposable source copies for mutation.
- Add regression tests under `cli/internal/config`, `server/internal/store`,
  `server/internal/api` and `server/cmd/meow-server`; persist the rerunnable
  gate at `scripts/verify_security_lifecycle.py` and an evidence report beside
  this specification. Update repository skill and operations documentation.
- Follow the established direct commit/merge workflow. Publish CLI v0.1.10,
  verify all four release archives and update the Homebrew tap. Install the
  verified CLI locally without changing login or hook trust. No server/gateway
  deployment or App Store submission change is included.

## Acceptance criteria

1. Revoking a Key physically removes its credential and per-key idempotency
   metadata; repeated revocation is 404 and revoked authentication is 401.
   Notifications, legacy questions/answers and daily notification receipts
   survive. A create/revoke loop does not accumulate credential rows. Batch
   cleanup removes previously revoked credentials, retaining active ones.
   Active-key lookup uses a partial index during legacy cleanup.
2. Gateway routes expire after 90 days without activity. Registration, a
   management update and an admitted authenticated wake renew activity.
   Expired routes cannot authenticate, update or wake even before cleanup.
   Expiry does not reset the independent token/environment push quota.
   Cleanup uses bounded transactions and runs at gateway startup and every
   five minutes, with logged failures and prompt cancellation.
3. Schema v9 transactionally adds route timestamps and cleanup indexes.
   Existing routes begin their inactivity window at upgrade time, keep their
   credentials and quotas, and remain usable by the current App. Migration
   failure rolls back the version and schema. Back up before upgrade; binary
   rollback requires the pre-upgrade snapshot. The App's existing 401/404/410
   recovery registers a fresh route and binding without losing local history.
4. CLI Save uses a unique, exclusive regular temporary file in the destination
   directory, explicitly mode 0600; close before atomic rename and remove on
   failure. A pre-created predictable `.tmp` or destination symlink cannot
   redirect writes into another file. The old config survives failed writes;
   concurrent saves publish one complete JSON config, never mixed credentials.
5. CLI releases scan each actual macOS/Linux ARM64/AMD64 executable before
   publication. Vulnerability/tool failure blocks publication. v0.1.10 is built
   using Go >=1.27.3, public downloads match SHA-256 and scan clean. Upgrade
   changes the executable, not saved credentials or trusted hook definitions.
6. The skill requires the security-fixed CLI, describes safe private config
   storage and upgrade verification, and explains gateway expiry/recovery and
   schema backup requirements in its self-hosting reference. Preserve all
   three Codex hook instructions and the notification-only V1 contract.

## Failure model and verification

- RED regressions for retained revoked rows, expired routes, maintenance wiring,
  temporary-file mode and symlink writes; existing auth/receipt/quota invariants.
- Full race and shuffled suites, Go vet/format, installer tests; migration
  rollback, expiry boundary/renewal, cancellation and concurrent file writes.
- Targeted changed-line coverage gate with a failing negative control; 3–5
  named manual mutations in disposable source, with exact test failures and
  source restoration checks. No runtime dependency added.
- Actual CLI temporary login/config persistence and local HTTP registration,
  wake expiry and recovery. Source and built-artifact vulnerability scans;
  secret-marker checks with explicit limitations.
- iOS UI is unchanged; do not claim a fresh physical phone test. Gate failures
  block shipping, and untested OS/runtime paths are recorded as limits.

Addendum: immediate credential deletion exercises foreign-key updates. Add
indexes on notification/question key references so rotation and legacy cleanup
do not scan unrelated devices' history. Verify SQLite query plans explicitly.
