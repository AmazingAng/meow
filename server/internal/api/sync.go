package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

var errSyncCursor = errors.New("invalid sync cursor")

type syncCursor struct {
	Device string `json:"device"`
	Epoch  string `json:"epoch"`
	After  int64  `json:"after"`
	Until  int64  `json:"until,omitempty"`
}

func decodeSyncCursor(raw string) (syncCursor, error) {
	var cursor syncCursor
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	err = d.Decode(&cursor)
	if err == nil && (!json.Valid(b) || cursor.Device == "" || cursor.Epoch == "" || cursor.After < 0 || cursor.Until < 0 || (cursor.Until != 0 && cursor.Until < cursor.After)) {
		err = errSyncCursor
	}
	return cursor, err
}
func encodeSyncCursor(cursor syncCursor) string {
	b, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) syncInbox(w http.ResponseWriter, r *http.Request, p principal) {
	q := r.URL.Query()
	limit := 200
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, 400, "invalid_request", "invalid sync limit")
			return
		}
		limit = n
	}
	if q.Get("cursor") != "" || q.Get("q") != "" || (q.Get("filter") != "" && q.Get("filter") != "all") {
		writeError(w, 400, "invalid_request", "sync enumerates complete history without filters")
		return
	}
	cursor := syncCursor{Device: p.device.ID, Epoch: s.syncEpoch}
	if raw := q.Get("sync_cursor"); raw != "" {
		var err error
		if len(raw) > 2048 {
			err = errSyncCursor
		} else {
			cursor, err = decodeSyncCursor(raw)
		}
		if err != nil || cursor.Device != p.device.ID {
			writeError(w, 400, "invalid_request", "invalid sync cursor")
			return
		}
		if cursor.Epoch != s.syncEpoch {
			cursor = syncCursor{Device: p.device.ID, Epoch: s.syncEpoch}
		}
	}
	current, err := s.st.SyncSequence(r.Context(), p.device.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if cursor.After > current || cursor.Until > current {
		cursor.After = 0
		cursor.Until = 0
	}
	if cursor.Until == 0 {
		cursor.Until = current
	}
	items, err := s.st.SyncInbox(r.Context(), p.device.ID, cursor.After, cursor.Until, limit+1)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	out := make([]notificationJSON, len(items))
	for i, item := range items {
		out[i] = toNotificationJSON(item.Notification)
	}
	if more {
		cursor.After = items[len(items)-1].Sequence
	} else {
		cursor.After = cursor.Until
		cursor.Until = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": out, "sync_cursor": encodeSyncCursor(cursor), "has_more": more})
}
