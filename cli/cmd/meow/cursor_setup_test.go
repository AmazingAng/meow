package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCursorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_KEY", "meow_testkey")
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")

	cursorPath := filepath.Join(dir, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(cursorPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("{\n  \"version\": 1,\n  \"hooks\": {\n    \"stop\": [{\"command\": \"echo keep-me\"}]\n  }\n}\n")
	if err := os.WriteFile(cursorPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	runAgent := func(args ...string) string {
		return captureOutput(t, "", func() {
			if code := run(append([]string{"setup"}, args...)); code != 0 {
				t.Errorf("setup %v exit %d", args, code)
			}
		})
	}
	runAgent("cursor", "--yes", "--no-test")
	got, err := os.ReadFile(cursorPath)
	if err != nil || !strings.Contains(string(got), "'/opt/meow' hook cursor-stop") || !strings.Contains(string(got), "echo keep-me") {
		t.Fatalf("cursor hooks: %s %v", got, err)
	}
	again := runAgent("cursor", "--yes", "--no-test")
	if !strings.Contains(again, "already connected") {
		t.Fatalf("reinstall: %s", again)
	}
	runAgent("cursor", "--uninstall", "--yes")
	left, _ := os.ReadFile(cursorPath)
	if strings.Contains(string(left), "cursor-stop") || !strings.Contains(string(left), "echo keep-me") {
		t.Fatalf("cursor uninstall: %s", left)
	}
}
