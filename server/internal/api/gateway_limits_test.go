package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"testing/synctest"
	"time"

	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

type gatewayLimitPush struct{}

func (gatewayLimitPush) Push(context.Context, push.Message) error { return nil }

func gatewayLimitStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func gatewayLimitRequest(handler http.Handler, ip, forwarded, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	r.RemoteAddr = ip + ":1234"
	r.Header.Set("X-Forwarded-For", forwarded)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func requireGatewayRateLimit(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Error struct{ Code string }
	}
	seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != 429 || err != nil || seconds < 1 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != "rate_limited" {
		t.Fatalf("expected retryable rate limit: status=%d retry=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
}

type unreadableGatewayBody struct{}

func (unreadableGatewayBody) Read([]byte) (int, error) { panic("rate-limited body must not be read") }

func TestGatewayLimitsAllRequestsBeforeBodyAndDatabase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := gatewayLimitStore(t)
		h := NewGateway(GatewayConfig{Store: st, Push: gatewayLimitPush{}})
		for i := 0; i < 200; i++ {
			w := gatewayLimitRequest(h, "192.0.2.1", "", "GET", fmt.Sprintf("/missing/%d", i), "", nil)
			if w.Code != 404 {
				t.Fatalf("request %d unexpectedly rejected: %d", i, w.Code)
			}
		}
		st.Close()
		for _, route := range []struct{ method, path string }{
			{"POST", "/v1/gateway/push"}, {"POST", "/v1/gateway/devices"},
			{"PATCH", "/v1/gateway/devices/other"}, {"DELETE", "/v1/gateway/devices/other"},
			{"GET", "/healthz"}, {"GET", "/another-missing"},
		} {
			w := gatewayLimitRequest(h, "192.0.2.1", "", route.method, route.path, "invalid", unreadableGatewayBody{})
			requireGatewayRateLimit(t, w)
		}
		other := gatewayLimitRequest(h, "192.0.2.2", "", "GET", "/missing", "", nil)
		if other.Code != 404 {
			t.Fatalf("one client exhausted another client's allowance: %d", other.Code)
		}
		time.Sleep(50 * time.Millisecond)
		if w := gatewayLimitRequest(h, "192.0.2.1", "", "GET", "/missing", "", nil); w.Code != 404 {
			t.Fatalf("20/s allowance did not replenish after 50ms: %d", w.Code)
		}
		requireGatewayRateLimit(t, gatewayLimitRequest(h, "192.0.2.1", "", "GET", "/missing", "", nil))
	})
}

func TestGatewayLimitsInvalidCredentialsShareBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewGateway(GatewayConfig{Store: gatewayLimitStore(t), Push: gatewayLimitPush{}})
		for i := 0; i < 200; i++ {
			w := gatewayLimitRequest(h, "192.0.2.1", "", "DELETE", fmt.Sprintf("/v1/gateway/devices/gwd_%d", i), fmt.Sprintf("invalid_%d", i), nil)
			if w.Code != 401 {
				t.Fatalf("invalid auth %d: %d", i, w.Code)
			}
		}
		requireGatewayRateLimit(t, gatewayLimitRequest(h, "192.0.2.1", "", "DELETE", "/v1/gateway/devices/gwd_new", "another_invalid", nil))
	})
}

func TestGatewayLimitsRoutesShareIPBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := gatewayLimitStore(t)
		h := NewGateway(GatewayConfig{Store: st, Push: gatewayLimitPush{}})
		routes := make([]store.GatewayRegistration, 5)
		for i := range routes {
			var err error
			routes[i], err = st.RegisterGatewayRoute(context.Background(), strings.Repeat("a", 64), "sandbox", fmt.Sprint("binding_", i))
			if err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 201; i++ {
			route := routes[i%len(routes)]
			b, err := json.Marshal(push.WakeRequest{DeviceID: route.ID, NotificationID: "ntf_review", Priority: "normal", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			w := gatewayLimitRequest(h, "192.0.2.1", "", "POST", "/v1/gateway/push", route.PushToken, bytes.NewReader(b))
			if i == 200 {
				requireGatewayRateLimit(t, w)
			} else if w.Code != 200 {
				t.Fatalf("wake %d: %d %s", i, w.Code, w.Body)
			}
		}
	})
}

func TestGatewayLimitsTrustedProxyAndAddressAliases(t *testing.T) {
	for _, tt := range []struct {
		name                                   string
		trusted                                bool
		firstIP, firstXFF, secondIP, secondXFF string
	}{
		{"spoofed-header", false, "192.0.2.1", "198.51.100.1", "192.0.2.1", "198.51.100.2"},
		{"trusted-last-hop", true, "192.0.2.1", "203.0.113.1, 198.51.100.1", "192.0.2.2", "203.0.113.2, 198.51.100.1"},
		{"mapped-ipv4", false, "192.0.2.1", "", "[::ffff:192.0.2.1]", ""},
		{"ipv6-prefix", false, "[2001:db8:abcd:1234::1]", "", "[2001:db8:abcd:1234::2]", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := NewGateway(GatewayConfig{Store: gatewayLimitStore(t), Push: gatewayLimitPush{}, TrustProxy: tt.trusted})
				for i := 0; i < 200; i++ {
					if w := gatewayLimitRequest(h, tt.firstIP, tt.firstXFF, "GET", "/missing", "", nil); w.Code != 404 {
						t.Fatalf("request %d: %d", i, w.Code)
					}
				}
				requireGatewayRateLimit(t, gatewayLimitRequest(h, tt.secondIP, tt.secondXFF, "GET", "/missing", "", nil))
				if tt.trusted {
					w := gatewayLimitRequest(h, tt.secondIP, "198.51.100.2", "GET", "/missing", "", nil)
					if w.Code != 404 {
						t.Fatalf("different forwarded client shared budget: %d", w.Code)
					}
				}
			})
		})
	}
}

func TestGatewayLimitsConcurrentIPAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewGateway(GatewayConfig{Store: gatewayLimitStore(t), Push: gatewayLimitPush{}})
		var wg sync.WaitGroup
		results := make(chan int, 250)
		for i := 0; i < 250; i++ {
			wg.Go(func() { results <- gatewayLimitRequest(h, "192.0.2.1", "", "GET", "/missing", "", nil).Code })
		}
		wg.Wait()
		close(results)
		counts := map[int]int{}
		for status := range results {
			counts[status]++
		}
		if counts[404] != 200 || counts[429] != 50 {
			t.Fatalf("concurrent allowance: %v", counts)
		}
	})
}

type gatewayBlockingPush struct {
	entered             chan struct{}
	release             chan struct{}
	mu                  sync.Mutex
	active, peak, calls int
	err                 error
}

func (p *gatewayBlockingPush) Push(ctx context.Context, _ push.Message) error {
	p.mu.Lock()
	p.active++
	p.calls++
	p.peak = max(p.peak, p.active)
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	p.entered <- struct{}{}
	select {
	case <-p.release:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func gatewayLimitRoute(t *testing.T, st *store.Store, binding string) store.GatewayRegistration {
	t.Helper()
	r, err := st.RegisterGatewayRoute(context.Background(), strings.Repeat("a", 64), "sandbox", binding)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func gatewayWakeRequest(h http.Handler, route store.GatewayRegistration, ip string, ctx context.Context) *httptest.ResponseRecorder {
	b, err := json.Marshal(push.WakeRequest{DeviceID: route.ID, NotificationID: "ntf_limits", Priority: "normal", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		panic(err)
	}
	r := httptest.NewRequest("POST", "/v1/gateway/push", bytes.NewReader(b)).WithContext(ctx)
	r.RemoteAddr = ip + ":1234"
	r.Header.Set("Authorization", "Bearer "+route.PushToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGatewayLimitsGlobalPushAdmission(t *testing.T) {
	for _, config := range []int{0, 2} {
		t.Run(fmt.Sprint(config), func(t *testing.T) {
			st := gatewayLimitStore(t)
			p := &gatewayBlockingPush{entered: make(chan struct{}, 64), release: make(chan struct{})}
			h := NewGateway(GatewayConfig{Store: st, Push: p, MaxConcurrentPushes: config})
			limit := config
			if limit == 0 {
				limit = 32
			}
			route := gatewayLimitRoute(t, st, "holding")
			busy := gatewayLimitRoute(t, st, "busy")
			var wg sync.WaitGroup
			defer func() { close(p.release); wg.Wait() }()
			for i := 0; i < limit; i++ {
				wg.Go(func() {
					w := gatewayWakeRequest(h, route, "192.0.2.1", context.Background())
					if w.Code != 200 {
						t.Errorf("admitted request: %d %s", w.Code, w.Body)
					}
				})
			}
			for i := 0; i < limit; i++ {
				select {
				case <-p.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("available slots rejected or serialized requests")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			w := gatewayWakeRequest(h, busy, "192.0.2.2", ctx)
			requireGatewayRateLimit(t, w)
			if w.Header().Get("Retry-After") != "1" {
				t.Fatalf("busy retry: %q", w.Header().Get("Retry-After"))
			}
			p.mu.Lock()
			calls, peak := p.calls, p.peak
			p.mu.Unlock()
			if calls != limit || peak != limit {
				t.Fatalf("provider calls=%d peak=%d limit=%d", calls, peak, limit)
			}
			// Busy wakes must not spend the durable route quota.
			for i := 0; i < 60; i++ {
				if err := st.TakeGatewayQuota(context.Background(), busy.ID); err != nil {
					t.Fatalf("busy spent route quota at %d: %v", i, err)
				}
			}
			if err := st.TakeGatewayQuota(context.Background(), busy.ID); err == nil {
				t.Fatal("route quota disappeared")
			}
			if w := gatewayLimitRequest(h, "192.0.2.3", "", "GET", "/healthz", "", nil); w.Code != 200 {
				t.Fatalf("busy blocked health: %d", w.Code)
			}
			registration := `{"apns_token":"` + strings.Repeat("b", 64) + `","apns_env":"production","binding_id":"still_registers"}`
			if w := gatewayLimitRequest(h, "192.0.2.3", "", "POST", "/v1/gateway/devices", "", strings.NewReader(registration)); w.Code != 201 {
				t.Fatalf("busy blocked registration: %d %s", w.Code, w.Body)
			}
			if w := gatewayLimitRequest(h, "192.0.2.3", "", "DELETE", "/v1/gateway/devices/"+route.ID, "invalid", nil); w.Code != 401 {
				t.Fatalf("busy bypassed authentication: %d", w.Code)
			}
			if w := gatewayLimitRequest(h, "192.0.2.3", "", "DELETE", "/v1/gateway/devices/"+busy.ID, busy.ManagementToken, nil); w.Code != 204 {
				t.Fatalf("busy blocked revocation: %d", w.Code)
			}
		})
	}
}

func TestGatewayLimitsReleasePushSlot(t *testing.T) {
	for _, mode := range []string{"success", "provider-error", "cancelled", "route-quota"} {
		t.Run(mode, func(t *testing.T) {
			st := gatewayLimitStore(t)
			p := &gatewayBlockingPush{entered: make(chan struct{}, 4), release: make(chan struct{})}
			h := NewGateway(GatewayConfig{Store: st, Push: p, MaxConcurrentPushes: 1})
			route := gatewayLimitRoute(t, st, "first")
			if mode == "provider-error" {
				p.err = errors.New("fixture provider failure")
			}
			if mode == "route-quota" {
				for i := 0; i < 60; i++ {
					if err := st.TakeGatewayQuota(context.Background(), route.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if mode != "cancelled" {
				close(p.release)
			}
			w := gatewayWakeRequest(h, route, "192.0.2.1", ctx)
			want := 200
			switch mode {
			case "provider-error", "cancelled":
				want = 502
			case "route-quota":
				want = 429
			}
			if w.Code != want {
				t.Fatalf("first request %d want %d: %s", w.Code, want, w.Body)
			}
			if mode == "cancelled" {
				close(p.release)
			}
			p.err = nil
			fresh := gatewayLimitRoute(t, st, "fresh")
			if next := gatewayWakeRequest(h, fresh, "192.0.2.2", context.Background()); next.Code != 200 {
				t.Fatalf("slot leaked: %d %s", next.Code, next.Body)
			}
		})
	}
}

func TestGatewayLimitsAddressProperties(t *testing.T) {
	property := func(a, b uint16, ipv4 uint32) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = fmt.Sprintf("[2001:db8:%x:%x::1]:80", a, b)
		first := gatewayClientIP(r, false)
		r.RemoteAddr = fmt.Sprintf("[2001:db8:%x:%x:abcd::2]:80", a, b)
		if gatewayClientIP(r, false) != first {
			return false
		}
		r.RemoteAddr = fmt.Sprintf("[2001:db8:%x:%x::1]:80", a^1, b)
		if gatewayClientIP(r, false) == first {
			return false
		}
		ip := fmt.Sprintf("%d.%d.%d.%d", byte(ipv4>>24), byte(ipv4>>16), byte(ipv4>>8), byte(ipv4))
		r.RemoteAddr = ip + ":80"
		first = gatewayClientIP(r, false)
		r.RemoteAddr = "[::ffff:" + ip + "]:80"
		return gatewayClientIP(r, false) == first
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100, Rand: rand.New(rand.NewSource(20261101))}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "local-transport"
	if key := gatewayClientIP(r, false); key != "local-transport" {
		t.Fatalf("non-IP transport key %q", key)
	}
}

func TestGatewayLimitsCancelledAdmission(t *testing.T) {
	g := &gateway{pushSlots: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/v1/gateway/push", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	if g.admitPush(w, r) || w.Code != http.StatusRequestTimeout || len(g.pushSlots) != 0 {
		t.Fatalf("cancelled request occupied a slot: status=%d slots=%d", w.Code, len(g.pushSlots))
	}
}

func TestGatewayLimitsLiveHTTP(t *testing.T) {
	st := gatewayLimitStore(t)
	p := &gatewayBlockingPush{entered: make(chan struct{}, 4), release: make(chan struct{})}
	server := httptest.NewServer(NewGateway(GatewayConfig{Store: st, Push: p, MaxConcurrentPushes: 2}))
	defer server.Close()
	route := gatewayLimitRoute(t, st, "live_http")
	client := &http.Client{Timeout: time.Second}
	wake := func() (*http.Response, error) {
		body, err := json.Marshal(push.WakeRequest{DeviceID: route.ID, NotificationID: "ntf_live_limits", Priority: "normal", ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			return nil, err
		}
		r, err := http.NewRequest("POST", server.URL+"/v1/gateway/push", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+route.PushToken)
		return client.Do(r)
	}
	var wg sync.WaitGroup
	defer func() { close(p.release); wg.Wait() }()
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			response, err := wake()
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Errorf("live admitted request: %d", response.StatusCode)
			}
		})
	}
	for i := 0; i < 2; i++ {
		select {
		case <-p.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("live provider slots not occupied")
		}
	}
	response, err := wake()
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 429 || response.Header.Get("Retry-After") != "1" {
		t.Fatalf("live busy response: %d %s", response.StatusCode, response.Header)
	}
	t.Log("real TCP gateway request: full provider capacity returns HTTP 429 with Retry-After: 1")
}
