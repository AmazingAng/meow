package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"meow.blue/meow/server/internal/api"
	"meow.blue/meow/server/internal/push"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuditIPv6RegistrationAllowance(t *testing.T) {
	e := setup(t)
	send := func(ip string) int {
		r := httptest.NewRequest("POST", "/v1/devices", bytes.NewBufferString(`{"name":"audit"}`))
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		e.srv.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 10; i++ {
		if s := send("[2001:db8:1234:5678::1]:1234"); s != 201 {
			t.Fatal(s)
		}
	}
	same := send("[2001:db8:1234:5678::1]:1234")
	alias := send("[2001:db8:1234:5678::2]:1234")
	t.Logf("same IPv6 address after 10: %d; another address in same /64: %d", same, alias)
	if same != 429 || alias != 429 {
		t.Fatalf("registration budget bypassed: same=%d alias=%d", same, alias)
	}
}
func TestAuditGatewayProviderRetryAfter(t *testing.T) {
	st, p, base := gatewayFixture(t)
	route := enroll(t, base, "audit_cooldown")
	p.err = &push.Error{Status: 429, Reason: "TooManyRequests", RetryAfter: 120 * time.Second}
	h := api.NewGateway(api.GatewayConfig{Store: st, Push: p})
	b, _ := json.Marshal(wake(route))
	r := httptest.NewRequest("POST", "/v1/gateway/push", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+route.PushToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	t.Logf("gateway upstream 429/120s -> status=%d retry-after=%q", w.Code, w.Header().Get("Retry-After"))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "120" {
		t.Fatal(fmt.Sprintf("upstream cooldown lost: %d %q", w.Code, w.Header().Get("Retry-After")))
	}
}
