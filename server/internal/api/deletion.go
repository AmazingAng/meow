package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"meow.blue/meow/server/internal/store"
)

func (s *Server) deleteNotification(w http.ResponseWriter, r *http.Request, p principal) {
	s.deleteSelection(w, r, p, store.NotificationDeletion{ID: r.PathValue("id")}, true)
}

func (s *Server) deleteInbox(w http.ResponseWriter, r *http.Request, p principal) {
	// Both bounds are mandatory: missing/unknown/duplicate parameters must never
	// turn a malformed request into an unbounded delete.
	q, err := parseDeletionRange(r)
	if err != nil {
		writeError(w, 400, "invalid_request", "from and before must be Unix milliseconds: 0 <= from < before <= 253402300799999")
		return
	}
	s.deleteSelection(w, r, p, q, false)
}

func parseDeletionRange(r *http.Request) (store.NotificationDeletion, error) {
	invalid := errors.New("invalid deletion range")
	q, err := url.ParseQuery(r.URL.RawQuery)
	// ParseQuery reports malformed escapes/separators rather than silently
	// accepting the remaining subset returned by URL.Query.
	if err != nil || len(q) != 2 || len(q["from"]) != 1 || len(q["before"]) != 1 {
		return store.NotificationDeletion{}, invalid
	}
	from, e1 := strconv.ParseInt(q.Get("from"), 10, 64)
	before, e2 := strconv.ParseInt(q.Get("before"), 10, 64)
	if e1 != nil || e2 != nil || from < 0 || before <= from || before > 253402300799999 {
		return store.NotificationDeletion{}, invalid
	}
	return store.NotificationDeletion{From: time.UnixMilli(from), Before: time.UnixMilli(before)}, nil
}

func (s *Server) deleteSelection(w http.ResponseWriter, r *http.Request, p principal, selection store.NotificationDeletion, single bool) {
	result, err := s.st.DeleteNotifications(r.Context(), p.device.ID, selection)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not_found", "notification not found")
	case err != nil:
		s.internalError(w, r, err)
	default:
		for _, id := range result.QuestionIDs {
			s.waits.publish(id)
		}
		if single {
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, 200, result)
		}
	}
}
