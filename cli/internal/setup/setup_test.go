package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	stopCmd   = "'/usr/local/bin/meow' hook claude-stop"
	notifyCmd = "'/usr/local/bin/meow' hook claude-notify"
)

func decodeSettings(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON %s: %v", b, err)
	}
	return v
}

func countMeow(t *testing.T, b []byte) int {
	t.Helper()
	return strings.Count(string(b), "hook claude-")
}

func TestClaudeSettingsInstallKeepsOtherSettings(t *testing.T) {
	src := []byte(`{
  "model": "opus",
  "permissions": {"allow": ["Bash(npm test)"]},
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "say done"}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "guard.sh", "timeout": 30}]}]
  },
  "cleanupPeriodDays": 30
}
`)
	out, err := ClaudeSettings(src, stopCmd, notifyCmd, true)
	if err != nil {
		t.Fatal(err)
	}
	v := decodeSettings(t, out)
	if v["model"] != "opus" || v["cleanupPeriodDays"] != float64(30) {
		t.Fatalf("other settings changed: %s", out)
	}
	// Untouched values keep their original bytes and key order.
	if !strings.Contains(string(out), `"permissions": {"allow": ["Bash(npm test)"]}`) {
		t.Fatalf("permissions reformatted: %s", out)
	}
	if i, j := strings.Index(string(out), `"model"`), strings.Index(string(out), `"cleanupPeriodDays"`); i > j {
		t.Fatal("key order changed")
	}
	hooks := v["hooks"].(map[string]any)
	stop := hooks["Stop"].([]any)
	if len(stop) != 2 || !strings.Contains(string(out), "say done") || !strings.Contains(string(out), "guard.sh") {
		t.Fatalf("existing hooks lost: %s", out)
	}
	h := stop[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if h["command"] != stopCmd || h["async"] != true || h["type"] != "command" {
		t.Fatalf("stop hook = %v", h)
	}
	n := hooks["Notification"].([]any)[0].(map[string]any)
	if n["matcher"] != ClaudeNotifyMatcher {
		t.Fatalf("notification matcher = %v", n["matcher"])
	}
	if strings.Contains(string(out), `\u0026`) || strings.Contains(string(out), `\u003c`) {
		t.Fatalf("HTML-escaped output: %s", out)
	}
}

func TestClaudeSettingsIdempotentAndUninstall(t *testing.T) {
	src := []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "say done"}]}]}}`)
	once, err := ClaudeSettings(src, stopCmd, notifyCmd, true)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := ClaudeSettings(once, stopCmd, notifyCmd, true)
	if err != nil {
		t.Fatal(err)
	}
	if countMeow(t, twice) != 2 || !EquivalentJSON(once, twice) {
		t.Fatalf("reinstall duplicated hooks: %s", twice)
	}
	// A reinstall with different flags replaces rather than adds.
	changed, _ := ClaudeSettings(once, stopCmd+" --no-summary", notifyCmd+" --no-summary", true)
	if countMeow(t, changed) != 2 || !strings.Contains(string(changed), "--no-summary") {
		t.Fatalf("flag change not applied: %s", changed)
	}

	removed, err := ClaudeSettings(twice, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if countMeow(t, removed) != 0 || !EquivalentJSON(removed, src) {
		t.Fatalf("uninstall left %s, want %s", removed, src)
	}
	if again, _ := ClaudeSettings(removed, "", "", false); string(again) != string(removed) {
		t.Fatal("uninstall with nothing to remove rewrote the file")
	}
}

func TestClaudeSettingsUninstallDropsAddedHooksKey(t *testing.T) {
	src := []byte(`{"model": "opus"}`)
	installed, _ := ClaudeSettings(src, stopCmd, notifyCmd, true)
	removed, err := ClaudeSettings(installed, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(removed), "hooks") || !EquivalentJSON(removed, src) {
		t.Fatalf("uninstall left %s", removed)
	}
}

func TestClaudeSettingsKeepsHandlersSharingAGroup(t *testing.T) {
	src := []byte(`{"hooks": {"Stop": [{"hooks": [
		{"type": "command", "command": "say done"},
		{"type": "command", "command": "/opt/bin/meow hook claude-stop", "async": true}]}]}}`)
	out, err := ClaudeSettings(src, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if countMeow(t, out) != 0 || !strings.Contains(string(out), "say done") {
		t.Fatalf("out = %s", out)
	}
}

func TestClaudeSettingsEmptyAndInvalid(t *testing.T) {
	for _, src := range []string{"", "  \n", "{}", `{"hooks": null}`} {
		out, err := ClaudeSettings([]byte(src), stopCmd, notifyCmd, true)
		if err != nil || countMeow(t, out) != 2 {
			t.Fatalf("install into %q = %s, %v", src, out, err)
		}
	}
	for _, src := range []string{`{"model": "opus",}`, `[]`, `{"hooks": []}`, `{"hooks": {"Stop": {}}}`, `{} {}`} {
		if _, err := ClaudeSettings([]byte(src), stopCmd, notifyCmd, true); err == nil {
			t.Errorf("ClaudeSettings(%q) succeeded, want error", src)
		}
	}
}

func TestClaudeSettingsKeepsNumbersExact(t *testing.T) {
	src := []byte(`{"hooks": {"PreToolUse": [{"hooks": [{"type": "command", "command": "x", "timeout": 12345678901234567890}]}]}}`)
	out, err := ClaudeSettings(src, stopCmd, notifyCmd, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "12345678901234567890") {
		t.Fatalf("number changed: %s", out)
	}
}

var codexArgv = []string{"/usr/local/bin/meow", "hook", "codex"}

func TestCodexConfigInstall(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"empty", "", `notify = ["/usr/local/bin/meow", "hook", "codex"]` + "\n"},
		{"no newline", `model = "o3"`, "model = \"o3\"\nnotify = [\"/usr/local/bin/meow\", \"hook\", \"codex\"]\n"},
		{"before first table", "model = \"o3\"\n\n[tui]\nnotifications = true\n",
			"model = \"o3\"\n\nnotify = [\"/usr/local/bin/meow\", \"hook\", \"codex\"]\n\n[tui]\nnotifications = true\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := CodexConfig([]byte(tc.src), codexArgv, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", out, tc.want)
			}
		})
	}
}

func TestCodexConfigIdempotentAndUninstall(t *testing.T) {
	src := "model = \"o3\"\n\n[tui]\nnotifications = true\n"
	once, _ := CodexConfig([]byte(src), codexArgv, true, false)
	twice, err := CodexConfig(once, codexArgv, true, false)
	if err != nil || string(twice) != string(once) {
		t.Fatalf("reinstall changed the file:\n%s", twice)
	}
	removed, err := CodexConfig(twice, nil, false, false)
	if err != nil || string(removed) != src {
		t.Fatalf("uninstall left\n%q\nwant\n%q", removed, src)
	}
}

func TestCodexConfigConflict(t *testing.T) {
	src := "notify = [\"python3\", \"/home/me/notify.py\"]\nmodel = \"o3\"\n"
	_, err := CodexConfig([]byte(src), codexArgv, true, false)
	var conflict *NotifyConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Existing, "notify.py") {
		t.Fatalf("err = %v", err)
	}
	// Uninstall never touches a notify meow didn't add.
	if out, _ := CodexConfig([]byte(src), nil, false, false); string(out) != src {
		t.Fatal("uninstall removed a foreign notify")
	}
	out, err := CodexConfig([]byte(src), codexArgv, true, true)
	if err != nil || strings.Contains(string(out), "notify.py") || !strings.HasPrefix(string(out), "notify = [\"/usr/local/bin/meow\"") {
		t.Fatalf("force = %s, %v", out, err)
	}
}

func TestCodexConfigMultilineAndTables(t *testing.T) {
	src := "notify = [\n  \"/opt/meow\",\n  \"hook\", # comment ]\n  \"codex\",\n]\nmodel = \"o3\"\n\n[profiles.x]\nnotify = \"not top level\"\n"
	existing, ours := CodexNotify([]byte(src))
	if !ours || !strings.Contains(existing, "/opt/meow") {
		t.Fatalf("CodexNotify = %q, %v", existing, ours)
	}
	out, err := CodexConfig([]byte(src), codexArgv, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "/opt/meow") || !strings.Contains(string(out), `notify = "not top level"`) || strings.Count(string(out), "notify =") != 2 {
		t.Fatalf("out =\n%s", out)
	}
	// A notify only inside a table is not top level.
	if e, _ := CodexNotify([]byte("[tui]\nnotify = 1\n")); e != "" {
		t.Fatalf("table key read as top level: %q", e)
	}
	// Brackets inside a multi-line string don't start a table.
	ml := "instructions = \"\"\"\n[not a table]\n\"\"\"\n"
	out, _ = CodexConfig([]byte(ml), codexArgv, true, false)
	if !strings.HasSuffix(string(out), "notify = [\"/usr/local/bin/meow\", \"hook\", \"codex\"]\n") {
		t.Fatalf("multi-line string treated as table:\n%s", out)
	}
}

func TestIsMeowNotify(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want bool
	}{
		{`notify = ["/usr/local/bin/meow", "hook", "codex"]`, true},
		{`notify = ['/opt/meow', 'hook', 'codex', '--no-summary']`, true},
		{"notify = [\n  \"/opt/meow\",\n  \"hook\", # comment ]\n  \"codex\",\n]", true},
		{`notify = ["/bin/sh", "-c", "echo \"hook\" codex"]`, false},
		{`notify = ["python3", "notify.py"] # meow hook codex`, false},
		{`notify = ["hook", "other", "codex"]`, false},
	} {
		if got := isMeowNotify(tc.s); got != tc.want {
			t.Errorf("isMeowNotify(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

func TestTOMLArrayEscaping(t *testing.T) {
	got := TOMLArray([]string{`/Users/a "b"/meow`, `C:\bin`, "tab\t"})
	want := `["/Users/a \"b\"/meow", "C:\\bin", "tab\u0009"]`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestWriteFileBacksUpAndFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "settings.json")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte("old"), 0o600)
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)
	b1, err := WriteFile(link, []byte("new"), now)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced by a file")
	}
	if got, _ := os.ReadFile(real); string(got) != "new" {
		t.Fatalf("target = %q", got)
	}
	if got, _ := os.ReadFile(b1); string(got) != "old" {
		t.Fatalf("backup = %q", got)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	// A second write in the same second keeps the first backup.
	b2, err := WriteFile(link, []byte("newer"), now)
	if err != nil || b2 == b1 {
		t.Fatalf("second backup = %q, %v", b2, err)
	}
	if got, _ := os.ReadFile(b1); string(got) != "old" {
		t.Fatal("first backup overwritten")
	}
	// A new file gets no backup.
	if b, err := WriteFile(filepath.Join(dir, "new", "f"), []byte("x"), now); err != nil || b != "" {
		t.Fatalf("new file backup = %q, %v", b, err)
	}
}

func TestIsMeowSkill(t *testing.T) {
	if !IsMeowSkill([]byte("<!-- meow-skill: installed by \"meow setup\" -->")) {
		t.Fatal("legacy setup skill not recognized")
	}
	if IsMeowSkill([]byte("---\nname: meow\ndescription: Meow\n---\n")) {
		t.Fatal("unmarked skill claimed as setup's")
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote("/Users/o'brien/bin/meow"); got != `'/Users/o'\''brien/bin/meow'` {
		t.Fatalf("got %s", got)
	}
}
