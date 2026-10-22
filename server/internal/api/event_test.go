package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"meow.blue/meow/message"
)

func TestNotificationEventHTTP(t *testing.T) {
	e := setup(t)
	for _, event := range []string{"result", "attention", "failure"} {
		code, body := gatewayRequest(t, e.url, "POST", "/v1/notifications", e.reg.Key.Secret, map[string]any{"title": "自定义标题", "event": event, "type": "info", "priority": "low"})
		if code != 201 {
			t.Fatalf("create=%d %s", code, body)
		}
		var object map[string]any
		_ = json.Unmarshal(body, &object)
		if object["event"] != event || object["title"] != "自定义标题" || object["priority"] != "low" || object["type"] != "info" {
			t.Errorf("create dropped event: %s", body)
		}
		code, body = gatewayRequest(t, e.url, "GET", "/v1/notifications/"+object["id"].(string), e.reg.DeviceToken, nil)
		_ = json.Unmarshal(body, &object)
		if code != 200 || object["event"] != event {
			t.Errorf("detail=%d %s", code, body)
		}
	}
	page, _, err := e.phone.Inbox(ctx, "", 50)
	if err != nil || len(page) != 3 {
		t.Fatalf("inbox=%v %v", page, err)
	}
	for _, item := range page {
		data, _ := json.Marshal(item)
		var object map[string]any
		_ = json.Unmarshal(data, &object)
		if object["event"] == nil {
			t.Errorf("client/inbox dropped event: %s", data)
		}
	}
	for _, invalid := range []string{"done", "success", "FAILURE"} {
		code, body := gatewayRequest(t, e.url, "POST", "/v1/notifications", e.reg.Key.Secret, map[string]any{"title": "bad", "event": invalid})
		if code != 422 {
			t.Errorf("invalid event accepted: %d %s", code, body)
		}
	}
}

func TestNotificationEventChangesIdempotencyFingerprint(t *testing.T) {
	e := setup(t)
	first, err := message.Decode([]byte(`{"title":"same","event":"result"}`))
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.agent.Notify(ctx, first, "event-key")
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.agent.Notify(ctx, first, "event-key")
	if err != nil || again.ID != n.ID {
		t.Fatalf("replay=%v %v", again, err)
	}
	changed, err := message.Decode([]byte(`{"title":"same","event":"failure"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.agent.Notify(ctx, changed, "event-key")
	apiErr(t, err, 422, "idempotency_mismatch")
}

func TestPlainTextNotificationEvent(t *testing.T) {
	e := setup(t)
	for _, event := range []string{"attention", "invalid"} {
		req, err := http.NewRequest("POST", e.url+"/v1/notifications?event="+event, strings.NewReader("Custom title\nOriginal body"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Authorization", "Bearer "+e.reg.Key.Secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		err = json.NewDecoder(res.Body).Decode(&object)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if event == "invalid" {
			if res.StatusCode != 422 {
				t.Fatalf("invalid query event=%d", res.StatusCode)
			}
			continue
		}
		if res.StatusCode != 201 || object["event"] != event || object["title"] != "Custom title" || object["body"] != "Original body" {
			t.Fatalf("event=%v status=%d", object, res.StatusCode)
		}
	}
}
