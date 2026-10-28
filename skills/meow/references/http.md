# Sending from a script, CI job or build service

Read this when something other than an agent on the user's computer sends notifications, such as a CI pipeline, a cron job or a service. Everything below comes from the repository https://github.com/AmazingAng/meow (`README.md` → HTTP API, and `contracts/notification-v1.schema.json`).

A service can call the existing HTTP API directly or invoke `meow send`. It needs no agent hooks, custom adapter or server change. Adapters are useful only when translating an existing external event format into Meow's notification object.

## Credentials

The sender needs the server address and a Device Key (`meow_…`), which can only send notifications.

- The user stores the Device Key in the platform's secret store, such as a GitHub Actions secret, and exposes it as `MEOW_KEY`. The server address can be a plain variable, `MEOW_SERVER`: usually `https://api.meow.blue`, or the self-hosted address the app shows. There is no default.
- Never write the key into the repository, a workflow file, a command you show, or a log. Don't use `curl -v` or `--trace`; they print the `Authorization` header.
- Anyone with the key can send notifications to the user. If it leaks, tell the user to choose **Settings → Reset Device Key…** in the app, confirm, and update each sender's secret. If revoking the old key fails, reset again to retry; don't claim it was revoked until the app confirms success. Re-registering creates a new device and key without revoking the old key.

## Project and session for services

Use the existing field names below: the API does not define `project_name` or `session_name` aliases. Context fields are optional; omit values that do not apply instead of inventing an agent conversation.

| Meaning | CLI flag | JSON field | Example / how to choose |
| --- | --- | --- | --- |
| Sending app or service | `--source` | `source` | `GitHub Actions`, `Backup service`, `Uptime monitor`. Keep the label stable; this appears in the App's Agent view even for a non-agent service. |
| Project display name | `--workspace` | `context.workspace` | `acme/api` or `Customer portal · production`. Use the same name across that project's notifications. |
| Stable project identity | `--workspace-id` | `context.workspace_id` | `repo:acme/api` or `service:portal:prod`. Prefer an existing repository/service ID. Hash a full local directory path as the hooks do; do not transmit that path or credentials. |
| Task/run/incident display name | `--session` | `context.session` | `Deploy #42 · attempt 1`, `Nightly backup · 2026-10-29`, `API unavailable`. This is the notification's headline. |
| Stable task/run/incident identity | `--session-id` | `context.session_id` | `run:1234:1`, `backup:2026-10-29`, `incident:789`. Reuse for updates about the same logical task; choose a new ID for a separate task. |

For an agent, a session means a conversation; for a service it means the unit of work the user should see together. For example:

| Service | Project | Session name | Session ID lifetime |
| --- | --- | --- | --- |
| CI | Repository | Workflow + run number + attempt | One pipeline attempt; a rerun gets a new ID if it should be a separate group. |
| Scheduled backup | Database/site being backed up | Job + scheduled occurrence | One backup run; progress and final result reuse its ID. |
| Monitoring | Monitored service/environment | Incident summary | One incident, from failure through recovery; the next outage gets a new ID. |

In the **Sessions** view, identity is scoped to the current server/device and composed of `source` + project identity + `session_id`. Project identity uses `workspace_id`, falling back to `workspace` when the ID is missing. Names can change without splitting a session when IDs remain stable; the latest nonempty session name labels the group. Changing `source` or switching between missing/present project IDs also changes identity, so send context consistently.

Without `session_id`, the app falls back to the session name; renaming it can split a group, and equal names within the same source/project can merge. Without both session fields, the message goes into Unclassified in the Sessions view. A supplied `session_id` without a name displays a shortened ID. With an explicit `event` but no session label/ID, the current app uses a generic Session headline rather than the original title; supply a meaningful `session` for a useful headline.

The **Projects** view intentionally combines equal `workspace` display names, even when project IDs or senders differ. Include the environment in the display name when users should distinguish it there. **Agent** groups by `source`; **Messages** keeps individual messages. These are local App views; this metadata does not add new iOS system notification grouping.

Session IDs group related notifications; they do **not** deduplicate them. Give each new update its own `Idempotency-Key`, and reuse that key and identical payload only for retries of that update. For incident `789`, for example, use session ID `incident:789` for both failure and recovery, with distinct keys `incident-789-open` and `incident-789-recovered`.

## With the CLI

If the job can install the CLI (step 1 in [../SKILL.md](../SKILL.md)), `MEOW_SERVER` and `MEOW_KEY` replace `meow setup`:

```sh
meow send --source "CI" --event failure --priority high --title "Build failed" "main @ abc1234: 3 tests failed"
```

To group notifications by repository and pipeline attempt, add context. For example, on GitHub Actions:

```sh
meow send --source "GitHub Actions" --workspace "$GITHUB_REPOSITORY" --workspace-id "repo:$GITHUB_REPOSITORY" \
  --session "$GITHUB_WORKFLOW #$GITHUB_RUN_NUMBER · attempt $GITHUB_RUN_ATTEMPT" \
  --session-id "run:$GITHUB_RUN_ID:$GITHUB_RUN_ATTEMPT" \
  --idempotency-key "ci-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT-build-failed" \
  --event failure --priority high --title "Build failed" "main @ abc1234: 3 tests failed"
```

An ordinary backup script can choose its own names and run ID:

```sh
meow send --source "Backup service" --workspace "Customer portal · production" --workspace-id "service:portal:prod" \
  --session "Nightly backup · 2026-10-29" --session-id "backup:2026-10-29" \
  --idempotency-key "portal-prod-backup-2026-10-29-completed" \
  --event result --title "Backup completed" "Snapshot stored successfully."
```

Replace the example date/IDs with the actual scheduled occurrence or job ID. A second independent run needs a distinct ID. In the App this reads as `💬 Nightly backup · 2026-10-29`, then `Customer portal · production · Backup service`, followed by the body preview; details also show the timestamp. No need to repeat the project name in the title or body.

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
  --data-binary '{"title":"Build failed","body":"main @ abc1234: 3 tests failed","source":"CI","event":"failure","priority":"high","context":{"workspace":"acme/api","workspace_id":"repo:acme/api","session":"Deploy #42 · attempt 1","session_id":"run:1234:1"}}' \
  "$MEOW_SERVER/v1/notifications"
```

| Field | Values |
| --- | --- |
| `title` | Required, up to 256 characters |
| `body` | Up to 4000 characters; Markdown is rendered in the app |
| `source` | Display label, up to 64 characters |
| `event` | `result`, `failure` or `attention`; optional, never inferred |
| `priority` | `low` (silent), `normal` (default) or `high` (Time Sensitive) |
| `type` | `info` (default) or `alert`. Independent of `event`; `alert` does not set the attention category. |
| `context` | Object with the project/session fields above. Names up to 200 characters, IDs up to 128, single line. |

The request body can be up to 64 KB.

Set `event=result` for new output (not necessarily success), `attention` when someone must act, or `failure` for an execution error. `priority` controls interruption level separately; neither a failure title nor `priority=high` sets the category. Meow only notifies: users handle an incident or decision in the originating service, not through phone approval/reply buttons.

## Responses

| Status | Meaning |
| --- | --- |
| `201` | Accepted. `push_status` in the JSON response is `queued`; it is not proof that the phone showed it. |
| `200` with `Idempotent-Replayed: true` | Same `Idempotency-Key` and same request: nothing new was sent. |
| `401` | Missing, wrong or revoked Device Key, or the wrong server. |
| `422` | Invalid field, or the `Idempotency-Key` was already used for a different request. |
| `429` | Rate limited. Retry later with the same `Idempotency-Key`. |

`Idempotency-Key` is optional: up to 128 non-space printable ASCII characters. Use one value per logical notification, including its phase when a run sends multiple updates, so a retried request isn't shown twice.

Don't let a failed notification fail the build. For example, append `|| echo "Meow notification failed" >&2` to the command.
