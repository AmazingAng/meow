package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONNotificationInput(t *testing.T) {
	for _, stdin := range []bool{false, true} {
		t.Run(fmt.Sprint(stdin), func(t *testing.T) {
			t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("MEOW_KEY", "meow_test")
			calls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var n map[string]any
				if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
					t.Error(err)
				}
				if n["title"] != "Build completed" || n["source"] != "CI" || n["context"].(map[string]any)["session_id"] != "run-1" {
					t.Errorf("notification=%v", n)
				}
				if r.Header.Get("Idempotency-Key") != "ci-run-1" {
					t.Error("missing supplied idempotency key")
				}
				fmt.Fprint(w, `{"id":"ntf_test"}`)
			}))
			defer ts.Close()
			t.Setenv("MEOW_SERVER", ts.URL)
			payload := `{"title":"Build completed","body":"All tests passed","source":"CI","context":{"workspace":"project","session_id":"run-1"}}`
			args := []string{"--json", "--quiet", "--idempotency-key", "ci-run-1"}
			input := ""
			if stdin {
				input = payload
			} else {
				args = append(args, payload)
			}
			captureOutput(t, input, func() {
				if code := cmdSend(args); code != exitOK {
					t.Errorf("send exit=%d", code)
				}
			})
			if calls != 1 {
				t.Fatalf("HTTP calls=%d", calls)
			}
		})
	}
}

func TestJSONNotificationRejectsInvalidInputsWithoutSending(t *testing.T) {
	t.Setenv("MEOW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("MEOW_KEY", "meow_test")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"id":"unexpected"}`)
	}))
	defer server.Close()
	t.Setenv("MEOW_SERVER", server.URL)
	for _, args := range [][]string{
		{"--json", "null"}, {"--json", "[]"}, {"--json", `{"title":`}, {"--json", `{"title":"x","type":"question"}`},
		{"--json", "--title", "override", `{"title":"x"}`}, {"--json", "--priority", "normal", `{"title":"x"}`},
		{"--json", "--idempotency-key", "bad key", `{"title":"x"}`}, {"--json", strings.Repeat(" ", 65537)},
	} {
		captureOutput(t, "", func() {
			if code := cmdSend(args); code != exitUsage {
				t.Errorf("invalid JSON input exit=%d", code)
			}
		})
	}
	captureOutput(t, strings.Repeat(" ", 65537), func() {
		if code := cmdSend([]string{"--json"}); code != exitUsage {
			t.Errorf("oversized stdin exit=%d", code)
		}
	})
	if calls != 0 {
		t.Fatalf("invalid inputs made %d HTTP calls", calls)
	}
}
