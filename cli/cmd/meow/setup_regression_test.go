package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupLeavesSkillsToSkillsSh(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	skill := filepath.Join(dir, "claude", "skills", "meow", "SKILL.md")
	options := setupOptions{install: true, summary: true, exe: filepath.Join(dir, "meow")}

	changes, notes, err := planClaude(options)
	if err != nil || len(changes) != 1 || changes[0].path == skill {
		t.Fatalf("fresh install must only add hooks: %+v %v", changes, err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "npx skills add AmazingAng/meow -g -a claude-code") {
		t.Fatalf("missing skill hint: %v", notes)
	}

	// An earlier meow setup wrote a marked skill: installing or uninstalling removes it.
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("---\nname: meow\n---\n<!-- meow-skill: installed by \"meow setup\" -->\nmeow ask\n")
	if err := os.WriteFile(skill, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, install := range []bool{true, false} {
		options.install = install
		changes, _, err := planClaude(options)
		removed := false
		for _, c := range changes {
			removed = removed || (c.path == skill && c.data == nil)
		}
		if err != nil || !removed {
			t.Fatalf("install=%v: legacy skill not removed: %+v %v", install, changes, err)
		}
	}

	// The skills.sh copy has no marker: setup never touches it and stops hinting.
	if err := os.WriteFile(skill, []byte("---\nname: meow\ndescription: Meow\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, install := range []bool{true, false} {
		options.install = install
		changes, notes, err := planClaude(options)
		for _, c := range changes {
			if c.path == skill {
				t.Fatalf("install=%v touched the skills.sh skill", install)
			}
		}
		if err != nil || strings.Contains(strings.Join(notes, "\n"), "npx skills") {
			t.Fatalf("install=%v: %v %v", install, notes, err)
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

func TestCodexCleanupFindsRecordedCustomExecutable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`notify = ["/old/phone-push", "hook", "codex"]`+"\n"), 0600)
	o := setupOptions{install: true, exe: "/new/meow", known: []string{"/old/phone-push"}}
	changes, err := planLegacyCodexCleanup(o)
	if err != nil || len(changes) != 1 || strings.Contains(string(changes[0].data), "notify") {
		t.Fatalf("cleanup: %v %v", changes, err)
	}
	foreign := []byte(`notify = ["/opt/other", "hook", "codex"]` + "\n")
	os.WriteFile(path, foreign, 0600)
	changes, err = planLegacyCodexCleanup(o)
	if err != nil || len(changes) != 0 {
		t.Fatalf("foreign notify touched: %v %v", changes, err)
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

func TestAlreadyConnectedSetupStillSuggestsMissingSkill(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
			t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
			t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
			t.Setenv("MEOW_KEY", "meow_testkey")
			t.Setenv("MEOW_SERVER", "https://setup.invalid")
			exe := filepath.Join(dir, "bin", "meow")
			if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MEOW_EXECUTABLE", exe)
			setup := func() string {
				return captureOutput(t, "", func() {
					if code := run([]string{"setup", agent, "--yes", "--no-test", "--no-skill"}); code != 0 {
						t.Errorf("setup exit=%d", code)
					}
				})
			}
			setup()
			out := setup()
			if !(strings.Contains(out, "already connected") || strings.Contains(out, "configuration is unchanged")) || !strings.Contains(out, "npx skills add AmazingAng/meow") {
				t.Fatalf("reinstall lost the missing-skill hint: %s", out)
			}
			skill := filepath.Join(dir, "claude", "skills", "meow", "SKILL.md")
			if agent == "codex" {
				skill = filepath.Join(dir, ".agents", "skills", "meow", "SKILL.md")
			}
			if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
				t.Fatal(err)
			}
			data := []byte("---\nname: meow\ndescription: Skills-managed Meow\n---\n")
			if err := os.WriteFile(skill, data, 0600); err != nil {
				t.Fatal(err)
			}
			if out := setup(); strings.Contains(out, "npx skills add") {
				t.Fatalf("installed skill still gets a missing-skill hint: %s", out)
			}
			if got, err := os.ReadFile(skill); err != nil || string(got) != string(data) {
				t.Fatalf("skills-managed content changed: %q %v", got, err)
			}
		})
	}
}
