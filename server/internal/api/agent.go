package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/store"
)

const (
	maxTitle  = message.MaxTitle
	maxBodyCh = message.MaxBody
	maxSource = message.MaxSource
)

func (s *Server) whoami(w http.ResponseWriter, r *http.Request, p principal) {
	d, err := s.st.Device(r.Context(), p.key.DeviceID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	registered := d.APNsToken != ""
	if s.gatewayURL != "" {
		binding, err := s.st.GatewayBinding(r.Context(), d.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.internalError(w, r, err)
			return
		}
		registered = err == nil && binding.GatewayURL == s.gatewayURL
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": toKeyJSON(*p.key, ""), "device": toDeviceJSON(d), "push_registered": registered})
}

type notifyInput = message.Notification

// readNotifyInput accepts JSON, or plain text for one-line curl usage:
//
//	curl -H "Authorization: Bearer $MEOW_KEY" -d "Build completed" "$MEOW_SERVER/v1/notifications"
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
	in = notifyInput{Event: message.EventKind(q.Get("event")), Type: q.Get("type"), Title: q.Get("title"), Source: q.Get("source"), Priority: q.Get("priority")}
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
	in, err := in.Normalize()
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	idem, ok := idempotency(w, r, in.CanonicalJSON())
	if !ok {
		return
	}
	n, replay, err := s.st.CreateNotification(r.Context(), store.NewNotification{
		DeviceID: p.key.DeviceID, KeyID: p.key.ID,
		Event: in.Event, Type: in.Type, Title: in.Title, Body: in.Body, Source: in.Source, Priority: in.Priority, Context: in.Context,
	}, idem)
	if s.idemFailed(w, r, err) {
		return
	}
	s.push.Enqueue(n.ID) // no-op for a replay that was already pushed
	writeJSON(w, created(w, replay), toNotificationJSON(n))
}

func (s *Server) idemFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrIdempotencyMismatch):
		writeError(w, http.StatusUnprocessableEntity, "idempotency_mismatch", "Idempotency-Key was already used for a different request")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "the original resource has expired or was deleted")
	case quotaFailed(w, err):
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
		if _, err = s.st.ExpireDue(r.Context()); err == nil {
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

// Quotas differ from short-term request limits: retrying immediately cannot help.
func quotaFailed(w http.ResponseWriter, err error) bool {
	var q *store.QuotaError
	if !errors.As(err, &q) {
		return false
	}
	writeError(w, http.StatusTooManyRequests, "quota_exceeded", q.Error())
	return true
}
