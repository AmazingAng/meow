# Server review fixes — executable specification

Tier 3 (authentication and asynchronous delivery). Spec approval: not obtained (autonomous run); proceeding under the user's explicit request to fix all genuine findings. This specification is available for review after the fact.

Setup: use branch `codex/server-review-fixes`, retaining existing working changes. No commits, deployment, tools installation, schema migration or new dependencies. Go's existing test/race/vet/coverage tools and Python standard library suffice. Add `server/internal/api/question_scope_test.go`, `server/internal/push/review_regression_test.go`, `scripts/verify_server_review.py`, and verification spec/evidence/results under `docs/verification/`. Scratch output belongs under ignored `dist/server-review-fixes/`.

Acceptance scenarios:
1. A same-device notify-only key receives 403/forbidden for GET question, both pending and answered, with no answer in its response. Ask-only/default keys and the owning mobile token retain access; other devices still receive 404; missing/revoked credentials receive 401.
2. A gateway 429 with Retry-After 60 must preserve that delay through the push error and dispatcher; the next attempt occurs no sooner than the larger of that delay and exponential backoff. HTTP-date is also supported. Missing/invalid/negative/overflowing header values safely use ordinary backoff. Non-retryable responses remain terminal. Context cancellation interrupts the delay and leaves the notification queued.
3. Before each attempt, reload notification/device/binding. If a question was cancelled, answered, rejected, or expired while waiting, skip delivery; expiry must work without a prior expiry sweep. Token rotation is used on the next attempt. Already accepted pushes are not sent twice. The existing retry budget remains bounded.

Failure model and checks:
- Answer disclosure or accidentally denying legitimate clients: HTTP integration credential matrix and mutation.
- Too-early retry, invalid input/overflow, ignoring shutdown: deterministic clock/HTTP boundary tests, parser properties/fuzzing, race/shuffle and mutation.
- Stale push after resolution or stale device credentials: database-backed dispatcher regression scenarios and race detector.

Invariants: existing suite remains green; API response shapes, endpoint paths, database schema and dependency set unchanged. Pushes already handed to APNs/gateway cannot be recalled; this fix checks state immediately before an attempt, not atomically with remote network acceptance. No multi-process queue claim or durable retry scheduler is introduced. Tests use local HTTP services and temporary databases, not production/APNs credentials.

Verification: baseline full suite; RED reproductions before edits; final full race/shuffle suite with coverage; build/vet/gofmt; changed executable statement coverage gate; 3–5 manual mutations in an isolated temporary copy; fuzz parser; real local HTTP integration; source hashes and gate negative controls. Staticcheck/independent agent review may be unavailable/not performed and must be explicitly recorded. Persist a single rerunnable verification entry point. No benchmark budget or UI changes apply.

## Revisions

- Test correction: the Go client intentionally returns an allocated empty Question even on HTTP errors. Replaced the invalid nil-pointer expectation with a direct HTTP assertion for 403 and absence of question text, answer and ID. The no-disclosure contract is unchanged. Baseline source snapshots use .txt extensions so Go does not treat them as an extra package.
