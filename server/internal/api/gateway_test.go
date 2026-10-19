package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meow.blue/meow/server/internal/api"
	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

type gatewayPush struct {
	mu       sync.Mutex
	messages []push.Message
	err      error
}

func (p *gatewayPush) Push(_ context.Context, m push.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, m)
	return p.err
}

type routeRegistration struct {
	ID              string `json:"id"`
	ManagementToken string `json:"management_token"`
	PushToken       string `json:"push_token"`
	BindingID       string `json:"binding_id"`
}

func gatewayRequest(t *testing.T, base, method, path, token string, body any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}
func gatewayFixture(t *testing.T) (*store.Store, *gatewayPush, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := &gatewayPush{}
	ts := httptest.NewServer(api.NewGateway(api.GatewayConfig{Store: st, Push: p}))
	t.Cleanup(func() { ts.Close(); st.Close() })
	return st, p, ts.URL
}
func enroll(t *testing.T, base, binding string) routeRegistration {
	t.Helper()
	status, body := gatewayRequest(t, base, "POST", "/v1/gateway/devices", "", map[string]any{
		"apns_token": strings.Repeat("a", 64), "apns_env": "sandbox", "binding_id": binding,
	})
	if status != 201 {
		t.Fatalf("enrollment status=%d body=%s", status, body)
	}
	var r routeRegistration
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.ID == "" || r.PushToken == "" || r.ManagementToken == "" || r.PushToken == r.ManagementToken || r.BindingID != binding {
		t.Fatalf("bad registration: %+v", r)
	}
	return r
}
func wake(r routeRegistration) map[string]any {
	return map[string]any{"device_id": r.ID, "notification_id": "ntf_test1", "priority": "normal", "expires_at": time.Now().Add(time.Minute).UTC()}
}

func TestGatewayRegistrationValidation(t *testing.T) {
	_, p, base := gatewayFixture(t)
	for _, body := range []map[string]any{
		{"apns_token": "x", "apns_env": "sandbox", "binding_id": "bind_1"},
		{"apns_token": strings.Repeat("a", 64), "apns_env": "wrong", "binding_id": "bind_1"},
		{"apns_token": strings.Repeat("a", 64), "apns_env": "sandbox", "binding_id": ""},
		{"apns_token": strings.Repeat("a", 64), "apns_env": "sandbox", "binding_id": "bind_1", "url": "http://internal"},
	} {
		status, _ := gatewayRequest(t, base, "POST", "/v1/gateway/devices", "", body)
		if status != 422 && status != 400 {
			t.Fatalf("invalid registration=%d", status)
		}
	}
	if len(p.messages) != 0 {
		t.Fatal("invalid enrollment must not send push")
	}
}

func TestGatewayWakePrivacyAndIsolation(t *testing.T) {
	_, p, base := gatewayFixture(t)
	r := enroll(t, base, "bind_1")
	other := enroll(t, base, "bind_2")
	for _, token := range []string{"", r.ManagementToken, other.PushToken, "meow_send_only"} {
		status, _ := gatewayRequest(t, base, "POST", "/v1/gateway/push", token, wake(r))
		if status != 401 {
			t.Fatalf("wrong wake credential=%d", status)
		}
	}
	status, body := gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, wake(r))
	if status != 200 || !bytes.Contains(body, []byte(`"accepted":true`)) {
		t.Fatalf("wake=%d %s", status, body)
	}
	if len(p.messages) != 1 {
		t.Fatal("wake count")
	}
	m := p.messages[0]
	if m.Token != strings.Repeat("a", 64) || !m.Sandbox {
		t.Fatalf("wrong target: %+v", m)
	}
	var payload struct {
		APS  map[string]any `json:"aps"`
		Meow map[string]any `json:"meow"`
	}
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.APS["category"] != nil || payload.APS["mutable-content"] != float64(1) {
		t.Fatalf("unsafe fallback: %s", m.Payload)
	}
	if payload.Meow["id"] != "ntf_test1" || payload.Meow["binding_id"] != "bind_1" || payload.Meow["relay"] != true || len(payload.Meow) != 3 {
		t.Fatalf("metadata leaked: %s", m.Payload)
	}
	for _, forbidden := range []string{r.PushToken, r.ManagementToken, "question_id", "choices", "source", "server", "mdt_"} {
		if bytes.Contains(m.Payload, []byte(forbidden)) {
			t.Fatalf("payload contains %s", forbidden)
		}
	}
	for _, field := range []string{"title", "body", "source", "apns_token", "server", "choices", "binding_id"} {
		in := wake(r)
		in[field] = "private value"
		status, _ := gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, in)
		if status != 400 {
			t.Fatalf("extra %s accepted: %d", field, status)
		}
	}
	for _, expiry := range []time.Time{time.Now().Add(-time.Minute), time.Now().Add(25 * time.Hour)} {
		in := wake(r)
		in["expires_at"] = expiry
		status, _ := gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, in)
		if status != 422 {
			t.Fatalf("invalid expiry=%d", status)
		}
	}
	in := wake(r)
	in["notification_id"] = "../v1/keys"
	if status, _ := gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, in); status != 422 {
		t.Fatalf("path injection=%d", status)
	}
}

func TestGatewayManagementAndRevocation(t *testing.T) {
	st, p, base := gatewayFixture(t)
	r := enroll(t, base, "bind_1")
	update := map[string]any{"apns_token": strings.Repeat("b", 64), "apns_env": "production"}
	for _, method := range []string{"PATCH", "DELETE"} {
		status, _ := gatewayRequest(t, base, method, "/v1/gateway/devices/"+r.ID, r.PushToken, update)
		if status != 401 {
			t.Fatalf("wake credential can manage: %d", status)
		}
	}
	status, _ := gatewayRequest(t, base, "PATCH", "/v1/gateway/devices/"+r.ID, r.ManagementToken, update)
	if status != 204 {
		t.Fatalf("update=%d", status)
	}
	status, _ = gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, wake(r))
	if status != 200 || p.messages[0].Token != strings.Repeat("b", 64) || p.messages[0].Sandbox {
		t.Fatal("updated target not used")
	}
	backup := filepath.Join(t.TempDir(), "snapshot.db")
	if err := st.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(disk, []byte(r.PushToken)) || bytes.Contains(disk, []byte(r.ManagementToken)) {
		t.Fatal("gateway stores plaintext secrets")
	}
	status, _ = gatewayRequest(t, base, "DELETE", "/v1/gateway/devices/"+r.ID, r.ManagementToken, nil)
	if status != 204 {
		t.Fatalf("revoke=%d", status)
	}
	status, _ = gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, wake(r))
	if status != 401 {
		t.Fatalf("revoked wake=%d", status)
	}
}

func TestGatewayAPNsFailureNeverAccepted(t *testing.T) {
	_, p, base := gatewayFixture(t)
	r := enroll(t, base, "bind_1")
	for _, err := range []error{errors.New("network unavailable"), &push.Error{Status: 410, Reason: "Unregistered"}, push.ErrNotConfigured} {
		p.err = err
		status, body := gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, wake(r))
		if status < 400 || bytes.Contains(body, []byte(`"accepted":true`)) {
			t.Fatalf("failure accepted=%d %s", status, body)
		}
	}
}

func TestGatewayConcurrentQuota(t *testing.T) {
	_, p, base := gatewayFixture(t)
	r := enroll(t, base, "bind_1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[int]int{}
	for range 80 {
		wg.Go(func() {
			status, _ := gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, wake(r))
			mu.Lock()
			counts[status]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if counts[200] != 60 || counts[429] != 20 || len(p.messages) != 60 {
		t.Fatalf("quota counts=%v pushes=%d", counts, len(p.messages))
	}
}

func TestGatewayHealthMalformedInputAndImmutableBinding(t *testing.T) {
	st, _, base := gatewayFixture(t)
	status, _ := gatewayRequest(t, base, "GET", "/healthz", "", nil)
	if status != 200 {
		t.Fatalf("healthy=%d", status)
	}
	r := enroll(t, base, "bind_1")
	status, _ = gatewayRequest(t, base, "PATCH", "/v1/gateway/devices/"+r.ID, r.ManagementToken, map[string]string{
		"apns_token": strings.Repeat("a", 64), "apns_env": "sandbox", "binding_id": "bind_different",
	})
	if status != 422 {
		t.Fatalf("binding mutated=%d", status)
	}
	for _, raw := range []string{`{"device_id":"x"} {"body":"secret"}`, `{"device_id":"x"`, strings.Repeat("x", 5000)} {
		req, err := http.NewRequest("POST", base+"/v1/gateway/push", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+r.PushToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("malformed body=%d", res.StatusCode)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	status, _ = gatewayRequest(t, base, "GET", "/healthz", "", nil)
	if status != 503 {
		t.Fatalf("closed database health=%d", status)
	}
	status, _ = gatewayRequest(t, base, "POST", "/v1/gateway/push", r.PushToken, wake(r))
	if status != 500 {
		t.Fatalf("database error was hidden=%d", status)
	}
}

func TestGatewayRegistrationIsAutomaticAndPerClient(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "trusted-proxy"}[trusted], func(t *testing.T) {
			st, _, _ := gatewayFixture(t)
			handler := api.NewGateway(api.GatewayConfig{Store: st, Push: &gatewayPush{}, TrustProxy: trusted})
			register := func(ip, forwarded string) int {
				req := httptest.NewRequest("POST", "/v1/gateway/devices", strings.NewReader(`{"apns_token":"`+strings.Repeat("a", 64)+`","apns_env":"sandbox","binding_id":"bind_auto"}`))
				req.RemoteAddr = ip + ":1234"
				req.Header.Set("X-Forwarded-For", forwarded)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec.Code
			}
			ip := "192.0.2.1"
			second := "192.0.2.2"
			forwarded := "198.51.100.1"
			if trusted {
				ip = "192.0.2.10"
				second = ip
			}
			for range 10 {
				if status := register(ip, forwarded); status != 201 {
					t.Fatalf("anonymous registration=%d", status)
				}
			}
			if status := register(ip, forwarded); status != 429 {
				t.Fatalf("per-client limit=%d", status)
			}
			if status := register(second, "198.51.100.2"); status != 201 {
				t.Fatalf("another client shares global limit=%d", status)
			}
			if !trusted && register(ip, "203.0.113.99") != 429 {
				t.Fatal("untrusted proxy header bypassed IP limit")
			}
		})
	}
}
