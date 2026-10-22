package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"

	"meow.blue/meow/adapters"
)

// PermissionRequest runs BEFORE reviewer routing, even for Approve for me.
// Its wire input has no reviewer. Read only explicit, matching turn metadata
// from the supplied rollout; never infer the mode from global config, time
// spent waiting, tool arguments or conversation text. Rollouts are not a stable
// API: missing/changed metadata must preserve the notification.
func codexAutomaticallyReviewed(input []byte) bool {
	var p adapters.CodexHookInput
	if json.Unmarshal(input, &p) != nil || p.Event != "PermissionRequest" || p.SessionID == "" || p.TurnID == "" {
		return false
	}
	// MCP approvals can override the turn's reviewer (e.g. Computer Use).
	// Only these built-in tools inherit it without a per-tool override.
	switch p.ToolName {
	case "Bash", "apply_patch", "Edit", "Write", "write_stdin", "request_permissions":
	default:
		return false
	}
	var path string
	if json.Unmarshal(p.TranscriptPath, &path) != nil || path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	// Verify the rollout identity independently of its filename. Both reads
	// are bounded, including very large transcripts and malformed long lines.
	header, err := io.ReadAll(io.NewSectionReader(f, 0, metadataTailLimit))
	if err != nil {
		return false
	}
	end := bytes.IndexByte(header, '\n')
	if end < 0 {
		return false
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.Unmarshal(header[:end], &meta) != nil || meta.Type != "session_meta" || meta.Payload.ID != p.SessionID {
		return false
	}
	start := max(int64(0), info.Size()-metadataTailLimit)
	tail, err := io.ReadAll(io.NewSectionReader(f, start, metadataTailLimit))
	if err != nil {
		return false
	}
	if start > 0 {
		end := bytes.IndexByte(tail, '\n')
		if end < 0 {
			return false
		}
		tail = tail[end+1:]
	}
	lines := bytes.Split(tail, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) == 0 {
			continue
		}
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				TurnID   string `json:"turn_id"`
				Reviewer string `json:"approvals_reviewer"`
			} `json:"payload"`
		}
		if json.Unmarshal(lines[i], &row) != nil {
			return false
		}
		if row.Type != "turn_context" {
			continue
		}
		// Only the latest context counts: a mode change or a newer turn must
		// not reuse old auto-review evidence.
		return row.Payload.TurnID == p.TurnID && row.Payload.Reviewer == "auto_review"
	}
	return false
}
