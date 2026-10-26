# Sending from a script, CI job or build service

Read this when something other than an agent on the user's computer sends notifications, such as a CI pipeline, a cron job or a service. Everything below comes from the repository https://github.com/AmazingAng/meow (`README.md` → HTTP API, and `contracts/notification-v1.schema.json`).

## Credentials

The sender needs the server address and a Device Key (`meow_…`), which can only send notifications.

- The user stores the Device Key in the platform's secret store, such as a GitHub Actions secret, and exposes it as `MEOW_KEY`. The server address can be a plain variable, `MEOW_SERVER`: usually `https://api.meow.blue`, or the self-hosted address the app shows. There is no default.
- Never write the key into the repository, a workflow file, a command you show, or a log. Don't use `curl -v` or `--trace`; they print the `Authorization` header.
- Anyone with the key can send notifications to the user. If it leaks, tell the user. The app has no key rotation in Settings, and re-registering creates a new key without revoking the old one.

## With the CLI

If the job can install the CLI (step 1 in [../SKILL.md](../SKILL.md)), `MEOW_SERVER` and `MEOW_KEY` replace `meow setup`:

```sh
meow send --source "CI" --event failure --priority high --title "Build failed" "main @ abc1234: 3 tests failed"
```

To group notifications by repository and run, add context. For example, on GitHub Actions:

```sh
meow send --source "GitHub Actions" --workspace "$GITHUB_REPOSITORY" --workspace-id "$GITHUB_REPOSITORY" \
  --session "$GITHUB_WORKFLOW" --session-id "$GITHUB_RUN_ID" \
  --event failure --title "Build failed" "main @ abc1234: 3 tests failed"
```

## With curl

`POST $MEOW_SERVER/v1/notifications` with `Authorization: Bearer $MEOW_KEY`.

Plain text: the first line is the title and the rest is the body. Optional query parameters are `title`, `source`, `priority`, `event` and `type`; context needs JSON. Use `--data-binary`; `-d` drops newlines.

```sh
printf 'Build failed\nmain @ abc1234: 3 tests failed\n' |
  curl -fsS -H "Authorization: Bearer $MEOW_KEY" --data-binary @- \
    "$MEOW_SERVER/v1/notifications?source=CI&event=failure&priority=high"
```

JSON needs `Content-Type: application/json`; without it the body is treated as plain text. Build the JSON with a tool such as `jq` when values contain quotes or newlines.

```sh
curl -fsS -H "Authorization: Bearer $MEOW_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: build-1234-attempt-1" \
  --data-binary '{"title":"Build failed","body":"main @ abc1234: 3 tests failed","source":"CI","event":"failure","priority":"high","context":{"workspace":"acme/api","workspace_id":"acme/api","session_id":"run-1234"}}' \
  "$MEOW_SERVER/v1/notifications"
```

| Field | Values |
| --- | --- |
| `title` | Required, up to 256 characters |
| `body` | Up to 4000 characters; Markdown is rendered in the app |
| `source` | Display label, up to 64 characters |
| `event` | `result`, `failure` or `attention`; optional, never inferred |
| `priority` | `low` (silent), `normal` (default) or `high` (Time Sensitive) |
| `type` | `info` (default) or `alert` (needs the user) |
| `context` | Object with `workspace`, `workspace_id`, `session` and `session_id`. Names up to 200 characters, IDs up to 128, single line. The app groups by source, `workspace_id` and `session_id`; names are only labels. |

The request body can be up to 64 KB.

## Responses

| Status | Meaning |
| --- | --- |
| `201` | Accepted. `push_status` in the JSON response is `queued`; it is not proof that the phone showed it. |
| `200` with `Idempotent-Replayed: true` | Same `Idempotency-Key` and same request: nothing new was sent. |
| `401` | Missing, wrong or revoked Device Key, or the wrong server. |
| `422` | Invalid field, or the `Idempotency-Key` was already used for a different request. |
| `429` | Rate limited. Retry later with the same `Idempotency-Key`. |

`Idempotency-Key` is optional: up to 128 printable ASCII characters. Use one value per logical notification, such as the build ID and attempt, so a retried request isn't shown twice.

Don't let a failed notification fail the build. For example, append `|| echo "Meow notification failed" >&2` to the command.
