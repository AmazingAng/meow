package main

import (
	"fmt"
	"os"
	"path/filepath"

	"meow.blue/meow/cli/internal/setup"
)

func cursorCommand(o setupOptions) string {
	command := setup.ShellQuote(o.exe) + " hook cursor-stop"
	if !o.summary {
		command += " --no-summary"
	}
	return command
}

func planCursor(o setupOptions) ([]fileChange, []string, error) {
	dir, err := cursorDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "hooks.json")
	old, exists, err := setup.ReadOptional(path)
	if err != nil {
		return nil, nil, err
	}
	command := ""
	if o.install {
		command = cursorCommand(o)
	}
	data, err := setup.CursorHooks(old, command, o.install, o.executables()...)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tilde(path), err)
	}
	var changes []fileChange
	if !setup.EquivalentJSON(old, data) {
		lines := []string{"remove the Meow stop hook"}
		if o.install {
			lines = []string{"stop hook (completed turn, or failure when status is error): " + command}
		}
		changes = append(changes, fileChange{path: path, data: data, exists: exists, lines: lines})
	}
	var notes []string
	if o.install {
		notes = []string{
			"Cursor reloads hooks.json on save. Finish a task and confirm the reminder on your iPhone. The setup test does not run the hook.",
			"Cursor's stop event has no assistant reply, so the notification is a reminder. Aborted turns are ignored. status error is a failure.",
			"Meow does not install a permission hook. Cursor blocks those actions when the hook prints nothing.",
			"Grok can load this same file, but sends its own payload. meow setup grokbot connects Grok. The Cursor adapter ignores that payload.",
		}
		home, _ := os.UserHomeDir()
		notes = append(notes, skillHint(filepath.Join(home, ".agents", "skills", "meow", "SKILL.md"), "cursor")...)
	}
	return changes, notes, nil
}

func grokbotCommands(o setupOptions) map[string]string {
	suffix := ""
	if !o.summary {
		suffix = " --no-summary"
	}
	base := setup.ShellQuote(o.exe) + " hook "
	return map[string]string{
		"Stop":         base + "grokbot-stop" + suffix,
		"StopFailure":  base + "grokbot-failure" + suffix,
		"Notification": base + "grokbot-notify" + suffix,
	}
}

func planGrokbot(o setupOptions) ([]fileChange, []string, error) {
	dir, err := grokDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "hooks", "meow.json")
	old, exists, err := setup.ReadOptional(path)
	if err != nil {
		return nil, nil, err
	}
	commands := grokbotCommands(o)
	data, err := setup.LifecycleHooks(old, commands, o.install, []string{"grokbot-stop", "grokbot-failure", "grokbot-notify"}, o.executables()...)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tilde(path), err)
	}
	var changes []fileChange
	if !setup.EquivalentJSON(old, data) {
		lines := []string{"remove the Meow Stop, StopFailure and Notification hooks"}
		if o.install {
			lines = []string{
				"Stop hook (end_turn with an assistant reply): " + commands["Stop"],
				"StopFailure hook (API error)",
				"Notification hook (permission_prompt and elicitation_dialog): " + commands["Notification"],
			}
		}
		changes = append(changes, fileChange{path: path, data: data, exists: exists, lines: lines})
	}
	var notes []string
	if o.install {
		notes = []string{
			"Grok loads hooks from its hooks directory. Open /hooks and reload, then finish a task and check your iPhone.",
			"Session-end Stop, subagent stops, blank replies and idle_prompt notifications are ignored.",
			"If Cursor is connected too, Grok may also run ~/.cursor/hooks.json. That adapter ignores Grok's payload, so the turn is sent once.",
		}
		home, _ := os.UserHomeDir()
		notes = append(notes, skillHint(filepath.Join(home, ".agents", "skills", "meow", "SKILL.md"), "grok")...)
	}
	return changes, notes, nil
}
