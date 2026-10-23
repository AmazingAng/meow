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

func TestCodexAutoReviewAttention(t *testing.T) {
	for _, tc := range []struct {
		name, reviewer, tool, session, turn string
		wantSend                            bool
	}{
		{"auto shell", "auto_review", "Bash", "s", "t", false},
		{"auto patch", "auto_review", "apply_patch", "s", "t", false},
		{"auto edit alias", "auto_review", "Edit", "s", "t", false},
		{"auto write alias", "auto_review", "Write", "s", "t", false},
		{"auto stdin", "auto_review", "write_stdin", "s", "t", false},
		{"auto permissions", "auto_review", "request_permissions", "s", "t", false},
		{"human", "user", "Bash", "s", "t", true},
		{"missing reviewer", "", "Bash", "s", "t", true},
		{"unknown reviewer", "future", "Bash", "s", "t", true},
		{"other session", "auto_review", "Bash", "other", "t", true},
		{"other turn", "auto_review", "Bash", "s", "other", true},
		{"MCP can require human", "auto_review", "mcp__computer__click", "s", "t", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			data := `{"type":"session_meta","payload":{"id":"` + tc.session + `"}}` + "\n" +
				`{"type":"turn_context","payload":{"turn_id":"` + tc.turn + `","approvals_reviewer":"` + tc.reviewer + `"}}` + "\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			input := codexApprovalInput(t, path, tc.tool)
			_, _, send, err := buildHookNotification("codex-permission", input, true)
			if err != nil || send != tc.wantSend {
				t.Fatalf("send=%v err=%v", send, err)
			}
			var p map[string]any
			json.Unmarshal(input, &p)
			p["hook_event_name"], p["last_assistant_message"] = "Stop", "Completed"
			stop, _ := json.Marshal(p)
			if _, _, send, err := buildHookNotification("codex-stop", stop, true); err != nil || !send {
				t.Fatalf("result suppressed: %v", err)
			}
		})
	}
}

func codexApprovalInput(t *testing.T, path, tool string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"session_id": "s", "turn_id": "t", "hook_event_name": "PermissionRequest", "transcript_path": path, "tool_name": tool, "tool_input": map[string]string{"description": "Allow this operation?"}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCodexApprovalMetadataFailsOpen(t *testing.T) {
	header := `{"type":"session_meta","payload":{"id":"s"}}` + "\n"
	auto := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"auto_review"}}` + "\n"
	human := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"user"}}` + "\n"
	for name, data := range map[string]string{
		"malformed":                "{broken\n",
		"missing identity":         auto,
		"newer human context":      header + auto + human,
		"newer unknown context":    header + auto + `{"type":"turn_context","payload":{"turn_id":"t"}}` + "\n",
		"newer other turn":         header + auto + `{"type":"turn_context","payload":{"turn_id":"other","approvals_reviewer":"auto_review"}}` + "\n",
		"newer incomplete context": header + auto + `{"type":"turn_context","payload":`,
		"truncated metadata":       header + strings.TrimSuffix(auto, "}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, send, err := buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || !send {
				t.Fatalf("send=%v err=%v", send, err)
			}
		})
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if _, _, send, err := buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || !send {
			t.Fatalf("path=%q send=%v err=%v", path, send, err)
		}
	}
}

func TestCodexAutoReviewLargeTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	// Disk defaults deliberately disagree with this turn. Only the event's
	// rollout is authoritative, including after switching modes mid-session.
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("approvals_reviewer = 'user'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout.jsonl")
	header := `{"type":"session_meta","payload":{"id":"s"}}` + "\n"
	data := header + strings.Repeat("{\"type\":\"unrelated\"}\n", 110000) +
		`{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"user"}}` + "\n" +
		`{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"auto_review"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, send, err := buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || send {
		t.Fatalf("send=%v err=%v", send, err)
	}
}

// A long active turn can push its context far outside the metadata tail. Image
// and tool records can also exceed that tail by themselves (the reported turn
// contained a 5 MiB record). Neither should turn auto review into an alert.
func TestCodexAutoReviewLongActiveTurn(t *testing.T) {
	header := `{"type":"session_meta","payload":{"id":"s"}}` + "\n"
	auto := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"auto_review"}}` + "\n"
	human := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"user"}}` + "\n"
	largeRecord := `{"type":"response_item","payload":{"text":"` + strings.Repeat("x", 6<<20) + `"}}` + "\n"
	longTurn := strings.Repeat("{\"type\":\"unrelated\"}\n", 450000)
	for _, tc := range []struct {
		name, data string
		wantSend   bool
	}{
		{"many records after context", header + auto + longTurn, false},
		{"large record after context", header + auto + largeRecord, false},
		{"large record without final newline", header + auto + strings.TrimSuffix(largeRecord, "\n"), false},
		{"latest human before long output", header + auto + human + longTurn, true},
		{"latest auto before long output", header + human + auto + longTurn, false},
		{"newer other turn before long output", header + auto + strings.Replace(auto, `"t"`, `"other"`, 1) + largeRecord, true},
		{"unknown reviewer before long output", header + auto + strings.Replace(auto, "auto_review", "future", 1) + largeRecord, true},
		{"corrupt record before long output", header + auto + "{broken\n" + longTurn, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, send, err := buildHookNotification("codex-permission", codexApprovalInput(t, path, "Bash"), true); err != nil || send != tc.wantSend {
				t.Fatalf("send=%v want=%v err=%v", send, tc.wantSend, err)
			}
		})
	}
}

func TestCodexAutoReviewDoesNotReachServer(t *testing.T) {
	var events []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n struct {
			Event string `json:"event"`
		}
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			t.Error(err)
		}
		events = append(events, n.Event)
		io.WriteString(w, `{"id":"ntf_test"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow.json"))
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("MEOW_SERVER", server.URL)
	t.Setenv("MEOW_KEY", "meow_test")
	t.Setenv("MEOW_HOOK_DISABLED", "")
	path := filepath.Join(dir, "rollout.jsonl")
	header := `{"type":"session_meta","payload":{"id":"s"}}` + "\n"
	for _, reviewer := range []string{"auto_review", "user"} {
		data := header + `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"` + reviewer + `"}}` + "\n" +
			`{"type":"response_item","payload":{"text":"` + strings.Repeat("x", 6<<20) + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		out := captureOutput(t, string(codexApprovalInput(t, path, "Bash")), func() {
			if cmdHook([]string{"codex-permission"}) != 0 {
				t.Error("nonzero exit")
			}
		})
		if out != "" {
			t.Fatalf("hook affected agent: %q", out)
		}
		if reviewer == "auto_review" && len(events) != 0 {
			t.Fatal("auto-review reached server")
		}
	}
	if len(events) != 1 || events[0] != "attention" {
		t.Fatalf("events=%v", events)
	}
}

func TestRolloutReverseReaderBoundaries(t *testing.T) {
	for _, n := range []int{1, approvalReadBlock - 1, approvalReadBlock, approvalReadBlock + 1, 2*approvalReadBlock + 7} {
		for _, suffix := range []string{"", "\n"} {
			lines := []string{"header", strings.Repeat("x", n), "", "context", strings.Repeat("y", n+3)}
			data := strings.Join(lines, "\n") + suffix
			reader := rolloutReverseReader{file: strings.NewReader(data), offset: int64(len(data))}
			if suffix != "" {
				line, err := reader.previousLine()
				if err != nil || len(line) != 0 {
					t.Fatalf("trailing newline: len=%d err=%v", len(line), err)
				}
			}
			for i := len(lines) - 1; i >= 0; i-- {
				line, err := reader.previousLine()
				if err != nil || string(line) != lines[i] {
					t.Fatalf("n=%d line=%d: len=%d err=%v", n, i, len(line), err)
				}
			}
			if _, err := reader.previousLine(); err != io.EOF {
				t.Fatalf("expected EOF, got %v", err)
			}
		}
	}
}

func TestRolloutReverseReaderRejectsOversizedOrTruncatedRecords(t *testing.T) {
	data := strings.Repeat("x", approvalRecordLimit+1)
	reader := rolloutReverseReader{file: strings.NewReader(data), offset: int64(len(data))}
	if _, err := reader.previousLine(); err != io.ErrShortBuffer {
		t.Fatalf("expected record limit, got %v", err)
	}
	reader = rolloutReverseReader{file: strings.NewReader("short"), offset: 100}
	if _, err := reader.previousLine(); err != io.EOF {
		t.Fatalf("expected truncated file read error, got %v", err)
	}
}
