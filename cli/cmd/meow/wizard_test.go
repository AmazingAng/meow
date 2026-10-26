package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"meow.blue/meow/cli/internal/config"
)

// fakeMeow answers the endpoints the wizard uses. Only "meow_good" is a valid key.
func fakeMeow(t *testing.T, phoneRegistered bool) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	sent := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok"))
			return
		}
		if r.Header.Get("Authorization") != "Bearer meow_good" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/whoami":
			json.NewEncoder(w).Encode(map[string]any{
				"key":    map[string]any{"id": "k1", "name": "laptop"},
				"device": map[string]any{"id": "d1", "name": "iPhone", "has_apns_token": phoneRegistered},
			})
		case "/v1/notifications":
			mu.Lock()
			sent++
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"n1","title":"test"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, func() int { mu.Lock(); defer mu.Unlock(); return sent }
}

// wizardEnv isolates HOME, the meow config and agent dirs. Only the agent
// directories listed in agents exist, and PATH has no agent commands.
func wizardEnv(t *testing.T, key string, agents ...string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", filepath.Join(dir, "empty-path"))
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "meow", "config.json"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	t.Setenv("MEOW_KEY", "")
	t.Setenv("MEOW_SERVER", "")
	exe := filepath.Join(dir, "bin", "meow")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("MEOW_EXECUTABLE", exe)
	for _, a := range agents {
		os.MkdirAll(filepath.Join(dir, a), 0o755)
	}
	if key != "" {
		if err := config.Save(config.Config{Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWizardConnectsOnlyDetectedAgents(t *testing.T) {
	ts, sent := fakeMeow(t, true)
	dir := wizardEnv(t, "meow_good", "claude")

	var code int
	out := captureOutput(t, "", func() { code = run([]string{"setup", "--server", ts.URL, "--yes"}) })
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"is reachable", `Logged in to device "iPhone"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "claude", "settings.json")); err != nil || !strings.Contains(string(b), "hook claude-stop") {
		t.Fatalf("Claude hooks not installed: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("Codex was configured although it isn't installed: %v", err)
	}
	if n := sent(); n != 1 {
		t.Fatalf("sent %d test notifications, want exactly 1", n)
	}
	cfg, _ := config.LoadFile()
	if cfg.Server != ts.URL || cfg.Key != "meow_good" || cfg.Installed["claude"] == "" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestWizardStopsOnBadKeyWithoutChanges(t *testing.T) {
	ts, sent := fakeMeow(t, true)
	dir := wizardEnv(t, "meow_rotated", "claude")

	var code int
	out := captureOutput(t, "", func() { code = run([]string{"setup", "--server", ts.URL, "--yes"}) })
	if code == exitOK || !strings.Contains(out, "isn't valid on this server") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("hooks were installed with an invalid key")
	}
	if sent() != 0 {
		t.Fatal("sent a notification with an invalid key")
	}
}

func TestWizardWarnsWhenPhoneCantReceive(t *testing.T) {
	ts, _ := fakeMeow(t, false)
	wizardEnv(t, "meow_good")

	out := captureOutput(t, "", func() { run([]string{"setup", "--server", ts.URL}) })
	if !strings.Contains(out, "hasn't registered for notifications") {
		t.Fatalf("no warning about the phone:\n%s", out)
	}
	if !strings.Contains(out, "No Claude Code, Codex or Cursor setup found") {
		t.Fatalf("no hint when no agent is installed:\n%s", out)
	}
}

func TestWizardWithoutAgentsStillSendsFirstNotification(t *testing.T) {
	ts, sent := fakeMeow(t, true)
	wizardEnv(t, "meow_good")
	var code int
	out := captureOutput(t, "", func() { code = run([]string{"setup", "--server", ts.URL}) })
	if code != exitOK || sent() != 1 || !strings.Contains(out, "Check that it appears on your iPhone") {
		t.Fatalf("exit %d, notifications %d:\n%s", code, sent(), out)
	}
	cfg, err := config.LoadFile()
	if err != nil || cfg.Server != ts.URL || len(cfg.Installed) != 0 {
		t.Fatalf("setup didn't preserve a standalone CLI login: %+v, %v", cfg, err)
	}
}

func TestDoctor(t *testing.T) {
	ts, sent := fakeMeow(t, false)
	wizardEnv(t, "meow_good", "codex")
	config.Save(config.Config{Server: ts.URL, Key: "meow_good"})

	var code int
	out := captureOutput(t, "", func() { code = run([]string{"doctor"}) })
	if code == exitOK {
		t.Fatalf("doctor passed with an unregistered phone:\n%s", out)
	}
	for _, want := range []string{"✓ Server", "✓ Device Key works", "✗ The iPhone hasn't registered", `✗ Codex is not connected. Run "meow setup codex"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if sent() != 0 {
		t.Fatal("doctor must not send anything")
	}
}

func TestCheckServerExplainsFailures(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := "http://" + ln.Addr().String()
	ln.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	notMeow := httptest.NewServer(http.NotFoundHandler())
	defer notMeow.Close()

	for _, tc := range []struct{ server, want string }{
		{"meow.example.com", "not a server address"},
		{closed, "refused the connection"},
		{tlsServer.URL, "isn't trusted"},
		{notMeow.URL, "doesn't look like a Meow server"},
		{"https://no-such-host.invalid", "can't find no-such-host.invalid"},
	} {
		err := checkServer(tc.server)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("checkServer(%q) = %v, want %q", tc.server, err, tc.want)
		}
	}
}

func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	if stdinIsTerminal() {
		t.Fatal("/dev/null treated as a terminal; prompts would read empty input")
	}
}

func TestMaskedDeviceKeyRejectedBeforeRequestOrSave(t *testing.T) {
	for _, key := range []string{"meow_abcd…wxyz", "meow_abcd...wxyz"} {
		t.Run(key, func(t *testing.T) {
			requests := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer ts.Close()
			wizardEnv(t, "")
			if _, err := checkKey(ts.URL, key); err == nil || !strings.Contains(err.Error(), "Copy Device Key") || strings.Contains(err.Error(), "wrong server") {
				t.Fatalf("masked key diagnosis: %v", err)
			}
			var code int
			out := captureOutput(t, "", func() { code = run([]string{"login", "--server", ts.URL, "--key", key}) })
			if code == exitOK || !strings.Contains(out, "masked or incomplete") {
				t.Fatalf("exit %d: %s", code, out)
			}
			if requests != 0 {
				t.Fatalf("sent %d requests with masked key", requests)
			}
			cfg, err := config.LoadFile()
			if err != nil || cfg.Key != "" {
				t.Fatalf("masked key persisted: %v", err)
			}
		})
	}
}
