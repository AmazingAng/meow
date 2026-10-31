# Notification-only launch hardening evidence

Spec: [launch-hardening-spec.md](launch-hardening-spec.md). Tier 3: credentials,
persisted data, concurrency and public API retirement.

- Spec approval: not obtained (autonomous run under the user's fix request).
- Independent verification: not performed. The implementation and checkers have
  the same author; these are executable checks, not independent assurance.
- Isolation: `codex/launch-hardening` branch from `6c79fa1`, initially clean.
  Existing ignored local state remained present. No production credentials or
  messages were used; tests use disposable databases, providers and simulators.
- New runtime dependencies: none. SQLite remains modernc v1.60.1. Official Go
  1.27.3 and govulncheck v1.9.0 were fetched into development caches.

## Reproduce

```sh
python3 -B scripts/verify_launch_hardening.py --output dist/launch-check-new --ios
```

Use a fresh output directory. The entry point runs the Go race/shuffle suite,
builds, vet, format, installer checks, official vulnerability scan, targeted
coverage gates, manual mutations, Compose configuration, secret scan, all native
iOS unit tests and all UIOptimizationTests. It records a source-tree hash before
and after the run, excluding this evidence document. Required layers, missing
inputs, source changes, skipped tests and unexpected mutant failures fail closed.
The iOS runner removes only its own temporary simulator in `finally`.

Final run: `dist/launch-hardening-acceptance`, completed 2026-11-01.
Source SHA-256: `637dff25f02bc5d2740e642d3a61abd79bd1f62f4fc9f22946be7e43976ed905`.
The before/after hash matched; no implementation edits followed this run.
Recorded tools: Xcode 26.6 (17F113), Apple Swift 6.3.3, Python 3.14.3,
Docker Compose 2.31.0-desktop.2, Gitleaks 8.30.1, Go 1.27.3 darwin/arm64.

| Layer | Final result |
| --- | --- |
| Go tests | 633 passes including subtests; race detector; shuffle seed 20261101; no failures |
| Native iOS | 252 unit + 33 UI = 285 passed, 0 failed, 0 skipped; iPhone 17 Pro / iOS 26.5 disposable simulator |
| Build / types | CLI and server built with Go 1.27.3; Swift app, extension and tests compiled |
| Vet / format / installer | All passed; Go formatting and diff whitespace clean |
| Vulnerability scan | govulncheck v1.9.0: `No vulnerabilities found.` |
| Security-file statement coverage | private_files.go 17/20 (85%); store/gateway.go 42/48 (87.5%); api/gateway.go 95/105 (90.5%); gateway_limits.go 17/17 (100%); each enforced at >=80% |
| Manual mutation | 8/8 rejected by their intended named tests: old-key reads, ask grants, 0644 files, default 32 cap, rotated-token deletion, false-success 410, per-route quota, erased migration usage |
| Checker controls | 7 known-bad controls rejected |
| Compose / secrets | Default concurrent cap 0 verified; diff/new-source scan had no findings |
| State | All required layers present; final source hash unchanged |

Inspected exported screenshots: `legacy-choice-read-only.png`,
`historical-answer-read-only.png`, and
`settings-reset-key-confirmation-large-zh.png`. Historical choices/answers are
readable without answer buttons; the native reset alert retains both actions.
At the largest accessibility size its long message uses the system scroll area.
The report/bundles/screenshots stay in ignored `dist/`; the persisted entry point
regenerates them rather than committing binaries or test bundles.

## Behavioral mapping

| Contract | Executable checks |
| --- | --- |
| No default 32-push cap; explicit cap remains | `TestGatewayLimitsDefaultHasNoFixedConcurrencyCap` admits 64 blocked provider calls across two tokens; admission/release/busy/live-TCP regressions verify opt-in caps and Retry-After |
| Multiple routes/IPs/case aliases cannot reset 60/minute allowance | `TestGatewayQuotaCannotBeResetByRoutesOrClientIPs`, `TestGatewayQuotaSharedAcrossRoutesAndTokenAliases`, `TestGatewayQuotaConcurrentAcrossProcesses` |
| v7 preserves active v6 usage; expiry restores allowance | `TestGatewayQuotaMigrationPreservesActiveUsage`, existing gateway backup/restore test |
| Token updates do not charge the wrong identity or permanently fail delivery | `TestGatewayQuotaRejectsStaleRouteWithoutSpendingNewToken`, `TestGatewayRouteChangeBeforeAdmissionCanRetry` |
| Invalid-token cleanup cannot erase a rotated token | `TestGatewayInvalidationPreservesConcurrentTokenRotation`, permanent-invalid-route cleanup regression |
| Sender keys notify only; legacy ask grants ignored, not promoted | `TestLaunchSenderKeysAreNotifyOnly`, `TestLaunchAskOnlyLegacyKeyDoesNotGainNotify`, existing key tests |
| Historical answers require the owning phone | `TestLaunchLegacyHistoryRequiresOwningPhone`: sender 403, other phone 404, owning phone reads old answer, original notify key still sends |
| Retired writes/waits return 410 without pushes or history changes | `TestLaunchQuestionExecutionRetiredWithoutSideEffects`, compiled-source retirement gate |
| Private database/sidecars/backups; no clobber or URI diversion | `TestPrivateDatabaseAndBackupWithPermissiveUmask`, `TestPrivateExistingFilesAndSymlinks`, `TestPrivatePathEncodingAndBackupFailure` |
| Old reply text survives without a sender; deletion is explicit | `SavedReplyRecordsTests` exercises Unicode records, restart, both record types, write failure, corrupt-file preservation and explicit deletion; native notification-only/reset/history regressions |
| Local history/read state/registration still isolated | Existing local SQLite, grouping, migration, stale-session, cache-404, key-reset and full UI suite |

## RED and corrections

Observed failures before fixes included default `notify,ask` grants, sender
question-history reads returning 200, active question creation returning 201,
0644 files, default rejection of half of 64 concurrent requests, and six saved
archive assertions across four Swift cases. Shared-quota RED additionally showed
the second route's request succeeding after the first spent all 60 attempts.
The token-update RED returned not-found/500 instead of retryable 429.

Removed tests exercised intentionally retired send/respond/wait contracts.
Their replacements assert 410, read-only owner access and preservation/deletion
of saved text. Store legacy writer helpers now live exclusively in `_test.go`
to keep migration/history/cascade fixtures without shipping their execution.

The first broad UI run had 10 failures caused by cross-case persistent state:
the fixture reset did not reset the actual phone's local history, read state,
credentials or display mode. Cases now reinstall the app and reset the keychain
only on the runner-created simulator. The pagination case explicitly selects
Messages and uses case-owned IDs. Original ordering/count/read assertions remain.
Intermediate runs were stopped when further backend fixes made their source
state obsolete. They are not final passing evidence.

The mutation checker rejected an ambiguous SQL anchor and a mutant that failed
to compile due to an unused import. Anchors were narrowed and the faulty mutant
corrected: a kill requires the intended named behavior test to fail, not a
compiler failure. No failing production test was converted into a skip.

## Limits and delivery impact

- Coverage gates cover four security-focused files at an explicit 80% minimum,
  not every changed line. Rare OS/SQLite error branches and all changed-line
  branch coverage remain unproved. Native iOS coverage is collected in xcresult;
  there is no Swift changed-line coverage gate or Swift mutation score.
- Mutation checks use eight chosen faults. Properties include existing IP
  normalization checks and Unicode archive round trips; the mutants were not
  rerun against a property-only subset. This is not exhaustive adversarial proof.
- Old V0 verification manifests/mutation anchors describe their recorded source
  states. Shared native packaging helpers include the current archive tests;
  their historical full-verifier entry points are not claimed to pass here.
- No physical-device/APNs, production deployment, Docker image execution or
  external TestFlight review was performed. Compose was checked with dummy
  environment values and without opening real keys or `.env` files.
- Go SDK approval methods intentionally disappear. V0 approval clients must
  upgrade. Historical records, current local inbox and existing notify keys stay.
- Schema v7 requires a pre-upgrade snapshot for rollback to old binaries. Modes
  do not encrypt content, secure untrusted parent directories or repair older
  backup copies elsewhere. Snapshot publication requires hard links on the
  destination filesystem. Healthy idle routes are retained; only APNs-confirmed
  invalid routes are removed. Expired quota identities are pruned on admission.
- Default concurrency is 0/unlimited, matching the supplied Bark source's shared
  HTTP/2-client pattern. An existing explicit `32` remains until changed to `0`.
- Local development binaries are rebuilt with Go 1.27.3. No CLI release tag,
  installation replacement, TestFlight upload or running-service update is part
  of this task. Published v0.1.9 binaries remain unchanged until a new release.
