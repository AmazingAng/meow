---
name: meow
description: Install, configure, troubleshoot and use Meow, notifications for AI agents on the user's iPhone. Use when the user wants to set up Meow or the meow CLI, connect Claude Code, Codex, Cursor or Grok to it, fix notifications that don't arrive, self-host a Meow server, or notify the user that an agent finished, failed or needs attention. V1 only delivers notifications; replies and decisions stay in the agent on the computer.
---

# Meow

Meow sends a notification to the user's iPhone. Meow V1 only delivers notifications. Use CLI 0.1.2 or later; older setup commands can overwrite this skill. The user reads them on the phone and responds in the agent on their computer.

- iPhone app: creates the device, shows the server address and the Device Key.
- `meow` CLI (macOS and Linux): sends notifications.
- Server: the iPhone and the CLI must reach the same Meow server. To run one, see [references/server.md](references/server.md).

This skill is installed with `npx skills add AmazingAng/meow -g` (Node.js 22.20 or later). `meow setup` doesn't install or update it. Update it with `npx skills update meow -g`.

Start by checking the current state, then do only the steps that are missing:

```sh
command -v meow || ls ~/.local/bin/meow
meow version && meow doctor
```

If `meow` isn't on PATH but `~/.local/bin/meow` exists, use `~/.local/bin/meow` in place of `meow` in every command below.

`meow doctor` changes nothing. It checks the server, the Device Key, push registration and the connected agents, and prints a next step for each failure. If every line starts with `✓`, go straight to [Use Meow](#use-meow).

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
- To pin a version: download the script, then run `sh install.sh --version 0.1.2 --install-dir "$HOME/.local/bin"`.
- Windows is not supported.

## 2. Connect the CLI to the iPhone

The user needs the Meow iPhone app first. In the app they choose a server and allow notifications. Settings shows:

- the server address (Settings → Server)
- the Device Key (tap the Device Key row in Settings to copy it). It starts with `meow_` and can only send notifications.

Never ask the user to paste the Device Key into the chat. Never put it on a command line, in a file you create, or in your output. `meow` reads it from a prompt so that it stays out of shell history.

You can't type into that prompt for the user. Ask them to open their own terminal window (not a `!` command inside the agent) and run:

```sh
meow setup --server https://THEIR_MEOW_SERVER
```

The app's "install and setup command" does the same thing and also installs the CLI. Setup:

- checks the server
- asks for the Device Key and saves it to `~/.config/meow/config.json` (mode 0600)
- offers to connect each agent it finds, showing the changes and asking before writing
- sends one test notification

If they only want to save the key, `meow login --server URL` prompts for it. When they're done, run `meow doctor` again.

## 3. Connect an agent

If the user connected agents during `meow setup`, skip this step. Otherwise, `meow setup claude`, `meow setup codex`, `meow setup cursor` or `meow setup grokbot` installs hooks so that the user is notified when the agent finishes a turn or is waiting for permission. It needs a saved Device Key.

From an agent, without a terminal, first run the command without `--yes`:

```sh
meow setup claude
```

It prints the planned changes, then exits with "re-run with --yes". Show the plan to the user and get their consent, then apply it:

```sh
meow setup claude --yes
```

- Claude Code: async `Stop`, `StopFailure` and `Notification` hooks in `settings.json` (`$CLAUDE_CONFIG_DIR` or `~/.claude`). Running sessions pick them up.
- Codex: async `Stop` and `PermissionRequest` in `hooks.json` (`$CODEX_HOME` or `~/.codex`). Setup cleans old Meow `notify` entries and restores any wrapped original handler while preserving foreign config. Start a new session, have the user authorize both hooks in `/hooks`, then finish a real task and confirm its result on the iPhone. No separate migration step is needed. Without authorized hooks, automatic Codex notifications are unavailable; manual `meow send` still works. `meow doctor` distinguishes configuration from observed execution, not current trust or phone delivery. The setup test verifies CLI delivery only. Use a Codex version supporting lifecycle hooks.
- Cursor: a `stop` command in `~/.cursor/hooks.json`. Cursor reloads the file on save. `completed` is a result and `error` is a failure; `aborted` is ignored. The stop payload has no assistant text, so the body is the last assistant reply in that turn's transcript. Cursor permission hooks are gates, and an empty reply blocks the action, so Meow does not install one. The command runs in the foreground with a 30 second timeout.
- Grok: `Stop`, `StopFailure` and `Notification` in `$GROK_HOME/hooks/meow.json` or `~/.grok/hooks/meow.json`. Reload with `/hooks`. Stop notifies only when the reason is `end_turn` and the assistant reply is nonblank. StopFailure notifies on an API error. Notification notifies for `permission_prompt` and `elicitation_dialog`. `idle_prompt`, session shutdown and subagent stops are ignored. Grok also loads `~/.cursor/hooks.json`, but that command's adapter ignores Grok's payload so a turn is not sent twice.
- Every edited file is backed up as `<file>.bak.<timestamp>`. Re-running is safe.
- `--no-summary` leaves the agent's last message out of notifications. `--no-test` skips the test notification.
- Setup never touches this skill. It backs up and removes an outdated `meow` skill that an earlier version of `meow setup` wrote. If setup lists that removal, reinstall this skill afterwards with `npx skills add AmazingAng/meow -g`.
- To undo: `meow setup claude --uninstall --yes`. It removes only the hooks Meow added. To remove this skill: `npx skills remove meow -g`.

## 4. Verify

```sh
meow doctor
meow send --source "Claude Code" "Meow is working"
```

Ask the user whether the notification arrived. `sent <id>` only means the server accepted it.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `No server configured` | The user runs `meow setup --server URL`, with the address from the app. |
| Server is not reachable, or the TLS certificate isn't trusted | Follow the message `meow doctor` prints. A LAN or `127.0.0.1` address only works on the same network or VPN. A self-signed certificate is rejected; use a public certificate, such as the one Caddy obtains. |
| The Device Key isn't valid on this server | Wrong server, or the key was rotated in the app. The user runs `meow login` and pastes the current key. |
| The iPhone hasn't registered for notifications | The user opens Meow and allows notifications. In iPhone Settings → Notifications → Meow, notifications must be on. |
| Sent, but nothing appears on the phone | Check Focus. For `high` priority, enable Time Sensitive Notifications for Meow, in Notifications settings and in the Focus's allowed apps. If the user runs the server, it needs push configured (see [references/server.md](references/server.md)). Without push, messages only appear in the app's inbox. |
| Hooks seem silent | They never print anything. Read `hook.log` next to the config (`~/.config/meow/`). `MEOW_HOOK_DISABLED=1` turns them off. |
| `meow` not found after installing | Use `~/.local/bin/meow`, or ask the user to add `~/.local/bin` to PATH. |
| A legacy ask/wait command fails with "notification-only" | Expected in V1. Use `meow send` and ask in the agent. |

`MEOW_SERVER` and `MEOW_KEY` override the config file, and `MEOW_SOURCE` sets the default sender label. Don't set `MEOW_KEY` in shell profiles or files you commit.

## Use Meow

### When the user needs to decide

Ask using the agent's normal computer interface, then send a notification with the question, options and context:

```sh
meow send --alert --priority high --source "Codex" --title "Needs your attention" \
  "Deploy v1.2.0? Options: approve or reject. Tests passed. Respond in the agent on your computer."
```

V1 does not support phone replies or approvals. Do not use the legacy ask/wait commands. Reading a notification never counts as an answer or approval.

### Tell the user without waiting

```sh
meow send --source "Claude Code" --title "Migration paused" "Table orders is locked; retrying in 10 minutes"
echo "Build log tail..." | meow send --title "Build failed" --priority high
```

`--priority` is `low` (silent), `normal` (the default) or `high` (Time Sensitive; use for important alerts). The title can be up to 256 characters, the body up to 4000 and `--source` up to 64.

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
