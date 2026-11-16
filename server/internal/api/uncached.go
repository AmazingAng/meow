package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

const uncachedPushConcurrency = 4

func cacheOptOut(r *http.Request) bool {
	for _, value := range r.Header.Values("Cache") {
		for _, directive := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(directive), "no") {
				return true
			}
		}
	}
	return false
}

func (s *Server) sendUncached(w http.ResponseWriter, r *http.Request, in store.NewNotification, idem store.Idem) {
	w.Header().Set("Cache-Control", "no-store")
	if s.directPush == nil || s.gatewayURL != "" {
		writeError(w, http.StatusUnprocessableEntity, "cache_required", "uncached sends require direct APNs; the wake gateway needs a source message")
		return
	}
	select {
	case s.directSlots <- struct{}{}:
		defer func() { <-s.directSlots }()
	default:
		tooManyRequests(w, time.Second)
		return
	}

	target, replay, err := s.st.ReserveUncachedNotification(r.Context(), in, idem)
	switch {
	case errors.Is(err, store.ErrNoPushToken):
		writeError(w, http.StatusConflict, "push_not_registered", "register the iPhone for push notifications before sending without a cache")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked credentials")
		return
	case s.idemFailed(w, r, err):
		return
	}

	n := target.Notification
	if replay {
		switch n.PushStatus {
		case store.PushAccepted:
			writeJSON(w, created(w, true), toNotificationJSON(n))
		case store.PushFailed:
			uncachedPushFailed(w)
		default:
			writeError(w, http.StatusConflict, "push_outcome_unknown", "an uncached attempt is in progress or its outcome is unknown")
		}
		return
	}

	msg, err := push.BuildMessage(target)
	if err == nil {
		// APNs expiration zero requests immediate delivery without APNs buffering.
		msg.Expiration = time.Unix(0, 0)
		err = s.directPush.Push(r.Context(), msg)
	}
	// Preserve the known outcome even if the caller disconnected during delivery.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer cancel()
	if saveErr := s.st.FinishUncachedNotification(ctx, n, idem, err == nil); saveErr != nil {
		s.log.Error("record uncached push outcome", "notification", n.ID, "err", saveErr)
	}
	if err != nil {
		var providerErr *push.Error
		if errors.As(err, &providerErr) && providerErr.InvalidToken() {
			if clearErr := s.st.ClearAPNsToken(ctx, target.Device.ID, msg.Token); clearErr != nil {
				s.log.Error("clear invalid APNs token", "notification", n.ID, "err", clearErr)
			}
		}
		uncachedPushFailed(w)
		return
	}
	n.PushStatus = store.PushAccepted
	writeJSON(w, created(w, false), toNotificationJSON(n))
}

func uncachedPushFailed(w http.ResponseWriter) {
	writeError(w, http.StatusBadGateway, "push_failed", "could not confirm that APNs accepted the uncached preview")
}
