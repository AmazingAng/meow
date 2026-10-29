// Package api implements the Meow HTTP API.
//
// Agents and scripts authenticate with a send-only Device Key ("meow_…").
// The iOS app authenticates with its device token ("mdt_…"); only the device
// token can answer questions or manage keys.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/store"
)

const maxBody = 64 << 10

// Enqueuer schedules a stored notification for push delivery.
type Enqueuer interface{ Enqueue(notificationID string) }

type Config struct {
	GatewayURL string // fixed operator-configured gateway; empty selects direct APNs
	Store      *store.Store
	Push       Enqueuer
	Log        *slog.Logger
	TrustProxy bool          // use the last X-Forwarded-For hop as the client IP
	MaxWait    time.Duration // upper bound for long polls; default 60s
}

type Server struct {
	gatewayURL string
	st         *store.Store
	push       Enqueuer
	log        *slog.Logger
	trustProxy bool
	maxWait    time.Duration
	waits      *broker

	ipLimit, regLimit, keyLimit, devLimit *limiter
	mux                                   *http.ServeMux
}

func New(c Config) *Server {
	if c.MaxWait <= 0 {
		c.MaxWait = 60 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		st: c.Store, push: c.Push, log: c.Log, trustProxy: c.TrustProxy, maxWait: c.MaxWait, gatewayURL: c.GatewayURL,
		waits:    newBroker(),
		ipLimit:  newLimiter(20, 200),     // all requests, per client IP
		regLimit: newLimiter(1.0/360, 10), // device registrations: 10/hour per IP
		keyLimit: newLimiter(1, 60),       // per Device Key
		devLimit: newLimiter(5, 100),      // per mobile device
	}

	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", s.health)

	// Agent API (Device Key).
	m.HandleFunc("GET /v1/whoami", s.withKey("", s.whoami))
	m.HandleFunc("POST /v1/notifications", s.withKey(store.ScopeNotify, s.createNotification))
	m.HandleFunc("POST /v1/questions", s.withKey(store.ScopeAsk, s.createQuestion))
	m.HandleFunc("GET /v1/questions/{id}", s.withAny(s.getQuestion))
	m.HandleFunc("GET /v1/questions/{id}/wait", s.withKey(store.ScopeAsk, s.waitQuestion))
	m.HandleFunc("POST /v1/questions/{id}/cancel", s.withKey(store.ScopeAsk, s.cancelQuestion))

	// Mobile API (device token).
	m.HandleFunc("POST /v1/devices", s.registerDevice)
	m.HandleFunc("GET /v1/push-config", s.withDevice(s.pushConfig))
	m.HandleFunc("PUT /v1/device/push-gateway", s.withDevice(s.bindGateway))
	m.HandleFunc("GET /v1/device", s.withDevice(s.getDevice))
	m.HandleFunc("PATCH /v1/device", s.withDevice(s.updateDevice))
	m.HandleFunc("GET /v1/keys", s.withDevice(s.listKeys))
	m.HandleFunc("POST /v1/keys", s.withDevice(s.createKey))
	m.HandleFunc("DELETE /v1/keys/{id}", s.withDevice(s.revokeKey))
	m.HandleFunc("DELETE /v1/inbox", s.withDevice(s.deleteInbox))
	m.HandleFunc("DELETE /v1/notifications/{id}", s.withDevice(s.deleteNotification))
	m.HandleFunc("GET /v1/inbox", s.withDevice(s.inbox))
	m.HandleFunc("GET /v1/inbox/counts", s.withDevice(s.counts))
	m.HandleFunc("POST /v1/inbox/read", s.withDevice(s.markAllRead))
	m.HandleFunc("POST /v1/inbox/read-selected", s.withDevice(s.markSelectedRead))
	m.HandleFunc("GET /v1/notifications/{id}", s.withDevice(s.getNotification))
	m.HandleFunc("POST /v1/notifications/{id}/read", s.withDevice(s.markRead))
	m.HandleFunc("POST /v1/notifications/{id}/delivered", s.withDevice(s.markDelivered))
	m.HandleFunc("POST /v1/questions/{id}/responses", s.withDevice(s.respond))

	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	s.mux = m
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	ip := s.clientIP(r)
	if ok, retry := s.ipLimit.allow(ip); !ok {
		tooManyRequests(rec, retry)
	} else {
		r.Body = http.MaxBytesReader(rec, r.Body, maxBody)
		s.mux.ServeHTTP(rec, r)
	}
	route := r.Pattern
	if route == "" {
		route = r.Method + " " + r.URL.Path
	}
	// Never log headers or bodies: they carry bearer secrets and message content.
	s.log.Info("http", "route", route, "status", rec.status, "dur", time.Since(start).Round(time.Millisecond), "ip", ip)
}

// ExpireDue expires overdue questions and wakes their waiters.
func (s *Server) ExpireDue(ctx context.Context) error {
	ids, err := s.st.ExpireDue(ctx)
	for _, id := range ids {
		s.waits.publish(id)
	}
	return err
}

// Close releases all long polls; call it when the HTTP server starts shutting down.
func (s *Server) Close() { s.waits.close() }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) clientIP(r *http.Request) string { return clientIP(r, s.trustProxy) }

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			hops := strings.Split(xff, ",")
			return strings.TrimSpace(hops[len(hops)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- authentication ----

type principal struct {
	key    *store.APIKey
	device *store.Device
}

func (p principal) deviceID() string {
	if p.key != nil {
		return p.key.DeviceID
	}
	return p.device.ID
}

type authedHandler func(http.ResponseWriter, *http.Request, principal)

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (principal, bool) {
	tok := bearer(r)
	var p principal
	var err error
	switch {
	case tok == "":
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return p, false
	case strings.HasPrefix(tok, "meow_"):
		var k store.APIKey
		if k, err = s.st.KeyBySecret(r.Context(), tok); err == nil {
			p.key = &k
		}
	case strings.HasPrefix(tok, "mdt_"):
		var d store.Device
		if d, err = s.st.DeviceByToken(r.Context(), tok); err == nil {
			p.device = &d
		}
	default:
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or revoked credentials")
		return p, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return p, false
	}
	var ok bool
	var retry time.Duration
	if p.key != nil {
		ok, retry = s.keyLimit.allow(p.key.ID)
	} else {
		ok, retry = s.devLimit.allow(p.device.ID)
	}
	if !ok {
		tooManyRequests(w, retry)
	}
	return p, ok
}

// withKey requires a Device Key, holding scope unless scope is empty.
func (s *Server) withKey(scope string, h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(w, r)
		switch {
		case !ok:
		case p.key == nil:
			writeError(w, http.StatusForbidden, "forbidden", "this endpoint requires a Device Key")
		case scope != "" && !p.key.Has(scope):
			writeError(w, http.StatusForbidden, "forbidden", "Device Key lacks the "+scope+" scope")
		default:
			h(w, r, p)
		}
	}
}

// withDevice requires the mobile device token.
func (s *Server) withDevice(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(w, r)
		switch {
		case !ok:
		case p.device == nil:
			writeError(w, http.StatusForbidden, "forbidden", "this endpoint requires the Meow app's device credentials")
		default:
			h(w, r, p)
		}
	}
}

func (s *Server) withAny(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := s.authenticate(w, r); ok {
			h(w, r, p)
		}
	}
}

// ---- request helpers ----

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body exceeds 64 KB")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
		}
		return false
	}
	return true
}

// idempotency reads the Idempotency-Key header and fingerprints the parsed request.
func idempotency(w http.ResponseWriter, r *http.Request, input any) (store.Idem, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return store.Idem{}, true
	}
	if err := message.ValidateIdempotencyKey(key); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", err.Error())
		return store.Idem{}, false
	}
	b, _ := json.Marshal(input)
	sum := sha256.Sum256(b)
	return store.Idem{Key: key, RequestHash: hex.EncodeToString(sum[:])}, true
}

// checkText validates a user-supplied string field.
func checkText(w http.ResponseWriter, field, v string, required bool, maxRunes int) bool {
	if err := message.ValidateText(field, v, required, maxRunes); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return false
	}
	return true
}

func checkEnum(w http.ResponseWriter, field, v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	writeError(w, http.StatusUnprocessableEntity, "invalid_request", field+" must be one of "+strings.Join(allowed, ", "))
	return false
}

// ---- response helpers ----

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]errorBody{"error": {Code: code, Message: msg}})
}

func tooManyRequests(w http.ResponseWriter, retry time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds()+0.999))))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return // client went away
	}
	s.log.Error("internal error", "route", r.Pattern, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal server error")
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
