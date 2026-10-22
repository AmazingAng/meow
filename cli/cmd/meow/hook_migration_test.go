package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meow.blue/meow/cli/internal/config"
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

func TestCodexHookOnlySetup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")
	original := []byte("# foreign handler\nnotify = [\"helper\", \"original\"]\n")
	os.WriteFile(filepath.Join(dir, "config.toml"), original, 0600)
	foreign := `{"other":true,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-stop"}]}]}}`
	os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(foreign), 0600)
	o := setupOptions{install: true, summary: true, exe: "/opt/meow"}
	changes, notes, err := planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	b, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	if !strings.Contains(string(b), "other-stop") || !strings.Contains(string(b), "codex-stop") || !strings.Contains(string(b), "PermissionRequest") {
		t.Fatalf("hooks %s", b)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(got) != string(original) {
		t.Fatal("foreign notify changed")
	}
	if codexLegacyConfigured() {
		t.Fatal("setup installed a legacy notify")
	}
	if _, observed := codexHookStatus(); observed {
		t.Fatal("configuration claimed hook execution")
	}
	if !strings.Contains(strings.Join(notes, "\n"), "Waiting for hook authorization or first execution") {
		t.Fatal("authorization step missing", notes)
	}
	if err := recordCodexHookRun([]byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop"}`), true); err != nil {
		t.Fatal(err)
	}
	if _, observed := codexHookStatus(); !observed {
		t.Fatal("runtime evidence not found")
	}
	if changes, _, err = planCodex(o); err != nil || len(changes) != 0 {
		t.Fatal("setup not idempotent", err, changes)
	}
	o.install = false
	changes, _, err = planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	b, _ = os.ReadFile(filepath.Join(dir, "hooks.json"))
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil || strings.Contains(string(b), "codex-stop") || !strings.Contains(string(b), "other-stop") {
		t.Fatalf("foreign hooks lost: %s", b)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(got) != string(original) {
		t.Fatal("uninstall changed foreign notify")
	}
}

func TestCodexHookOnlyFreshInstallCreatesNoConfigToml(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	changes, _, err := planCodex(setupOptions{install: true, exe: "/opt/meow"})
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("fresh setup created config.toml", err)
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
	if _, observed := codexHookStatus(); observed {
		t.Fatal("changed hook definition reused stale receipt")
	}
}

func TestDoctorCodexHookStates(t *testing.T) {
	for _, state := range []string{"configured", "observed", "legacy remains", "permission hook missing"} {
		t.Run(state, func(t *testing.T) {
			ts, sent := fakeMeow(t, true)
			dir := wizardEnv(t, "meow_good", "codex")
			if err := config.Save(config.Config{Server: ts.URL, Key: "meow_good"}); err != nil {
				t.Fatal(err)
			}
			captureOutput(t, "", func() {
				if cmdSetup([]string{"codex", "--yes", "--no-test"}) != 0 {
					t.Error("setup failed")
				}
			})
			if state != "configured" {
				if err := recordCodexHookRun([]byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop"}`), true); err != nil {
					t.Fatal(err)
				}
			}
			if state == "legacy remains" {
				os.WriteFile(filepath.Join(dir, "codex", "config.toml"), []byte(`notify = ["meow", "hook", "codex"]`+"\n"), 0600)
			}
			if state == "permission hook missing" {
				path := filepath.Join(dir, "codex", "hooks.json")
				b, _ := os.ReadFile(path)
				var doc map[string]any
				json.Unmarshal(b, &doc)
				delete(doc["hooks"].(map[string]any), "PermissionRequest")
				b, _ = json.Marshal(doc)
				os.WriteFile(path, b, 0600)
			}
			var code int
			out := captureOutput(t, "", func() { code = cmdDoctor(nil) })
			if (code == 0) != (state == "observed") {
				t.Fatalf("state=%s exit=%d output=%s", state, code, out)
			}
			if state == "configured" && !strings.Contains(out, "waiting for hook authorization or first execution") {
				t.Fatal(out)
			}
			if state == "legacy remains" && !strings.Contains(out, "Old Meow notify configuration found") {
				t.Fatal(out)
			}
			if sent() != 0 {
				t.Fatal("doctor or no-test setup sent a notification")
			}
		})
	}
}
