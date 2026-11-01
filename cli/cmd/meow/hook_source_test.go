package main

import (
	"encoding/json"
	"io"
	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/client"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAgentHookSourceOverrideOnWire(t *testing.T) {
	var sources []string
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n struct {
			Source string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			t.Error(err)
		}
		sources = append(sources, n.Source)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(201)
		io.WriteString(w, `{"id":"ntf_test"}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MEOW_CONFIG", path)
	t.Setenv("MEOW_SERVER", server.URL)
	t.Setenv("MEOW_KEY", "meow_disposable")
	t.Setenv("CODEX_HOME", t.TempDir())
	cases := []struct{ kind, payload string }{
		{"codex-stop", `{"hook_event_name":"Stop","session_id":"s","turn_id":"t","last_assistant_message":"Done"}`},
		{"codex-permission", `{"hook_event_name":"PermissionRequest","session_id":"s","turn_id":"t","tool_name":"Bash","tool_input":{"description":"Run check"}}`},
		{"codex-input", `{"hook_event_name":"PreToolUse","session_id":"s","turn_id":"t","tool_use_id":"call1","tool_name":"request_user_input_async","tool_input":{"questions":[{"title":"Continue?"}]}}`},
		{"claude-stop", `{"session_id":"s","prompt_id":"p","last_assistant_message":"Done"}`},
	}
	for _, tc := range cases {
		for _, override := range []bool{false, true} {
			contents := `{}`
			if override {
				contents = `{"agent_sources":{"codex":"codex-macmini","claude":"claude-laptop","cursor":"cursor-work"}}`
			}
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			before := len(sources)
			var code int
			out := captureOutput(t, tc.payload, func() { code = run([]string{"hook", tc.kind}) })
			if code != 0 || out != "" || len(sources) != before+1 {
				t.Fatalf("%s: exit=%d output=%q sends=%d", tc.kind, code, out, len(sources)-before)
			}
			want := "Codex"
			if override {
				want = "codex-macmini"
			}
			if tc.kind == "claude-stop" {
				want = "Claude Code"
				if override {
					want = "claude-laptop"
				}
			}
			if sources[before] != want {
				t.Fatalf("%s: source=%q want=%q", tc.kind, sources[before], want)
			}
			if keys[before] == "" {
				t.Fatal("missing delivery key")
			}
			// Permission requests intentionally use a fresh nonce per invocation.
			if override && tc.kind != "codex-permission" && keys[before] != keys[before-1] {
				t.Fatal("display override changed delivery identity")
			}
		}
	}
}

func TestHookSourceDefaultsValidationAndIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, kind, value, want string
		invalid                 bool
	}{
		{"missing", "codex-stop", "", "Codex", false},
		{"blank", "codex-input", "  \t", "Codex", false},
		{"trim", "codex-permission", " codex-macmini ", "codex-macmini", false},
		{"other agent", "claude-stop", "codex-macmini", "Codex", false},
		{"unicode boundary", "codex-stop", strings.Repeat("猫", 64), strings.Repeat("猫", 64), false},
		{"too long", "codex-stop", strings.Repeat("猫", 65), "Codex", true},
		{"nul", "codex-stop", "private\x00label", "Codex", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := client.NotifyRequest{Source: "Codex"}
			err := applyHookSource(tc.kind, config.Config{AgentSources: map[string]string{"codex": tc.value}}, &req)
			if (err != nil) != tc.invalid || req.Source != tc.want {
				t.Fatalf("source=%q err=%v", req.Source, err)
			}
		})
	}
}

func TestCursorSourceFrozenAcrossConfigChanges(t *testing.T) {
	t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MEOW_LANGUAGE", "en")
	cfg := config.Config{Server: "https://example.invalid", Key: "meow_disposable", AgentSources: map[string]string{"cursor": "cursor-work"}}
	raw := []byte(cursorInput("cursor-stop", "conversation", "generation-1", "", ""))
	first, key, send, err := cursorStopNotification(raw, true, cfg)
	if err != nil || !send || first.Source != "cursor-work" {
		t.Fatalf("first=%+v send=%v err=%v", first, send, err)
	}
	cfg.AgentSources["cursor"] = "cursor-renamed"
	replay, replayKey, send, err := cursorStopNotification(raw, true, cfg)
	if err != nil || !send || replayKey != key || !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay changed: %+v key=%s err=%v", replay, replayKey, err)
	}
	next, _, send, err := cursorStopNotification([]byte(cursorInput("cursor-stop", "conversation", "generation-2", "", "")), true, cfg)
	if err != nil || !send || next.Source != "cursor-renamed" {
		t.Fatalf("next=%+v send=%v err=%v", next, send, err)
	}
	cfg.AgentSources["cursor"] = strings.Repeat("猫", 65)
	if _, _, _, err := cursorStopNotification([]byte(cursorInput("cursor-stop", "conversation", "generation-3", "", "")), true, cfg); err == nil {
		t.Fatal("invalid label frozen")
	}
	cfg.AgentSources["cursor"] = "cursor-fixed"
	next, _, send, err = cursorStopNotification([]byte(cursorInput("cursor-stop", "conversation", "generation-3", "", "")), true, cfg)
	if err != nil || !send || next.Source != "cursor-fixed" {
		t.Fatalf("invalid label poisoned snapshot: %+v %v", next, err)
	}
}
