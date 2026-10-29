# Working on Meow

This file applies throughout the repository. Follow the user's current request;
when a request explicitly changes a product decision below, update its related
documentation and tests along with the implementation.

## Product scope and references

Meow V1 delivers notifications from agents and services to an iPhone. Users read
the result and continue on their computer. The current app does not approve
actions or send replies. Legacy questions and answers remain readable; preserve
previously queued replies as copyable records rather than sending them again.

Start with [README.md](README.md) and [docs/architecture.md](docs/architecture.md).
Use [contracts/notification-v1.schema.json](contracts/notification-v1.schema.json)
for the wire contract and [the inbox grouping PRD](docs/prd/Meow_Inbox_Grouping_PRD.md)
for local grouping behavior. Check the implementation when older documentation
and current behavior disagree, and correct documentation within the task's scope.

Keep changes small and focused. Prefer existing modules and dependencies over new
frameworks, runtime plugin systems, temporary feature gates or special cases for
one user's session. Preserve unrelated working-tree changes, including work from
other agents. Use an isolated worktree when overlapping edits would be unsafe.

## Module boundaries

| Path | Responsibility |
| --- | --- |
| `message/` | Canonical notification types, normalization, limits and plain-text previews |
| `adapters/` | Pure external-event decoding, filtering, origin metadata and deduplication |
| `cli/cmd/meow/` | Commands, hook hosting, local metadata lookup, retries and diagnostics |
| `cli/internal/setup/` | Agent configuration installation and removal |
| `client/` | Shared Go HTTP client and typed transport errors |
| `server/internal/api/` | Authentication, scopes, HTTP validation and idempotency |
| `server/internal/store/` | SQLite records, device isolation, serialized writes and retention |
| `server/internal/push/` | APNs and gateway delivery, payload fitting and push retries |
| `ios/Shared/` | Models and logic shared by the app and notification service extension |
| `ios/Meow/` | SwiftUI app, local inbox and presentation |
| `contracts/` | JSON Schema and shared compatibility fixtures |
| `deploy/` | Deployment examples and operating instructions |

Adapters must not read files, environment variables or credentials, or perform
network requests. Put host-dependent work in the CLI and inject it through the
existing adapter interfaces. Services that already emit the standard JSON can
use the existing API without an agent-specific server change.

The server is one Go process with SQLite in WAL mode and serialized writes.
Preserve the pure-Go build and existing dependency pins in `go.mod`. Do not add
CGO, an ORM, a queue service or another database for an unrelated change.

## Notification and delivery invariants

- `event` is explicit: `result`, `attention` or `failure`. Never infer it from a
  title, body, priority or translated text. A result does not imply success.
- Preserve the original body. Render Markdown in app details; generate plain
  text for native notification previews before truncating. Keep Go and Swift
  presentation aligned using shared fixtures, not separate ad hoc regex fixes.
- Session IDs identify sessions; names are mutable display labels. Full local
  project paths are hashed before transmission. `source` is a display label,
  not an authentication identity.
- Keep presentation consistent: session headline, then project and Agent, then
  body (with a separate date row in details). Use the existing shared helpers.
- Ignore Claude Stop events with no nonblank original assistant reply. Apply
  this check before `--no-summary`, which must still notify for a real reply.
- Hooks must exit successfully and remain silent even when delivery fails.
  Diagnostics must not contain credentials, private bodies or raw tool input.
  Preserve stable idempotency keys and payloads across retries.
- Setup must preserve unrelated agent configuration and hooks, back up edited
  files, and remove only Meow-owned entries. The retired Meow Codex `notify`
  route must not resume sending alongside native hooks. Skills are distributed
  separately; setup does not generate a new skill.
- APNs acceptance is a provider acknowledgement, not proof of display on a
  phone. Keep push status, delivery reporting and read state distinct.

## Credentials, isolation and saved history

- Device Keys (`meow_…`) belong to senders; device tokens (`mdt_…`) belong to the
  app. Preserve scope checks and server-side hashed credential storage. Do not
  expose app credentials through sender APIs or copy credentials into commands,
  fixtures, logs, screenshots or Git history. Use placeholders and disposable
  credentials for verification.
- Keep APNs private keys outside the repository and Docker build context. In
  gateway mode, keys stay on the gateway; the phone fetches content directly
  from its configured origin. See [docs/wake-gateway.md](docs/wake-gateway.md).
- Bind callbacks, caches and pending work to the server/device registration.
  A late response or old notification must not operate on a new registration.
  Failed origin hydration must retain the existing generic, safe fallback.
- The iPhone's local SQLite history is independent of the server cache. Server
  expiry, omission or a detail `404` must not erase downloaded history.
  Search, filtering and grouping use all saved local messages before pagination.
  Preserve explicit deletion semantics and per-view, per-scope merge rules.
- Shared App Group SQLite is the sole authority for iPhone read state and unread
  counts. App/NSE writes coordinate through transactions. Reads are local and
  must not require read/count HTTP; legacy server endpoints remain compatible.
  Pushes must omit server badge values. Preserve local read state on upsert,
  scoped deletion tombstones, registration fencing and transactional legacy import.
  Background badge updates are best-effort; reconcile from SQLite on app entry.
- The rollout retention default is `720h`. Do not silently shorten it: older
  clients replace their local inbox from the server list. Follow the build 17+
  migration guidance in [deploy/README.md](deploy/README.md). Explicit environment
  settings override defaults; increasing retention cannot recover deleted data.
- Use online snapshots for backups. Rehearse restoration on a separate database
  or volume. Check schema compatibility before proposing a binary-only rollback.

## Development and verification

Use the Go version declared in `go.mod`. Common build commands:

```sh
go build -o bin/meow ./cli/cmd/meow
go build -o bin/meow-server ./server/cmd/meow-server
```

For Go behavior changes, add a focused regression and run the relevant tests.
Before shipping Go changes, use the existing CI checks:

```sh
go test -race ./...
go vet ./...
python3 -B scripts/test_install.py
git diff --check
```

Run installer tests when changing installation or release behavior. Format Go
with `gofmt`. Documentation-only changes need link/path and whitespace checks,
not a new application test suite. Do not weaken assertions or hide failures;
report unrelated failures separately from the checks that actually passed.

iOS supports iOS 17+. `ios/project.yml` is the source of truth for XcodeGen;
regenerate with `xcodegen generate` from `ios/`. Do not commit the generated
`ios/Meow.xcodeproj` or derived build output. Newer platform APIs need availability
guards. Update English and Simplified Chinese strings for user-visible changes.

For relevant UI and model verification, the existing runner creates disposable
HTTP fixtures and a simulator:

```sh
python3 -B ios/scripts/verify_ui_optimization.py --output dist/ui-check
```

Choose a fresh output directory for each run. The runner supports `--test-case`
for a focused UI check and `--ui-only` when unit tests need not be repeated.
For layout changes, inspect exported screenshots, including Chinese and large
Dynamic Type where relevant. Real SpringBoard banner verification uses
`ios/scripts/verify_system_notification_banner.py`. Neither a simulator fixture
nor an accepted APNs request proves physical-device delivery.

Other focused verifiers live in `scripts/` and `ios/scripts/`; read their help
and scope before use. Use isolated state and test devices. Do not send pushes to
real users, rotate real keys, delete real records or restart active applications
unless those actions are part of the user's authorized task.

## Delivery and releases

Follow the requested Git workflow; keep commits focused and exclude others'
uncommitted work. Do not create a PR, push a release tag, publish a release or
deploy a service merely to validate a change.

- CLI: `v*` tags trigger `.github/workflows/release.yml` and GoReleaser. A release
  includes macOS/Linux ARM64 and AMD64 archives plus checksums. Verify published
  downloads and synchronize the Homebrew formula with `scripts/update_homebrew.py`.
  Distinguish a locally built fix from an official published version.
- iOS: update `CURRENT_PROJECT_VERSION` in `ios/project.yml`, regenerate and
  archive. Verify the archive/export with `ios/scripts/verify_distribution.py`.
  An archive is not an upload. Use App Store Connect distribution when external
  TestFlight groups need access, rather than Internal Only.
- If command-line export reports `Failed to Use Accounts`, report the actual
  failure and retain the validated archive. The Xcode UI can have working
  authentication despite that error; do not assume the account is signed out.
- State separately what was committed, built, uploaded, processed or deployed,
  and whether an update affects CLI users, the server, the gateway or iOS.
