package push

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"meow.blue/meow/server/internal/store"
)

type pushFunc func(context.Context, Message) error

func (f pushFunc) Push(ctx context.Context, m Message) error { return f(ctx, m) }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reviewStore(t *testing.T) (*store.Store, store.Registration) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "meow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg, err := st.RegisterDevice(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.Repeat("ab", 32)
	if _, err := st.UpdateDevice(context.Background(), reg.Device.ID, store.DeviceUpdate{APNsToken: &tok}); err != nil {
		t.Fatal(err)
	}
	return st, reg
}

func TestDispatcherReloadsBeforeRetry(t *testing.T) {
	for _, action := range []string{"cancel", "answer", "reject", "expire", "rotate", "accepted"} {
		t.Run(action, func(t *testing.T) {
			st, reg := reviewStore(t)
			ctx := context.Background()
			now := time.Now()
			st.Now = func() time.Time { return now }
			q, _, err := st.CreateQuestion(ctx, store.NewQuestion{NewNotification: store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Title: "Question", Priority: "normal"}, Choices: []string{"approve", "reject"}, TTL: time.Hour}, store.Idem{})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			rotated := strings.Repeat("cd", 32)
			p := pushFunc(func(ctx context.Context, m Message) error {
				calls++
				if calls == 1 {
					switch action {
					case "cancel":
						_, err = st.CancelQuestion(ctx, q.ID, reg.Device.ID)
					case "answer":
						_, _, err = st.Respond(ctx, q.ID, reg.Device.ID, "approve", "r")
					case "reject":
						_, _, err = st.Respond(ctx, q.ID, reg.Device.ID, "reject", "r")
					case "expire":
						now = q.ExpiresAt
					case "rotate":
						_, err = st.UpdateDevice(ctx, reg.Device.ID, store.DeviceUpdate{APNsToken: &rotated})
					case "accepted":
						err = st.SetPushStatus(ctx, q.NotificationID, store.PushAccepted, "")
					}
					if err != nil {
						t.Fatal(err)
					}
					return &Error{Status: 503}
				}
				if action == "rotate" && m.Token != rotated {
					t.Error("retry used stale token")
				}
				return nil
			})
			d := NewDispatcher(st, p, slog.New(slog.DiscardHandler))
			d.Backoff = time.Millisecond
			d.deliver(ctx, q.NotificationID)
			n, err := st.Notification(ctx, q.NotificationID)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls, wantStatus := 1, store.PushSkipped
			if action == "rotate" {
				wantCalls, wantStatus = 2, store.PushAccepted
			}
			if action == "accepted" {
				wantStatus = store.PushAccepted
			}
			if calls != wantCalls || n.PushStatus != wantStatus {
				t.Fatalf("calls=%d status=%s; want %d %s", calls, n.PushStatus, wantCalls, wantStatus)
			}
		})
	}
}

func TestGatewayRetryAfterReachesDispatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st, reg := reviewStore(t)
		ctx := context.Background()
		n, _, err := st.CreateNotification(ctx, store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "retry", Priority: "normal"}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		g, err := NewGatewayClient("https://gateway.example")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetGatewayBinding(ctx, reg.Device.ID, store.GatewayBinding{GatewayURL: g.BaseURL, DeviceID: "gateway-device", PushToken: "test-token"}); err != nil {
			t.Fatal(err)
		}
		var attempts []time.Time
		g.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts = append(attempts, time.Now())
			status, body := 200, `{"accepted":true}`
			if len(attempts) == 1 {
				status, body = 429, `{}`
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		d := NewDispatcher(st, g, slog.New(slog.DiscardHandler))
		d.deliver(ctx, n.ID)
		if len(attempts) != 2 || attempts[1].Sub(attempts[0]) < time.Minute {
			t.Fatalf("retry timestamps=%v; expected at least 60s", attempts)
		}
		got, err := st.Notification(ctx, n.ID)
		if err != nil || got.PushStatus != store.PushAccepted {
			t.Fatalf("notification=%+v err=%v", got, err)
		}
	})
}

func TestGatewayRetryAfterLocalHTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()
	g, err := NewGatewayClient(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = g.Push(context.Background(), Message{Binding: &store.GatewayBinding{GatewayURL: g.BaseURL}, Wake: &WakeRequest{NotificationID: "ntf_1"}})
	ae, ok := err.(*Error)
	if !ok || !ae.Retryable() {
		t.Fatalf("error=%v", err)
	}
	if ae.RetryAfter != time.Minute {
		t.Fatalf("lost Retry-After: %+v", ae)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"60", time.Minute}, {" 60 ", time.Minute}, {"0", 0}, {"", 0}, {"-1", 0}, {"garbage", 0}, {"1.5", 0},
		{"9223372037", 0}, {"18446744073709551616", 0},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute},
		{now.Format(http.TimeFormat), 0}, {now.Add(-time.Minute).Format(http.TimeFormat), 0},
	} {
		if got := retryAfter(tc.raw, now); got != tc.want {
			t.Errorf("retryAfter(%q)=%v want %v", tc.raw, got, tc.want)
		}
	}
}

func FuzzRetryAfter(f *testing.F) {
	for _, seed := range []uint64{0, 1, 60, 9223372036, 9223372037, 1 << 63, 1<<64 - 1} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seconds uint64) {
		want := time.Duration(0)
		if seconds <= uint64((1<<63-1)/int64(time.Second)) {
			want = time.Duration(seconds) * time.Second
		}
		if got := retryAfter(strconv.FormatUint(seconds, 10), time.Now()); got != want {
			t.Fatalf("seconds=%d got=%v want=%v", seconds, got, want)
		}
	})
}

func TestDispatcherRetryPolicy(t *testing.T) {
	for _, mode := range []string{"backoff", "untyped", "terminal", "exhausted", "cancel-wait", "cancel-before"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st, reg := reviewStore(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				n, _, err := st.CreateNotification(ctx, store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "retry", Priority: "normal"}, store.Idem{})
				if err != nil {
					t.Fatal(err)
				}
				var times []time.Time
				p := pushFunc(func(ctx context.Context, m Message) error {
					times = append(times, time.Now())
					switch mode {
					case "terminal":
						return &Error{Status: 403, RetryAfter: time.Minute}
					case "exhausted":
						return &Error{Status: 503}
					case "cancel-wait":
						go func() { time.Sleep(time.Second); cancel() }()
						return &Error{Status: 429, RetryAfter: time.Minute}
					}
					if len(times) == 1 {
						if mode == "untyped" {
							return errors.New("network failure")
						}
						return &Error{Status: 429, RetryAfter: time.Second}
					}
					return nil
				})
				d := NewDispatcher(st, p, slog.New(slog.DiscardHandler))
				if mode == "cancel-before" {
					cancel()
				}
				start := time.Now()
				d.deliver(ctx, n.ID)
				got, err := st.Notification(context.Background(), n.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantCalls, wantStatus, wantElapsed := 2, store.PushAccepted, 2*time.Second
				switch mode {
				case "terminal":
					wantCalls, wantStatus, wantElapsed = 1, store.PushFailed, 0
				case "exhausted":
					wantCalls, wantStatus, wantElapsed = 4, store.PushFailed, 14*time.Second
				case "cancel-wait":
					wantCalls, wantStatus, wantElapsed = 1, store.PushQueued, time.Second
				case "cancel-before":
					wantCalls, wantStatus, wantElapsed = 0, store.PushQueued, 0
				}
				if len(times) != wantCalls || got.PushStatus != wantStatus || time.Since(start) != wantElapsed {
					t.Fatalf("calls=%d status=%s elapsed=%v; want %d %s %v", len(times), got.PushStatus, time.Since(start), wantCalls, wantStatus, wantElapsed)
				}
			})
		})
	}
}

func TestDispatcherReloadsGatewayBinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st, reg := reviewStore(t)
		ctx := context.Background()
		n, _, err := st.CreateNotification(ctx, store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "binding", Priority: "normal"}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		g, err := NewGatewayClient("https://gateway.example")
		if err != nil {
			t.Fatal(err)
		}
		binding := store.GatewayBinding{GatewayURL: g.BaseURL, DeviceID: "gateway-device", PushToken: "old"}
		if err := st.SetGatewayBinding(ctx, reg.Device.ID, binding); err != nil {
			t.Fatal(err)
		}
		calls := 0
		g.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			status, body := 200, `{"accepted":true}`
			if calls == 1 {
				binding.PushToken = "new"
				if err := st.SetGatewayBinding(ctx, reg.Device.ID, binding); err != nil {
					t.Fatal(err)
				}
				status, body = 503, `{}`
			} else if r.Header.Get("Authorization") != "Bearer new" {
				t.Error("retry used stale gateway credential")
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		NewDispatcher(st, g, slog.New(slog.DiscardHandler)).deliver(ctx, n.ID)
		got, err := st.Notification(ctx, n.ID)
		if err != nil || calls != 2 || got.PushStatus != store.PushAccepted {
			t.Fatalf("calls=%d notification=%+v err=%v", calls, got, err)
		}
	})
}
