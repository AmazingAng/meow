# Codex input reminders — 2026-11-01

The previous integration installed only Stop and PermissionRequest. Explicit
choice/text input uses a different tool event, so waiting for a user's answer
could produce no Meow reminder. This is fixed by an input-only PreToolUse hook.

## Contract

- Only request_user_input/request_user_input_async, optionally qualified with
  `functions.`, match. The adapter independently checks the name.
- Questions and string/object choices become high-priority `attention` messages.
  Answers, arbitrary command arguments, prose and child creation are not mirrored.
- Auto-review permission suppression is unchanged; explicit human questions still
  notify in auto-review mode. Different calls in a turn have different keys;
  retries of one call retain the key. --no-summary omits content, not the reminder.
- Empty/malformed questions do not notify. Native questions require options,
  matching the tested Codex runtime; async questions can be free-form.
- Setup upgrades and uninstalls only Meow-owned hooks, preserves foreign handlers,
  and uses an anchored matcher. Doctor recognizes missing/outdated input hooks.
- App/server/protocol/permissions/legacy-notify policy remain unchanged. No daemon.

## Verification

Source base: d772d97; branch fix/codex-input-reminders. Implementation/test/tool
source-set SHA256 (adapters/, cli/, runtime verifier and skill, sorted JSON):
`4fdffc21cd1eafb11600942bb439eff913d53f0f138e578d25fa30142eed3483`.
Per-file hashes: local `dist/codex-input-reminders/final/source-sha256.json`.

Commands:

```sh
go test -race -shuffle=20261101 -count=1 -json ./...
go vet ./...
python3 -B scripts/test_install.py
go build -o bin/meow ./cli/cmd/meow
python3 -B scripts/verify_codex_input_runtime.py --output dist/codex-input-check-NEW --meow bin/meow
git diff --check
```

- Final full Go run: **603 cases passed, zero skipped tests**. This includes
  the new adapter, identity/replay/Unicode/no-summary, unrelated-tool filtering,
  auto-review independence and setup upgrade/idempotence/uninstall regressions,
  plus existing goal/subagent/suggestion/permission tests. Vet passed.
- Installer: **9 tests passed**. No dependency or installer behavior change.
- Real Codex `0.165.0-alpha.1.2` app-server, isolated CODEX_HOME, loopback model
  and loopback Meow server: **3 scenarios passed**. Native choices, dynamic async
  choices and async free text each emitted exactly one attention reminder before
  the fixture client was allowed to answer. All discovered fixture hooks had to
  report `trusted` through the supported hooks/list API before measurement.
- Runtime results: local `dist/codex-input-reminders/runtime-final-good/report.json`.
  The persisted verifier reviews only its generated hooks through Codex's normal
  CLI flow, in a temporary directory outside any Git repository. It never reads
  or edits undocumented hook trust storage or real user authorization.

## Failures and corrections

The initial regression tests failed on the missing adapter and missing setup
hook. They then passed after the implementation. No assertions were skipped.

An early app-server probe incorrectly assumed the TUI/exec trust-bypass flag
also applied to app-server. The hooks were untrusted, so the apparent dynamic
tool hook gap was not valid evidence. After normal fixture hook review and an
explicit trusted-status check, both native and dynamic tools triggered PreToolUse.
The earlier proposed background-listener option was withdrawn; none was added.

Fixture development also corrected the request method to the generated schema's
`item/tool/requestUserInput`, terminal capability/review/PTY cleanup handling,
and a native free-text fixture rejected by Codex for having no options. The
latter produced a real defensive adapter regression: invalid native questions
must not send a reminder. Async free text was then verified separately.

Automatic review rejected a test operation that would have trusted the real
repository root. That operation was not performed. The test was moved outside
the repository with disposable state; real user trust settings were untouched.
Only owned stalled fixture processes were interrupted during test-tool repair.

## Rollout and limits

This source fix requires a CLI build containing it, then `meow setup codex`, a
new session and user authorization of the new input hook in `/hooks`. Updating
an old binary alone cannot install the extra hook. Current published v0.1.8 does
not contain this change. No official CLI release, real user hook update,
TestFlight upload or server deployment was performed in this task.

The runtime fixture proves real Codex hook dispatch and HTTP submission order,
not physical APNs display, desktop card rendering, all Codex versions/clients,
or current authorization in the user's sessions. Background command hooks remain
best-effort if their host exits. Native invalid tools can be rejected after
PreToolUse; this adapter validates the documented question shape, not all future
upstream tool semantics. No independent reviewer or Swift/UI run was used;
there are no iOS changes.
