---
name: meow
description: Install, configure, troubleshoot and use Meow, notifications for AI agents on the user's iPhone. Use when the user wants to set up Meow or the meow CLI, connect Claude Code, Codex, Cursor or another agent to it, send from a script, CI job or build service, fix notifications that don't arrive, self-host a Meow server, or notify the user that an agent finished, failed or needs attention. V1 only delivers notifications; replies and decisions stay in the agent on the computer.
---

# Meow

Meow sends a notification to the user's iPhone. Meow V1 only delivers notifications: the user reads them on the phone and responds in the agent on their computer.

Use CLI 0.1.7 or later. Older setup commands can overwrite this skill, and `meow setup cursor` needs 0.1.7.

- iPhone app: creates the device, shows the server address and the Device Key.
- `meow` CLI (macOS and Linux): sends notifications.
- Server: most users use the official hosted server, `https://api.meow.blue`, which the app suggests on first launch. It needs no setup on the user's side. The iPhone and the CLI must use the same server. Pass the address on initial setup/login; later sends use the saved configuration or `MEOW_SERVER`. Notification content is stored on that server, so never send secrets. To host one instead, see [references/server.md](references/server.md).
- Scripts, CI jobs and services can send over HTTP or with the CLI; see [references/http.md](references/http.md).

This skill is installed with `npx skills add AmazingAng/meow -g` (Node.js 22.20 or later). `meow setup` doesn't install or update it. Update it with `npx skills update meow -g`.

Start by checking the current state, then do only the steps that are missing:

```sh
command -v meow || ls ~/.local/bin/meow
meow version
```

If `meow` isn't on PATH but `~/.local/bin/meow` exists, use `~/.local/bin/meow` in place of `meow` in every command below.

Check the version before proceeding: if it is older than 0.1.7, upgrade using step 1 even if notifications already work. A custom/dev version string does not establish compatibility; check its provenance rather than assuming it is the latest release.

Then run `meow doctor`. It changes nothing and checks the server, the Device Key, push registration and the connected agents, but does not enforce the minimum CLI version. If the version is suitable and every check passes, go straight to [Use Meow](#use-meow).

## 1. Install the CLI

You don't need sudo, Go or a GitHub account. Pick one method:

```sh
# macOS with Homebrew
brew install amazingang/tap/meow

# macOS or Linux: verifies SHA-256 and installs into ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/AmazingAng/meow/main/scripts/install.sh | sh
```

- If `~/.local/bin` is not on PATH, the installer prints the `export PATH=...` line. Suggest it to the user; don't edit their shell profile without asking.
- Don't pass `--server` to the installer from an agent. With `--server` it starts interactive setup, which needs a terminal.
- To pin a version: download the script, then run `sh install.sh --version 0.1.7 --install-dir "$HOME/.local/bin"`.
- Windows is not supported.

To upgrade an existing Homebrew installation, use `brew update && brew upgrade amazingang/tap/meow`. For a direct installation, re-run the installer above without `--server`; it replaces the binary without changing saved credentials or hooks. Afterwards check `command -v meow` and `meow version` again. If PATH still selects another copy, use the upgraded binary's absolute path. Do not re-run setup or prompt for a Device Key merely to upgrade; only re-run an agent's setup when its hook configuration needs updating (for example Cursor's two-hook integration).

## 2. Connect the CLI to the iPhone

The user needs the Meow iPhone app first. In the app they choose a server and allow notifications. Settings shows:

- the server address (Settings → Server). This is usually `https://api.meow.blue`; if it shows another address, the user chose a self-hosted server and you use that one.
- the Device Key (tap the Device Key row in Settings to copy it). It starts with `meow_` and can only send notifications.

Never ask the user to paste the Device Key into the chat. Never put it on a command line, in a file you create, or in your output. `meow` reads it from a prompt so that it stays out of shell history.

You can't type into that prompt for the user. Ask them to open their own terminal window (not a `!` command inside the agent) and run:

```sh
meow setup --server https://api.meow.blue
```

For a self-hosted server, replace the address with the one the app shows.

The app's "install and setup command" does the same thing and also installs the CLI. Setup:

- checks the server
- asks for the Device Key and saves it to `~/.config/meow/config.json` (mode 0600)
- offers to connect each agent it finds, showing the changes and asking before writing
- sends one test notification

If they only want to save the key, `meow login --server URL` prompts for it. When they're done, run `meow doctor` again.

## 3. Connect an agent

If the user connected agents during `meow setup`, skip this step. Otherwise, `meow setup claude`, `meow setup codex` or `meow setup cursor` installs hooks so that the user is notified when the agent finishes a turn or is waiting for permission. It needs a saved Device Key.

From an agent, without a terminal, first run the command without `--yes`:

```sh
meow setup claude
```

It prints the planned changes, then exits with "re-run with --yes". Show the plan to the user and get their consent, then apply it:

```sh
meow setup claude --yes
```

- Claude Code: async `Stop`, `StopFailure` and `Notification` hooks in `settings.json` (`$CLAUDE_CONFIG_DIR` or `~/.claude`). Running sessions pick them up.
- Codex: async `Stop`, `PermissionRequest` and an input-only `PreToolUse` in `hooks.json` (`$CODEX_HOME` or `~/.codex`). Setup cleans old Meow `notify` entries and restores any wrapped original handler while preserving foreign config. After upgrading to a CLI with the input hook, re-run `meow setup codex`, start a new session and have the user authorize all three hooks in `/hooks`. Both `request_user_input` and desktop `request_user_input_async` can then remind before an answer. Without authorized hooks, automatic Codex notifications are unavailable; manual `meow send` still works. `meow doctor` distinguishes configuration from observed execution, not current trust or phone delivery. The setup test verifies CLI delivery only. Use a Codex version supporting lifecycle hook coverage for those tools.
- Cursor: `afterAgentResponse` and `stop` hooks in `~/.cursor/hooks.json`; Cursor reloads the file on save. A completed turn sends a result with the reply text, an error sends a failure, and an aborted turn sends nothing. If no reply was captured, the notification is a generic reminder. Meow installs no permission hook, because Cursor treats those as gates that block the action. Re-run `meow setup cursor` after upgrading the CLI.
- Every edited file is backed up as `<file>.bak.<timestamp>`. Re-running is safe.
- `--no-summary` leaves the agent's last message out of notifications. `--no-test` skips the test notification.
- Setup never touches this skill. It backs up and removes an outdated `meow` skill that an earlier version of `meow setup` wrote. If setup lists that removal, reinstall this skill afterwards with `npx skills add AmazingAng/meow -g`.
- To undo: `meow setup claude --uninstall --yes`. It removes only the hooks Meow added. To remove this skill: `npx skills remove meow -g`.

### Other agents

`meow setup` supports only `claude`, `codex` and `cursor`. Don't hand-write hook configuration for another agent. Without hooks there are no automatic reminders, so send them yourself with `meow send --source "AGENT_NAME"` when a task finishes, fails or needs the user (see [Use Meow](#use-meow)). To be notified when a long command ends, chain a send after it:

```sh
(
  meow_task_status=0
  make test || meow_task_status=$?
  meow_task_event=result
  if [ "$meow_task_status" -ne 0 ]; then meow_task_event=failure; fi
  meow send --source "AGENT_NAME" --event "$meow_task_event" --title "make test exited with $meow_task_status" \
    "Details are in the agent on your computer." || printf '%s\n' 'Meow notification failed' >&2
  exit "$meow_task_status"
)
```

This preserves the task's exit status even with `set -e` or a failed notification. The subshell confines the status variable and `exit` to this command.

## 4. Verify

```sh
meow doctor
meow send --source "Claude Code" --event result --session "Connection test" "Meow is working"
```

Ask the user whether the notification arrived. `sent <id>` only means the server accepted it.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `No server configured` | The CLI has no default server. The user runs `meow setup --server https://api.meow.blue`, or the address the app shows. |
| Server is not reachable, or the TLS certificate isn't trusted | Follow the message `meow doctor` prints. A LAN or `127.0.0.1` address only works on the same network or VPN. A self-signed certificate is rejected; use a public certificate, such as the one Caddy obtains. |
| The Device Key isn't valid on this server | Wrong server, or the key was rotated in the app. The user runs `meow login` and pastes the current key. |
| The iPhone hasn't registered for notifications | The user opens Meow and allows notifications. In iPhone Settings → Notifications → Meow, notifications must be on. |
| Sent, but nothing appears on the phone | Check Focus. For `high` priority, enable Time Sensitive Notifications for Meow, in Notifications settings and in the Focus's allowed apps. If the user runs the server, it needs push configured (see [references/server.md](references/server.md)). Without push, messages only appear in the app's inbox. |
| Hooks seem silent | They never print anything. Read `hook.log` next to the config (`~/.config/meow/`). `MEOW_HOOK_DISABLED=1` turns them off. |
| `meow` not found after installing | Use `~/.local/bin/meow`, or ask the user to add `~/.local/bin` to PATH. |
| A legacy ask/wait command fails with "notification-only" | Expected in V1. Use `meow send` and ask in the agent. |

`MEOW_SERVER` and `MEOW_KEY` override the config file, and `MEOW_SOURCE` sets the default sender label. Don't set `MEOW_KEY` in shell profiles or files you commit; in CI, use the platform's secret store ([references/http.md](references/http.md)).

## Use Meow

### When the user needs to decide

For an input path without a verified automatic reminder, send the question, options and context **before** invoking the input tool or waiting for the answer. Sending after a blocking question returns is too late. Then ask using the agent's normal computer interface. An authorized Codex input hook already sends this reminder for `request_user_input` and `request_user_input_async`; do not duplicate it with a manual send.

```sh
meow send --alert --priority high --event attention --source "Codex" --title "Needs your attention" \
  "Deploy v1.2.0? Options: approve or reject. Tests passed. Respond in the agent on your computer."
```

`--event attention` supplies the semantic category used by the app's category icons and group attention indicator. `--alert` independently sets `type=alert`; it does not set `event`. Neither a title nor `--priority high` supplies a category. V1 does not support phone replies or approvals. Do not use the legacy ask/wait commands. Reading a notification never counts as an answer or approval.

### Tell the user without waiting

```sh
meow send --source "Claude Code" --event result --title "Migration paused" "Table orders is locked; retrying in 10 minutes"
echo "Build log tail..." | meow send --source "Claude Code" --event failure --title "Build failed" --priority high
```

`--priority` is `low` (silent), `normal` (the default) or `high` (Time Sensitive; use for important alerts). The title can be up to 256 characters, the body up to 4000 and `--source` up to 64.

Use `--event result` for new output (not necessarily success), `attention` when the user needs to act, and `failure` for an execution error. Include known project/session context below on real agent sends; these short examples omit it for readability.

### Project and session

The app groups notifications by agent, project and session. Pass the same values the hooks use, so a manual send joins the same group:

```sh
meow send --source "Claude Code" --event result \
  --workspace "$(basename "$PWD")" \
  --workspace-id "$(printf '%s' "$PWD" | shasum -a 256 | cut -c1-32)" \
  --session-id "$CLAUDE_CODE_SESSION_ID" \
  --title "Migration done" "Applied 3 migrations to staging"
```

- `--source`: exactly `Claude Code`, `Codex` or `Cursor` for those agents. Another agent uses its own name, the same every time.
- `--workspace` and `--workspace-id`: run from the project root, the directory the session started in. The ID is the first 32 hex characters of the SHA-256 of that absolute path, as the hooks compute it. On Linux, use `sha256sum` in place of `shasum -a 256`.
- `--session-id`: Claude Code provides it as `$CLAUDE_CODE_SESSION_ID`; an empty value is left out. For other agents, leave it out. Never make one up: a wrong ID puts the notification in a separate group.
- `--session` is only a display name. Leave it out unless you know it.
- `MEOW_WORKSPACE`, `MEOW_WORKSPACE_ID`, `MEOW_SESSION` and `MEOW_SESSION_ID` set the same fields.

For non-agent apps, CI, backups and monitors, read [service context examples](references/http.md#project-and-session-for-services). `--workspace` / `context.workspace` is the project name; `--session` / `context.session` is the task, run or incident name. Services choose their own stable IDs; they do not need an agent session or Meow hooks.

### Rules

- Ask only when you need the user: a risky or irreversible step, a real choice, or missing information. Decide routine things yourself.
- Keep the question short and include its options and necessary context in the message passed to `meow send`.
- Wait for an explicit answer in the agent on the computer. Never treat silence or notification delivery as approval.
- Ask one question at a time, and wait for its answer before asking another.
- Never put secrets, tokens or passwords in a question or a notification.
- Hooks provide a neutral “New result” reminder when a turn ends. Use `meow send` for a concrete result, an important development, or a decision that needs context, including at the end of a task when useful. Include the full result/context, not just “done”.
- V1 does not deduplicate explicit sends against Stop reminders. Do not claim sending will suppress the automatic reminder, and do not use an unsupported `--progress` flag.
- If an explicit send fails, mention it briefly in the conversation and continue; do not repeatedly retry or create a notification loop. Hooks already perform bounded transient retries.
- Bodies retain up to 4000 Unicode scalars and newlines; banner previews are shorter. Set `MEOW_LANGUAGE=zh` in the agent launch environment for Chinese automatic titles (English fallback).
