package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMissingServerNeverUsesAPlaceholder(t *testing.T) {
	for _, wait := range []bool{false, true} {
		calls := 0
		c := New("", "meow_test")
		c.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"qst_test","status":"answered"}`)), Header: make(http.Header)}, nil
		})}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var err error
		if wait {
			_, err = c.Wait(ctx, "qst_test", nil)
		} else {
			_, err = c.WhoAmI(ctx)
		}
		cancel()
		if err == nil || calls != 0 {
			t.Fatalf("missing server performed a request: wait=%v calls=%d err=%v", wait, calls, err)
		}
	}
}
