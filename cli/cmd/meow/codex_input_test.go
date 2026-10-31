package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCodexInputRemindsDuringAutoReview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"s\"}}\n{\"type\":\"turn_context\",\"payload\":{\"turn_id\":\"t\",\"approvals_reviewer\":\"auto_review\"}}\n"), 0600)
	var input map[string]any
	json.Unmarshal(codexApprovalInput(t, path, "Bash"), &input)
	input["hook_event_name"] = "PreToolUse"
	input["tool_name"] = "request_user_input_async"
	input["tool_use_id"] = "call-input"
	input["tool_input"] = map[string]any{"questions": []any{map[string]string{"title": "选择发布区域"}}}
	b, _ := json.Marshal(input)
	n, key, send, err := buildHookNotification("codex-input", b, true)
	if err != nil || !send || n.Body != "选择发布区域" || key == "" {
		t.Fatalf("real human input suppressed in auto-review: %+v %v", n, err)
	}
	if _, _, send, err = buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || send {
		t.Fatal("auto-review permission suppression regressed")
	}
}

func TestCodexSetupInputMatcherUpgradeAndUninstall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	foreign := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"other-hook"}]}],"Stop":[{"hooks":[{"type":"command","command":"'/opt/meow' hook codex-stop","async":true}]}]}}`
	os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(foreign), 0600)
	o := setupOptions{exe: "/opt/meow", install: true, summary: true}
	changes, _, err := planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	b, _ := os.ReadFile(filepath.Join(dir, "hooks.json"))
	var d struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	json.Unmarshal(b, &d)
	found := false
	for _, g := range d.Hooks["PreToolUse"] {
		for _, h := range g.Hooks {
			if strings.Contains(h.Command, "codex-input") {
				found = true
				if !h.Async {
					t.Fatal("input reminder blocks Codex")
				}
				re, err := regexp.Compile(g.Matcher)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"request_user_input", "request_user_input_async", "functions.request_user_input_async"} {
					if !re.MatchString(name) {
						t.Fatal("input tool does not match", name)
					}
				}
				for _, name := range []string{"Bash", "spawn_agent", "request_permissions", "prefixrequest_user_input", "request_user_input_extra"} {
					if re.MatchString(name) {
						t.Fatal("unrelated tool matched", name)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("setup did not install user-input hook")
	}
	if !strings.Contains(string(b), "other-hook") {
		t.Fatal("lost foreign pre-tool hook")
	}
	if changes, _, err = planCodex(o); err != nil || len(changes) != 0 {
		t.Fatal("input setup is not idempotent", err)
	}
	o.install = false
	changes, _, err = planCodex(o)
	if err != nil {
		t.Fatal(err)
	}
	applyTestPlan(t, changes)
	b, _ = os.ReadFile(filepath.Join(dir, "hooks.json"))
	if strings.Contains(string(b), "codex-input") || !strings.Contains(string(b), "other-hook") {
		t.Fatal("uninstall removed foreign hook or kept input hook")
	}
}
