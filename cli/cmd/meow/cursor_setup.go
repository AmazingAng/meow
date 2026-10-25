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
			"The notification body is the last assistant reply in that turn's transcript. Aborted turns are ignored. status error is a failure.",
			"Meow does not install a permission hook. Cursor blocks those actions when the hook prints nothing.",
		}
		home, _ := os.UserHomeDir()
		notes = append(notes, skillHint(filepath.Join(home, ".agents", "skills", "meow", "SKILL.md"), "cursor")...)
	}
	return changes, notes, nil
}
