# Meow server review fixes — evidence

2026-10-20. All three reported findings were reproduced and fixed. Work is on `codex/server-review-fixes`; prior local changes were retained. No dependencies, schema migrations or API response shape changes were introduced.

Specification: [server-review-fixes-spec.md](server-review-fixes-spec.md). Spec approval: **not obtained (autonomous run)**, under the user's explicit repair request. Independent verification: **not performed**. The evidence is scoped author-written validation, not independent assurance.

## Behavior and regression mapping

| Finding / invariant | Change | Executable evidence |
| --- | --- | --- |
| Notify-only credentials could read question answers | GET question now requires `ask` for Device Keys; owning device tokens remain authorized. README endpoint table updated. | `TestQuestionReadRequiresAskScope`: pending/answered deny with 403 and no content; default/ask-only/mobile clients succeed; invalid/missing/revoked keys return 401. Existing `TestCredentialSeparation` checks other-device 404. |
| Gateway Retry-After was ignored | Gateway error carries parsed seconds/HTTP-date delay; dispatcher waits max(exponential backoff, Retry-After). Invalid/overflowing headers use ordinary backoff. | `TestGatewayRetryAfterReachesDispatcher` observes 60 virtual seconds between requests; `TestGatewayRetryAfterLocalHTTP` checks a real local HTTP response; `TestRetryAfterParsing` and `FuzzRetryAfter` cover parser limits. |
| A stale question was retried after cancellation | Reload target and rebuild message before every attempt; skip resolved/expired questions, including expiry before a sweep. | `TestDispatcherReloadsBeforeRetry`: cancellation, approval, rejection, expiry, token rotation, already accepted; `TestDispatcherReloadsGatewayBinding`: rotated gateway credential used on retry. |
| Existing backoff, bounded attempts and shutdown behavior | Existing retry budget retained; context interruption leaves queued for restart. | `TestDispatcherRetryPolicy`: ordinary/untyped failure, max(backoff, delay), terminal error, four-attempt exhaustion, shutdown before attempt and during wait. |
| Existing features remain working | Full repository Go suite and compiler checks | Final race/shuffle/build/vet run below. Existing payload, routing, response, store and CLI tests remain green. |

## Final fresh verification

Entry point from repository root:

```sh
python3 scripts/verify_server_review.py
```

Toolchain: **Go 1.27.2 darwin/arm64**, Python standard library. The script recreates only its dedicated ignored `dist/server-review-fixes/final` output directory. [Machine-readable results](server-review-fixes-results.json) include every relevant Go/SQL source and harness hash, baseline Git commit, test counts, selected coverage blocks, executed mutant failures and layer completion manifest. Existing uncommitted sources were tested as present, not inferred from the baseline commit. Source hashes were unchanged throughout the final run.

| Layer | Result |
| --- | --- |
| `go test ./... -race -shuffle=on -count=1 -coverprofile=… -json` | **122 top-level tests/fuzz seed suites; 185 including subtests; 0 failures**. No race report. |
| New behavior coverage gate | **14/14 selected instrumented blocks, 18/18 statements executed**. Selection anchors cover new scope check, gateway error delay, parser, shutdown guard, expiry check and retry timing. Existing error-handling blocks merely relocated inside the attempt loop are outside this gate. This is not whole-file or branch coverage. |
| Coverage checker controls | Rejects zero counters and missing file coverage. Disabling the checker causes the negative control itself to fail. These controls establish those specific failure paths, not complete checker correctness. |
| Build / types | `go build ./...` passed. |
| Static analysis / formatting | `go vet ./...` passed; all six touched Go files pass `gofmt -l`. `git diff --check` passed. |
| Parser property fuzzing | Five seconds, **85,280 executions**; no failure. Property checks representable integer delays round-trip exactly and overflowing inputs fall back to zero. |
| Manual mutation | **5/5 killed**: remove scope check; drop gateway header; ignore requested delay; allow resolved questions; allow expired questions. Each compiled mutant ran with `-count=1` in a temporary copy and had named failing tests, not merely a build failure. Original sources were restored in that copy after each run. |
| Real process / HTTP | Built and launched `meow-server` on loopback with a disposable SQLite database. Registered device, created notify-only key, asked and answered a question. Notify-only GET returned **403 with only the error object**; ask-capable GET returned **200 with the answer**. Process shut down cleanly. |
| Capability / dependency review | No new external service, subprocess, environment access or dependency in production code. The additional per-retry reads use the existing store and gateway binding. Verification-only subprocesses/HTTP services are local. |

## Failure history and limits

Before implementation, tests reproduced notify-only access (200 instead of 403), gateway retry after 2 seconds despite Retry-After 60, and duplicate stale sends after cancellation/answer/expiry; token rotation also used the old token. All are green in the final run.

Two harness corrections were needed: the client returns an allocated empty Question on errors, so denial is now verified against the actual HTTP response rather than a nil pointer; the standalone response request must include Idempotency-Key and expect HTTP 201, matching the existing API. Baseline snapshots were renamed `.go.txt` after Go initially treated scratch `.go` files as another package. No production behavior was weakened to accommodate these corrections.

Staticcheck was unavailable; `go vet` ran. Dependency auditing is not applicable to this change because dependencies did not change. No performance budget, migration or UI surface changed. No independent subagent review or real APNs/device delivery was performed. Mutations validate the specified regression tests collectively; parser fuzzing is not claimed to verify authorization or dispatch state.

A push already handed to APNs/the gateway cannot be recalled. State checks occur immediately before each attempt and are not atomic with remote acceptance; the original backoff-staleness bug is fixed, not all possible distributed delivery races. Retry-After remains an in-memory worker wait; no persistent retry scheduling or multi-process delivery claiming was added.

Dismissed findings: none.
