package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/cli/internal/setup"
	"meow.blue/meow/client"
)

// meow hook <kind> [--no-summary] [payload] is run
// by the agents' hooks (see "meow setup"). It must never disturb the agent:
// it always exits 0, never writes to stdout or stderr, and records problems
// in hook.log next to the config file instead.

const (
	hookTimeout  = 5 * time.Second
	summaryRunes = 4000
	maxHookLog   = 256 << 10
)

// claudeHookInput is the JSON Claude Code sends on stdin to Stop and Notification hooks.
type claudeHookInput struct {
	SessionTitle         string `json:"session_title"`
	TranscriptPath       string `json:"transcript_path"`
	SessionID            string `json:"session_id"`
	PromptID             string `json:"prompt_id"`
	Cwd                  string `json:"cwd"`
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message"`
	Message              string `json:"message"`
	Title                string `json:"title"`
	NotificationType     string `json:"notification_type"`
	Error                string `json:"error"`
	ErrorDetails         string `json:"error_details"`
}

// codexNotifyPayload is the JSON Codex passes as the last argument to its notify command.
type codexNotifyPayload struct {
	ThreadName           string  `json:"thread-name"`
	Type                 string  `json:"type"`
	ThreadID             string  `json:"thread-id"`
	TurnID               string  `json:"turn-id"`
	Cwd                  string  `json:"cwd"`
	LastAssistantMessage *string `json:"last-assistant-message"`
}

func cmdHook(args []string) (code int) {
	code = exitOK
	defer func() {
		if r := recover(); r != nil {
			hookLog("hook panic recovered")
		}
	}()
	if len(args) == 0 {
		return
	}
	kind, summary, payload := args[0], true, ""
	var forwarded []string
	for _, a := range args[1:] {
		if a == "--no-summary" {
			summary = false
		} else if strings.HasPrefix(a, setup.ForwardNotifyPrefix) {
			if kind != "codex" || forwarded != nil {
				hookLog("invalid duplicate or non-Codex forwarding option")
				return
			}
			var err error
			forwarded, err = setup.DecodeForwardNotify(strings.TrimPrefix(a, setup.ForwardNotifyPrefix))
			if err != nil {
				hookLog("%s", err)
				return
			}
		} else {
			payload = a
		}
	}
	if len(forwarded) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		done := make(chan struct{})
		go func() {
			defer close(done)
			forwardNotify(ctx, forwarded, payload)
		}()
		defer func() { <-done; cancel() }()
	}
	if os.Getenv("MEOW_HOOK_DISABLED") == "1" {
		return
	}

	var input []byte
	switch kind {
	case "claude-stop", "claude-notify", "claude-failure", "codex-stop", "codex-permission":
		var err error
		if input, err = io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1)); err != nil {
			hookLog("%s: read input: %v", kind, err)
			return
		}
	case "codex":
		input = []byte(payload)
	default:
		hookLog("%s: unknown hook", kind)
		return
	}

	if len(input) > 1<<20 {
		hookLog("%s: oversized input", kind)
		return
	}
	req, idem, send, err := buildHookNotification(kind, input, summary)
	if err != nil {
		hookLog("%s: invalid hook input", kind)
		return
	}
	if !send {
		return
	}
	if kind == "codex-stop" {
		if err := recordCodexHookRun(input, summary); err != nil {
			hookLog("codex-stop: cannot record runtime evidence")
		}
		if codexLegacyActive() {
			return
		}
	}
	cfg, err := config.Load()
	if err != nil || cfg.Key == "" {
		hookLog("%s: no Device Key configured; run \"meow login\"", kind)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if idem == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			hookLog("%s: cannot create delivery key", kind)
			return
		}
		idem = "hook-" + hex.EncodeToString(nonce[:])
	}
	if err := sendHook(ctx, client.New(cfg.Server, cfg.Key), req, idem); err != nil {
		// Error messages may contain private response bodies or URLs. Log only status.
		var apiErr *client.Error
		if errors.As(err, &apiErr) {
			hookLog("%s: send failed (HTTP %d)", kind, apiErr.Status)
		} else {
			hookLog("%s: send failed (transport or response error)", kind)
		}
	}
	return
}

// Preserve the raw payload as one final argument and isolate both output streams.
// An external handler's failure must not affect Meow or expose message content.
func forwardNotify(ctx context.Context, argv []string, payload string) {
	command := exec.CommandContext(ctx, argv[0], append(append([]string{}, argv[1:]...), payload)...)
	command.WaitDelay = 250 * time.Millisecond
	if err := command.Run(); err != nil {
		hookLog("original Codex notify failed: %v", err)
	}
}

// buildHookNotification turns an agent's hook input into a notification.
// send is false for events that should not notify.
func buildHookNotification(kind string, input []byte, summary bool) (req client.NotifyRequest, idem string, send bool, err error) {
	if text := strings.TrimSpace(string(input)); !strings.HasPrefix(text, "{") || text == "{}" {
		return req, "", false, fmt.Errorf("invalid hook input: expected event object")
	}
	switch kind {
	case "claude-stop", "claude-notify", "claude-failure":
		var in claudeHookInput
		if err := json.Unmarshal(input, &in); err != nil {
			return req, "", false, fmt.Errorf("invalid hook input: %w", err)
		}
		if in.SessionID == "" && in.Message == "" && in.Error == "" {
			return req, "", false, fmt.Errorf("invalid hook input: missing event fields")
		}
		req.Source = "Claude Code"
		req.Context = hookContext(req.Source, in.Cwd, in.SessionID, in.SessionTitle, in.TranscriptPath)
		if kind == "claude-stop" {
			// stop_hook_active describes a previous continuation, not whether
			// this response is final. A nonblocking notifier must not suppress it.
			req.Title = withProject(hookTitle("result"), in.Cwd)
			if summary {
				req.Body = summarize(in.LastAssistantMessage)
			}
			// One prompt can produce multiple responses after other Stop hooks
			// request more work. Deduplicate the same notification, not the prompt.
			body, _ := json.Marshal(req)
			idem = hookIdemKey("claude-stop", in.SessionID, in.PromptID, string(body))
		} else if kind == "claude-failure" {
			req.Title = withProject(hookTitle("failure"), in.Cwd)
			req.Type, req.Priority = "alert", "high"
			if summary {
				req.Body = summarize(strings.TrimSpace(in.Error + "\n" + in.ErrorDetails))
			}
			body, _ := json.Marshal(req)
			idem = hookIdemKey(kind, in.SessionID, in.PromptID, string(body))
		} else {
			title := strings.TrimSpace(in.Title)
			if title == "" {
				title = hookTitle("attention")
			}
			req.Title = withProject(title, in.Cwd)
			if summary {
				req.Body = summarize(in.Message)
			}
			req.Type, req.Priority = "alert", "high"
		}
	case "codex":
		var p codexNotifyPayload
		if err := json.Unmarshal(input, &p); err != nil {
			return req, "", false, fmt.Errorf("invalid notify payload: %w", err)
		}
		if p.Type != "agent-turn-complete" {
			return req, "", false, nil
		}
		req.Source = "Codex"
		req.Context = hookContext(req.Source, p.Cwd, p.ThreadID, p.ThreadName, "")
		req.Title = withProject(hookTitle("result"), p.Cwd)
		if summary && p.LastAssistantMessage != nil {
			req.Body = summarize(*p.LastAssistantMessage)
		}
		body, _ := json.Marshal(req)
		idem = hookIdemKey("codex", p.ThreadID, p.TurnID, string(body))
	case "codex-stop", "codex-permission":
		var p codexHookInput
		if err := json.Unmarshal(input, &p); err != nil {
			return req, "", false, fmt.Errorf("invalid hook input")
		}
		expected := "Stop"
		if kind == "codex-permission" {
			expected = "PermissionRequest"
		}
		if p.Event != expected || p.SessionID == "" || p.TurnID == "" {
			return req, "", false, fmt.Errorf("missing or mismatched Codex event identity")
		}
		if kind == "codex-stop" && p.isTemporarySuggestion() {
			return req, "", false, nil
		}
		req.Source = "Codex"
		req.Context = hookContext(req.Source, p.Cwd, p.SessionID, p.SessionTitle, "")
		req.Title = withProject(hookTitle("result"), p.Cwd)
		if summary {
			req.Body = summarize(p.LastAssistantMessage)
		}
		body, _ := json.Marshal(req)
		idem = hookIdemKey("codex", p.SessionID, p.TurnID, string(body))
		if kind == "codex-permission" {
			req.Title = withProject(hookTitle("attention"), p.Cwd)
			req.Type, req.Priority = "alert", "high"
			req.Body = ""
			if summary {
				req.Body = summarize(strings.TrimSpace(p.ToolName + "\n" + string(p.ToolInput)))
			}
			// No unique permission-event ID: cmdHook creates one key per invocation,
			// reused across attempts, so repeated legitimate prompts are never suppressed.
			idem = ""
		}
	default:
		return req, "", false, fmt.Errorf("unknown hook %q", kind)
	}
	return req, idem, true, nil
}

// withProject appends the project folder name: "New result · meow-repo".
func withProject(title, cwd string) string {
	if cwd == "" {
		return title
	}
	if base := filepath.Base(cwd); base != "/" && base != "." {
		title += " · " + base
	}
	return truncateRunes(title, 200)
}

// summarize preserves the reply for storage; banner previews are built downstream.
func summarize(s string) string {
	return truncateRunes(strings.TrimSpace(s), summaryRunes)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// hookIdemKey dedupes repeated deliveries of one event; "" when the event
// carries no identifiers.
func hookIdemKey(parts ...string) string {
	for _, p := range parts {
		if p == "" {
			return ""
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "hook-" + hex.EncodeToString(sum[:16])
}

// hookLog appends a line to hook.log. It never logs message content or keys.
func hookLog(format string, args ...any) {
	cfgPath, err := config.Path()
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(cfgPath), "hook.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxHookLog {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// hookTitle deliberately uses an explicit setting: a background process locale
// need not match the phone or desktop user's language.
func hookTitle(kind string) string {
	chinese := strings.HasPrefix(strings.ToLower(os.Getenv("MEOW_LANGUAGE")), "zh")
	switch kind {
	case "attention":
		if chinese {
			return "需要你确认"
		}
		return "Needs your attention"
	case "failure":
		if chinese {
			return "运行出错"
		}
		return "Run failed"
	default:
		if chinese {
			return "有新结果"
		}
		return "New result"
	}
}

type codexHookInput struct {
	SessionTitle         string          `json:"session_title"`
	SessionID            string          `json:"session_id"`
	TurnID               string          `json:"turn_id"`
	TranscriptPath       json.RawMessage `json:"transcript_path"`
	Event                string          `json:"hook_event_name"`
	Cwd                  string          `json:"cwd"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
}

// Codex Desktop generates composer predictions and suggestions in ephemeral
// threads. Stop currently exposes no thread_source/turn_trigger, only a null
// transcript_path for these threads. Require BOTH explicit null and the narrow
// suggestion envelope; ordinary chat JSON, missing metadata, prose and other
// ephemeral results must still notify. Do not infer persistence from file existence.
func (p codexHookInput) isTemporarySuggestion() bool {
	if strings.TrimSpace(string(p.TranscriptPath)) != "null" {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(p.LastAssistantMessage), &object) != nil || len(object) != 1 {
		return false
	}
	if value, ok := object["suggestion"]; ok {
		var suggestion *string
		return json.Unmarshal(value, &suggestion) == nil
	}
	if value, ok := object["suggestions"]; ok && strings.HasPrefix(strings.TrimSpace(string(value)), "[") {
		var suggestions []map[string]json.RawMessage
		if json.Unmarshal(value, &suggestions) != nil {
			return false
		}
		for _, suggestion := range suggestions {
			var title string
			if json.Unmarshal(suggestion["title"], &title) != nil || strings.TrimSpace(title) == "" {
				return false
			}
		}
		return true
	}
	return false
}

func sendHook(ctx context.Context, c *client.Client, req client.NotifyRequest, idem string) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(1<<(attempt-1)) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		call, cancel := context.WithTimeout(ctx, hookTimeout)
		_, err = c.Notify(call, req, idem)
		cancel()
		if err == nil {
			return nil
		}
		var apiErr *client.Error
		var networkErr net.Error
		retry := errors.As(err, &apiErr) && apiErr.Temporary()
		if !errors.As(err, &apiErr) {
			retry = errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		}
		if !retry || ctx.Err() != nil {
			return err
		}
	}
	return err
}
