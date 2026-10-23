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
	// Verify the rollout identity independently of its filename. The header
	// read is bounded; the context search below walks back through this turn.
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
	// Search back to the latest context, regardless of how much output the
	// active turn produced. Snapshot the size so appends cannot prolong this
	// scan. Memory is bounded per record, rather than limiting the whole turn.
	reader := rolloutReverseReader{file: f, offset: info.Size()}
	for {
		line, err := reader.previousLine()
		if err != nil {
			return false
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row struct {
			Type    string `json:"type"`
			Payload struct {
				TurnID   string `json:"turn_id"`
				Reviewer string `json:"approvals_reviewer"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			return false
		}
		if row.Type != "turn_context" {
			continue
		}
		// Only the latest context counts: a mode change or a newer turn must
		// not reuse old auto-review evidence.
		return row.Payload.TurnID == p.TurnID && row.Payload.Reviewer == "auto_review"
	}
}

// Rollout lines can contain multi-megabyte screenshots. Bound a single record
// to avoid unbounded allocation on corrupt files, but never cap the number of
// records searched. Unsupported oversized records preserve the notification.
const approvalRecordLimit = 64 << 20
const approvalReadBlock = 64 << 10

type rolloutReverseReader struct {
	file   io.ReaderAt
	offset int64
	block  []byte
}

func (r *rolloutReverseReader) previousLine() ([]byte, error) {
	var fragments [][]byte
	size := 0
	for {
		if len(r.block) == 0 && r.offset > 0 {
			n := min(r.offset, int64(approvalReadBlock))
			r.offset -= n
			r.block = make([]byte, int(n))
			if _, err := r.file.ReadAt(r.block, r.offset); err != nil {
				return nil, err
			}
		}
		if len(r.block) == 0 && len(fragments) == 0 {
			return nil, io.EOF
		}
		i := bytes.LastIndexByte(r.block, '\n')
		part := r.block[i+1:]
		size += len(part)
		if size > approvalRecordLimit {
			return nil, io.ErrShortBuffer
		}
		fragments = append(fragments, part)
		if i >= 0 {
			r.block = r.block[:i]
		} else {
			r.block = nil
		}
		if i >= 0 || r.offset == 0 {
			if len(fragments) == 1 {
				return part, nil
			}
			line := make([]byte, 0, size)
			for j := len(fragments) - 1; j >= 0; j-- {
				line = append(line, fragments[j]...)
			}
			return line, nil
		}
	}
}
