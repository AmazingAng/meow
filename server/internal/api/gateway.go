package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

type GatewayConfig struct {
	TrustProxy bool
	Store      *store.Store
	Push       push.Pusher
}

type gateway struct {
	GatewayConfig
	enrollLimit *limiter
	mux         *http.ServeMux
}

// NewGateway is an APNs wake relay with automatic device registration. It never receives a
// self-hosted server URL or message content and cannot call either server APIs.
func NewGateway(c GatewayConfig) http.Handler {
	g := &gateway{GatewayConfig: c, enrollLimit: newLimiter(1.0/360, 10), mux: http.NewServeMux()}
	g.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := g.Store.Ping(r.Context()); err != nil {
			writeError(w, 503, "unavailable", "database unavailable")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	g.mux.HandleFunc("POST /v1/gateway/devices", g.enroll)
	g.mux.HandleFunc("PATCH /v1/gateway/devices/{id}", g.update)
	g.mux.HandleFunc("DELETE /v1/gateway/devices/{id}", g.revoke)
	g.mux.HandleFunc("POST /v1/gateway/push", g.wake)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		g.mux.ServeHTTP(w, r)
	})
}

// Reject unknown fields and multiple JSON values, so message content cannot
// accidentally become part of the relay protocol.
func strictJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		writeError(w, 400, "invalid_json", "invalid or unsupported request fields")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_json", "expected one JSON object")
		return false
	}
	return true
}

var bindingRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type gatewayDeviceInput struct {
	APNsToken string `json:"apns_token"`
	APNsEnv   string `json:"apns_env"`
	BindingID string `json:"binding_id,omitempty"`
}

func validGatewayDevice(w http.ResponseWriter, in gatewayDeviceInput) bool {
	if !apnsTokenRE.MatchString(in.APNsToken) {
		writeError(w, 422, "invalid_request", "invalid APNs token")
		return false
	}
	return checkEnum(w, "apns_env", in.APNsEnv, "sandbox", "production")
}

func (g *gateway) enroll(w http.ResponseWriter, r *http.Request) {
	if ok, retry := g.enrollLimit.allow(clientIP(r, g.TrustProxy)); !ok {
		tooManyRequests(w, retry)
		return
	}
	var in gatewayDeviceInput
	if !strictJSON(w, r, &in) || !validGatewayDevice(w, in) {
		return
	}
	if !bindingRE.MatchString(in.BindingID) {
		writeError(w, 422, "invalid_request", "invalid binding ID")
		return
	}
	reg, err := g.Store.RegisterGatewayRoute(r.Context(), in.APNsToken, in.APNsEnv, in.BindingID)
	if err != nil {
		gatewayStoreError(w, err)
		return
	}
	writeJSON(w, 201, reg)
}

func gatewayStoreError(w http.ResponseWriter, err error) {
	var quota *store.QuotaError
	switch {
	case errors.As(err, &quota):
		tooManyRequests(w, time.Minute)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 401, "unauthorized", "invalid or revoked route credentials")
	default:
		writeError(w, 500, "internal", "gateway database unavailable")
	}
}

func (g *gateway) update(w http.ResponseWriter, r *http.Request) {
	if _, err := g.Store.GatewayRoute(r.Context(), r.PathValue("id"), bearer(r), true); err != nil {
		gatewayStoreError(w, err)
		return
	}
	var in gatewayDeviceInput
	if !strictJSON(w, r, &in) || !validGatewayDevice(w, in) {
		return
	}
	// The binding is immutable; updates rotate the APNs token only.
	if in.BindingID != "" {
		writeError(w, 422, "invalid_request", "binding ID is immutable")
		return
	}
	if err := g.Store.UpdateGatewayRoute(r.Context(), r.PathValue("id"), bearer(r), in.APNsToken, in.APNsEnv); err != nil {
		gatewayStoreError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (g *gateway) revoke(w http.ResponseWriter, r *http.Request) {
	if err := g.Store.RevokeGatewayRoute(r.Context(), r.PathValue("id"), bearer(r)); err != nil {
		gatewayStoreError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (g *gateway) wake(w http.ResponseWriter, r *http.Request) {
	var in push.WakeRequest
	if !strictJSON(w, r, &in) {
		return
	}
	route, err := g.Store.GatewayRoute(r.Context(), in.DeviceID, bearer(r), false)
	if err != nil {
		gatewayStoreError(w, err)
		return
	}
	now := g.Store.Now()
	if !push.ValidNotificationID(in.NotificationID) || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(24*time.Hour)) {
		writeError(w, 422, "invalid_request", "invalid notification ID or expiry")
		return
	}
	if !checkEnum(w, "priority", in.Priority, "low", "normal", "high") {
		return
	}
	if err := g.Store.TakeGatewayQuota(r.Context(), route.ID); err != nil {
		gatewayStoreError(w, err)
		return
	}
	err = g.Push.Push(r.Context(), push.WakeMessage(route, in))
	if err != nil {
		status := http.StatusBadGateway
		var ae *push.Error
		switch {
		case errors.Is(err, push.ErrNotConfigured):
			status = 503
		case errors.As(err, &ae) && ae.InvalidToken():
			status = 410
		case errors.As(err, &ae) && !ae.Retryable():
			status = 422
		}
		writeError(w, status, "push_failed", "APNs did not accept the wake notification")
		return
	}
	writeJSON(w, 200, map[string]bool{"accepted": true})
}
