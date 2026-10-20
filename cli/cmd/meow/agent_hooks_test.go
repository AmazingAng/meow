package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"meow.blue/meow/client"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookFullBodyAndNewEvents(t *testing.T) {
	t.Setenv("MEOW_LANGUAGE", "en")
	body := strings.Repeat("正文\n", 100) + "important tail"
	for _, kind := range []string{"claude-stop", "codex-stop"} {
		input, _ := json.Marshal(map[string]any{"session_id": "s", "turn_id": "t", "prompt_id": "p", "hook_event_name": "Stop", "last_assistant_message": body})
		req, _, send, err := buildHookNotification(kind, input, true)
		if err != nil || !send || req.Title != "New result" || req.Body != body {
			t.Fatalf("%s: %+v %v %v", kind, req, send, err)
		}
	}
	for _, tc := range []struct{ kind, input, title, body string }{
		{"claude-failure", `{"session_id":"s","prompt_id":"p","error":"rate_limit","error_details":"Try later"}`, "Run failed", "rate_limit\nTry later"},
		{"codex-permission", `{"session_id":"s","turn_id":"t","hook_event_name":"PermissionRequest","tool_name":"shell","tool_input":{"command":"git push"}}`, "Needs your attention", "shell\n{\"command\":\"git push\"}"},
	} {
		req, _, send, err := buildHookNotification(tc.kind, []byte(tc.input), true)
		if err != nil || !send || req.Title != tc.title || req.Body != tc.body || req.Priority != "high" {
			t.Fatalf("%s: %+v %v", tc.kind, req, err)
		}
	}
	t.Setenv("MEOW_LANGUAGE", "zh-CN")
	req, _, _, _ := buildHookNotification("claude-stop", []byte(`{"session_id":"s","prompt_id":"p"}`), true)
	if req.Title != "有新结果" {
		t.Fatal(req.Title)
	}
}

func TestHookRetriesFrozenPayload(t *testing.T) {
	for _, tc := range []struct {
		status   int
		code     string
		attempts int
	}{{503, "unavailable", 3}, {429, "rate_limited", 3}, {429, "quota_exceeded", 1}, {401, "unauthorized", 1}, {400, "invalid_request", 1}} {
		t.Run(fmt.Sprint(tc.status, tc.code), func(t *testing.T) {
			count := 0
			firstKey, firstBody := "", ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				key := r.Header.Get("Idempotency-Key")
				count++
				if count == 1 {
					firstKey, firstBody = key, string(b)
				}
				if key == "" || key != firstKey || string(b) != firstBody {
					t.Errorf("unstable retry key/body")
				}
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"SECRET_SERVER_TEXT"}}`, tc.code)
			}))
			defer server.Close()
			dir := t.TempDir()
			t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
			t.Setenv("MEOW_SERVER", server.URL)
			t.Setenv("MEOW_KEY", "meow_test")
			out := captureOutput(t, `{"message":"private","notification_type":"permission_prompt"}`, func() {
				if cmdHook([]string{"claude-notify"}) != 0 {
					t.Error("nonzero")
				}
			})
			if out != "" || count != tc.attempts {
				t.Fatalf("count=%d output=%q", count, out)
			}
			log, _ := os.ReadFile(filepath.Join(dir, "hook.log"))
			if strings.Contains(string(log), "SECRET_SERVER_TEXT") {
				t.Fatal("leaked server content")
			}
		})
	}
}

func TestSetupBacksUpCustomizedLegacySkill(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("MEOW_KEY", "meow_test")
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")
	path := filepath.Join(dir, "skills", "meow", "SKILL.md")
	os.MkdirAll(filepath.Dir(path), 0700)
	data := []byte("<!-- meow-skill: legacy -->\nUser custom content\n")
	os.WriteFile(path, data, 0600)
	captureOutput(t, "", func() {
		if cmdSetup([]string{"claude", "--yes", "--no-test"}) != 0 {
			t.Error("setup failed")
		}
	})
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 1 {
		t.Fatalf("backups=%v", backups)
	}
	got, _ := os.ReadFile(backups[0])
	if string(got) != string(data) {
		t.Fatal("backup lost user content")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("legacy file remains")
	}
}

func TestHookRejectsNonObjectInput(t *testing.T) {
	for _, input := range []string{"null", "[]", "\"hello\"", "{}"} {
		if _, _, send, err := buildHookNotification("claude-stop", []byte(input), true); err == nil || send {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestHookTransportRetryAfterCommittedRequest(t *testing.T) {
	var keys []string
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(b))
		if len(keys) == 1 {
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		w.WriteHeader(200)
		io.WriteString(w, `{"id":"ntf_once"}`)
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := sendHook(ctx, client.New(ts.URL, "meow_test"), client.NotifyRequest{Title: "Result", Body: "entire\nreply"}, "event-key"); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != keys[1] || bodies[0] != bodies[1] {
		t.Fatalf("retries %+v %+v", keys, bodies)
	}
}

func TestHookRetryBudget(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := sendHook(ctx, client.New(ts.URL, "meow_test"), client.NotifyRequest{Title: "Result"}, "key"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("budget ignored during backoff")
	}
}

func TestCodexContinuationUsesDistinctDeliveryKey(t *testing.T) {
	first := []byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop","last_assistant_message":"First"}`)
	second := []byte(`{"session_id":"s","turn_id":"t","hook_event_name":"Stop","last_assistant_message":"Final"}`)
	_, a, _, err := buildHookNotification("codex-stop", first, true)
	if err != nil {
		t.Fatal(err)
	}
	_, b, _, err := buildHookNotification("codex-stop", second, true)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || a == "" || b == "" {
		t.Fatal("changed continuation reuses key and would get idempotency_mismatch")
	}
	_, again, _, _ := buildHookNotification("codex-stop", second, true)
	if again != b {
		t.Fatal("same delivery is not stable")
	}
}
