package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"

	"github.com/pelletier/go-toml/v2"
	"meow.blue/meow/cli/internal/setup"
)

func forwardFlag(t *testing.T, command []string) string {
	t.Helper()
	data, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	return "--forward-notify=" + string(data)
}

func plannedNotify(t *testing.T, original []string) (string, setupOptions) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("# preserve me\nnotify = "+setup.TOMLArray(original)+"\n[tui]\nnotifications = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path, setupOptions{install: true, exe: filepath.Join(dir, "meow")}
}

func notifyArgv(t *testing.T, data []byte) []string {
	t.Helper()
	var doc struct{ Notify []string }
	if err := toml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Notify
}

func TestCodexCoexistenceUpgradeRestoresOriginal(t *testing.T) {
	original := []string{"/Applications/Codex Computer Use.app/helper", "turn-ended", "雪 ' $(touch never)"}
	path, o := plannedNotify(t, original)
	old := []byte("# preserve me\nnotify = " + setup.TOMLArray([]string{o.exe, "hook", "codex", forwardFlag(t, original)}) + "\n[tui]\nnotifications = true\n")
	os.WriteFile(path, old, 0600)
	changes, err := planLegacyCodexCleanup(o)
	if err != nil || len(changes) != 1 {
		t.Fatalf("cleanup: %v %v", changes, err)
	}
	if !reflect.DeepEqual(notifyArgv(t, changes[0].data), original) || !strings.Contains(string(changes[0].data), "# preserve me") || !strings.Contains(string(changes[0].data), "[tui]\nnotifications = true") {
		t.Fatal("original handler or unrelated config lost")
	}
	applyTestPlan(t, changes)
	if again, err := planLegacyCodexCleanup(o); err != nil || len(again) != 0 {
		t.Fatalf("repeat changed foreign config: %v %v", again, err)
	}
}

func TestCodexCoexistenceGeneratedArgumentRoundTrips(t *testing.T) {
	path, o := plannedNotify(t, []string{"helper"})
	property := func(argument string) bool {
		original := []string{"helper", argument}
		old := []byte("notify = " + setup.TOMLArray([]string{o.exe, "hook", "codex", forwardFlag(t, original)}) + "\n")
		if os.WriteFile(path, old, 0600) != nil {
			return false
		}
		changes, err := planLegacyCodexCleanup(o)
		return err == nil && len(changes) == 1 && reflect.DeepEqual(notifyArgv(t, changes[0].data), original)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 50}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCoexistenceKeepsBackupThroughSetup(t *testing.T) {
	ts, _ := fakeMeow(t, true)
	dir := wizardEnv(t, "meow_good", "codex")
	cfgPath := filepath.Join(dir, "codex", "config.toml")
	original := []string{"/opt/Computer Use/helper", "turn-ended"}
	exe := filepath.Join(dir, "bin", "meow")
	old := []byte("notify = " + setup.TOMLArray([]string{exe, "hook", "codex", forwardFlag(t, original)}) + "\n")
	os.WriteFile(cfgPath, old, 0600)
	var code int
	output := captureOutput(t, "", func() { code = run([]string{"setup", "--server", ts.URL, "--yes"}) })
	if code != 0 {
		t.Fatalf("setup failed: %s", output)
	}
	got, _ := os.ReadFile(cfgPath)
	if !reflect.DeepEqual(notifyArgv(t, got), original) {
		t.Fatal("original handler not restored")
	}
	backups, err := filepath.Glob(cfgPath + ".bak.*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup missing: %v %v", backups, err)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil || string(data) != string(old) {
		t.Fatal("backup bytes changed")
	}
	if !strings.Contains(output, "Detected agents: Codex") || !strings.Contains(output, "Waiting for hook authorization or first execution") || !strings.Contains(output, "does not verify agent hooks") {
		t.Fatal("setup status misleading", output)
	}
}

func TestCodexForwardNotifyKeepsArgumentsAndRunsWhenMeowDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_HOOK_DISABLED", "1")
	script := filepath.Join(dir, "Computer Use helper")
	capture := filepath.Join(dir, "arguments")
	t.Setenv("MEOW_TEST_CAPTURE", capture)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$MEOW_TEST_CAPTURE\"\nprintf 'must stay silent'\nprintf 'also silent' >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := `{"type":"unknown-event","text":"中文 ' $(touch never)"}`
	var code int
	output := captureOutput(t, "", func() {
		code = cmdHook([]string{"codex", forwardFlag(t, []string{script, "turn-ended", "a b"}), payload})
	})
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal("original notification never ran", err)
	}
	if !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"), []string{"turn-ended", "a b", payload}) {
		t.Fatalf("argv changed: %q", data)
	}
	if code != 0 || output != "" {
		t.Fatalf("forwarding disturbed agent: %d %q", code, output)
	}
}

func TestCodexForwardFailureDoesNotReviveRetiredMeowNotify(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_HOOK_DISABLED", "")
	t.Setenv("MEOW_KEY", "meow_fixture")
	t.Setenv("MEOW_SERVER", "")
	var sent atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"fixture"}`))
	}))
	defer ts.Close()
	t.Setenv("MEOW_SERVER", ts.URL)
	output := captureOutput(t, "", func() {
		cmdHook([]string{"codex", forwardFlag(t, []string{filepath.Join(dir, "missing executable")}), `{"type":"agent-turn-complete"}`})
	})
	if sent.Load() != 0 || output != "" {
		t.Fatalf("retired notify sent a push: sent=%d output=%q", sent.Load(), output)
	}
}

func TestCodexForwardMeowFailureAndUnrecognizedPayloadStillReachOriginal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_HOOK_DISABLED", "")
	t.Setenv("MEOW_KEY", "meow_fixture")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer ts.Close()
	t.Setenv("MEOW_SERVER", ts.URL)
	script := filepath.Join(dir, "helper")
	capture := filepath.Join(dir, "capture")
	t.Setenv("MEOW_TEST_CAPTURE", capture)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$MEOW_TEST_CAPTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"type":"agent-turn-complete"}`, `{"type":"future-event"}`, `invalid JSON`} {
		if err := os.Remove(capture); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		output := captureOutput(t, "", func() { cmdHook([]string{"codex", forwardFlag(t, []string{script}), payload}) })
		data, err := os.ReadFile(capture)
		if err != nil || string(data) != payload || output != "" {
			t.Fatalf("forwarding lost after Meow failure: %v %q", err, output)
		}
	}
}

func TestCodexForwardTimeoutIsBounded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	forwardNotify(ctx, []string{"/bin/sh", "-c", "exec sleep 60"}, "payload")
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatal("fixture did not exercise the deadline")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("forwarded helper was not bounded")
	}
	// A real nonzero helper exit and a deadline both remain silent to the agent.
	forwardNotify(context.Background(), []string{"/usr/bin/false"}, "payload")
	data, err := os.ReadFile(filepath.Join(dir, "hook.log"))
	if err != nil || !strings.Contains(string(data), "original Codex notify failed") || strings.Contains(string(data), "payload") {
		t.Fatalf("safe failure logging: %v", err)
	}
}

func TestCodexMalformedForwardingNeverExecutes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	for _, args := range [][]string{
		{"codex", "--forward-notify=not-json", "{}"},
		{"codex", "--forward-notify=[]", "{}"},
		{"codex", forwardFlag(t, []string{"/usr/bin/true"}), forwardFlag(t, []string{"/usr/bin/true"}), "{}"},
		{"claude-stop", forwardFlag(t, []string{"/usr/bin/true"}), "{}"},
	} {
		if output := captureOutput(t, "", func() {
			if cmdHook(args) != 0 {
				t.Error("hook returned nonzero")
			}
		}); output != "" {
			t.Fatal("hook printed error", output)
		}
	}
}

func TestCodexUninstallLeavesLaterForeignReplacementAlone(t *testing.T) {
	original := []string{"/opt/other", "turn-ended"}
	path, options := plannedNotify(t, original)
	options.install = false
	changes, err := planLegacyCodexCleanup(options)
	if err != nil || len(changes) != 0 {
		t.Fatalf("uninstall modified a foreign replacement: %v %v", changes, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(notifyArgv(t, data), original) {
		t.Fatal("foreign config changed")
	}
}

func TestCodexMalformedExistingNotifyIsNeverOverwritten(t *testing.T) {
	path, options := plannedNotify(t, []string{"helper"})
	for _, source := range []string{"notify = 1\n", "notify = [\"\"]\n"} {
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		changes, err := planLegacyCodexCleanup(options)
		if err == nil || len(changes) != 0 {
			t.Fatal("invalid existing handler was silently replaced", err)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != source {
			t.Fatal("invalid original config changed")
		}
	}
}

func TestCodexRetiredNotifyNeverSends(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_HOOK_DISABLED", "")
	t.Setenv("MEOW_KEY", "meow_fixture")
	var sent atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"test"}`))
	}))
	defer ts.Close()
	t.Setenv("MEOW_SERVER", ts.URL)
	for _, body := range []string{"Actual result", `{"suggestion":"Next"}`, `{"suggestions":[]}`} {
		payload, _ := json.Marshal(map[string]string{"type": "agent-turn-complete", "last-assistant-message": body})
		out := captureOutput(t, "", func() {
			if cmdHook([]string{"codex", string(payload)}) != 0 {
				t.Error("nonzero")
			}
		})
		if out != "" {
			t.Fatal(out)
		}
	}
	if sent.Load() != 0 {
		t.Fatal("retired notify sent pushes", sent.Load())
	}
}
