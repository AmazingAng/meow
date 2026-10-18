package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"meow.blue/meow/server/internal/store"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestAPNsRequest(t *testing.T) {
	key, p8 := testKey(t)
	var got *http.Request
	var body []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
	}))
	defer ts.Close()

	a, err := NewAPNs(p8, "KEYID12345", "TEAMID1234", "blue.meow.app")
	if err != nil {
		t.Fatal(err)
	}
	a.SandboxURL, a.HTTP = ts.URL, ts.Client()
	err = a.Push(context.Background(), Message{Token: "abcd", Sandbox: true, Priority: "low", Payload: []byte(`{"aps":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/3/device/abcd" || got.Header.Get("apns-topic") != "blue.meow.app" ||
		got.Header.Get("apns-push-type") != "alert" || got.Header.Get("apns-priority") != "5" || string(body) != `{"aps":{}}` {
		t.Fatalf("request = %s %v %s", got.URL.Path, got.Header, body)
	}

	// The provider token is an ES256 JWT that verifies with the key.
	jwt := strings.TrimPrefix(got.Header.Get("authorization"), "bearer ")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", jwt)
	}
	var header map[string]string
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(hb, &header)
	if header["alg"] != "ES256" || header["kid"] != "KEYID12345" {
		t.Fatalf("jwt header = %v", header)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Fatal("jwt signature does not verify")
	}
}

func TestAPNsErrorAndTokenRefresh(t *testing.T) {
	_, p8 := testKey(t)
	var mu sync.Mutex
	var auths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("authorization"))
		n := len(auths)
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/gone"):
			w.WriteHeader(http.StatusGone)
			io.WriteString(w, `{"reason":"Unregistered"}`)
		case n == 1:
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"reason":"ExpiredProviderToken"}`)
		}
	}))
	defer ts.Close()
	a, _ := NewAPNs(p8, "K", "T", "blue.meow.app")
	a.ProductionURL, a.HTTP = ts.URL, ts.Client()
	now := time.Now()
	a.Now = func() time.Time { now = now.Add(time.Second); return now }

	if err := a.Push(context.Background(), Message{Token: "ok"}); err != nil {
		t.Fatalf("push after refresh: %v", err)
	}
	if len(auths) != 2 || auths[0] == auths[1] {
		t.Fatal("provider token was not refreshed after ExpiredProviderToken")
	}
	err := a.Push(context.Background(), Message{Token: "gone"})
	ae, ok := err.(*Error)
	if !ok || !ae.InvalidToken() || ae.Retryable() {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildMessage(t *testing.T) {
	expires := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
	m, err := BuildMessage(store.PushTarget{
		Device: store.Device{APNsToken: "tok", APNsEnv: "sandbox"},
		Notification: store.Notification{
			ID: "ntf_1", Type: "question", Title: "Deploy to production?", Source: "Claude Code", Priority: "high",
			QuestionID: "qst_1",
			Question:   &store.QuestionState{Status: "pending", Choices: []string{"approve", "reject"}, ExpiresAt: expires},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Sandbox || m.Token != "tok" || !m.Expiration.Equal(expires) {
		t.Fatalf("message = %+v", m)
	}
	var p struct {
		APS struct {
			Alert    map[string]string `json:"alert"`
			Category string            `json:"category"`
			Level    string            `json:"interruption-level"`
			Thread   string            `json:"thread-id"`
		} `json:"aps"`
		Meow map[string]any `json:"meow"`
	}
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.APS.Alert["title"] != "Claude Code" || p.APS.Alert["body"] != "Deploy to production?" ||
		p.APS.Category != CategoryApproval || p.APS.Level != "time-sensitive" || p.APS.Thread != "Claude Code" ||
		p.Meow["question_id"] != "qst_1" {
		t.Fatalf("payload = %s", m.Payload)
	}
	if len(m.Payload) > 4096 {
		t.Fatalf("payload is %d bytes", len(m.Payload))
	}
}

func TestCategory(t *testing.T) {
	q := func(c ...string) store.Notification {
		return store.Notification{Question: &store.QuestionState{Choices: c}}
	}
	for _, tc := range []struct {
		n    store.Notification
		want string
	}{
		{store.Notification{}, CategoryInfo},
		{store.Notification{Question: &store.QuestionState{}}, CategoryReply},
		{q("Approve", "Reject"), CategoryApproval},
		{q("reject", "approve"), CategoryApproval},
		{q("postgres", "sqlite"), CategoryChoice},
	} {
		if got := Category(tc.n); got != tc.want {
			t.Errorf("Category(%v) = %s, want %s", tc.n.Question, got, tc.want)
		}
	}
}

type fakePusher struct {
	mu   sync.Mutex
	errs []error
	msgs []Message
}

func (f *fakePusher) Push(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	return nil
}

func TestDispatcher(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "meow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg, _ := st.RegisterDevice(ctx, "")
	tok := strings.Repeat("ab", 32)
	st.UpdateDevice(ctx, reg.Device.ID, store.DeviceUpdate{APNsToken: &tok})
	notify := func() string {
		n, _, err := st.CreateNotification(ctx, store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "hi", Priority: "normal"}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return n.ID
	}
	status := func(id string) string {
		n, _ := st.Notification(ctx, id)
		return n.PushStatus
	}

	fp := &fakePusher{errs: []error{&Error{Status: 503}}}
	d := NewDispatcher(st, fp, slog.New(slog.DiscardHandler))
	d.Backoff = time.Millisecond

	// A transient failure is retried and ends accepted.
	id := notify()
	d.deliver(ctx, id)
	if s := status(id); s != store.PushAccepted || len(fp.msgs) != 2 {
		t.Fatalf("status = %s after %d attempts", s, len(fp.msgs))
	}
	// Already-accepted notifications are not pushed again.
	d.deliver(ctx, id)
	if len(fp.msgs) != 2 {
		t.Fatal("accepted notification pushed twice")
	}
	// An invalid token is cleared and later pushes are skipped.
	fp.errs = []error{&Error{Status: 410, Reason: "Unregistered"}}
	id = notify()
	d.deliver(ctx, id)
	if s := status(id); s != store.PushFailed {
		t.Fatalf("status = %s", s)
	}
	id = notify()
	d.deliver(ctx, id)
	if s := status(id); s != store.PushSkipped {
		t.Fatalf("status after token cleared = %s", s)
	}
}

func TestBuildMessageFitsPayloadLimit(t *testing.T) {
	q := &store.QuestionState{Status: "pending", Choices: []string{"approve", "reject"}, ExpiresAt: time.Now()}
	for _, tc := range []struct {
		name, title, body, source string
		question                  *store.QuestionState
	}{
		{"emoji body", "Deploy to production?", strings.Repeat("😺", 4000), "Claude Code", q},
		{"escaped body", "Build", strings.Repeat(`"\<>&`, 800), "", nil},
		{"control chars", "x", strings.Repeat("\x01", 4000), "", nil},
		{"long title", strings.Repeat("😺", 256), strings.Repeat("😺", 4000), strings.Repeat("😺", 64), q},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := BuildMessage(store.PushTarget{Notification: store.Notification{
				ID: "ntf_1", Type: "info", Title: tc.title, Body: tc.body, Source: tc.source, Priority: "high",
				QuestionID: "qst_1", Question: tc.question,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Payload) > MaxPayload {
				t.Fatalf("payload is %d bytes", len(m.Payload))
			}
			var p struct {
				APS struct {
					Alert map[string]string `json:"alert"`
				} `json:"aps"`
			}
			if err := json.Unmarshal(m.Payload, &p); err != nil || p.APS.Alert["title"] == "" {
				t.Fatalf("payload = %s, %v", m.Payload, err)
			}
			if len(m.Payload) < MaxPayload-200 && utf8.RuneCountInString(p.APS.Alert["body"]) < maxAlertBody {
				t.Errorf("body over-truncated: payload only %d bytes", len(m.Payload))
			}
		})
	}
}
