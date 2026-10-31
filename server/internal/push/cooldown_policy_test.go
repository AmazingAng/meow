package push

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAPNsCooldownPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
		want   time.Duration
	}{
		{"provider-unavailable", 503, "120", 15 * time.Minute}, {"long-provider-delay", 500, "1200", 20 * time.Minute},
		{"token-rate", 429, "", time.Minute}, {"long-token-delay", 429, "120", 2 * time.Minute},
		{"overflow", 503, "9223372036854775807", 15 * time.Minute}, {"invalid", 503, "bad", 15 * time.Minute},
		{"date", 503, "Sun, 01 Nov 2026 00:30:00 GMT", 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p8 := testKey(t)
			a, err := NewAPNs(p8, "test", "test", "test")
			if err != nil {
				t.Fatal(err)
			}
			a.Now = func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			a.ProductionURL = server.URL
			err = a.Push(context.Background(), Message{Token: "fake", Payload: []byte(`{}`)})
			var e *Error
			if !errors.As(err, &e) || e.RetryAfter != tc.want {
				t.Fatalf("error=%+v expected delay=%s", e, tc.want)
			}
		})
	}
}
