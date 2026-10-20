package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func applyTestPlan(t *testing.T, changes []fileChange) {
	t.Helper()
	for _, c := range changes {
		if c.data == nil {
			if err := os.Remove(c.path); err != nil {
				t.Fatal(err)
			}
		} else {
			os.MkdirAll(filepath.Dir(c.path), 0700)
			if err := os.WriteFile(c.path, c.data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCodexStagedMigration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")
	original := []byte("# foreign handler\nnotify = [\"helper\", \"original\"]\n")
	os.WriteFile(filepath.Join(dir, "config.toml"), original, 0600)
	foreign := `{"other":true,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-stop"}]}]}}`
	os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(foreign), 0600)
	options := setupOptions{install: true, summary: true, exe: "/opt/meow"}
	changes, _, err := planCodex(options)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	b, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if !strings.Contains(string(b), "other-stop") || !strings.Contains(string(b), "codex-stop") || !strings.Contains(string(b), "PermissionRequest") {
		t.Fatalf("hooks %s", b)
	}
	if !codexLegacyActive() {
		t.Fatal("working fallback removed before trust")
	}
	complete := options
	complete.finishMigration = true
	if _, _, err := planCodex(complete); err == nil {
		t.Fatal("migration allowed without runtime evidence")
	}
	input := []byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop"}`)
	if err := recordCodexHookRun(input, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := codexHookStatus(); !ok {
		t.Fatal("runtime evidence not found")
	}
	changed := complete
	changed.summary = false
	if _, _, err := planCodex(changed); err == nil {
		t.Fatal("stale evidence accepted for changed hook")
	}
	changes, _, err = planCodex(complete)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	if codexLegacyActive() {
		t.Fatal("fallback was not retired")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if !strings.Contains(string(data), `notify = ["helper", "original"]`) {
		t.Fatalf("foreign handler lost: %s", data)
	}
	// Ordinary repeated setup must not re-enable a retired fallback.
	changes, _, err = planCodex(options)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	if codexLegacyActive() {
		t.Fatal("reinstall undid migration")
	}
	options.install = false
	changes, _, err = planCodex(options)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	b, _ = os.ReadFile(filepath.Join(dir, "hooks.json"))
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil || strings.Contains(string(b), "codex-stop") || !strings.Contains(string(b), "other-stop") {
		t.Fatalf("uninstall harmed foreign hooks: %s", b)
	}
}

func TestCodexCompletedMigrationSurvivesIdleTime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")
	o := setupOptions{install: true, summary: true, exe: "/opt/meow"}
	changes, _, err := planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	if err := recordCodexHookRun([]byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop"}`), true); err != nil {
		t.Fatal(err)
	}
	o.finishMigration = true
	changes, _, err = planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	var r codexReceipt
	oldReceipt, _ := os.ReadFile(receiptPath(dir))
	json.Unmarshal(oldReceipt, &r)
	r.ObservedAt = time.Now().Add(-48 * time.Hour)
	b, _ := json.Marshal(r)
	os.WriteFile(receiptPath(dir), b, 0600)
	o.finishMigration = false
	changes, _, err = planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	if codexLegacyActive() {
		t.Fatal("idle time resurrected retired fallback")
	}
}

func TestCodexConcurrentReceipts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")
	o := setupOptions{install: true, summary: true, exe: "/opt/meow"}
	changes, _, err := planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	errors := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			errors <- recordCodexHookRun([]byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop"}`), true)
		}()
	}
	for i := 0; i < 20; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if !codexReceiptMatches(dir, codexCommands(o)["Stop"]) {
		t.Fatal("torn receipt")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".meow-receipt-*"))
	if len(files) != 0 {
		t.Fatal("temporary receipt leaked")
	}
}

func TestCodexReceiptInvalidatedByDefinitionChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")
	o := setupOptions{install: true, summary: true, exe: "/opt/meow"}
	changes, _, err := planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	if err := recordCodexHookRun([]byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop"}`), true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hooks.json")
	b, _ := os.ReadFile(path)
	b = []byte(strings.ReplaceAll(string(b), `"async": true`, `"async": true, "timeout": 60`))
	os.WriteFile(path, b, 0600)
	o.finishMigration = true
	if _, _, err := planCodex(o); err == nil {
		t.Fatal("changed hook definition reused stale receipt")
	}
}
