package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"meow.blue/meow/client"
	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/store"
)

func TestNotificationContextHTTP(t *testing.T) {
	e := setup(t)
	context := map[string]any{"workspace": "工程", "workspace_id": "workspace-1", "session": "部署审查 😺", "session_id": "session-1"}
	status, b := gatewayRequest(t, e.url, "POST", "/v1/notifications", e.reg.Key.Secret, map[string]any{"title": "Result", "source": "Codex", "context": context})
	if status != http.StatusCreated {
		t.Fatalf("create=%d %s", status, b)
	}
	var n map[string]any
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	got, ok := n["context"].(map[string]any)
	if !ok || got["session"] != context["session"] {
		t.Fatalf("context=%v", got)
	}
	status, b = gatewayRequest(t, e.url, "GET", "/v1/notifications/"+n["id"].(string), e.reg.DeviceToken, nil)
	if status != 200 {
		t.Fatalf("detail=%d %s", status, b)
	}
	_ = json.Unmarshal(b, &n)
	if n["context"].(map[string]any)["session_id"] != "session-1" {
		t.Fatalf("detail=%s", b)
	}
	page, _, err := e.phone.Inbox(ctx, "", 50)
	if err != nil || len(page) != 1 {
		t.Fatalf("inbox=%v %v", page, err)
	}
	raw, _ := json.Marshal(page[0])
	var saved map[string]any
	_ = json.Unmarshal(raw, &saved)
	if saved["context"] == nil {
		t.Fatalf("client dropped context: %s", raw)
	}
	other, err := e.phone.Register(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	status, _ = gatewayRequest(t, e.url, "GET", "/v1/notifications/"+n["id"].(string), other.DeviceToken, nil)
	if status != 404 {
		t.Fatalf("foreign status=%d", status)
	}
}

func TestContextValidationAndLegacyIdempotency(t *testing.T) {
	e := setup(t)
	for _, field := range []string{"workspace", "workspace_id", "session", "session_id"} {
		max := 200
		if strings.HasSuffix(field, "_id") {
			max = 128
		}
		for _, bad := range []string{strings.Repeat("猫", max+1), "bad\x00name", "bad\nname"} {
			code, _ := gatewayRequest(t, e.url, "POST", "/v1/notifications", e.reg.Key.Secret, map[string]any{"title": "test", "context": map[string]string{field: bad}})
			if code != 422 {
				t.Fatalf("field %s: status=%d", field, code)
			}
		}
	}
	for _, bad := range []any{"not an object", 12, []string{"bad"}, map[string]any{"session": 42}} {
		code, _ := gatewayRequest(t, e.url, "POST", "/v1/notifications", e.reg.Key.Secret, map[string]any{"title": "test", "context": bad})
		if code != 400 {
			t.Fatalf("bad context=%v status=%d", bad, code)
		}
	}
	// Reconstruct a request hash written by the previous server, whose input
	// struct had no context field at all. Existing retries must still replay.
	oldHash := sha256.Sum256([]byte(`{"type":"info","title":"legacy-v2","body":"","source":"Codex","priority":"normal"}`))
	original, _, err := e.st.CreateNotification(ctx, store.NewNotification{DeviceID: e.reg.Device.ID, KeyID: e.reg.Key.ID,
		Type: "info", Title: "legacy-v2", Source: "Codex", Priority: "normal"}, store.Idem{Key: "old-v2", RequestHash: hex.EncodeToString(oldHash[:])})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "legacy-v2", Source: "Codex"}, "old-v2")
	if err != nil || replayed.ID != original.ID {
		t.Fatalf("v2 hash changed: %v %v", replayed, err)
	}
	n, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "legacy", Source: "Codex"}, "legacy-key")
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "legacy", Source: "Codex"}, "legacy-key")
	if err != nil || n.ID != again.ID {
		t.Fatalf("legacy replay=%v %v", again, err)
	}
	request := client.NotifyRequest{Title: "context", Context: &message.Context{Session: " Named ", SessionID: " s1 "}}
	first, err := e.agent.Notify(ctx, request, "context-key")
	if err != nil {
		t.Fatal(err)
	}
	request.Context.Session = "Named"
	request.Context.SessionID = "s1"
	second, err := e.agent.Notify(ctx, request, "context-key")
	if err != nil || first.ID != second.ID || first.Context.Session != "Named" {
		t.Fatalf("normalized replay=%v %v", second, err)
	}
	request.Context.SessionID = "s2"
	_, err = e.agent.Notify(ctx, request, "context-key")
	apiErr(t, err, 422, "idempotency_mismatch")
}
