package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/store"
)

const maxName = 64

var apnsTokenRE = regexp.MustCompile(`^[0-9a-f]{64,200}$`)

// registerDevice creates an anonymous device. The response carries the device
// token and the first Device Key; neither can be retrieved again.
func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request) {
	if ok, retry := s.regLimit.allow(s.clientIP(r)); !ok {
		tooManyRequests(w, retry)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body exceeds 64 KB")
		return
	}
	if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &in) != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !checkText(w, "name", in.Name, false, maxName) {
		return
	}
	reg, err := s.st.RegisterDevice(r.Context(), in.Name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"device":       toDeviceJSON(reg.Device),
		"device_token": reg.DeviceToken,
		"key":          toKeyJSON(reg.Key, reg.KeySecret),
	})
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request, p principal) {
	writeJSON(w, http.StatusOK, toDeviceJSON(*p.device))
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Name      *string `json:"name"`
		APNsToken *string `json:"apns_token"`
		APNsEnv   *string `json:"apns_env"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if !checkText(w, "name", name, false, maxName) {
			return
		}
		in.Name = &name
	}
	if in.APNsToken != nil {
		tok := strings.ToLower(strings.TrimSpace(*in.APNsToken))
		if tok != "" && !apnsTokenRE.MatchString(tok) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request", "apns_token must be a hex APNs device token")
			return
		}
		in.APNsToken = &tok
	}
	if in.APNsEnv != nil && !checkEnum(w, "apns_env", *in.APNsEnv, "sandbox", "production") {
		return
	}
	d, err := s.st.UpdateDevice(r.Context(), p.device.ID, store.DeviceUpdate{Name: in.Name, APNsToken: in.APNsToken, APNsEnv: in.APNsEnv})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeviceJSON(d))
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request, p principal) {
	keys, err := s.st.ListKeys(r.Context(), p.device.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]keyJSON, len(keys))
	for i, k := range keys {
		out[i] = toKeyJSON(k, "")
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !checkText(w, "name", in.Name, false, maxName) {
		return
	}
	scopes := store.DefaultScopes
	if len(in.Scopes) > 0 {
		scopes = nil
		for _, want := range []string{store.ScopeNotify, store.ScopeAsk} {
			for _, sc := range in.Scopes {
				if sc == want {
					scopes = append(scopes, want)
					break
				}
			}
		}
		for _, sc := range in.Scopes {
			if !checkEnum(w, "scopes[]", sc, store.ScopeNotify, store.ScopeAsk) {
				return
			}
		}
	}
	k, secret, err := s.st.CreateKey(r.Context(), p.device.ID, in.Name, scopes)
	if quotaFailed(w, err) {
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toKeyJSON(k, secret))
}

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request, p principal) {
	err := s.st.RevokeKey(r.Context(), p.device.ID, r.PathValue("id"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "key not found")
	default:
		s.internalError(w, r, err)
	}
}

// inbox lists notifications newest first. Pass next_cursor back as ?cursor= for the next page.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request, p principal) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		limit = n
	}
	var cur store.Cursor
	if v := q.Get("cursor"); v != "" {
		at, id, ok := strings.Cut(v, ".")
		msv, err := strconv.ParseInt(at, 10, 64)
		if !ok || err != nil || id == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid cursor")
			return
		}
		cur = store.Cursor{At: time.UnixMilli(msv).UTC(), ID: id}
	}
	iq := store.InboxQuery{After: cur, Limit: limit, Search: strings.TrimSpace(q.Get("q"))}
	switch f := q.Get("filter"); f {
	case "", "all":
	case "unread":
		iq.Unread = true
	case "pending":
		iq.Pending = true
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "filter must be all, unread or pending")
		return
	}
	if !checkText(w, "q", iq.Search, false, 100) {
		return
	}
	ns, err := s.st.Inbox(r.Context(), p.device.ID, iq)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]notificationJSON, len(ns))
	for i, n := range ns {
		out[i] = toNotificationJSON(n)
	}
	var next *string
	if len(ns) == limit {
		last := ns[len(ns)-1]
		c := strconv.FormatInt(last.CreatedAt.UnixMilli(), 10) + "." + last.ID
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": out, "next_cursor": next})
}

func (s *Server) getNotification(w http.ResponseWriter, r *http.Request, p principal) {
	n, err := s.st.Notification(r.Context(), r.PathValue("id"))
	if err == nil && n.DeviceID != p.device.ID {
		err = store.ErrNotFound
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toNotificationJSON(n))
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "notification not found")
	default:
		s.internalError(w, r, err)
	}
}

// counts returns the unread and pending totals shown as tab and app icon badges.
func (s *Server) counts(w http.ResponseWriter, r *http.Request, p principal) {
	unread, pending, err := s.st.InboxCounts(r.Context(), p.device.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread": unread, "pending": pending})
}

func (s *Server) markAllRead(w http.ResponseWriter, r *http.Request, p principal) {
	n, err := s.st.MarkAllRead(r.Context(), p.device.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"marked": n})
}

func (s *Server) markRead(w http.ResponseWriter, r *http.Request, p principal) {
	s.mark(w, r, s.st.MarkRead(r.Context(), p.device.ID, r.PathValue("id")))
}

// markDelivered is called by the notification service extension when a push is shown.
func (s *Server) markDelivered(w http.ResponseWriter, r *http.Request, p principal) {
	s.mark(w, r, s.st.MarkDelivered(r.Context(), p.device.ID, r.PathValue("id")))
}

func (s *Server) mark(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "notification not found")
	default:
		s.internalError(w, r, err)
	}
}

// respond answers a question from the iOS app. An Idempotency-Key is required
// so that a reply queued offline and retried is applied at most once.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, p principal) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || message.ValidateIdempotencyKey(key) != nil {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "an Idempotency-Key header of at most 128 printable ASCII characters is required")
		return
	}
	var in struct {
		Answer string `json:"answer"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Answer = strings.TrimSpace(in.Answer)
	if !checkText(w, "answer", in.Answer, true, maxAnswer) {
		return
	}
	id := r.PathValue("id")
	q, replay, err := s.st.Respond(r.Context(), id, p.device.ID, in.Answer, key)
	switch {
	case err == nil:
		s.waits.publish(id)
		writeJSON(w, created(w, replay), toQuestionJSON(q))
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "question not found")
	case errors.Is(err, store.ErrResolved):
		writeJSON(w, http.StatusConflict, conflictBody("already_resolved", "question is already "+q.Status, q))
	case errors.Is(err, store.ErrExpired):
		s.waits.publish(id)
		writeJSON(w, http.StatusGone, conflictBody("expired", "question expired before it was answered", q))
	case errors.Is(err, store.ErrInvalidAnswer):
		writeError(w, http.StatusUnprocessableEntity, "invalid_answer", "answer must be one of the question's choices")
	default:
		s.internalError(w, r, err)
	}
}

// pushConfig exposes only transport configuration, never outbound secrets.
func (s *Server) pushConfig(w http.ResponseWriter, r *http.Request, p principal) {
	mode := "direct"
	if s.gatewayURL != "" {
		mode = "gateway"
	}
	writeJSON(w, 200, map[string]string{"mode": mode, "gateway_url": s.gatewayURL})
}

func (s *Server) bindGateway(w http.ResponseWriter, r *http.Request, p principal) {
	if s.gatewayURL == "" {
		writeError(w, 409, "direct_mode", "this server uses direct APNs")
		return
	}
	var b store.GatewayBinding
	if !strictJSON(w, r, &b) {
		return
	}
	if !strings.HasPrefix(b.DeviceID, "gwd_") || !bindingRE.MatchString(b.DeviceID) || !bindingRE.MatchString(b.BindingID) || !strings.HasPrefix(b.PushToken, "gwp_") || len(b.PushToken) != 43 || !bindingRE.MatchString(b.PushToken) {
		writeError(w, 422, "invalid_request", "invalid gateway binding")
		return
	}
	b.GatewayURL = s.gatewayURL
	if err := s.st.SetGatewayBinding(r.Context(), p.device.ID, b); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
