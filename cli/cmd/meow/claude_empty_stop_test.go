package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestClaudeEmptyStopsNeverSend(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ntf_test"}`))
	}))
	defer server.Close()
	t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MEOW_SERVER", server.URL)
	t.Setenv("MEOW_KEY", "meow_disposable_test")
	for _, input := range []string{
		`{"session_id":"same-session","hook_event_name":"Stop"}`,
		`{"session_id":"same-session","hook_event_name":"Stop","last_assistant_message":null}`,
		`{"session_id":"same-session","hook_event_name":"Stop","prompt_id":"new-prompt","last_assistant_message":""}`,
		`{"session_id":"same-session","hook_event_name":"Stop","last_assistant_message":" \n\t\u3000"}`,
	} {
		for _, summary := range []bool{true, false} {
			_, key, send, err := buildHookNotification("claude-stop", []byte(input), summary)
			if err != nil || send || key != "" {
				t.Errorf("empty Stop sent: summary=%v send=%v key=%q err=%v", summary, send, key, err)
			}
		}
		for range 2 {
			output := captureOutput(t, input, func() {
				if code := cmdHook([]string{"claude-stop"}); code != exitOK {
					t.Errorf("hook exit=%d", code)
				}
			})
			if output != "" {
				t.Errorf("ignored hook wrote output: %q", output)
			}
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("empty stops created %d HTTP notifications", got)
	}
}

func TestClaudeMeaningfulStopStillSendsWithNoSummary(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ntf_test"}`))
	}))
	defer server.Close()
	t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MEOW_SERVER", server.URL)
	t.Setenv("MEOW_KEY", "meow_disposable_test")
	input := `{"session_id":"same-session","hook_event_name":"Stop","last_assistant_message":"Done."}`
	captureOutput(t, input, func() {
		if code := cmdHook([]string{"claude-stop", "--no-summary"}); code != exitOK {
			t.Errorf("hook exit=%d", code)
		}
	})
	if body == nil || body["event"] != "result" || (body["body"] != nil && body["body"] != "") {
		t.Fatalf("real result with hidden summary must still notify: %v", body)
	}
}
