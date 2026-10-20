package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"
)

func TestLifecyclePreservesForeignAndEmptyGroups(t *testing.T) {
	property := func(value string) bool {
		original, _ := json.Marshal(map[string]any{"description": value, "hooks": map[string]any{"Stop": []any{map[string]any{"matcher": "", "hooks": []any{}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo " + value}}}}}})
		commands := map[string]string{"Stop": "'/opt/meow' hook codex-stop"}
		first, e := LifecycleHooks(original, commands, true, []string{"codex-stop"}, "/opt/meow")
		if e != nil {
			return false
		}
		second, e := LifecycleHooks(first, commands, true, []string{"codex-stop"}, "/opt/meow")
		if e != nil || !EquivalentJSON(first, second) {
			return false
		}
		removed, e := LifecycleHooks(second, nil, false, []string{"codex-stop"}, "/opt/meow")
		return e == nil && EquivalentJSON(original, removed)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleRejectsMalformedAndPreservesCommandSuffixes(t *testing.T) {
	for _, input := range []string{`null`, `{} trailing`, `{"hooks":[]}`, `{"hooks":{"Stop":true}}`, `{"hooks":{"Stop":[null]}}`, `{"hooks":{"Stop":[{"hooks":null}]}}`, `{"hooks":{"Stop":[{"hooks":[false]}]}}`} {
		if _, err := LifecycleHooks([]byte(input), nil, false, nil); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
	original := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"'/opt/meow' hook codex-stop && echo mine"}]}]}}`)
	got, err := LifecycleHooks(original, nil, false, []string{"codex-stop"}, "/opt/meow")
	if err != nil || !EquivalentJSON(got, original) {
		t.Fatalf("foreign suffix removed: %s %v", got, err)
	}
}

func TestLegacyBackupSymlinkAndFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "custom.md")
	link := filepath.Join(dir, "SKILL.md")
	data := "User custom notes"
	os.WriteFile(target, []byte(data), 0600)
	os.Symlink(target, link)
	backup, err := BackupAndRemove(link, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{target, backup} {
		b, e := os.ReadFile(p)
		if e != nil || string(b) != data {
			t.Fatalf("lost %s", p)
		}
	}
	os.WriteFile(link, []byte(data), 0600)
	// Existing non-file backup path must fail safely, never delete the original.
	now := time.Now()
	base := link + ".bak." + now.Format("20060102-150405")
	for i := 0; i <= 100; i++ {
		p := base
		if i > 0 {
			p = fmt.Sprintf("%s-%d", base, i)
		}
		os.Mkdir(p, 0700)
	}
	if _, err := BackupAndRemove(link, now); err == nil {
		t.Fatal("backup failure ignored")
	}
	b, _ := os.ReadFile(link)
	if string(b) != data {
		t.Fatal("deleted without backup")
	}
}

func FuzzLifecycleRoundTrip(f *testing.F) {
	f.Add("foreign-command")
	f.Fuzz(func(t *testing.T, s string) {
		// Keep generated commands foreign, including quotes and control characters.
		input, _ := json.Marshal(map[string]any{"hooks": map[string]any{"Stop": []any{hookGroup("", "echo "+s)}}})
		commands := map[string]string{"Stop": "'/opt/meow' hook codex-stop"}
		got, err := LifecycleHooks(input, commands, true, []string{"codex-stop"}, "/opt/meow")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "codex-stop") {
			t.Fatal("own hook missing")
		}
		restored, err := LifecycleHooks(got, nil, false, []string{"codex-stop"}, "/opt/meow")
		if err != nil || !EquivalentJSON(input, restored) {
			t.Fatal("foreign hook changed")
		}
	})
}
