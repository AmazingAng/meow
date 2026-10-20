package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillCommandsUseInstalledExecutable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	executable := filepath.Join(dir, "private tools' bin", "meow")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n[ \"$1\" = ask ] && [ \"$2\" = 'Deploy v1.2.0 to production?' ]\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex"} {
		change, ok, err := skillChange(filepath.Join(dir, agent, "SKILL.md"), agent, setupOptions{install: true, skill: true, exe: executable})
		if err != nil || !ok {
			t.Fatalf("skill plan: %v %v", ok, err)
		}
		text := string(change.data)
		first := strings.SplitN(strings.SplitN(text, "~~~sh\n", 2)[1], "\n~~~", 2)[0]
		command := exec.Command("/bin/sh", "-c", first)
		command.Env = []string{"PATH=/usr/bin:/bin"}
		if out, err := command.CombinedOutput(); err != nil {
			t.Errorf("%s skill cannot run installed executable: %v %s", agent, err, out)
		}
	}
}
func TestClaudeCompletionIdempotencyAfterContinuation(t *testing.T) {
	first := []byte(`{"session_id":"s","prompt_id":"p","stop_hook_active":false,"last_assistant_message":"Initial response"}`)
	final := []byte(`{"session_id":"s","prompt_id":"p","stop_hook_active":true,"last_assistant_message":"Validation finished"}`)
	_, oldKey, _, err := buildHookNotification("claude-stop", first, true)
	if err != nil {
		t.Fatal(err)
	}
	req, key, send, err := buildHookNotification("claude-stop", final, true)
	if err != nil || !send || req.Body != "Validation finished" {
		t.Errorf("final response suppressed: %+v %v %v", req, send, err)
	}
	if key == "" || key == oldKey {
		t.Errorf("distinct completions share an idempotency key: %q", key)
	}
	_, again, _, _ := buildHookNotification("claude-stop", final, true)
	if again != key {
		t.Fatal("same completion is not idempotent")
	}
}

func TestCodexPlanKeepsCustomExecutableAndForeignCommands(t *testing.T) {
	fixtureRoot := t.TempDir()
	t.Setenv("CODEX_HOME", fixtureRoot)
	t.Setenv("MEOW_CONFIG", filepath.Join(fixtureRoot, "meow.json"))
	executable := filepath.Join(fixtureRoot, "phone-push")
	options := setupOptions{install: true, exe: executable}
	changes, _, err := planCodex(options)
	if err != nil || len(changes) != 1 {
		t.Fatalf("initial plan: %v %v", changes, err)
	}
	configPath := filepath.Join(fixtureRoot, "config.toml")
	if err := os.WriteFile(configPath, changes[0].data, 0600); err != nil {
		t.Fatal(err)
	}
	if again, _, err := planCodex(options); err != nil || len(again) != 0 {
		t.Fatalf("custom executable reinstall: %v %v", again, err)
	}
	options.install = false
	changes, _, err = planCodex(options)
	if err != nil {
		t.Fatal(err)
	}
	var removed bool
	for _, change := range changes {
		if change.path == configPath {
			removed = true
			if strings.Contains(string(change.data), "notify") {
				t.Fatal("custom notify was not removed")
			}
		}
	}
	if !removed {
		t.Fatal("custom notify removal was not planned")
	}
	foreign := []byte("notify = [\"/opt/other\", \"hook\", \"codex\"]\n")
	if err := os.WriteFile(configPath, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	options.install = true
	if kept, _, err := planCodex(options); err != nil || len(kept) != 1 || !strings.Contains(string(kept[0].data), "--forward-notify=") {
		t.Fatalf("foreign notify must be preserved by coexistence: %v %v", kept, err)
	}
	options.force = true
	changes, notes, err := planCodex(options)
	if err != nil || len(changes) != 1 || len(notes) == 0 {
		t.Fatalf("forced plan: %v %v %v", changes, notes, err)
	}
}

func TestSetupUninstallNeedsNoDeviceKey(t *testing.T) {
	fixtureRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(fixtureRoot, "claude"))
	t.Setenv("MEOW_CONFIG", filepath.Join(fixtureRoot, "config.json"))
	t.Setenv("MEOW_KEY", "")
	var code int
	output := captureOutput(t, "", func() { code = cmdSetup([]string{"claude", "--uninstall", "--yes"}) })
	if code != 0 || !strings.Contains(output, "Nothing to remove") {
		t.Fatalf("uninstall: exit=%d output=%s", code, output)
	}
}
