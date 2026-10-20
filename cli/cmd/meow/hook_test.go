package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildClaudeStop(t *testing.T) {
	in := `{"session_id":"s1","prompt_id":"p1","cwd":"/Users/me/src/meow-repo","hook_event_name":"Stop",
		"stop_hook_active":false,"last_assistant_message":"Fixed the flaky test.\n\nAll 42 tests pass."}`
	req, idem, send, err := buildHookNotification("claude-stop", []byte(in), true)
	if err != nil || !send {
		t.Fatalf("send=%v err=%v", send, err)
	}
	if req.Source != "Claude Code" || req.Title != "New result · meow-repo" || req.Body != "Fixed the flaky test.\n\nAll 42 tests pass." {
		t.Fatalf("req = %+v", req)
	}
	if idem == "" {
		t.Fatal("no idempotency key")
	}
	_, idem2, _, _ := buildHookNotification("claude-stop", []byte(in), true)
	if idem2 != idem {
		t.Fatal("idempotency key not stable for the same event")
	}

	req, _, _, _ = buildHookNotification("claude-stop", []byte(in), false)
	if req.Body != "" {
		t.Fatalf("--no-summary body = %q", req.Body)
	}

	active := strings.Replace(in, `"stop_hook_active":false`, `"stop_hook_active":true`, 1)
	if _, _, send, err := buildHookNotification("claude-stop", []byte(active), true); !send || err != nil {
		t.Fatalf("stop_hook_active: send=%v err=%v", send, err)
	}
}

func TestBuildClaudeNotify(t *testing.T) {
	in := `{"session_id":"s1","cwd":"/w/api","hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`
	req, _, send, err := buildHookNotification("claude-notify", []byte(in), true)
	if err != nil || !send {
		t.Fatalf("send=%v err=%v", send, err)
	}
	if req.Title != "Needs your attention · api" || req.Type != "alert" || req.Priority != "high" || !strings.Contains(req.Body, "permission") {
		t.Fatalf("req = %+v", req)
	}
	withTitle := strings.Replace(in, `"message"`, `"title":"Permission needed","message"`, 1)
	if req, _, _, _ = buildHookNotification("claude-notify", []byte(withTitle), true); req.Title != "Permission needed · api" {
		t.Fatalf("title = %q", req.Title)
	}
}

func TestBuildCodex(t *testing.T) {
	in := `{"type":"agent-turn-complete","thread-id":"t1","turn-id":"9","cwd":"/w/web","input-messages":["hi"],"last-assistant-message":"Done: refactored the router."}`
	req, idem, send, err := buildHookNotification("codex", []byte(in), true)
	if err != nil || !send || idem == "" {
		t.Fatalf("send=%v idem=%q err=%v", send, idem, err)
	}
	if req.Source != "Codex" || req.Title != "New result · web" || req.Body != "Done: refactored the router." {
		t.Fatalf("req = %+v", req)
	}
	null := `{"type":"agent-turn-complete","thread-id":"t1","turn-id":"9","last-assistant-message":null}`
	if req, _, _, err = buildHookNotification("codex", []byte(null), true); err != nil || req.Body != "" || req.Title != "New result" {
		t.Fatalf("null message: %+v %v", req, err)
	}
	if _, _, send, _ := buildHookNotification("codex", []byte(`{"type":"something-new"}`), true); send {
		t.Fatal("unknown event type sent")
	}
	if _, _, _, err := buildHookNotification("codex", []byte(`not json`), true); err == nil {
		t.Fatal("invalid payload accepted")
	}
}

func TestSummarizeTruncates(t *testing.T) {
	s := summarize(strings.Repeat("猫", 5000))
	if n := len([]rune(s)); n != summaryRunes || !strings.HasSuffix(s, "…") {
		t.Fatalf("len=%d", n)
	}
	if hookIdemKey("codex", "", "1") != "" {
		t.Fatal("key from missing identifiers")
	}
}

// captureOutput runs fn with stdout and stderr redirected and returns what was written.
func captureOutput(t *testing.T, stdin string, fn func()) string {
	t.Helper()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	os.Stdin, os.Stdout, os.Stderr = inR, outW, outW
	go func() { io.WriteString(inW, stdin); inW.Close() }()
	fn()
	outW.Close()
	b, _ := io.ReadAll(outR)
	return string(b)
}

func TestHookSendsAndStaysSilent(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	var auth, idem string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, body)
		auth, idem = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"ntf_1"}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_SERVER", ts.URL)
	t.Setenv("MEOW_KEY", "meow_testkey")

	stop := `{"session_id":"s","prompt_id":"p","cwd":"/w/repo","stop_hook_active":false,"last_assistant_message":"done"}`
	var code int
	out := captureOutput(t, stop, func() { code = run([]string{"hook", "claude-stop"}) })
	if code != 0 || out != "" {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	if len(got) != 1 || got[0]["title"] != "New result · repo" || got[0]["source"] != "Claude Code" {
		t.Fatalf("sent %v", got)
	}
	if auth != "Bearer meow_testkey" || !strings.HasPrefix(idem, "hook-") {
		t.Fatalf("auth=%q idem=%q", auth, idem)
	}

	codexPayload := `{"type":"agent-turn-complete","thread-id":"t","turn-id":"1","cwd":"/w/web","last-assistant-message":"ok"}`
	out = captureOutput(t, "", func() { code = run([]string{"hook", "codex", codexPayload}) })
	if code != 0 || out != "" || len(got) != 2 || got[1]["source"] != "Codex" {
		t.Fatalf("codex: exit=%d output=%q sent=%v", code, out, got)
	}
}

func TestHookFailuresAreSilentAndLogged(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_SERVER", ts.URL)
	t.Setenv("MEOW_KEY", "meow_secretkey123")

	cases := []struct {
		name, stdin string
		args        []string
	}{
		{"server error", `{"session_id":"s","prompt_id":"p","last_assistant_message":"private text"}`, []string{"hook", "claude-stop"}},
		{"bad json", `{nope`, []string{"hook", "claude-notify"}},
		{"unknown kind", ``, []string{"hook", "gemini"}},
		{"no kind", ``, []string{"hook"}},
	}
	for _, tc := range cases {
		var code int
		out := captureOutput(t, tc.stdin, func() { code = run(tc.args) })
		if code != 0 || out != "" {
			t.Errorf("%s: exit=%d output=%q", tc.name, code, out)
		}
	}
	log, err := os.ReadFile(filepath.Join(dir, "hook.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "send failed") || !strings.Contains(string(log), "invalid hook input") {
		t.Fatalf("log = %s", log)
	}
	if strings.Contains(string(log), "meow_secretkey123") || strings.Contains(string(log), "private text") {
		t.Fatalf("log leaks secrets or content: %s", log)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "hook.log")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %v", fi.Mode().Perm())
	}
}

func TestHookWithoutKeyOrDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_KEY", "")
	start := time.Now()
	var code int
	out := captureOutput(t, `{"session_id":"s","prompt_id":"p"}`, func() { code = run([]string{"hook", "claude-stop"}) })
	if code != 0 || out != "" || time.Since(start) > 2*time.Second {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	if log, _ := os.ReadFile(filepath.Join(dir, "hook.log")); !strings.Contains(string(log), "no Device Key") {
		t.Fatalf("log = %s", log)
	}

	t.Setenv("MEOW_HOOK_DISABLED", "1")
	os.Remove(filepath.Join(dir, "hook.log"))
	captureOutput(t, `{nope`, func() { code = run([]string{"hook", "claude-stop"}) })
	if _, err := os.Stat(filepath.Join(dir, "hook.log")); err == nil || code != 0 {
		t.Fatal("disabled hook did work")
	}
}
