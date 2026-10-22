package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"meow.blue/meow/message"
)

func TestSendEventFlagsAndJSON(t *testing.T) {
	for _, event := range []string{"result", "attention", "failure"} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(fmt.Sprint(event, jsonMode), func(t *testing.T) {
				t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "meow.json"))
				t.Setenv("MEOW_KEY", "meow_test")
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var n message.Notification
					if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
						t.Error(err)
					}
					if string(n.Event) != event || n.Title != "自定义标题" || n.Type != "info" || n.Priority != "low" {
						t.Errorf("request=%+v", n)
					}
					fmt.Fprint(w, `{"id":"ntf_done"}`)
				}))
				defer server.Close()
				t.Setenv("MEOW_SERVER", server.URL)
				input := ""
				args := []string{"send"}
				if jsonMode {
					args = []string{"--json", "--quiet"}
					input = fmt.Sprintf(`{"event":%q,"title":"自定义标题","priority":"low"}`, event)
				} else {
					args = []string{"--event", event, "--priority", "low", "--quiet", "自定义标题"}
				}
				captureOutput(t, input, func() {
					if code := cmdSend(args); code != exitOK {
						t.Errorf("exit=%d", code)
					}
				})
				if calls != 1 {
					t.Fatalf("requests=%d", calls)
				}
				captureOutput(t, "", func() {
					if code := cmdSend([]string{"--event", "success", "title"}); code == exitOK {
						t.Error("unknown event accepted")
					}
				})
				captureOutput(t, `{"title":"x"}`, func() {
					if code := cmdSend([]string{"--json", "--event", event}); code == exitOK {
						t.Error("JSON mixed with event flag accepted")
					}
				})
				if calls != 1 {
					t.Fatalf("invalid input reached server: %d", calls)
				}
			})
		}
	}
}
