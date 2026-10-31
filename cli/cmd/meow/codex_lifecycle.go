package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/cli/internal/setup"
)

const codexInputMatcher = `^(functions\.)?request_user_input(_async)?$`

func codexCommands(o setupOptions) map[string]string {
	suffix := ""
	if !o.summary {
		suffix = " --no-summary"
	}
	base := setup.ShellQuote(o.exe) + " hook "
	return map[string]string{"Stop": base + "codex-stop" + suffix, "PermissionRequest": base + "codex-permission" + suffix, "PreToolUse": base + "codex-input" + suffix}
}

func planCodex(o setupOptions) ([]fileChange, []string, error) {
	dir, err := codexDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "hooks.json")
	old, exists, err := setup.ReadOptional(path)
	if err != nil {
		return nil, nil, err
	}
	commands := codexCommands(o)
	data, err := setup.LifecycleHooksMatching(old, commands, map[string]string{"PreToolUse": codexInputMatcher}, o.install, []string{"codex-stop", "codex-permission", "codex-input"}, o.executables()...)
	if err != nil {
		return nil, nil, fmt.Errorf("hooks.json: %w", err)
	}
	verified := codexReceiptMatches(dir, commands["Stop"]) && containsCommand(old, "Stop", commands["Stop"]) && setup.EquivalentJSON(old, data)
	changes, err := planLegacyCodexCleanup(o)
	if err != nil {
		return nil, nil, err
	}
	// Write hooks before cleaning the old entry. Both edits are backed up and
	// atomic; repeating setup completes an interrupted upgrade.
	if !setup.EquivalentJSON(old, data) {
		change := fileChange{path: path, data: data, exists: exists, lines: []string{"merge Meow Stop, PermissionRequest and user-input-only PreToolUse async hooks; preserve other handlers"}}
		changes = append([]fileChange{change}, changes...)
	}
	var notes []string
	if o.install {
		notes = []string{"Codex uses Meow lifecycle hooks. Start a new Codex session to load the configuration."}
		if verified {
			notes = append(notes, "A Stop hook run was previously observed. Check /hooks for current authorization and confirm a real task result arrives on your iPhone.")
		} else {
			notes = append(notes, "Next: open /hooks in Codex and authorize all three Meow hooks, including the user-input hook, then finish a normal task and check its result on your iPhone.",
				"Waiting for hook authorization or first execution. Automatic task notifications require authorized hooks; use a Codex version with lifecycle hook support.")
		}
		notes = append(notes, "The setup test checks CLI-to-server delivery only; it does not verify Codex hooks.")
		home, _ := os.UserHomeDir()
		notes = append(notes, skillHint(filepath.Join(home, ".agents", "skills", "meow", "SKILL.md"), "codex")...)
	}
	return changes, notes, nil
}

// Do not inspect undocumented Codex trust databases. An execution receipt proves
// only that this exact Stop command was invoked, not ongoing trust or all hosts.
type codexReceipt struct {
	CommandHash string    `json:"command_hash"`
	HooksHash   string    `json:"hooks_hash"`
	ObservedAt  time.Time `json:"observed_at"`
}

func commandHash(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}
func receiptPath(dir string) string { return filepath.Join(dir, ".meow-hook-receipt.json") }
func containsCommand(data []byte, event, command string, matcher ...string) bool {
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Type    string `json:"type"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	for _, g := range doc.Hooks[event] {
		if len(matcher) > 0 && g.Matcher != matcher[0] {
			continue
		}
		for _, h := range g.Hooks {
			if h.Command == command && h.Type == "command" && h.Async {
				return true
			}
		}
	}
	return false
}
func codexReceiptMatches(dir, command string) bool {
	b, err := os.ReadFile(receiptPath(dir))
	if err != nil {
		return false
	}
	hooks, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		return false
	}
	var r codexReceipt
	return json.Unmarshal(b, &r) == nil && r.CommandHash == commandHash(command) && r.HooksHash == commandHash(string(hooks)) && !r.ObservedAt.IsZero() && time.Since(r.ObservedAt) >= 0
}
func recordCodexHookRun(input []byte, summary bool) error {
	var event codexHookInput
	if json.Unmarshal(input, &event) != nil || event.Event != "Stop" || event.SessionID == "" || event.TurnID == "" {
		return fmt.Errorf("invalid event")
	}
	dir, err := codexDir()
	if err != nil {
		return err
	}
	exe, err := meowExecutable()
	if err != nil {
		return err
	}
	command := codexCommands(setupOptions{exe: exe, summary: summary})["Stop"]
	b, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		return err
	}
	if !containsCommand(b, "Stop", command) {
		return fmt.Errorf("executed command does not match installed hooks")
	}
	data, _ := json.Marshal(codexReceipt{CommandHash: commandHash(command), HooksHash: commandHash(string(b)), ObservedAt: time.Now().UTC()})
	// Unique temporary files avoid collisions between parallel sessions.
	f, err := os.CreateTemp(dir, ".meow-receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), receiptPath(dir))
}
func codexLegacyConfigured() bool {
	dir, err := codexDir()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		return false
	}
	cfg, _ := config.Load()
	exe, _ := meowExecutable()
	_, ours := setup.CodexNotify(b, exe, cfg.Installed["codex"])
	return ours
}
func codexHookStatus() (string, bool) {
	dir, err := codexDir()
	if err != nil {
		return "Codex hook state unavailable", false
	}
	cfg, _ := config.Load()
	exe := cfg.Installed["codex"]
	if exe == "" {
		exe, _ = meowExecutable()
	}
	b, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if err != nil {
		return "Codex lifecycle hooks missing; run meow setup codex", false
	}
	for _, summary := range []bool{true, false} {
		commands := codexCommands(setupOptions{exe: exe, summary: summary})
		command := commands["Stop"]
		if !containsCommand(b, "PreToolUse", commands["PreToolUse"], codexInputMatcher) {
			continue
		}
		if containsCommand(b, "Stop", command) && containsCommand(b, "PermissionRequest", commands["PermissionRequest"]) && codexReceiptMatches(dir, command) {
			return "Codex Stop hook execution previously observed; current trust cannot be verified (check /hooks)", true
		}
	}
	commands := codexCommands(setupOptions{exe: exe, summary: true})
	quietCommands := codexCommands(setupOptions{exe: exe, summary: false})
	if !containsCommand(b, "PreToolUse", commands["PreToolUse"], codexInputMatcher) && !containsCommand(b, "PreToolUse", quietCommands["PreToolUse"], codexInputMatcher) {
		return "Codex user-input hook missing or outdated; run meow setup codex, restart the session and authorize the Meow hooks in /hooks", false
	}
	return "Codex waiting for hook authorization or first execution; open /hooks, authorize all three Meow hooks, then finish a normal task", false
}
