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

func codexCommands(o setupOptions) map[string]string {
	suffix := ""
	if !o.summary {
		suffix = " --no-summary"
	}
	base := setup.ShellQuote(o.exe) + " hook "
	return map[string]string{"Stop": base + "codex-stop" + suffix, "PermissionRequest": base + "codex-permission" + suffix}
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
	data, err := setup.LifecycleHooks(old, commands, o.install, []string{"codex-stop", "codex-permission"}, o.executables()...)
	if err != nil {
		return nil, nil, fmt.Errorf("hooks.json: %w", err)
	}
	verified := codexReceiptMatches(dir, commands["Stop"]) && containsCommand(old, "Stop", commands["Stop"]) && setup.EquivalentJSON(old, data)
	if o.finishMigration && (!o.install || !verified) {
		return nil, nil, fmt.Errorf("migration requires the current Stop hook to run first; trust it in Codex /hooks, finish a turn, then retry")
	}
	legacy := o
	// A previously completed migration stays completed on repeated setup. Changed
	// definitions lack matching evidence and get the fallback again.
	if o.install && (o.finishMigration || (verified && !codexLegacyActive())) {
		legacy.install = false
	}
	changes, notes, err := planCodexNotify(legacy)
	if err != nil {
		return nil, nil, err
	}
	// Install hooks before removing a fallback. Config writes themselves are atomic
	// and backed up; an interrupted install leaves a recoverable staged state.
	if !setup.EquivalentJSON(old, data) {
		change := fileChange{path: path, data: data, exists: exists, lines: []string{"merge Meow Stop and PermissionRequest async hooks; preserve other handlers"}}
		changes = append([]fileChange{change}, changes...)
	}
	if o.install {
		notes = []string{"Start a new Codex session. Open /hooks to review and trust the Meow hooks; changed commands require trust again."}
		if legacy.install {
			notes = append(notes, "Legacy notify remains active. After a Stop hook runs, use meow setup codex --finish-migration to switch; keep the same --no-summary setting if used.")
		} else {
			notes = append(notes, "Legacy Meow notify is retired; any original notify handler is restored. Older Codex clients without lifecycle hooks will not send Meow notifications.")
		}
		if !verified {
			notes = append(notes, "Hook execution/trust is unverified. meow doctor reports observed execution, not the current Codex trust decision.")
		}
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
func containsCommand(data []byte, event, command string) bool {
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
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
func codexLegacyActive() bool {
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
		command := codexCommands(setupOptions{exe: exe, summary: summary})["Stop"]
		if containsCommand(b, "Stop", command) && codexReceiptMatches(dir, command) {
			return "Codex Stop hook execution previously observed; current trust cannot be verified (check /hooks)", true
		}
	}
	return "Codex lifecycle execution/trust unverified; check /hooks, then finish a turn", false
}
