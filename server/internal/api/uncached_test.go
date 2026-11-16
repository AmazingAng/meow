package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meow.blue/meow/server/internal/api"
	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

func uncachedRequest(t *testing.T, handler http.Handler, key, cache, idem, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/notifications", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	if cache != "" {
		r.Header.Set("Cache", cache)
	}
	if idem != "" {
		r.Header.Set("Idempotency-Key", idem)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestUncachedUnavailableRejectsBeforeStoring(t *testing.T) {
	for _, gateway := range []string{"", "https://gateway.example.invalid"} {
		t.Run(gateway, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "meow.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			device, err := st.RegisterDevice(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			h := api.New(api.Config{Store: st, Push: &recordingPush{}, GatewayURL: gateway})
			w := uncachedRequest(t, h, device.KeySecret, "no", "retry", `{"title":"secret title","body":"secret body"}`)
			if w.Code != 422 || !strings.Contains(w.Body.String(), "cache_required") {
				t.Fatalf("rejection=%d %s", w.Code, w.Body.String())
			}
			items, err := st.Inbox(ctx, device.Device.ID, store.InboxQuery{Limit: 10})
			if err != nil || len(items) != 0 {
				t.Fatalf("rejected notification retained: %v", err)
			}
		})
	}
}

func TestUncachedSendHasNoServerHistoryAndPreservesIdempotency(t *testing.T) {
	h, st, device, provider := setupUncached(t, false)
	st.Limits.DailyMessages = 1
	body := `{"title":"secret title","body":"secret body","source":"secret source","context":{"workspace":"secret project","session":"secret session"}}`
	w := uncachedRequest(t, h, device.KeySecret, "no", "retry", body)
	var first struct {
		ID         string
		PushStatus string `json:"push_status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); w.Code != 201 || err != nil || first.ID == "" || first.PushStatus != "accepted" {
		t.Fatalf("send=%d %s %v", w.Code, w.Body.String(), err)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control=%q", got)
	}
	if len(provider.messages) != 1 || provider.messages[0].Expiration.Unix() != 0 || !strings.Contains(string(provider.messages[0].Payload), "secret body") {
		t.Fatal("expected one immediate, non-buffered push preview")
	}
	w = uncachedRequest(t, h, device.KeySecret, "NO", "retry", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), first.ID) || w.Header().Get("Idempotent-Replayed") != "true" || len(provider.messages) != 1 {
		t.Fatalf("replay=%d %s", w.Code, w.Body.String())
	}
	w = uncachedRequest(t, h, device.KeySecret, "no", "retry", `{"title":"changed"}`)
	if w.Code != 422 {
		t.Fatalf("different content=%d", w.Code)
	}
	w = uncachedRequest(t, h, device.KeySecret, "", "retry", body)
	if w.Code != 422 {
		t.Fatalf("different policy=%d", w.Code)
	}
	w = uncachedRequest(t, h, device.KeySecret, "", "second", body)
	if w.Code != 429 {
		t.Fatalf("quota reset by zero cache=%d", w.Code)
	}
	if _, err := st.Notification(ctx, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("notification detail remained: %v", err)
	}
	items, err := st.Inbox(ctx, device.Device.ID, store.InboxQuery{Limit: 10})
	if err != nil || len(items) != 0 {
		t.Fatalf("server inbox not empty: %v", err)
	}
	snapshot := filepath.Join(t.TempDir(), "backup.db")
	if err := st.Backup(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Notification(ctx, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("backup contains message: %v", err)
	}
}

func setupUncached(t *testing.T, noCache bool) (*api.Server, *store.Store, store.Registration, *gatewayPush) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "meow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	device, err := st.RegisterDevice(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if _, err := st.UpdateDevice(ctx, device.Device.ID, store.DeviceUpdate{APNsToken: &token}); err != nil {
		t.Fatal(err)
	}
	provider := &gatewayPush{}
	return api.New(api.Config{Store: st, DirectPush: provider, Push: &recordingPush{}, NoCache: noCache}), st, device, provider
}

func TestUncachedZeroModeAndRejectedProviderHaveNoReplaySideEffects(t *testing.T) {
	h, st, device, provider := setupUncached(t, true)
	provider.err = &push.Error{Status: 502, Reason: "private provider detail"}
	body := `{"title":"secret","body":"unretained"}`
	for range 2 {
		w := uncachedRequest(t, h, device.KeySecret, "yes", "failed-attempt", body)
		if w.Code != 502 || strings.Contains(w.Body.String(), "private provider detail") {
			t.Fatalf("rejected provider=%d %s", w.Code, w.Body.String())
		}
	}
	if len(provider.messages) != 1 {
		t.Fatalf("failed attempt redelivered: %d", len(provider.messages))
	}
	items, err := st.Inbox(ctx, device.Device.ID, store.InboxQuery{Limit: 10})
	if err != nil || len(items) != 0 {
		t.Fatalf("failed notification cached: %d %v", len(items), err)
	}
	provider.err = nil
	w := uncachedRequest(t, h, device.KeySecret, "", "", body)
	if w.Code != 201 || len(provider.messages) != 2 {
		t.Fatalf("zero mode without retry header=%d", w.Code)
	}
}

func TestUncachedRequiresPushTokenAndSenderCredential(t *testing.T) {
	h, st, device, provider := setupUncached(t, false)
	st.Limits.DailyMessages = 1
	body := `{"title":"not registered"}`
	token := ""
	if _, err := st.UpdateDevice(ctx, device.Device.ID, store.DeviceUpdate{APNsToken: &token}); err != nil {
		t.Fatal(err)
	}
	if w := uncachedRequest(t, h, device.DeviceToken, "no", "forbidden", body); w.Code != 403 {
		t.Fatalf("mobile credential sent with cache opt-out: %d", w.Code)
	}
	if w := uncachedRequest(t, h, device.KeySecret, "no", "missing", body); w.Code != 409 || !strings.Contains(w.Body.String(), "push_not_registered") {
		t.Fatalf("missing push token=%d %s", w.Code, w.Body.String())
	}
	token = strings.Repeat("b", 64)
	if _, err := st.UpdateDevice(ctx, device.Device.ID, store.DeviceUpdate{APNsToken: &token}); err != nil {
		t.Fatal(err)
	}
	if w := uncachedRequest(t, h, device.KeySecret, "no", "missing", body); w.Code != 201 || len(provider.messages) != 1 {
		t.Fatalf("invalid request consumed quota or idempotency: %d", w.Code)
	}
}

type blockingUncachedProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingUncachedProvider) Push(ctx context.Context, _ push.Message) error {
	p.started <- struct{}{}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestUncachedConcurrentReplayDoesNotStartSecondPush(t *testing.T) {
	_, st, device, _ := setupUncached(t, false)
	provider := &blockingUncachedProvider{started: make(chan struct{}, 2), release: make(chan struct{})}
	h := api.New(api.Config{Store: st, DirectPush: provider})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	send := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/notifications", strings.NewReader("test")).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer "+device.KeySecret)
		r.Header.Set("Cache", "no")
		r.Header.Set("Idempotency-Key", "one-logical-send")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- send() }()
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("provider not reached")
	}
	replay := send()
	close(provider.release)
	if w := <-first; w.Code != 201 {
		t.Fatalf("original send=%d %s", w.Code, w.Body.String())
	}
	if replay.Code != 409 || !strings.Contains(replay.Body.String(), "push_outcome_unknown") || len(provider.started) != 0 {
		t.Fatalf("concurrent replay sent or returned a false outcome: %d %s", replay.Code, replay.Body.String())
	}
	if w := send(); w.Code != 200 || len(provider.started) != 0 {
		t.Fatalf("completed replay=%d", w.Code)
	}
}
