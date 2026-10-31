# Security and efficiency follow-up

Tier 3: public API limits, delivery retries and durable inbox synchronization.
Authorized by the user's request to fix the five confirmed review findings.
Spec approval: not obtained (autonomous run). Independent verification: not performed.

## Setup and boundaries

- Branch `codex/security-efficiency-fixes` from `ae1eea2`, initially clean.
- Existing Go 1.27.3, pure-Go modernc SQLite v1.60.1, Foundation/SwiftUI and
  disposable simulator harness. No new runtime dependencies or production data.
- Persist regressions, `scripts/verify_security_efficiency.py`, and
  `docs/engineering/security-efficiency-evidence.md`.
- Follow the user's direct commit/merge workflow. No release tag, deployment,
  CLI replacement or TestFlight upload is authorized by this fix task.

## Executable acceptance

1. API and gateway share IP normalization: mapped IPv4 aliases share a budget;
   IPv6 addresses in one /64 share a budget; distinct prefixes remain independent.
   Anonymous registration remains supported with its existing 10/hour allowance.
2. Disabled push diagnostics contain no payload, body or credentials at debug.
3. APNs parses delay-seconds and HTTP-date Retry-After; malformed/overflow values
   do not wrap. APNs 5XX waits at least 15 minutes, and 429 uses at least one
   minute when no longer cooldown was supplied. Gateway preserves retryable
   status/cooldown. The scheduler releases workers during delay and never retries
   sooner than requested. Existing optional caps and default unlimited admission
   remain. Scheduling durability across server restart is outside this change.
4. Inbox rows do not parse Markdown in View.body. A bounded actor cache computes
   previews off the main actor, shares identical bodies and caches empty results.
   Changed/cancelled views cannot display stale text. Formatting and original
   bodies remain unchanged, including Chinese, code and tables.
5. Opt-in inbox sync uses monotonic database change sequences, not message names,
   arrival order, device clocks or an NSE-delivered high-water mark. First sync
   pages all server records; later unchanged sync is one empty page. Inserts and
   visible server-state updates are discoverable. Deletions/expiry never erase
   local history. Legacy clients retain ordinary inbox pagination.
6. Sync cursors are fenced by authenticated device and server process epoch.
   A restart/restore causes a safe backfill, preserving local history. Pagination
   has a fixed upper bound so ongoing traffic cannot extend a sync forever.
   Updated rows moving beyond that bound are found by the next sync. A cursor
   never advances unless its received page and checkpoint commit atomically.
   Partial failure, cancellation, reset, tombstones and read-state races cannot
   lose messages or apply old-server data to a new registration.
7. New iOS streams sync pages to local SQLite and reloads presentation afterwards;
   it does not collect every server body into a second full in-memory snapshot.
   Servers without the additive sync envelope keep the existing full-history
   behavior, reusing their first response. Existing local read state wins.
   Addendum: a transactional, per-scope local content revision skips unchanged
   SQLite payload decoding; extension writes, reads and deletes invalidate it,
   while cursor-only updates do not.
8. Schema v8 adds change tracking without removing history or changing retention,
   key permissions, file modes, notification formats or gateway quotas. Upgrades
   preserve current records; older binaries require a pre-upgrade snapshot for
   rollback. No destructive migration or automatic local history retention.

## Failure model and gauntlet

- RED: IPv6 registration aliases, debug body logging and provider cooldown loss;
  native tests for repeated parsing/full sync and missing durable checkpoints.
- Race/shuffled Go suite; auth/cursor isolation; hostile/malformed cursors;
  actual loopback HTTP; sequence update/pagination/restore regression tests.
- Native Swift storage/client/cache tests, Unicode/property cases and callback
  fencing; full existing iOS unit/UI suite on a temporary simulator.
- Vet/format, official vulnerability and diff-secret scan; explicit coverage
  gates with negative controls and named-test manual mutations in disposable
  source copies. Record measured coverage limits rather than claiming all paths.
- Benchmark the same legal 3920-character Markdown and 5000-message unchanged
  history used in review. Verify cache work and request counts, not a flaky
  hardware-specific timing assertion. Inspect exported UI screenshots.
