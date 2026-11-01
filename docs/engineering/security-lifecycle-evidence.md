# Credential and gateway lifecycle evidence

Spec: [security-lifecycle-spec.md](security-lifecycle-spec.md).
Tier 3; spec approval not obtained (autonomous run). Independent verification
not performed. The user authorized the reviewed fixes and corresponding skill
updates; no production credentials, records or deployments were used.

## Reproduce

```sh
python3 -B scripts/verify_security_lifecycle.py --output dist/lifecycle-new-run
```

Requires Go 1.27.3 and the existing `gitleaks` executable. The gate downloads
pinned govulncheck v1.9.0, uses disposable files/databases and loopback HTTP,
builds all four CLI targets and does not install, publish or deploy anything.
It refuses an existing output directory and audits an expected-layer manifest.

Final run: `dist/security-lifecycle-final/report.json`, full input SHA-256
`57d49c9030a350d6ff90a4cf1715a206d3fba8a6c1297ab4ac5c1ab33466ea7f`.
The branch retained unrelated uncommitted App Store metadata/screenshots and
the screenshot generator; those inputs enter the full hash, but are excluded
from the security commit. The executable sources tested are the committed fix.
No runtime dependency was added or upgraded; source Go was already 1.27.3.

## Acceptance mapping and results

| Requirement | Executed evidence |
| --- | --- |
| Physical revocation preserves notification/answer history and daily quota; repeated rotations remain bounded | `TestRevokedKeysAreRemovedWithoutLosingHistoryOrQuota`; API scope/revocation suite |
| Old revoked rows cleaned in batches; active keys survive; lookup/deletion indexed | `TestPruneRemovesLegacyRevokedKeysAndUsesActiveIndex`, `TestCredentialDeletionDoesNotScanOtherDevicesHistory` |
| Route expiry, both credentials rejected exactly at 90 days, management/wake renewal | `TestGatewayRouteExpiryBoundaryAndRenewal`, `TestGatewayLifetimeGenerated` (30 seeded generated ages) |
| Atomic schema upgrade/rollback, legacy route credentials and quota preserved | `TestGatewayLifecycleMigrationPreservesRoutesAndRollsBack`; historical v1–v8 fixtures |
| Bounded resumable cleanup, renewed routes and shared quota survive; cancellation | `TestGatewayPruneBatchesPreserveRenewedRoutesAndQuota` |
| Immediate/five-minute maintenance, failure logging and shutdown | `TestGatewayMaintenanceStartsRetriesLogsAndStops` using virtual time |
| Expired route recovery through existing App protocol | `TestGatewayExpiredBindingCanRegisterAgain`, actual loopback API; no iOS source change |
| Safe mode, hostile predictable/destination symlinks, complete concurrent writes, failure cleanup | Three `TestSave...` regressions; actual compiled CLI login/save/send against local HTTP |
| Release binaries free of the known certificate vulnerability | Source plus macOS/Linux ARM64/AMD64 binary govulncheck: no vulnerabilities found |
| Skill instructions | Skill Creator quick validator passed; changed relative references checked; no hook command/trust changes |

- `go test -race -shuffle=20261102 -count=1 ... -json ./...`: **675** passing
  named tests including subtests, zero failed/skipped. Go vet and gofmt passed.
- Changed production statement coverage: **53/57 (93.0%)**, failing threshold
  **85%**. Four uncovered error blocks are CreateTemp/Chmod failures, a quota
  RowsAffected error and route-backfill SQL failure; do not claim full I/O-fault
  coverage. `main.run` live-APNs startup wiring is excluded from this measurement.
- **5/5** manual mutants killed by their named behavioral tests: predictable
  temporary file, retained revoked credential, inclusive expiry boundary,
  omitted wake renewal and late scheduled cleanup. Mutants ran in disposable
  source copies, asserted exact byte changes, reached named test failures and
  restored original bytes. Go's content-addressed compiler cache rebuilt them.
- Checker controls rejected empty/failed/skipped test evidence and zero-hit
  coverage. The latter reached the actual coverage threshold failure path.
- Installer: **9** tests passed, including four target archives and checksum,
  regular-file extraction and atomic replacement behavior.
- Diff secret scan and whitespace check passed. This is not proof that every
  possible secret representation is absent.

## Release gate validation

GoReleaser **v2.18.3** downloaded to ignored verification output, manifest
SHA-256 checked; executable SHA-256
`c04cbdee28394522d1798eb8b9a5e74a90300af5ec7b86661421bd8e341efbff`.
`goreleaser check` and `build --snapshot --clean` passed in an independent
temporary Git checkout, executing all four actual post-build vulnerability
hooks. Host GOOS/GOARCH overrides prevent trying to execute a cross-built
scanner. The main repository's `dist` was not cleaned.

Adversarial negative control: replace each temporary build's artifact with the
known-vulnerable local v0.1.9 executable before its real scan hook. GoReleaser
failed with **GO-2026-6809** before packaging/publication. The fixture config
was restored. Logs: `dist/security-review-20261102/goreleaser-{preflight,negative-control}.log`.

## Operational boundaries

The API and gateway require redeployment; each migrates its own database to v9.
Take online snapshots first. Rollback requires the matching older snapshot.
No live deployment or phone delivery test was performed by this verifier.
The current App already handles expired-route re-enrollment, so no new iOS
binary or change to the submitted build 27 is needed for these fixes.

macOS ARM64 CLI ran locally against a fixture. Other target binaries were
cross-built and scanned, not executed on Linux/Intel hosts. Published downloads,
Homebrew synchronization and installation are separate release evidence.
