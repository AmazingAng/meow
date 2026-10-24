package push

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/store"
)

func TestSystemNotificationLayoutMatchesIOSRelayFixtures(t *testing.T) {
	data, err := os.ReadFile("../../../ios/MeowTests/Fixtures/notification-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Title, Source, Body, ExpectedTitle, ExpectedSubtitle, ExpectedBody string
		Context                                                                  *message.Context
		Event                                                                    message.EventKind
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			m, err := BuildMessage(store.PushTarget{Notification: store.Notification{
				ID: "ntf_layout", Title: f.Title, Source: f.Source, Body: f.Body, Context: f.Context, Event: f.Event,
			}})
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				APS struct{ Alert map[string]string }
			}
			if err := json.Unmarshal(m.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			alert := payload.APS.Alert
			if alert["title"] != f.ExpectedTitle || alert["subtitle"] != f.ExpectedSubtitle || alert["body"] != f.ExpectedBody {
				t.Fatalf("alert = %#v; want %q / %q / %q", alert, f.ExpectedTitle, f.ExpectedSubtitle, f.ExpectedBody)
			}
		})
	}
}

func TestSessionAndProjectHeadingsStayWithinAPNsPayloadLimit(t *testing.T) {
	for index, session := range []string{"Review", strings.Repeat("😺", 1500)} {
		t.Run([]string{"long-project", "long-project-and-session"}[index], func(t *testing.T) {
			m, err := BuildMessage(store.PushTarget{Notification: store.Notification{
				ID: "ntf_layout", Source: "Codex", Title: "New result", Body: strings.Repeat("猫", 200),
				Context: &message.Context{Session: session, Workspace: strings.Repeat("😺", 1500)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Payload) > MaxPayload || !json.Valid(m.Payload) {
				t.Fatalf("invalid payload: %d bytes", len(m.Payload))
			}
			var payload struct {
				APS struct{ Alert map[string]string }
			}
			if err := json.Unmarshal(m.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.APS.Alert["title"] == "" {
				t.Fatal("lost session heading")
			}
			if session == "Review" && payload.APS.Alert["title"] != session {
				t.Fatal("shortened session before project")
			}
		})
	}
}
