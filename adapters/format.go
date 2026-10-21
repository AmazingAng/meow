package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"meow.blue/meow/message"
	"path/filepath"
	"strings"
)

// Origin is host-local input. Cwd and TranscriptPath never enter the wire model.
type Origin struct{ Source, Cwd, SessionID, SessionName, TranscriptPath string }

func (o Origin) Context() *message.Context {
	c := &message.Context{SessionID: message.CleanLabel(o.SessionID, message.MaxContextID), Session: message.CleanLabel(o.SessionName, message.MaxContextName)}
	if o.Cwd != "" {
		path := filepath.Clean(o.Cwd)
		if base := filepath.Base(path); base != "/" && base != "." {
			c.Workspace = message.CleanLabel(base, message.MaxContextName)
		}
		hash := sha256.Sum256([]byte(path))
		c.WorkspaceID = hex.EncodeToString(hash[:16])
	}
	if *c == (message.Context{}) {
		return nil
	}
	return c
}

func WithProject(title, cwd string) string {
	if cwd == "" {
		return title
	}
	if base := filepath.Base(cwd); base != "/" && base != "." {
		title += " · " + base
	}
	return message.Truncate(title, 200)
}
func Summary(text string) string { return message.Truncate(strings.TrimSpace(text), message.MaxBody) }

func IdempotencyKey(parts ...string) string {
	for _, part := range parts {
		if part == "" {
			return ""
		}
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "hook-" + hex.EncodeToString(hash[:16])
}

func Title(kind, language string) string {
	zh := strings.HasPrefix(strings.ToLower(language), "zh")
	switch kind {
	case AttentionTitle:
		if zh {
			return "需要你确认"
		}
		return "Needs your attention"
	case FailureTitle:
		if zh {
			return "运行出错"
		}
		return "Run failed"
	default:
		if zh {
			return "有新结果"
		}
		return "New result"
	}
}
