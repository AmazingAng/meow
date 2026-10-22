package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"meow.blue/meow/adapters"
	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/cli/internal/setup"
	"meow.blue/meow/client"
	"meow.blue/meow/message"
)

// meow hook <kind> [--no-summary] [payload] is run
// by the agents' hooks (see "meow setup"). It must never disturb the agent:
// it always exits 0, never writes to stdout or stderr, and records problems
// in hook.log next to the config file instead.

const (
	hookTimeout = 5 * time.Second
	maxHookLog  = 256 << 10
)

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
	// An already-running Codex may still have the retired notify command cached.
	// Preserve its original external handler above, but never send Meow pushes.
	if kind == "codex" {
		return
	}
	if os.Getenv("MEOW_HOOK_DISABLED") == "1" {
		return
	}

	if _, err := adapters.Lookup(kind); err != nil {
		hookLog("%s: unknown hook", kind)
		return
	}
	var input []byte
	if kind == "codex" {
		input = []byte(payload)
	} else {
		var err error
		input, err = io.ReadAll(io.LimitReader(os.Stdin, adapters.MaxInputBytes+1))
		if err != nil {
			hookLog("%s: read input failed", kind)
			return
		}
	}
	if len(input) > adapters.MaxInputBytes {
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

// buildHookNotification supplies host policies to the pure adapter pipeline.
func buildHookNotification(kind string, input []byte, summary bool) (client.NotifyRequest, string, bool, error) {
	if kind == "codex-permission" && len(input) <= adapters.MaxInputBytes && codexAutomaticallyReviewed(input) {
		return client.NotifyRequest{}, "", false, nil
	}
	r, err := adapters.Build(kind, input, adapters.Options{IncludeSummary: summary, Language: os.Getenv("MEOW_LANGUAGE"),
		ResolveContext: func(o adapters.Origin) *message.Context {
			return hookContext(o.Source, o.Cwd, o.SessionID, o.SessionName, o.TranscriptPath)
		},
	})
	return r.Notification, r.IdempotencyKey, r.Send, err
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

type codexHookInput = adapters.CodexHookInput

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
