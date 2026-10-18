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
	"time"

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
	e.srv = api.New(api.Config{Store: st, Push: e.push, MaxWait: 5 * time.Second})
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
func TestAskWaitAnswer(t *testing.T) {
	e := setup(t)
	q, err := e.agent.Ask(ctx, client.AskRequest{Question: "What should I do next?", Source: "Codex"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "pending" || q.ID == "" || q.ExpiresAt.IsZero() {
		t.Fatalf("created question = %+v", q)
	}

	done := make(chan *client.Question, 1)
	go func() {
		got, err := e.agent.Wait(ctx, q.ID, nil)
		if err != nil {
			t.Error(err)
		}
		done <- got
	}()
	time.Sleep(100 * time.Millisecond) // let the waiter subscribe

	const answer = "Run the evaluation first"
	if _, err := e.phone.Respond(ctx, q.ID, answer, "reply-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.Status != "answered" || got.Answer == nil || *got.Answer != answer {
			t.Fatalf("waiter got %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter was not woken by the response")
	}
}

func TestApproveRejectSingleOutcome(t *testing.T) {
	e := setup(t)
	q, err := e.agent.Ask(ctx, client.AskRequest{Question: "Deploy?", Choices: []string{"approve", "reject"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.phone.Respond(ctx, q.ID, "maybe", "r0"); err != nil {
		apiErr(t, err, 422, "invalid_answer")
	}
	got, err := e.phone.Respond(ctx, q.ID, "reject", "r1")
	if err != nil || got.Status != "rejected" {
		t.Fatalf("reject = %+v, %v", got, err)
	}
	// Retry of the same submission (e.g. offline queue) replays the result.
	if got, err = e.phone.Respond(ctx, q.ID, "reject", "r1"); err != nil || got.Status != "rejected" {
		t.Fatalf("replay = %+v, %v", got, err)
	}
	// A different submission is refused and reports the actual outcome.
	_, err = e.phone.Respond(ctx, q.ID, "approve", "r2")
	if ae := apiErr(t, err, 409, "already_resolved"); ae.Question == nil || ae.Question.Status != "rejected" {
		t.Fatalf("conflict body = %+v", ae.Question)
	}
}

func TestExpiredResponseRejected(t *testing.T) {
	e := setup(t)
	q, err := e.agent.Ask(ctx, client.AskRequest{Question: "Deploy?", Choices: []string{"approve", "reject"}, Timeout: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	e.st.Now = func() time.Time { return time.Now().Add(time.Minute) }
	_, err = e.phone.Respond(ctx, q.ID, "approve", "r1")
	if ae := apiErr(t, err, 410, "expired"); ae.Question == nil || ae.Question.Status != "expired" || ae.Question.Answer != nil {
		t.Fatalf("expired body = %+v", ae.Question)
	}
	got, err := e.agent.Question(ctx, q.ID)
	if err != nil || got.Status != "expired" {
		t.Fatalf("question = %+v, %v", got, err)
	}
}

func TestWaitReturnsOnExpiry(t *testing.T) {
	e := setup(t)
	q, err := e.agent.Ask(ctx, client.AskRequest{Question: "Anyone?", Timeout: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Shift the clock so the question expires ~200ms from now.
	shift := 10*time.Second - 200*time.Millisecond
	e.st.Now = func() time.Time { return time.Now().Add(shift) }
	start := time.Now()
	got, err := e.agent.WaitOnce(ctx, q.ID, 5*time.Second)
	if err != nil || got.Status != "expired" {
		t.Fatalf("wait = %+v, %v", got, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("wait took %v; should return at expiry", d)
	}
}

func TestWaitTimeoutReturnsPending(t *testing.T) {
	e := setup(t)
	q, _ := e.agent.Ask(ctx, client.AskRequest{Question: "Anyone?"}, "")
	got, err := e.agent.WaitOnce(ctx, q.ID, time.Second)
	if err != nil || got.Status != "pending" {
		t.Fatalf("wait = %+v, %v", got, err)
	}
}

func TestCancelWakesWaiter(t *testing.T) {
	e := setup(t)
	q, _ := e.agent.Ask(ctx, client.AskRequest{Question: "Anyone?"}, "")
	done := make(chan string, 1)
	go func() {
		got, _ := e.agent.WaitOnce(ctx, q.ID, 5*time.Second)
		done <- got.Status
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := e.agent.Cancel(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if s := <-done; s != "cancelled" {
		t.Fatalf("waiter saw %q", s)
	}
	_, err := e.phone.Respond(ctx, q.ID, "late", "r1")
	apiErr(t, err, 409, "already_resolved")
}

func TestCredentialSeparation(t *testing.T) {
	e := setup(t)
	q, _ := e.agent.Ask(ctx, client.AskRequest{Question: "Deploy?", Choices: []string{"approve", "reject"}}, "")

	// A Device Key can send but must never be able to approve.
	_, err := e.agent.Respond(ctx, q.ID, "approve", "r1")
	apiErr(t, err, 403, "forbidden")
	_, _, err = e.agent.Inbox(ctx, "", 10)
	apiErr(t, err, 403, "forbidden")
	_, err = e.agent.CreateKey(ctx, "x", nil)
	apiErr(t, err, 403, "forbidden")

	// The device token is not a send credential.
	_, err = e.phone.Notify(ctx, client.NotifyRequest{Title: "hi"}, "")
	apiErr(t, err, 403, "forbidden")

	_, err = client.New(e.url, "meow_bogus").Notify(ctx, client.NotifyRequest{Title: "hi"}, "")
	apiErr(t, err, 401, "unauthorized")
	_, err = client.New(e.url, "").Notify(ctx, client.NotifyRequest{Title: "hi"}, "")
	apiErr(t, err, 401, "unauthorized")

	// Another device cannot see or answer this device's question.
	other, err := client.New(e.url, "").Register(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.New(e.url, other.DeviceToken).Respond(ctx, q.ID, "approve", "r1")
	apiErr(t, err, 404, "not_found")
	_, err = client.New(e.url, other.Key.Secret).Question(ctx, q.ID)
	apiErr(t, err, 404, "not_found")
}

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
	_, err = ci.Ask(ctx, client.AskRequest{Question: "?"}, "")
	apiErr(t, err, 403, "forbidden")

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
		{"one choice", func() error {
			_, err := e.agent.Ask(ctx, client.AskRequest{Question: "?", Choices: []string{"ok"}}, "")
			return err
		}},
		{"duplicate choices", func() error {
			_, err := e.agent.Ask(ctx, client.AskRequest{Question: "?", Choices: []string{"ok", "OK"}}, "")
			return err
		}},
		{"timeout too long", func() error {
			_, err := e.agent.Ask(ctx, client.AskRequest{Question: "?", Timeout: 999999}, "")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { apiErr(t, tc.call(), 422, "invalid_request") })
	}
}

func TestRespondRequiresIdempotencyKey(t *testing.T) {
	e := setup(t)
	q, _ := e.agent.Ask(ctx, client.AskRequest{Question: "?"}, "")
	_, err := e.phone.Respond(ctx, q.ID, "hi", "")
	apiErr(t, err, 400, "invalid_idempotency_key")
}

func TestServerRestartKeepsQuestions(t *testing.T) {
	e := setup(t)
	q, _ := e.agent.Ask(ctx, client.AskRequest{Question: "Deploy?", Choices: []string{"approve", "reject"}}, "")

	// A second server on the same database file, as after a restart.
	st2, err := store.Open(e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	srv2 := api.New(api.Config{Store: st2, Push: &recordingPush{}})
	ts2 := httptest.NewServer(srv2)
	defer ts2.Close()
	defer srv2.Close()

	got, err := client.New(ts2.URL, e.reg.DeviceToken).Respond(ctx, q.ID, "approve", "r1")
	if err != nil || got.Status != "answered" {
		t.Fatalf("respond after restart = %+v, %v", got, err)
	}
	got, err = client.New(ts2.URL, e.reg.Key.Secret).Question(ctx, q.ID)
	if err != nil || *got.Answer != "approve" {
		t.Fatalf("question after restart = %+v, %v", got, err)
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
	e.agent.Ask(ctx, client.AskRequest{Question: "Deploy?", Choices: []string{"approve", "reject"}}, "")

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
	e.st.Limits = store.Limits{PendingQuestions: 1, KeysPerDevice: 1}
	if _, err := e.agent.Ask(ctx, client.AskRequest{Question: "first?"}, ""); err != nil {
		t.Fatal(err)
	}
	_, err := e.agent.Ask(ctx, client.AskRequest{Question: "second?"}, "")
	ae := apiErr(t, err, http.StatusTooManyRequests, "quota_exceeded")
	if ae.Temporary() || !strings.Contains(ae.Message, "unanswered questions") {
		t.Fatalf("quota error = %+v, temporary=%v", ae, ae.Temporary())
	}
	_, err = e.phone.CreateKey(ctx, "extra", []string{"notify"})
	apiErr(t, err, http.StatusTooManyRequests, "quota_exceeded")
}
