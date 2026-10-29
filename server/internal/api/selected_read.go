package api

import (
	"net/http"

	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

func (s *Server) markSelectedRead(w http.ResponseWriter, r *http.Request, p principal) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !strictJSON(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > store.MaxReadBatch {
		writeError(w, 422, "invalid_request", "ids must contain 1 to 200 notification IDs")
		return
	}
	for _, id := range in.IDs {
		if !push.ValidNotificationID(id) {
			writeError(w, 422, "invalid_request", "invalid notification ID")
			return
		}
	}
	n, err := s.st.MarkSelectedRead(r.Context(), p.device.ID, in.IDs)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"marked": n})
}
