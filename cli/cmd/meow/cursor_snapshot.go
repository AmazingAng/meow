package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"meow.blue/meow/adapters"
	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/client"
	"meow.blue/meow/message"
)

// This is bounded local hook state, not an outbox. Its lifetime matches the
// server's 24-hour idempotency window. Each record is private and atomically
// published so concurrent hook processes never observe a partially written file.
const cursorSnapshotLifetime = 24 * time.Hour
const cursorSnapshotLimit = 64 << 10

type cursorResponseInput struct {
	ConversationID string  `json:"conversation_id"`
	GenerationID   string  `json:"generation_id"`
	HookEventName  string  `json:"hook_event_name"`
	Text           *string `json:"text"`
}

type cursorReplySnapshot struct {
	Body string `json:"body"`
}

var cursorSnapshotStat = os.Lstat
var cursorSnapshotChmod = os.Chmod
var cursorSnapshotReadDir = os.ReadDir
var cursorSnapshotRemove = os.Remove

func cursorSnapshotDir(cfg config.Config) (string, error) {
	if cfg.Server == "" || cfg.Key == "" {
		return "", fmt.Errorf("missing Cursor registration")
	}
	path, err := config.Path()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(path), "cursor-snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := cursorSnapshotStat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("invalid Cursor snapshot directory")
	}
	if err := cursorSnapshotChmod(dir, 0700); err != nil {
		return "", err
	}
	entries, err := cursorSnapshotReadDir(dir)
	if err != nil {
		return "", err
	}
	cutoff := time.Now().Add(-cursorSnapshotLifetime)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			if err := cursorSnapshotRemove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}
	return dir, nil
}

func cursorSnapshotPath(dir string, cfg config.Config, conversation, generation, kind string) string {
	parts, _ := json.Marshal([]string{cfg.Server, cfg.Key, conversation, generation, kind})
	digest := sha256.Sum256(parts)
	return filepath.Join(dir, hex.EncodeToString(digest[:])+".json")
}

func captureCursorResponse(input []byte, summary bool, cfg config.Config) error {
	var event cursorResponseInput
	if err := json.Unmarshal(input, &event); err != nil {
		return err
	}
	if event.HookEventName != "afterAgentResponse" || strings.TrimSpace(event.ConversationID) == "" || strings.TrimSpace(event.GenerationID) == "" || event.Text == nil {
		return fmt.Errorf("invalid Cursor response identity")
	}
	if !summary {
		return nil
	}
	dir, err := cursorSnapshotDir(cfg)
	if err != nil {
		return err
	}
	path := cursorSnapshotPath(dir, cfg, event.ConversationID, event.GenerationID, "response")
	return writeCursorSnapshot(path, cursorReplySnapshot{Body: adapters.Summary(*event.Text)}, true)
}

func cursorStopNotification(input []byte, summary bool, cfg config.Config) (client.NotifyRequest, string, bool, error) {
	adapter, _ := adapters.Lookup("cursor-stop")
	event, send, err := adapter.Decode(input)
	if err != nil || !send {
		return client.NotifyRequest{}, "", false, err
	}
	if strings.TrimSpace(event.EventID) == "" {
		return client.NotifyRequest{}, "", false, fmt.Errorf("missing Cursor generation")
	}
	dir, err := cursorSnapshotDir(cfg)
	if err != nil {
		return client.NotifyRequest{}, "", false, err
	}
	policy := event.DedupeNamespace + ":" + strconv.FormatBool(summary) + ":" + os.Getenv("MEOW_LANGUAGE")
	path := cursorSnapshotPath(dir, cfg, event.Origin.SessionID, event.EventID, policy)
	var frozen adapters.Result
	if exists, err := readCursorSnapshot(path, &frozen); err != nil {
		return client.NotifyRequest{}, "", false, err
	} else if exists {
		return cursorFrozenResult(frozen)
	}
	body := ""
	if summary {
		var reply cursorReplySnapshot
		response := cursorSnapshotPath(dir, cfg, event.Origin.SessionID, event.EventID, "response")
		if exists, err := readCursorSnapshot(response, &reply); err == nil && exists {
			body = reply.Body
		}
	}
	result, err := adapters.Build("cursor-stop", input, adapters.Options{
		IncludeSummary: summary, Language: os.Getenv("MEOW_LANGUAGE"),
		ResolveContext: func(o adapters.Origin) *message.Context {
			return hookContext(o.Source, o.Cwd, o.SessionID, o.SessionName, o.TranscriptPath)
		},
		ResolveBody: func(adapters.Origin) string { return body },
	})
	if err != nil {
		return client.NotifyRequest{}, "", false, err
	}
	// The first stop freezes context and presentation too, including a generic
	// fallback. Later replies, renames or replays must not create another payload.
	if err := writeCursorSnapshot(path, result, false); err != nil {
		return client.NotifyRequest{}, "", false, err
	}
	if _, err := readCursorSnapshot(path, &frozen); err != nil {
		return client.NotifyRequest{}, "", false, err
	}
	return cursorFrozenResult(frozen)
}

func cursorFrozenResult(result adapters.Result) (client.NotifyRequest, string, bool, error) {
	if !result.Send || result.IdempotencyKey == "" || result.Notification.Source != "Cursor" {
		return client.NotifyRequest{}, "", false, fmt.Errorf("invalid frozen Cursor delivery")
	}
	if _, err := result.Notification.Normalize(); err != nil {
		return client.NotifyRequest{}, "", false, err
	}
	return result.Notification, result.IdempotencyKey, true, nil
}

func readCursorSnapshot(path string, value any) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > cursorSnapshotLimit {
		return false, fmt.Errorf("invalid Cursor snapshot file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, value); err != nil {
		return false, err
	}
	return true, nil
}

type cursorSnapshotFile interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}

var cursorCreateSnapshot = func(dir string) (cursorSnapshotFile, error) { return os.CreateTemp(dir, "cursor-*.tmp") }
var cursorPublishSnapshot = os.Link
var cursorReplaceSnapshot = os.Rename

func writeCursorSnapshot(path string, value any, replace bool) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := cursorCreateSnapshot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if _, err := file.Write(b); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if replace {
		return cursorReplaceSnapshot(file.Name(), path)
	}
	if err := cursorPublishSnapshot(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
