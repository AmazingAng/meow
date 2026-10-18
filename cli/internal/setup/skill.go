package setup

import (
	"bytes"
	"fmt"
	"strings"
)

const skillMarker = "meow-skill:"

// IsMeowSkill reports whether a SKILL.md was written by meow setup.
func IsMeowSkill(b []byte) bool { return bytes.Contains(b, []byte(skillMarker)) }

// Skill returns the SKILL.md that teaches an agent to reach the user with
// meow. agent is "claude" or "codex".
func Skill(agent string, executable ...string) []byte {
	source, waitNote := "Codex",
		"If your shell tool has a time limit, keep §--timeout§ below it."
	if agent == "claude" {
		source, waitNote = "Claude Code",
			"Run it with the Bash tool's maximum timeout (600000 ms) and keep §--timeout§ at 540 or less."
	}
	command := "meow"
	if len(executable) > 0 && executable[0] != "" {
		command = ShellQuote(executable[0])
	}
	s := fmt.Sprintf(skillTemplate, source, waitNote, command)
	return []byte(strings.ReplaceAll(s, "§", "`"))
}

// The template uses § for backticks, which a Go raw string cannot contain.
const skillTemplate = `---
name: meow
description: Reach the user on their iPhone with Meow. Use when you need the user's decision to continue (approve a risky step such as a deploy or a destructive command, choose between options, answer a question) and they may be away from the terminal, or to tell them about an important milestone or failure in the middle of a task.
---
<!-- meow-skill: installed by "meow setup"; "meow setup <agent> --uninstall" removes it -->

# Meow

Meow pushes a notification to the user's iPhone. They can answer from the lock screen, and the answer comes back to your command.

## Ask and wait for the answer

~~~sh
%[3]s ask "Deploy v1.2.0 to production?" --choices approve,reject \
  --body "services/api, all tests passed" --source "%[1]s" --timeout 540
~~~

- The command blocks until the user answers, then prints the answer on stdout.
- Exit codes: §0§ answered, §3§ rejected, §4§ expired without an answer, §5§ cancelled, §1§ error.
- Leave out §--choices§ for a free-text answer. It is printed exactly as the user typed it.
- §--choices approve,reject§ shows Approve and Reject buttons on the notification. Other choices (2 to 8 short labels) are picked in the app.
- %[2]s If the command is cut off before the user answers, the question stays open: run §%[3]s wait <question-id>§ (the ID is printed on stderr) to keep waiting.

## Rules

- Ask only when you need the user. Decide routine things yourself.
- Keep the question short (under 256 characters) and put the context the user needs in §--body§.
- Rejected (3), expired (4) and cancelled (5) all mean no: do not take the action. Never treat silence as approval.
- Ask one question at a time, and wait for its answer before asking another.
- Never put secrets, tokens or passwords in the question or the body.

## Tell the user without waiting

~~~sh
%[3]s send --source "%[1]s" --title "Migration paused" "Table orders is locked; retrying in 10 minutes"
~~~

A notification is already sent automatically when you finish a task, so use §%[3]s send§ only for news the user should not miss in the middle of one.
`
