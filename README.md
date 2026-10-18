# Meow

Push notifications and quick replies for AI agents. An agent sends a notification or asks a question, your iPhone shows it, you answer from the lock screen, and the waiting CLI gets the answer.

```
server/   Go API server: one process, one SQLite file (WAL), APNs delivery
cli/      the `meow` command
client/   Go client for the HTTP API (used by the CLI and the server tests)
ios/      SwiftUI app (iOS 17+) and its notification service extension
```

## Quick start (local, no iPhone needed)

```sh
go build -o bin/meow-server ./server/cmd/meow-server
go build -o bin/meow ./cli/cmd/meow

MEOW_ADDR=127.0.0.1:8080 bin/meow-server &

# Stand in for the iOS app: registers a device and logs the CLI in with its Device Key.
eval "$(bin/meow dev register --server http://127.0.0.1:8080)"

bin/meow "Build completed"
bin/meow ask "Deploy to production?" --choices approve,reject   # blocks…

# in another terminal (same MEOW_DEVICE_TOKEN):
bin/meow dev inbox
bin/meow dev reply qst_… approve
```

Without APNs credentials, notifications are stored and shown in the inbox but marked `skipped` instead of pushed.

## CLI

```
meow <message>                         info notification
meow send <message> --priority high    reads stdin when no message is given
meow ask <question> [--choices a,b] [--timeout 300] [--no-wait] [--json]
meow wait <question-id>
meow login [--key meow_…] [--server URL]
meow whoami
meow setup claude|codex [--uninstall] [--no-summary] [--no-skill]
```

`meow ask` prints the answer to stdout. Exit codes: `0` answered, `3` rejected, `4` expired, `5` cancelled, `130` interrupted (Ctrl-C cancels the question). It keeps waiting through network failures and server restarts.

Configuration lives in `~/.config/meow/config.json` (mode 0600); `MEOW_KEY`, `MEOW_SERVER` and `MEOW_SOURCE` override it.

## Agent integrations

```sh
go build -o ~/.local/bin/meow ./cli/cmd/meow   # any stable path on your PATH
meow login                                       # paste the Device Key from the app
meow setup claude                                # Claude Code
meow setup codex                                 # Codex
```

`meow setup` shows what it will change and asks first; each edited file is backed up as `<file>.bak.<timestamp>`. Re-running is safe, and `--uninstall` removes only what meow added.

| Agent | What is installed | You get notified when |
| --- | --- | --- |
| Claude Code | async `Stop` and `Notification` hooks in `settings.json` (`$CLAUDE_CONFIG_DIR` or `~/.claude`), skill in `skills/meow/` | a task finishes; a permission prompt or MCP form has been waiting about 6 seconds |
| Codex | top-level `notify` in `config.toml` (`$CODEX_HOME` or `~/.codex`), skill in `~/.agents/skills/meow/` | a turn finishes |

- Completion notifications include the first 200 characters of the agent's last message; pass `--no-summary` to leave it out.
- The skill teaches the agent to ask you with `meow ask` when it needs a decision.
- Codex runs only one `notify` command. If you already have one, setup stops and shows it; `--force` replaces it.
- The hook command (`meow hook …`) always exits 0 and prints nothing, so it never blocks or disturbs the agent. Problems are logged to `hook.log` next to the meow config. Set `MEOW_HOOK_DISABLED=1` to silence it.

## HTTP API

All requests use `Authorization: Bearer <token>`. There are two credentials:

- **Device Key** (`meow_…`): send-only, for agents and scripts. Scopes `notify` and `ask`. Revocable. It can never answer questions.
- **Device token** (`mdt_…`): held by the iOS app. Answers questions and manages keys.

Only SHA-256 hashes of both are stored.

| Endpoint | Credential | |
| --- | --- | --- |
| `POST /v1/notifications` | Device Key | JSON `{title, body, source, priority, type}` or a plain-text body |
| `POST /v1/questions` | Device Key | `{question, body, source, choices, timeout}` → `{id, status, expires_at, …}` |
| `GET /v1/questions/{id}` | either | current state |
| `GET /v1/questions/{id}/wait?timeout=30` | Device Key | long poll until resolved, at most 60s |
| `POST /v1/questions/{id}/cancel` | Device Key | |
| `POST /v1/questions/{id}/responses` | device token | `{answer}`; `Idempotency-Key` required |
| `POST /v1/devices` | none | register; returns the device token and the first Device Key once |
| `GET/PATCH /v1/device`, `GET/POST /v1/keys`, `DELETE /v1/keys/{id}` | device token | |
| `GET /v1/inbox`, `POST /v1/notifications/{id}/read`, `…/delivered` | device token | |

```sh
curl -H "Authorization: Bearer $MEOW_KEY" -d "Build completed" https://api.meow.blue/v1/notifications
```

Question lifecycle: `pending` → `answered` | `rejected` | `expired` | `cancelled`. Exactly one response is accepted per question. A retry with the same `Idempotency-Key` and answer returns the stored result. Any other second response gets `409`, and a response after expiry gets `410`. Expiry never approves anything.

`POST` requests that create resources accept an optional `Idempotency-Key`; replays return `200` with `Idempotent-Replayed: true`.

Push status is recorded per notification: `queued`, `accepted` (APNs accepted it, which is not proof it was displayed), `failed` or `skipped`. `delivered_at` and `read_at` are reported by the app.

## Server configuration

For a persistent Docker deployment with Caddy HTTPS, see [deploy/README.md](deploy/README.md).

| Variable | Default | |
| --- | --- | --- |
| `MEOW_ADDR` | `:8080` | |
| `MEOW_DB` | `meow.db` | keep it on a persistent local volume |
| `MEOW_RETENTION` | `720h` | resolved questions and notifications older than this are pruned |
| `MEOW_TRUST_PROXY` | | `true` to rate-limit by `X-Forwarded-For` behind a reverse proxy |
| `MEOW_APNS_KEY_FILE`, `MEOW_APNS_KEY_ID`, `MEOW_APNS_TEAM_ID`, `MEOW_APNS_TOPIC` | | APNs token auth (.p8); push is disabled when unset |

Back up the live database with `meow-server backup /path/to/backup.db` (a consistent `VACUUM INTO` snapshot). Do not copy the `.db` file while the server is running.

## iOS app

```sh
cd ios
xcodegen generate          # brew install xcodegen; creates Meow.xcodeproj from project.yml
open Meow.xcodeproj
```

- Bundle ID `blue.meow.app`, App Group `group.blue.meow.app`, team set in `project.yml`.
- `ios/scripts/make_assets.py <design/assets> ios/Meow/Resources/Assets.xcassets` regenerates the icon, logos and mascot from the design masters.
- Debug builds accept launch arguments `-meowServer http://127.0.0.1:8080 -meowAutoRegister YES` to skip onboarding against a local server.
- Simulator pushes: fill in the IDs in `ios/scripts/approval.apns` and run `xcrun simctl push booted blue.meow.app ios/scripts/approval.apns`.

Notification actions: Approve requires unlocking the phone, Reject is destructive, and free-text questions get a reply field. Questions with other choices are answered in the app. Answers are saved before sending and retried with the same `Idempotency-Key`, so an answer given offline or from the lock screen is applied at most once.

## Development

```sh
go test -race ./...
```
