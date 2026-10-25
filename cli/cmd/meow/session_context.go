package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"meow.blue/meow/adapters"
	"meow.blue/meow/message"

	_ "modernc.org/sqlite"
)

// Metadata lookups are optional and bounded. They never read conversation text.
// Codex uses session_index.jsonl. Claude uses title records in the event's own
// transcript. Cursor reads the composer name for the event's conversation id.
// Grok reads generated_title from that session's summary.json.
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
		case "Cursor":
			c.Session = readCursorSessionName(c.SessionID)
		case "Grok":
			c.Session = readGrokSessionName(cwd, id)
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

// Cursor keeps the chat title in composerHeaders, keyed by the hook's
// conversation id. Only that row's name is read. A missing or unreadable
// database leaves the session id in place.
func readCursorSessionName(id string) string {
	if id == "" {
		return ""
	}
	path := cursorStateDB()
	if path == "" {
		return ""
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return ""
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT value FROM composerHeaders WHERE composerId = ?`, id).Scan(&raw); err != nil || len(raw) > 1<<20 {
		return ""
	}
	var header struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &header) != nil {
		return ""
	}
	return cleanContextLabel(header.Name, message.MaxContextName)
}

// Grok stores one directory per session under ~/.grok/sessions/<encoded cwd>/.
// The display title is generated_title, then session_summary. The cwd-keyed
// file is tried first. A hash bucket or a renamed directory still resolves by
// reading summary.json one level down, never the conversation log.
func readGrokSessionName(cwd, id string) string {
	if !safeSessionID(id) {
		return ""
	}
	root, err := grokDir()
	if err != nil || root == "" {
		return ""
	}
	root = filepath.Join(root, "sessions")
	if cwd != "" {
		if name := readGrokSummary(filepath.Join(root, url.PathEscape(cwd), id, "summary.json")); name != "" {
			return name
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if name := readGrokSummary(filepath.Join(root, entry.Name(), id, "summary.json")); name != "" {
			return name
		}
	}
	return ""
}

func safeSessionID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

func readGrokSummary(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var summary struct {
		GeneratedTitle string `json:"generated_title"`
		SessionSummary string `json:"session_summary"`
	}
	if json.Unmarshal(b, &summary) != nil {
		return ""
	}
	title := strings.TrimSpace(summary.GeneratedTitle)
	if title == "" {
		title = strings.TrimSpace(summary.SessionSummary)
	}
	return cleanContextLabel(title, message.MaxContextName)
}

func cursorStateDB() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	for _, path := range []string{
		filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb"),
		filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb"),
	} {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

func sqliteReadOnlyDSN(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	return u.String()
}
