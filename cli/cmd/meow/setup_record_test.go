package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meow.blue/meow/cli/internal/config"
)

// A binary not named meow is still found on uninstall, because setup records
// the executable it wrote into each agent's config.
func TestUninstallFindsRecordedExecutable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow", "config.json"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	t.Setenv("MEOW_KEY", "")
	t.Setenv("MEOW_SERVER", "")
	exe := filepath.Join(dir, "bin", "meow-dev")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("MEOW_EXECUTABLE", exe)
	if err := config.Save(config.Config{Server: "http://127.0.0.1:1", Key: "meow_testkey"}); err != nil {
		t.Fatal(err)
	}

	for _, agent := range []string{"claude", "codex"} {
		if code := run([]string{"setup", agent, "--yes", "--no-test"}); code != 0 {
			t.Fatalf("install %s exit %d", agent, code)
		}
	}
	cfg, _ := config.LoadFile()
	if cfg.Installed["claude"] != exe || cfg.Installed["codex"] != exe {
		t.Fatalf("recorded = %v", cfg.Installed)
	}

	// Uninstall from a different binary, as after an upgrade.
	t.Setenv("MEOW_EXECUTABLE", filepath.Join(dir, "bin", "meow"))
	for _, agent := range []string{"claude", "codex"} {
		if code := run([]string{"setup", agent, "--uninstall", "--yes"}); code != 0 {
			t.Fatalf("uninstall %s exit %d", agent, code)
		}
	}
	settings, _ := os.ReadFile(filepath.Join(dir, "claude", "settings.json"))
	toml, _ := os.ReadFile(filepath.Join(dir, "codex", "config.toml"))
	if strings.Contains(string(settings), "meow-dev") || strings.Contains(string(toml), "meow-dev") {
		t.Fatalf("hooks left behind:\n%s\n%s", settings, toml)
	}
	if cfg, _ := config.LoadFile(); len(cfg.Installed) != 0 {
		t.Fatalf("record not cleared: %v", cfg.Installed)
	}
	if cfg, _ := config.LoadFile(); cfg.Key != "meow_testkey" {
		t.Fatal("recording an install changed the saved key")
	}
}
