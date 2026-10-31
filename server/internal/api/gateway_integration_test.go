package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meow.blue/meow/client"
	"meow.blue/meow/server/internal/api"
	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

// Runs the actual persisted dispatcher -> HTTP gateway -> APNs boundary, then
// fetches and answers against the originating API with its mobile credential.
func TestGatewayEndToEndDispatchAndRead(t *testing.T) {
	_, apns, gatewayURL := gatewayFixture(t)
	route := enroll(t, gatewayURL, "bind_1")
	st, err := store.Open(filepath.Join(t.TempDir(), "origin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	relay, err := push.NewGatewayClient(gatewayURL)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := push.NewDispatcher(st, relay, slog.New(slog.DiscardHandler))
	dispatcher.Backoff = time.Millisecond
	server := api.New(api.Config{Store: st, Push: dispatcher, GatewayURL: gatewayURL})
	ts := httptest.NewServer(server)
	defer ts.Close()
	defer server.Close()
	bg, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { dispatcher.Run(bg, 2); close(done) }()
	defer func() { cancel(); <-done }()
	registration, err := client.New(ts.URL, "").Register(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}

	agent := client.New(ts.URL, registration.Key.Secret)
	status, body := gatewayRequest(t, ts.URL, "PUT", "/v1/device/push-gateway", registration.DeviceToken, map[string]string{"gateway_device_id": route.ID, "push_token": route.PushToken, "binding_id": route.BindingID})
	if status != 204 {
		t.Fatalf("bind=%d %s", status, body)
	}
	q, err := agent.Notify(ctx, client.NotifyRequest{Title: "PRIVATE QUESTION", Body: "PRIVATE BODY", Source: "PRIVATE SOURCE"}, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitPush := func(id, want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			n, err := st.Notification(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if n.PushStatus == want {
				return
			}
			if n.PushStatus != store.PushQueued {
				t.Fatalf("push=%s err=%s", n.PushStatus, n.PushError)
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("notification never reached %s", want)
	}
	awaitPush(q.ID, store.PushAccepted)
	status, body = gatewayRequest(t, ts.URL, "GET", "/v1/notifications/"+q.ID, registration.DeviceToken, nil)
	var n client.Notification
	err = json.Unmarshal(body, &n)
	if status != 200 || err != nil || n.Title != "PRIVATE QUESTION" || n.Body != "PRIVATE BODY" {
		t.Fatalf("fetch origin=%+v %v", n, err)
	}
	apns.mu.Lock()
	payload := string(apns.messages[0].Payload)
	apns.mu.Unlock()
	for _, secret := range []string{"PRIVATE QUESTION", "PRIVATE BODY", "PRIVATE SOURCE", registration.DeviceToken, registration.Key.Secret, route.PushToken, route.ManagementToken} {
		if strings.Contains(payload, secret) {
			t.Fatal("content or credential leaked to APNs")
		}
	}
	apns.mu.Lock()
	apns.err = &push.Error{Status: 503, Reason: "ServiceUnavailable"}
	apns.mu.Unlock()
	failed, err := agent.Notify(ctx, client.NotifyRequest{Title: "fail", Priority: "normal"}, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitPush(failed.ID, store.PushFailed)
}
