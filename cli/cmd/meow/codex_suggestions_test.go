package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func suggestionEvent(t *testing.T, event, body string, transcript any, includeTranscript bool) []byte {
	t.Helper()
	p := map[string]any{"session_id": "session", "turn_id": "turn", "hook_event_name": event, "last_assistant_message": body}
	if includeTranscript {
		p["transcript_path"] = transcript
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCodexTemporarySuggestionsAreSilent(t *testing.T) {
	for _, body := range []string{
		`{"suggestion":"收到了，自动推送正常了"}`,
		`{"suggestion":null}`,
		` { "suggestion": "" } `,
		`{"suggestions":[]}`,
		`{"suggestions":[{"title":"Next step","description":"Check the result","prompt":"Review the change"}]}`,
		`{"exclude":[]}`,
		`{"exclude":[{"id":"candidate-1","reason":"Excluded by the suggestion safety check"}]}`,
	} {
		for _, summary := range []bool{true, false} {
			_, _, send, err := buildHookNotification("codex-stop", suggestionEvent(t, "Stop", body, nil, true), summary)
			if err != nil || send {
				t.Errorf("body=%s summary=%v send=%v err=%v", body, summary, send, err)
			}
		}
	}
}

func TestCodexSuggestionFilterPreservesRealResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		transcript any
		include    bool
	}{
		{"persistent suggestion JSON", `{"suggestion":"Do this"}`, "/sessions/main.jsonl", true},
		{"persistent suggestions JSON", `{"suggestions":[]}`, "/sessions/main.jsonl", true},
		{"persistent exclusion JSON", `{"exclude":[]}`, "/sessions/main.jsonl", true},
		{"persistent safety-shaped JSON", `{"exclude":[{"id":"cache","reason":"Not used"}]}`, "/sessions/main.jsonl", true},
		{"older host with no field", `{"suggestion":"Do this"}`, nil, false},
		{"exclusion JSON with no metadata", `{"exclude":[]}`, nil, false},
		{"exclusion JSON with empty path", `{"exclude":[]}`, "", true},
		{"empty path is not explicit null", `{"suggestion":"Do this"}`, "", true},
		{"temporary normal JSON", `{"status":"done","count":2}`, nil, true},
		{"temporary prose", "The suggestion is to deploy.", nil, true},
		{"additional fields", `{"suggestion":"deploy","result":"passed"}`, nil, true},
		{"unrecognized type", `{"suggestion":{"status":"done"}}`, nil, true},
		{"not suggestion records", `{"suggestions":[1,2]}`, nil, true},
		{"truncated JSON", `{"suggestion":"unfinished`, nil, true},
		{"Markdown example", "```json\n{\"suggestion\":\"example\"}\n```", nil, true},
		{"exclusions with additional top-level fields", `{"exclude":[],"result":"passed"}`, nil, true},
		{"null exclusions", `{"exclude":null}`, nil, true},
		{"wrong exclusion type", `{"exclude":{}}`, nil, true},
		{"primitive exclusion items", `{"exclude":[1]}`, nil, true},
		{"incomplete exclusion item", `{"exclude":[{"id":"cache"}]}`, nil, true},
		{"empty exclusion reason", `{"exclude":[{"id":"cache","reason":" "}]}`, nil, true},
		{"empty exclusion id", `{"exclude":[{"id":"","reason":"Not used"}]}`, nil, true},
		{"numeric exclusion id", `{"exclude":[{"id":1,"reason":"Not used"}]}`, nil, true},
		{"invalid exclusion reason", `{"exclude":[{"id":"cache","reason":false}]}`, nil, true},
		{"extra exclusion item field", `{"exclude":[{"id":"cache","reason":"Not used","result":"passed"}]}`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, key, send, err := buildHookNotification("codex-stop", suggestionEvent(t, "Stop", tc.body, tc.transcript, tc.include), true)
			if err != nil || !send || key == "" || req.Body != strings.TrimSpace(tc.body) {
				t.Fatalf("send=%v key=%q body=%q err=%v", send, key, req.Body, err)
			}
		})
	}
	for _, kind := range []string{"codex-permission", "claude-stop"} {
		event := "Stop"
		if kind == "codex-permission" {
			event = "PermissionRequest"
		}
		for _, body := range []string{`{"suggestion":"Allow?"}`, `{"exclude":[]}`} {
			_, _, send, err := buildHookNotification(kind, suggestionEvent(t, event, body, nil, true), true)
			if err != nil || !send {
				t.Fatalf("%s body=%s was filtered: %v", kind, body, err)
			}
		}
	}
}

func TestCodexSuggestionDoesNotReachServer(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		io.WriteString(w, `{"id":"ntf_result"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("MEOW_SERVER", server.URL)
	t.Setenv("MEOW_KEY", "meow_test")
	t.Setenv("MEOW_HOOK_DISABLED", "")
	// A running Codex can retain the old notify config after upgrading. Stop
	// must deliver actual results anyway, while filtering ephemeral suggestions.
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`notify = ["meow", "hook", "codex"]`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, event := range [][]byte{
		suggestionEvent(t, "Stop", "Actual result", "/sessions/main.jsonl", true),
		suggestionEvent(t, "Stop", `{"suggestion":"PRIVATE_SUGGESTION"}`, nil, true),
		suggestionEvent(t, "Stop", `{"exclude":[]}`, nil, true),
		suggestionEvent(t, "Stop", `{"exclude":[{"id":"PRIVATE_ID","reason":"PRIVATE_SAFETY_REASON"}]}`, nil, true),
	} {
		out := captureOutput(t, string(event), func() {
			if cmdHook([]string{"codex-stop"}) != 0 {
				t.Error("nonzero exit")
			}
		})
		if out != "" {
			t.Fatalf("hook output: %q", out)
		}
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "Actual result") {
		t.Fatalf("notifications: %v", bodies)
	}
}
