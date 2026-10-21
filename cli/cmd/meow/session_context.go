package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"meow.blue/meow/adapters"
	"meow.blue/meow/message"
)

// Metadata lookups are optional and bounded. No database, subprocess, recursive
// transcript search, user prompts or conversation text are used as session names.
const metadataTailLimit = 2 << 20

func hookContext(agent, cwd, id, name, transcript string) *message.Context {
	c := (adapters.Origin{Source: agent, Cwd: cwd, SessionID: id, SessionName: name, TranscriptPath: transcript}).Context()
	if c == nil {
		return nil
	}
	if c.Session == "" && c.SessionID != "" {
		switch agent {
		case "Codex":
			home := os.Getenv("CODEX_HOME")
			if home == "" {
				if user, err := os.UserHomeDir(); err == nil {
					home = filepath.Join(user, ".codex")
				}
			}
			if home != "" {
				c.Session = readSessionName(filepath.Join(home, "session_index.jsonl"), c.SessionID, false)
			}
		case "Claude Code":
			// Use only the event's own transcript. Never guess a path from a session ID.
			if transcript != "" {
				c.Session = readSessionName(transcript, c.SessionID, true)
			}
		}
	}
	return c
}

func cleanContextLabel(s string, limit int) string { return message.CleanLabel(s, limit) }

func readSessionName(path, id string, claude bool) string {
	// Reject directories and special files before opening (e.g. a pipe).
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	start := max(int64(0), info.Size()-metadataTailLimit)
	b, err := io.ReadAll(io.NewSectionReader(f, start, metadataTailLimit))
	if err != nil {
		return ""
	}
	if start > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			return ""
		}
	}
	lines := bytes.Split(b, []byte{'\n'})
	ai := ""
	for i := len(lines) - 1; i >= 0; i-- {
		var row struct {
			ID          string `json:"id"`
			Name        string `json:"thread_name"`
			SessionID   string `json:"sessionId"`
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if json.Unmarshal(lines[i], &row) != nil {
			continue
		}
		if !claude && row.ID == id {
			return cleanContextLabel(row.Name, message.MaxContextName)
		}
		if claude && row.SessionID == id {
			if row.Type == "custom-title" {
				return cleanContextLabel(row.CustomTitle, message.MaxContextName)
			}
			if row.Type == "ai-title" && ai == "" {
				ai = cleanContextLabel(row.AITitle, message.MaxContextName)
			}
		}
	}
	return ai
}
