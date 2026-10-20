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

func TestCodexCoexistenceInstallReinstallAndRestore(t *testing.T) {
	original := []string{"/Applications/Codex Computer Use.app/helper", "turn-ended", "雪 ' $(touch never)"}
	path, options := plannedNotify(t, original)
	changes, _, err := planCodexNotify(options)
	if err != nil {
		t.Fatalf("normal install must preserve existing notify: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes: %+v", changes)
	}
	argv := notifyArgv(t, changes[0].data)
	if !strings.Contains(strings.Join(argv, " "), forwardFlag(t, original)) {
		t.Fatalf("original argv lost: %q", argv)
	}
	if !strings.Contains(string(changes[0].data), "# preserve me") || !strings.Contains(string(changes[0].data), "[tui]\nnotifications = true") {
		t.Fatal("unrelated config was modified")
	}
	if err := os.WriteFile(path, changes[0].data, 0600); err != nil {
		t.Fatal(err)
	}
	if again, _, err := planCodexNotify(options); err != nil || len(again) != 0 {
		t.Fatalf("reinstall must be idempotent: %v %v", again, err)
	}
	options.summary = true
	options.exe = filepath.Join(filepath.Dir(path), "new-tools", "meow")
	changes, _, err = planCodexNotify(options)
	if err != nil || len(changes) != 1 {
		t.Fatalf("upgrade: %v %v", changes, err)
	}
	if strings.Count(strings.Join(notifyArgv(t, changes[0].data), " "), "--forward-notify=") != 1 {
		t.Fatal("notify wrapped twice")
	}
	if err := os.WriteFile(path, changes[0].data, 0600); err != nil {
		t.Fatal(err)
	}
	options.install = false
	changes, _, err = planCodexNotify(options)
	if err != nil || len(changes) != 1 || !reflect.DeepEqual(notifyArgv(t, changes[0].data), original) {
		t.Fatalf("uninstall didn't restore original: %v %v", changes, err)
	}
}

func TestCodexCoexistenceGeneratedArgumentRoundTrips(t *testing.T) {
	path, options := plannedNotify(t, []string{"helper"})
	property := func(argument string) bool {
		original := []string{"helper", argument}
		if os.WriteFile(path, []byte("notify = "+setup.TOMLArray(original)+"\n"), 0600) != nil {
			return false
		}
		changes, _, err := planCodexNotify(options)
		if err != nil || len(changes) != 1 {
			return false
		}
		if os.WriteFile(path, changes[0].data, 0600) != nil {
			return false
		}
		remove := options
		remove.install = false
		changes, _, err = planCodexNotify(remove)
		return err == nil && len(changes) == 1 && reflect.DeepEqual(notifyArgv(t, changes[0].data), original)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 50}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCoexistenceKeepsBackupThroughSetup(t *testing.T) {
	ts, _ := fakeMeow(t, true)
	dir := wizardEnv(t, "meow_good", "codex")
	path := filepath.Join(dir, "codex", "config.toml")
	original := []byte("notify = [\"/opt/Computer Use/helper\", \"turn-ended\"]\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var code int
	output := captureOutput(t, "", func() { code = run([]string{"setup", "--server", ts.URL, "--yes"}) })
	if code != 0 {
		t.Fatalf("wizard failed: %s", output)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents", "skills", "meow", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("setup must leave the Codex skill to skills.sh", err)
	}
	if !strings.Contains(output, "npx skills add AmazingAng/meow -g -a codex") {
		t.Fatal("missing skill hint", output)
	}
	backups, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup missing: %v %v", backups, err)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil || string(data) != string(original) {
		t.Fatal("backup did not preserve original bytes")
	}
	if !strings.Contains(output, "Detected agents: Codex") || !strings.Contains(output, "Not detected: Claude Code") {
		t.Fatal("agent detection is ambiguous", output)
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

func TestCodexForwardFailureDoesNotBlockMeow(t *testing.T) {
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
	if sent.Load() != 1 || output != "" {
		t.Fatalf("Meow notification lost: sent=%d output=%q", sent.Load(), output)
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
	changes, _, err := planCodexNotify(options)
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
		changes, _, err := planCodexNotify(options)
		if err == nil || len(changes) != 0 {
			t.Fatal("invalid existing handler was silently replaced", err)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != source {
			t.Fatal("invalid original config changed")
		}
	}
}

func TestCodexForwardRunsAlongsideMeowDelivery(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_HOOK_DISABLED", "")
	t.Setenv("MEOW_KEY", "meow_fixture")
	signal := filepath.Join(dir, "delivered")
	capture := filepath.Join(dir, "helper-result")
	t.Setenv("MEOW_TEST_SIGNAL", signal)
	t.Setenv("MEOW_TEST_CAPTURE", capture)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(signal, []byte("delivered"), 0600); err != nil {
			t.Error(err)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"fixture"}`))
	}))
	defer ts.Close()
	t.Setenv("MEOW_SERVER", ts.URL)
	helper := filepath.Join(dir, "wait-for-meow")
	script := "#!/bin/sh\ni=0\nwhile [ ! -f \"$MEOW_TEST_SIGNAL\" ] && [ \"$i\" -lt 200 ]; do i=$((i+1)); /bin/sleep 0.01; done\nif [ -f \"$MEOW_TEST_SIGNAL\" ]; then printf yes; else printf late; fi > \"$MEOW_TEST_CAPTURE\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	output := captureOutput(t, "", func() { cmdHook([]string{"codex", forwardFlag(t, []string{helper}), `{"type":"agent-turn-complete"}`}) })
	result, err := os.ReadFile(capture)
	if err != nil || string(result) != "yes" || output != "" {
		t.Fatalf("handlers did not run independently: %q %v", result, err)
	}
	// Config/credential failure must still leave the original handler functioning.
	t.Setenv("MEOW_KEY", "")
	if err := os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	if output := captureOutput(t, "", func() { cmdHook([]string{"codex", forwardFlag(t, []string{helper}), `{"type":"agent-turn-complete"}`}) }); output != "" {
		t.Fatal(output)
	}
	if result, err := os.ReadFile(capture); err != nil || string(result) != "yes" {
		t.Fatal("missing Meow key suppressed original handler", err)
	}
}
