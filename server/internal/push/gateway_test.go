package push

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meow.blue/meow/server/internal/store"
)

func TestGatewayClientExplicitAcceptance(t *testing.T) {
	status := 200
	body := `{"accepted":true}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/v1/gateway/push" || r.Header.Get("Authorization") != "Bearer gwp_device" {
			t.Error("wrong route/credential")
		}
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if len(in) != 4 || in["notification_id"] != "ntf_1" {
			t.Errorf("wake contains unexpected fields: %v", in)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()
	g, err := NewGatewayClient(ts.URL + "/prefix")
	if err != nil {
		t.Fatal(err)
	}
	m := Message{Binding: &store.GatewayBinding{GatewayURL: g.BaseURL, PushToken: "gwp_device"}, Wake: &WakeRequest{NotificationID: "ntf_1"}, Payload: []byte("SECRET BODY MUST NOT LEAVE SERVER")}
	for _, test := range []struct {
		status int
		body   string
		ok     bool
	}{
		{200, `{"accepted":true}`, true}, {202, `{"accepted":true}`, false}, {200, `{}`, false}, {200, `not json`, false}, {200, `{"accepted":true}garbage`, false}, {503, `{"accepted":true}`, false}, {401, `{}`, false},
	} {
		status, body = test.status, test.body
		err := g.Push(context.Background(), m)
		if (err == nil) != test.ok {
			t.Fatalf("status=%d body=%s error=%v", status, body, err)
		}
	}
	m.Binding.GatewayURL = "https://other.example"
	if g.Push(context.Background(), m) == nil {
		t.Fatal("credential sent to different gateway")
	}
}

func TestDirectAndGatewayNotificationInterruptionLevels(t *testing.T) {
	for _, test := range []struct{ priority, level string }{
		{"low", "passive"}, {"normal", "active"}, {"high", "time-sensitive"},
	} {
		t.Run(test.priority, func(t *testing.T) {
			direct, err := BuildMessage(store.PushTarget{Notification: store.Notification{
				ID: "ntf_focus", Title: "Focus test", Priority: test.priority,
			}})
			if err != nil {
				t.Fatal(err)
			}
			wake := WakeMessage(store.GatewayRoute{BindingID: "bind_focus"}, WakeRequest{
				NotificationID: "ntf_focus", Priority: test.priority,
			})
			for _, msg := range []Message{direct, wake} {
				var payload struct {
					APS map[string]any `json:"aps"`
				}
				if err := json.Unmarshal(msg.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.APS["interruption-level"] != test.level {
					t.Fatalf("wrong interruption level: %v", payload.APS)
				}
				if test.priority == "low" {
					if _, ok := payload.APS["sound"]; ok {
						t.Fatal("low priority must be silent")
					}
				} else if payload.APS["sound"] != "meow.wav" {
					t.Fatal("normal and high priority must request the bundled meow sound")
				}
			}
		})
	}
}

func TestGatewayClientNeverFollowsRedirect(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer ts.Close()
	g, _ := NewGatewayClient(ts.URL)
	err := g.Push(context.Background(), Message{Binding: &store.GatewayBinding{GatewayURL: g.BaseURL, PushToken: "gwp_device"}, Wake: &WakeRequest{NotificationID: "ntf_1"}})
	if err == nil || redirected {
		t.Fatalf("redirect followed=%v err=%v", redirected, err)
	}
}

func TestGatewayURLValidation(t *testing.T) {
	for _, raw := range []string{"https://user:password@example.com", "https://example.com?q=secret", "https://example.com#fragment", "file:///etc/passwd", "", "://"} {
		if _, err := GatewayURL(raw); err == nil {
			t.Fatalf("invalid URL accepted: %s", raw)
		}
	}
	for _, raw := range []string{"https://push.example.com/", "https://push.example.com/prefix", "http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080", "http://192.168.31.235:8080"} {
		got, err := GatewayURL(raw)
		if err != nil || strings.HasSuffix(got, "/") {
			t.Fatalf("valid URL rejected=%s %v", raw, err)
		}
	}
}

func FuzzGatewayIdentifiers(f *testing.F) {
	for _, s := range []string{"ntf_abc123", "../v1/keys", "ntf_a/b", "ntf_中文", "ntf_a?b", "ntf_"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		expected := strings.HasPrefix(s, "ntf_") && len(s) > 4 && len(s) <= 68
		if expected {
			for _, c := range s[4:] {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
					expected = false
					break
				}
			}
		}
		if ValidNotificationID(s) != expected {
			t.Fatalf("identifier validation mismatch: %q", s)
		}
		if ValidNotificationID(s) && (!strings.HasPrefix(s, "ntf_") || len(s) > 68 || strings.ContainsAny(s, "/.?%\\\n\r")) {
			t.Fatalf("unsafe ID accepted: %q", s)
		}
	})
}
