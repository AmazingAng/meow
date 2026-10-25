package setup

import (
	"strings"
	"testing"
)

func TestCursorHooksPreserveForeignCommands(t *testing.T) {
	src := []byte(`{
  "version": 1,
  "hooks": {
    "afterFileEdit": [{"command": "./hooks/format.sh"}],
    "stop": [{"command": "echo done"}, {"command": "meow hook cursor-stop --no-summary"}]
  }
}
`)
	command := "'/opt/meow' hook cursor-stop"
	once, err := CursorHooks(src, command, "", true, "/opt/meow")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(once), "echo done") || !strings.Contains(string(once), "./hooks/format.sh") || !strings.Contains(string(once), command) {
		t.Fatalf("lost or missed a hook: %s", once)
	}
	if strings.Contains(string(once), "--no-summary") {
		t.Fatalf("old meow command kept: %s", once)
	}
	twice, err := CursorHooks(once, command, "", true, "/opt/meow")
	if err != nil || !EquivalentJSON(once, twice) || strings.Count(string(twice), "cursor-stop") != 1 {
		t.Fatalf("reinstall changed hooks: %s", twice)
	}
	removed, err := CursorHooks(twice, "", "", false, "/opt/meow")
	if err != nil || strings.Contains(string(removed), "cursor-stop") || !strings.Contains(string(removed), "echo done") || !strings.Contains(string(removed), "format.sh") {
		t.Fatalf("uninstall: %s %v", removed, err)
	}
	for _, input := range []string{`null`, `{"hooks":[]}`, `{"hooks":{"stop":{}}}`} {
		if _, err := CursorHooks([]byte(input), command, "", false); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestCursorHooksCreatesVersionedFile(t *testing.T) {
	out, err := CursorHooks(nil, "'/opt/meow' hook cursor-stop", "", true, "/opt/meow")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"version": 1`) || !strings.Contains(string(out), `"timeout": 30`) {
		t.Fatalf("created: %s", out)
	}
	gone, err := CursorHooks(out, "", "", false, "/opt/meow")
	if err != nil || strings.Contains(string(gone), "hooks") || !strings.Contains(string(gone), `"version"`) {
		t.Fatalf("empty uninstall: %s %v", gone, err)
	}
}

func TestCursorHooksPairedRoundTrip(t *testing.T) {
	stop, response := "'/opt/meow' hook cursor-stop", "'/opt/meow' hook cursor-response"
	old := []byte(`{"version":1,"hooks":{"stop":[{"command":"meow hook cursor-stop"}],"afterAgentResponse":[{"command":"echo preserve"},{"command":"meow hook cursor-response"}]}}`)
	installed, err := CursorHooks(old, stop, response, true, "/opt/meow")
	if err != nil || strings.Count(string(installed), "hook cursor-stop") != 1 || strings.Count(string(installed), "hook cursor-response") != 1 || !strings.Contains(string(installed), "echo preserve") {
		t.Fatalf("paired install: %s %v", installed, err)
	}
	removed, err := CursorHooks(installed, "", "", false, "/opt/meow")
	if err != nil || strings.Contains(string(removed), "hook cursor-") || !strings.Contains(string(removed), "echo preserve") {
		t.Fatalf("paired uninstall: %s %v", removed, err)
	}
}
