package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/client"
)

// meow hook <claude-stop|claude-notify|codex> [--no-summary] [payload] is run
// by the agents' hooks (see "meow setup"). It must never disturb the agent:
// it always exits 0, never writes to stdout or stderr, and records problems
// in hook.log next to the config file instead.

const (
	hookTimeout  = 5 * time.Second
	summaryRunes = 200
	maxHookLog   = 256 << 10
)

// claudeHookInput is the JSON Claude Code sends on stdin to Stop and Notification hooks.
type claudeHookInput struct {
	SessionID            string `json:"session_id"`
	PromptID             string `json:"prompt_id"`
	Cwd                  string `json:"cwd"`
	StopHookActive       bool   `json:"stop_hook_active"`
	LastAssistantMessage string `json:"last_assistant_message"`
	Message              string `json:"message"`
	Title                string `json:"title"`
	NotificationType     string `json:"notification_type"`
}

// codexNotifyPayload is the JSON Codex passes as the last argument to its notify command.
type codexNotifyPayload struct {
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
			hookLog("panic: %v", r)
		}
	}()
	if len(args) == 0 || os.Getenv("MEOW_HOOK_DISABLED") == "1" {
		return
	}
	kind, summary, payload := args[0], true, ""
	for _, a := range args[1:] {
		if a == "--no-summary" {
			summary = false
		} else {
			payload = a
		}
	}

	var input []byte
	switch kind {
	case "claude-stop", "claude-notify":
		var err error
		if input, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20)); err != nil {
			hookLog("%s: read input: %v", kind, err)
			return
		}
	case "codex":
		input = []byte(payload)
	default:
		hookLog("%s: unknown hook", kind)
		return
	}

	req, idem, send, err := buildHookNotification(kind, input, summary)
	if err != nil {
		hookLog("%s: %v", kind, err)
		return
	}
	if !send {
		return
	}
	cfg, err := config.Load()
	if err != nil || cfg.Key == "" {
		hookLog("%s: no Device Key configured; run \"meow login\"", kind)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	if _, err := client.New(cfg.Server, cfg.Key).Notify(ctx, req, idem); err != nil {
		hookLog("%s: send failed: %v", kind, err)
	}
	return
}

// buildHookNotification turns an agent's hook input into a notification.
// send is false for events that should not notify.
func buildHookNotification(kind string, input []byte, summary bool) (req client.NotifyRequest, idem string, send bool, err error) {
	switch kind {
	case "claude-stop", "claude-notify":
		var in claudeHookInput
		if err := json.Unmarshal(input, &in); err != nil {
			return req, "", false, fmt.Errorf("invalid hook input: %w", err)
		}
		req.Source = "Claude Code"
		if kind == "claude-stop" {
			// stop_hook_active describes a previous continuation, not whether
			// this response is final. A nonblocking notifier must not suppress it.
			req.Title = withProject("Task complete", in.Cwd)
			if summary {
				req.Body = summarize(in.LastAssistantMessage)
			}
			// One prompt can produce multiple responses after other Stop hooks
			// request more work. Deduplicate the same notification, not the prompt.
			body, _ := json.Marshal(req)
			idem = hookIdemKey("claude-stop", in.SessionID, in.PromptID, string(body))
		} else {
			title := strings.TrimSpace(in.Title)
			if title == "" {
				title = "Needs your attention"
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
		req.Title = withProject("Task complete", p.Cwd)
		if summary && p.LastAssistantMessage != nil {
			req.Body = summarize(*p.LastAssistantMessage)
		}
		idem = hookIdemKey("codex", p.ThreadID, p.TurnID)
	default:
		return req, "", false, fmt.Errorf("unknown hook %q", kind)
	}
	return req, idem, true, nil
}

// withProject appends the project folder name: "Task complete · meow-repo".
func withProject(title, cwd string) string {
	if cwd == "" {
		return title
	}
	if base := filepath.Base(cwd); base != "/" && base != "." {
		title += " · " + base
	}
	return truncateRunes(title, 200)
}

// summarize collapses whitespace and shortens text for a notification body.
func summarize(s string) string {
	return truncateRunes(strings.Join(strings.Fields(s), " "), summaryRunes)
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
