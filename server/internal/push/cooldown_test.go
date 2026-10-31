package push

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuditAPNsProviderRetryAfter(t *testing.T) {
	_, p8 := testKey(t)
	a, err := NewAPNs(p8, "test", "test", "blue.meow.app")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1200")
		w.WriteHeader(503)
		w.Write([]byte(`{"reason":"ServiceUnavailable"}`))
	}))
	defer ts.Close()
	a.ProductionURL = ts.URL
	err = a.Push(context.Background(), Message{Token: "fake", Payload: []byte(`{}`)})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatal(err)
	}
	t.Logf("APNs 503/1200s -> status=%d retry-after=%s", e.Status, e.RetryAfter)
	if e.RetryAfter != 1200*time.Second {
		t.Fatalf("provider cooldown discarded: %s", e.RetryAfter)
	}
}
