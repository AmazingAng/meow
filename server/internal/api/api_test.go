package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"meow.blue/meow/client"
	"meow.blue/meow/server/internal/api"
	"meow.blue/meow/server/internal/store"
)

type recordingPush struct {
	mu  sync.Mutex
	ids []string
}

func (p *recordingPush) Enqueue(id string) {
	p.mu.Lock()
	p.ids = append(p.ids, id)
	p.mu.Unlock()
}

type env struct {
	srv    *api.Server
	st     *store.Store
	url    string
	push   *recordingPush
	agent  *client.Client // Device Key
	phone  *client.Client // device token
	reg    *client.Registration
	dbPath string
}

func setup(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meow.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, push: &recordingPush{}, dbPath: path}
	e.srv = api.New(api.Config{Store: st, Push: e.push})
	ts := httptest.NewServer(e.srv)
	t.Cleanup(func() {
		e.srv.Close()
		ts.Close()
		st.Close()
	})
	e.url = ts.URL
	e.reg, err = client.New(ts.URL, "").Register(context.Background(), "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	e.agent = client.New(ts.URL, e.reg.Key.Secret)
	e.phone = client.New(ts.URL, e.reg.DeviceToken)
	return e
}

func apiErr(t *testing.T, err error, status int, code string) *client.Error {
	t.Helper()
	var ae *client.Error
	if !errors.As(err, &ae) || ae.Status != status || (code != "" && ae.Code != code) {
		t.Fatalf("got error %v (%+v), want HTTP %d %s", err, ae, status, code)
	}
	return ae
}

var ctx = context.Background()

// The core V0 loop: a waiting CLI receives exactly the answer given on the phone.

func TestKeyScopesAndRevocation(t *testing.T) {
	e := setup(t)
	k, err := e.phone.CreateKey(ctx, "ci", []string{"notify"})
	if err != nil || k.Secret == "" || len(k.Scopes) != 1 {
		t.Fatalf("CreateKey = %+v, %v", k, err)
	}
	ci := client.New(e.url, k.Secret)
	if _, err := ci.Notify(ctx, client.NotifyRequest{Title: "CI passed"}, ""); err != nil {
		t.Fatal(err)
	}

	if err := e.phone.RevokeKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	_, err = ci.Notify(ctx, client.NotifyRequest{Title: "after revoke"}, "")
	apiErr(t, err, 401, "unauthorized")
}

func TestNotifyIdempotency(t *testing.T) {
	e := setup(t)
	req := client.NotifyRequest{Title: "Build completed", Body: "project/web", Source: "Codex"}
	n1, err := e.agent.Notify(ctx, req, "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	n2, err := e.agent.Notify(ctx, req, "idem-1")
	if err != nil || n2.ID != n1.ID {
		t.Fatalf("replay = %v, %v", n2.ID, err)
	}
	req.Title = "Different"
	_, err = e.agent.Notify(ctx, req, "idem-1")
	apiErr(t, err, 422, "idempotency_mismatch")

	ns, _, err := e.phone.Inbox(ctx, "", 10)
	if err != nil || len(ns) != 1 || ns[0].Source != "Codex" {
		t.Fatalf("inbox = %+v, %v", ns, err)
	}
}

func TestPlainTextNotification(t *testing.T) {
	e := setup(t)
	req, _ := http.NewRequest(http.MethodPost, e.url+"/v1/notifications?priority=high", strings.NewReader("Tests failed\n3 failures in auth"))
	req.Header.Set("Authorization", "Bearer "+e.reg.Key.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	ns, _, _ := e.phone.Inbox(ctx, "", 10)
	if len(ns) != 1 || ns[0].Title != "Tests failed" || ns[0].Body != "3 failures in auth" || ns[0].Priority != "high" {
		t.Fatalf("inbox = %+v", ns)
	}
	if len(e.push.ids) != 1 || e.push.ids[0] != ns[0].ID {
		t.Fatalf("enqueued = %v", e.push.ids)
	}
}

func TestValidation(t *testing.T) {
	e := setup(t)
	cases := []struct {
		name string
		call func() error
	}{
		{"empty title", func() error { _, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "  "}, ""); return err }},
		{"bad priority", func() error {
			_, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "x", Priority: "urgent"}, "")
			return err
		}},
		{"long title", func() error {
			_, err := e.agent.Notify(ctx, client.NotifyRequest{Title: strings.Repeat("x", 300)}, "")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { apiErr(t, tc.call(), 422, "invalid_request") })
	}
}

func TestRegistrationRateLimit(t *testing.T) {
	e := setup(t) // uses one registration
	var last error
	for range 15 {
		if _, last = client.New(e.url, "").Register(ctx, ""); last != nil {
			break
		}
	}
	apiErr(t, last, 429, "rate_limited")
}

func TestInboxFilterAndCounts(t *testing.T) {
	e := setup(t)
	e.agent.Notify(ctx, client.NotifyRequest{Title: "Build completed", Source: "Codex"}, "")
	seedPendingHistory(t, e)

	get := func(query string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, e.url+query, nil)
		req.Header.Set("Authorization", "Bearer "+e.reg.DeviceToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	var page struct {
		Notifications []client.Notification `json:"notifications"`
	}
	if get("/v1/inbox?filter=pending", &page); len(page.Notifications) != 1 || page.Notifications[0].Type != "question" {
		t.Fatalf("pending = %+v", page.Notifications)
	}
	if get("/v1/inbox?q=codex", &page); len(page.Notifications) != 1 {
		t.Fatalf("search = %+v", page.Notifications)
	}
	var one client.Notification
	if code := get("/v1/notifications/"+page.Notifications[0].ID, &one); code != 200 || one.ID != page.Notifications[0].ID {
		t.Fatalf("get notification = %d %+v", code, one)
	}
	other, _ := client.New(e.url, "").Register(ctx, "other")
	req, _ := http.NewRequest(http.MethodGet, e.url+"/v1/notifications/"+one.ID, nil)
	req.Header.Set("Authorization", "Bearer "+other.DeviceToken)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 404 {
		t.Fatalf("other device get = %v, %v", resp.StatusCode, err)
	}
	if code := get("/v1/inbox?filter=archived", nil); code != 400 {
		t.Fatalf("bad filter status = %d", code)
	}
	var counts map[string]int
	if get("/v1/inbox/counts", &counts); counts["unread"] != 2 || counts["pending"] != 1 {
		t.Fatalf("counts = %v", counts)
	}

	req, _ = http.NewRequest(http.MethodPost, e.url+"/v1/inbox/read", nil)
	req.Header.Set("Authorization", "Bearer "+e.reg.DeviceToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("read all = %v, %v", resp, err)
	}
	resp.Body.Close()
	if get("/v1/inbox/counts", &counts); counts["unread"] != 0 || counts["pending"] != 1 {
		t.Fatalf("counts after read all = %v", counts)
	}
}

func TestQuotaExceededIsNotRetried(t *testing.T) {
	e := setup(t)
	e.st.Limits = store.Limits{DailyMessages: 1, KeysPerDevice: 1}
	if _, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "first"}, ""); err != nil {
		t.Fatal(err)
	}
	_, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "second"}, "")
	ae := apiErr(t, err, http.StatusTooManyRequests, "quota_exceeded")
	if ae.Temporary() || !strings.Contains(ae.Message, "messages in 24 hours") {
		t.Fatalf("quota error = %+v, temporary=%v", ae, ae.Temporary())
	}
	_, err = e.phone.CreateKey(ctx, "extra", []string{"notify"})
	apiErr(t, err, http.StatusTooManyRequests, "quota_exceeded")
}
