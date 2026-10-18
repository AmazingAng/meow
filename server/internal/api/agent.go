package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"meow.blue/meow/server/internal/store"
)

const (
	maxTitle  = 256
	maxBodyCh = 4000
	maxSource = 64
	maxAnswer = 2000
	maxChoice = 32

	defaultQuestionTTL = 15 * time.Minute
	minQuestionTTL     = 10 * time.Second
	maxQuestionTTL     = 24 * time.Hour
)

func (s *Server) whoami(w http.ResponseWriter, r *http.Request, p principal) {
	d, err := s.st.Device(r.Context(), p.key.DeviceID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": toKeyJSON(*p.key, ""), "device": toDeviceJSON(d)})
}

type notifyInput struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Source   string `json:"source"`
	Priority string `json:"priority"`
}

// readNotifyInput accepts JSON, or plain text for one-line curl usage:
//
//	curl -H "Authorization: Bearer $MEOW_KEY" -d "Build completed" https://api.meow.blue/v1/notifications
//
// For plain text the first line is the title and the rest the body; query
// parameters title, type, priority and source may be used too.
func readNotifyInput(w http.ResponseWriter, r *http.Request) (notifyInput, bool) {
	var in notifyInput
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct == "application/json" {
		return in, decodeJSON(w, r, &in)
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body exceeds 64 KB")
		return in, false
	}
	q := r.URL.Query()
	text := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
	in = notifyInput{Type: q.Get("type"), Title: q.Get("title"), Source: q.Get("source"), Priority: q.Get("priority")}
	if in.Title != "" {
		in.Body = text
	} else {
		in.Title, in.Body, _ = strings.Cut(text, "\n")
	}
	return in, true
}

func (s *Server) createNotification(w http.ResponseWriter, r *http.Request, p principal) {
	in, ok := readNotifyInput(w, r)
	if !ok {
		return
	}
	in.Title, in.Body, in.Source = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body), strings.TrimSpace(in.Source)
	if in.Type == "" {
		in.Type = "info"
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if !checkEnum(w, "type", in.Type, "info", "alert") ||
		!checkEnum(w, "priority", in.Priority, "low", "normal", "high") ||
		!checkText(w, "title", in.Title, true, maxTitle) ||
		!checkText(w, "body", in.Body, false, maxBodyCh) ||
		!checkText(w, "source", in.Source, false, maxSource) {
		return
	}
	idem, ok := idempotency(w, r, in)
	if !ok {
		return
	}
	n, replay, err := s.st.CreateNotification(r.Context(), store.NewNotification{
		DeviceID: p.key.DeviceID, KeyID: p.key.ID,
		Type: in.Type, Title: in.Title, Body: in.Body, Source: in.Source, Priority: in.Priority,
	}, idem)
	if s.idemFailed(w, r, err) {
		return
	}
	s.push.Enqueue(n.ID) // no-op for a replay that was already pushed
	writeJSON(w, created(w, replay), toNotificationJSON(n))
}

type askInput struct {
	Question string   `json:"question"`
	Body     string   `json:"body"`
	Source   string   `json:"source"`
	Priority string   `json:"priority"`
	Choices  []string `json:"choices"`
	Timeout  int      `json:"timeout"` // seconds
}

func (s *Server) createQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	var in askInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Question, in.Body, in.Source = strings.TrimSpace(in.Question), strings.TrimSpace(in.Body), strings.TrimSpace(in.Source)
	if in.Priority == "" {
		in.Priority = "high" // someone is blocked waiting for the answer
	}
	ttl := defaultQuestionTTL
	if in.Timeout != 0 {
		ttl = time.Duration(in.Timeout) * time.Second
	}
	if ttl < minQuestionTTL || ttl > maxQuestionTTL {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "timeout must be between 10 and 86400 seconds")
		return
	}
	if !checkText(w, "question", in.Question, true, maxTitle) ||
		!checkText(w, "body", in.Body, false, maxBodyCh) ||
		!checkText(w, "source", in.Source, false, maxSource) ||
		!checkEnum(w, "priority", in.Priority, "low", "normal", "high") {
		return
	}
	choices, ok := normalizeChoices(w, in.Choices)
	if !ok {
		return
	}
	in.Choices = choices
	idem, ok := idempotency(w, r, in)
	if !ok {
		return
	}
	q, replay, err := s.st.CreateQuestion(r.Context(), store.NewQuestion{
		NewNotification: store.NewNotification{
			DeviceID: p.key.DeviceID, KeyID: p.key.ID,
			Title: in.Question, Body: in.Body, Source: in.Source, Priority: in.Priority,
		},
		Choices: choices,
		TTL:     ttl,
	}, idem)
	if s.idemFailed(w, r, err) {
		return
	}
	s.push.Enqueue(q.NotificationID)
	writeJSON(w, created(w, replay), toQuestionJSON(q))
}

func normalizeChoices(w http.ResponseWriter, in []string) ([]string, bool) {
	if len(in) == 0 {
		return nil, true // free-text reply
	}
	if len(in) < 2 || len(in) > 8 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "choices must have between 2 and 8 entries")
		return nil, false
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if !checkText(w, "choice", c, true, maxChoice) {
			return nil, false
		}
		if seen[strings.ToLower(c)] {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request", "choices must be unique")
			return nil, false
		}
		seen[strings.ToLower(c)] = true
		out = append(out, c)
	}
	return out, true
}

func (s *Server) idemFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrIdempotencyMismatch):
		writeError(w, http.StatusUnprocessableEntity, "idempotency_mismatch", "Idempotency-Key was already used for a different request")
	default:
		s.internalError(w, r, err)
	}
	return true
}

// created returns 201, or 200 with Idempotent-Replayed for a replayed request.
func created(w http.ResponseWriter, replay bool) int {
	if replay {
		w.Header().Set("Idempotent-Replayed", "true")
		return http.StatusOK
	}
	return http.StatusCreated
}

// loadQuestion fetches a question owned by the caller, expiring it first if
// it is overdue. It writes the error response and returns false on failure.
func (s *Server) loadQuestion(w http.ResponseWriter, r *http.Request, p principal, id string) (store.Question, bool) {
	q, err := s.st.Question(r.Context(), id)
	if err == nil && q.DeviceID != p.deviceID() {
		err = store.ErrNotFound
	}
	if err == nil && q.Status == store.StatusPending && !s.st.Now().Before(q.ExpiresAt) {
		if err = s.ExpireDue(r.Context()); err == nil {
			q, err = s.st.Question(r.Context(), id)
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "question not found")
		return q, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return q, false
	}
	return q, true
}

func (s *Server) getQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	if q, ok := s.loadQuestion(w, r, p, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, toQuestionJSON(q))
	}
}

// waitQuestion long-polls until the question leaves "pending" or ?timeout=
// seconds pass (default 30, capped at MaxWait), then returns its current state.
func (s *Server) waitQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	timeout := 30 * time.Second
	if v := r.URL.Query().Get("timeout"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "timeout must be a non-negative number of seconds")
			return
		}
		timeout = time.Duration(n) * time.Second
	}
	timeout = min(timeout, s.maxWait)

	id := r.PathValue("id")
	updates, unsubscribe := s.waits.subscribe(id) // before the first read, so no update is missed
	defer unsubscribe()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		q, ok := s.loadQuestion(w, r, p, id)
		if !ok {
			return
		}
		if q.Status != store.StatusPending {
			writeJSON(w, http.StatusOK, toQuestionJSON(q))
			return
		}
		// Wake at expiry too, so the waiter sees "expired" without the background sweep.
		expiry := time.NewTimer(max(q.ExpiresAt.Sub(s.st.Now()), 0) + 10*time.Millisecond)
		select {
		case <-updates:
			expiry.Stop()
			continue
		case <-expiry.C:
			continue
		case <-deadline.C:
		case <-s.waits.done: // shutting down; the client polls again after restart
		case <-r.Context().Done():
			expiry.Stop()
			return
		}
		expiry.Stop()
		writeJSON(w, http.StatusOK, toQuestionJSON(q))
		return
	}
}

func (s *Server) cancelQuestion(w http.ResponseWriter, r *http.Request, p principal) {
	q, err := s.st.CancelQuestion(r.Context(), r.PathValue("id"), p.key.DeviceID)
	switch {
	case err == nil:
		s.waits.publish(q.ID)
		writeJSON(w, http.StatusOK, toQuestionJSON(q))
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "question not found")
	case errors.Is(err, store.ErrResolved):
		writeJSON(w, http.StatusConflict, conflictBody("already_resolved", "question is already "+q.Status, q))
	default:
		s.internalError(w, r, err)
	}
}

func conflictBody(code, msg string, q store.Question) map[string]any {
	return map[string]any{"error": errorBody{Code: code, Message: msg}, "question": toQuestionJSON(q)}
}
