package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type cursorCapturedRequest struct {
	Body    string          `json:"body"`
	Event   string          `json:"event"`
	Context json.RawMessage `json:"context"`
	Key     string          `json:"-"`
}

func cursorHarness(t *testing.T) (string, *[]cursorCapturedRequest) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MEOW_CONFIG", filepath.Join(home, "config.json"))
	t.Setenv("MEOW_KEY", "meow_disposable_cursor_test")
	t.Setenv("MEOW_HOOK_DISABLED", "0")
	t.Setenv("MEOW_LANGUAGE", "en")
	requests := new([]cursorCapturedRequest)
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cursorCapturedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		req.Key = r.Header.Get("Idempotency-Key")
		mu.Lock()
		*requests = append(*requests, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"ntf_fixture"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("MEOW_SERVER", server.URL)
	return home, requests
}

func cursorInput(kind, conversation, generation, text, transcript string) string {
	event := map[string]any{"conversation_id": conversation, "generation_id": generation, "workspace_roots": []string{"/work/project"}, "transcript_path": transcript}
	if kind == "cursor-response" {
		event["hook_event_name"] = "afterAgentResponse"
		event["text"] = text
	} else {
		event["hook_event_name"] = "stop"
		event["status"] = "completed"
	}
	b, _ := json.Marshal(event)
	return string(b)
}

func cursorInvoke(t *testing.T, kind, input string, summary bool) {
	t.Helper()
	args := []string{kind}
	if !summary {
		args = append(args, "--no-summary")
	}
	output := captureOutput(t, input, func() {
		if code := cmdHook(args); code != 0 {
			t.Errorf("hook exit %d", code)
		}
	})
	if output != "" {
		t.Fatalf("hook exposed output: %q", output)
	}
}

func TestCursorGenerationSnapshotsAndReplay(t *testing.T) {
	home, requests := cursorHarness(t)
	path := filepath.Join(home, "transcript.jsonl")
	if err := os.WriteFile(path, []byte("{\"role\":\"assistant\",\"message\":{\"content\":\"unrelated transcript reply\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	response1 := cursorInput("cursor-response", "chat", "g1", "First answer", path)
	stop1 := cursorInput("cursor-stop", "chat", "g1", "", path)
	cursorInvoke(t, "cursor-response", response1, true)
	if len(*requests) != 0 {
		t.Fatal("capture sent a notification")
	}
	cursorInvoke(t, "cursor-stop", stop1, true)
	if len(*requests) != 1 || (*requests)[0].Body != "First answer" {
		t.Fatalf("first reply: %+v", *requests)
	}
	first := (*requests)[0]
	cursorInvoke(t, "cursor-response", cursorInput("cursor-response", "chat", "g2", "Second answer", path), true)
	cursorInvoke(t, "cursor-response", cursorInput("cursor-response", "chat", "g1", "late replacement", path), true)
	cursorInvoke(t, "cursor-stop", stop1, true)
	if len(*requests) != 2 || (*requests)[1].Body != first.Body || (*requests)[1].Key != first.Key || string((*requests)[1].Context) != string(first.Context) {
		t.Fatalf("replay changed: %+v", *requests)
	}
	cursorInvoke(t, "cursor-stop", cursorInput("cursor-stop", "chat", "g2", "", path), true)
	if len(*requests) != 3 || (*requests)[2].Body != "Second answer" || (*requests)[2].Key == first.Key {
		t.Fatalf("second generation: %+v", *requests)
	}
}

func TestCursorMissingSnapshotFreezesGenericNotification(t *testing.T) {
	home, requests := cursorHarness(t)
	path := filepath.Join(home, "transcript.jsonl")
	if err := os.WriteFile(path, []byte("{\"role\":\"assistant\",\"message\":{\"content\":\"Must not read\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stop := cursorInput("cursor-stop", "chat", "missing", "", path)
	cursorInvoke(t, "cursor-stop", stop, true)
	cursorInvoke(t, "cursor-response", cursorInput("cursor-response", "chat", "missing", "Arrived late", path), true)
	cursorInvoke(t, "cursor-stop", stop, true)
	if len(*requests) != 2 || (*requests)[0].Body != "" || (*requests)[1].Body != "" || (*requests)[0].Key != (*requests)[1].Key {
		t.Fatalf("missing snapshot: %+v", *requests)
	}
}

func TestCursorSnapshotIsolatedByConversationAndRegistration(t *testing.T) {
	_, requests := cursorHarness(t)
	cursorInvoke(t, "cursor-response", cursorInput("cursor-response", "chat-A", "shared-generation", "A secret", ""), true)
	cursorInvoke(t, "cursor-stop", cursorInput("cursor-stop", "chat-B", "shared-generation", "", ""), true)
	if len(*requests) != 1 || (*requests)[0].Body != "" {
		t.Fatalf("cross conversation: %+v", *requests)
	}
	t.Setenv("MEOW_KEY", "meow_another_disposable_registration")
	cursorInvoke(t, "cursor-stop", cursorInput("cursor-stop", "chat-A", "shared-generation", "", ""), true)
	if len(*requests) != 2 || (*requests)[1].Body != "" {
		t.Fatalf("cross registration: %+v", *requests)
	}
}

func TestCursorNoSummaryAndIgnoredStops(t *testing.T) {
	home, requests := cursorHarness(t)
	cursorInvoke(t, "cursor-response", cursorInput("cursor-response", "chat", "g", "Private omitted text", ""), false)
	cursorInvoke(t, "cursor-stop", cursorInput("cursor-stop", "chat", "g", "", ""), false)
	if len(*requests) != 1 || (*requests)[0].Body != "" {
		t.Fatalf("no-summary: %+v", *requests)
	}
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			b, _ := os.ReadFile(path)
			if strings.Contains(string(b), "Private omitted text") {
				t.Errorf("omitted text saved in %s", path)
			}
		}
		return nil
	})
	for _, status := range []string{"aborted", "future"} {
		var event map[string]any
		_ = json.Unmarshal([]byte(cursorInput("cursor-stop", "chat", "ignored", "", "")), &event)
		event["status"] = status
		b, _ := json.Marshal(event)
		cursorInvoke(t, "cursor-stop", string(b), true)
	}
	if len(*requests) != 1 {
		t.Fatalf("ignored stop sent: %+v", *requests)
	}
}

func TestCursorSetupCapturesResponseAndPreservesForeignHooks(t *testing.T) {
	home, _ := cursorHarness(t)
	t.Setenv("MEOW_EXECUTABLE", "/opt/meow")
	p := filepath.Join(home, ".cursor", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"version":1,"hooks":{"afterAgentResponse":[{"command":"echo keep-response"}],"stop":[{"command":"echo keep-stop"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	apply := func(args ...string) {
		captureOutput(t, "", func() {
			if code := cmdSetup(append([]string{"cursor", "--yes", "--no-test"}, args...)); code != 0 {
				t.Errorf("setup exit %d", code)
			}
		})
	}
	apply()
	b, _ := os.ReadFile(p)
	if strings.Count(string(b), " hook cursor-response") != 1 || strings.Count(string(b), " hook cursor-stop") != 1 {
		t.Fatalf("missing paired hooks: %s", b)
	}
	apply()
	b, _ = os.ReadFile(p)
	if strings.Count(string(b), " hook cursor-response") != 1 {
		t.Fatalf("duplicate response hook: %s", b)
	}
	apply("--no-summary")
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), " hook cursor-response") || !strings.Contains(string(b), "--no-summary") {
		t.Fatalf("no-summary captures text: %s", b)
	}
	apply("--uninstall")
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), " hook cursor-") || !strings.Contains(string(b), "keep-response") || !strings.Contains(string(b), "keep-stop") {
		t.Fatalf("foreign hooks not preserved: %s", b)
	}
}

func TestCursorInvalidCaptureDiagnosticIsPrivate(t *testing.T) {
	home, requests := cursorHarness(t)
	input := `{"hook_event_name":"wrong","conversation_id":"chat","generation_id":"g","text":"private diagnostic marker"}`
	cursorInvoke(t, "cursor-response", input, true)
	if len(*requests) != 0 {
		t.Fatal("invalid capture sent")
	}
	log, err := os.ReadFile(filepath.Join(home, "hook.log"))
	if err != nil || !strings.Contains(string(log), "cursor-response: snapshot unavailable") {
		t.Fatalf("missing diagnostic: %s %v", log, err)
	}
	if strings.Contains(string(log), "private diagnostic marker") || strings.Contains(string(log), "meow_disposable_cursor_test") {
		t.Fatal("diagnostic exposed private values")
	}
}
