package push

import (
	"encoding/json"
	"meow.blue/meow/server/internal/store"
	"testing"
	"time"
)

func TestDirectPushNeverOverridesPhoneUnreadBadge(t *testing.T) {
	m, err := BuildMessage(store.PushTarget{Notification: store.Notification{ID: "ntf_badge", Type: "info", Title: "fixture", Body: "body", Priority: "normal", CreatedAt: time.Now()}, Device: store.Device{ID: "dev_fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err = json.Unmarshal(m.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	aps := payload["aps"].(map[string]any)
	if badge, ok := aps["badge"]; ok {
		t.Fatalf("server must not set local badge: %v", badge)
	}
}
