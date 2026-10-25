package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"meow.blue/meow/message"
)

func TestHookSessionContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte("{\"id\":\"s1\",\"thread_name\":\"Old\"}\n{\"id\":\"other\",\"thread_name\":\"Wrong\"}\n{\"id\":\"s1\",\"thread_name\":\"部署审查 😺\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"codex", "codex-stop", "codex-permission"} {
		t.Run(kind, func(t *testing.T) {
			in := `{"type":"agent-turn-complete","thread-id":"s1","turn-id":"t1","session_id":"s1","turn_id":"t1","hook_event_name":"Stop","cwd":"/private/team/project"}`
			if kind == "codex-permission" {
				in = `{"session_id":"s1","turn_id":"t1","hook_event_name":"PermissionRequest","cwd":"/private/team/project"}`
			}
			req, _, send, err := buildHookNotification(kind, []byte(in), false)
			if err != nil || !send {
				t.Fatalf("send=%v err=%v", send, err)
			}
			b, _ := json.Marshal(req)
			var data map[string]any
			_ = json.Unmarshal(b, &data)
			c, ok := data["context"].(map[string]any)
			if !ok || c["session"] != "部署审查 😺" || c["session_id"] != "s1" || c["workspace"] != "project" {
				t.Fatalf("context=%v", c)
			}
			if c["workspace_id"] == "" || c["workspace_id"] == "/private/team/project" {
				t.Fatalf("workspace identity=%v", c)
			}
		})
	}
}

func TestClaudeContextTitlesAndBoundedFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	contents := `{"type":"ai-title","sessionId":"s1","aiTitle":"AI title"}
{"type":"custom-title","sessionId":"s1","customTitle":"Named session"}
{"type":"ai-title","sessionId":"s1","aiTitle":"Later generated title"}
{"type":"custom-title","sessionId":"other","customTitle":"Wrong"}
`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"claude-stop", "claude-notify", "claude-failure"} {
		raw, _ := json.Marshal(map[string]any{"session_id": "s1", "prompt_id": "p", "cwd": "/w/project", "transcript_path": path, "last_assistant_message": "Done"})
		req, _, send, err := buildHookNotification(kind, raw, false)
		if err != nil || !send || req.Context == nil || req.Context.Session != "Named session" {
			t.Fatalf("%s: %+v %v", kind, req, err)
		}
	}
	if got := readSessionName(path, "missing", true); got != "" {
		t.Fatalf("wrong session: %s", got)
	}
	if got := readSessionName(dir, "s1", true); got != "" {
		t.Fatalf("directory accepted: %s", got)
	}
	if got := readSessionName(path+"missing", "s1", true); got != "" {
		t.Fatal(got)
	}
	if err := os.WriteFile(path, []byte(`{"type":"ai-title","sessionId":"s1","aiTitle":"AI title"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionName(path, "s1", true); got != "AI title" {
		t.Fatal(got)
	}
	if got := hookContext("Unknown", "", "", "", ""); got != nil {
		t.Fatal(got)
	}
	explicit := hookContext("Claude Code", "/", "s1", " Explicit\nname ", path)
	if explicit.Session != "Explicit name" || explicit.Workspace != "" {
		t.Fatal(explicit)
	}
	// A truncated partial first line is ignored; later complete records still work.
	huge := strings.Repeat("x", metadataTailLimit+100) + "\n{\"id\":\"s1\",\"thread_name\":\"Tail\"}\n{bad}\n"
	if err := os.WriteFile(path, []byte(huge), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionName(path, "s1", false); got != "Tail" {
		t.Fatal(got)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", metadataTailLimit+100)), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionName(path, "s1", false); got != "" {
		t.Fatal(got)
	}
}

func TestContextNormalizationAndIdentity(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("session-%d", i)
		a := hookContext("Other", "/w/a/../project", id, "Same title", "")
		b := hookContext("Other", "/w/project", id, "Renamed", "")
		if a.WorkspaceID != b.WorkspaceID || a.SessionID != b.SessionID || a.Session == b.Session {
			t.Fatalf("identity lost: %v %v", a, b)
		}
	}
	long := cleanContextLabel(strings.Repeat("猫", 300)+"\x00\n", 200)
	if utf8.RuneCountInString(long) != 200 || strings.ContainsAny(long, "\x00\n") {
		t.Fatal(long)
	}
}

func TestCodexContextHomeFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "session_index.jsonl")
	if err := os.WriteFile(file, []byte(`{"id":"s1","thread_name":"From default home"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got := hookContext("Codex", "", "s1", "", "")
	if got.Session != "From default home" {
		t.Fatal(got)
	}
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(file, 0600)
	if os.Geteuid() != 0 {
		if got := readSessionName(file, "s1", false); got != "" {
			t.Fatal("unreadable file was used")
		}
	}
	t.Setenv("HOME", "")
	got = hookContext("Codex", "", "s1", "", "")
	if got.Session != "" || got.SessionID != "s1" {
		t.Fatal(got)
	}
}

func TestContextMetadataFrozenAcrossTransportRetry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	path := filepath.Join(dir, "session_index.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"s1","thread_name":"Original"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var first string
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		count++
		if count == 1 {
			first = string(b)
			if !strings.Contains(first, `"session":"Original"`) {
				t.Error("missing original context")
			}
			if err := os.WriteFile(path, []byte(`{"id":"s1","thread_name":"Renamed mid-retry"}`), 0600); err != nil {
				t.Error(err)
			}
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"code":"unavailable"}}`)
			return
		}
		if string(b) != first {
			t.Error("retry changed context")
		}
		fmt.Fprint(w, `{"id":"ntf_done"}`)
	}))
	defer server.Close()
	t.Setenv("MEOW_SERVER", server.URL)
	t.Setenv("MEOW_KEY", "meow_test")
	output := captureOutput(t, `{"hook_event_name":"Stop","session_id":"s1","turn_id":"t1","cwd":"/w/p"}`, func() {
		if cmdHook([]string{"codex-stop"}) != 0 {
			t.Error("hook failed")
		}
	})
	if count != 2 || output != "" {
		t.Fatalf("count=%d output=%s", count, output)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "hook.log"))
	if strings.Contains(string(log), "Original") || strings.Contains(string(log), "Renamed") {
		t.Fatal("context leaked into log")
	}
}

func TestSendContextFlagsAndEnvironment(t *testing.T) {
	for _, flags := range []bool{true, false} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "meow.json"))
			t.Setenv("MEOW_KEY", "meow_test")
			t.Setenv("MEOW_SOURCE", "Other agent")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Context *message.Context `json:"context"`
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				want := message.Context{Workspace: "Project", WorkspaceID: "w1", Session: "Session A", SessionID: "s1"}
				if in.Context == nil || *in.Context != want {
					t.Errorf("context=%+v", in.Context)
				}
				fmt.Fprint(w, `{"id":"ntf_done"}`)
			}))
			defer server.Close()
			t.Setenv("MEOW_SERVER", server.URL)
			args := []string{"Done", "--quiet"}
			if flags {
				args = append(args, "--workspace", "Project", "--workspace-id", "w1", "--session", "Session A", "--session-id", "s1")
			} else {
				t.Setenv("MEOW_WORKSPACE", "Project")
				t.Setenv("MEOW_WORKSPACE_ID", "w1")
				t.Setenv("MEOW_SESSION", "Session A")
				t.Setenv("MEOW_SESSION_ID", "s1")
			}
			captureOutput(t, "", func() {
				if cmdSend(args) != exitOK {
					t.Error("send failed")
				}
			})
		})
	}
}
