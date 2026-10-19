package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"meow.blue/meow/server/internal/api"
)

func TestGatewayMobileBindingRequiresDevice(t *testing.T) {
	e := setup(t)
	ts := httptest.NewServer(api.New(api.Config{Store: e.st, GatewayURL: "https://push.example.com"}))
	defer ts.Close()
	binding := map[string]any{"gateway_device_id": "gwd_device", "push_token": "gwp_" + strings.Repeat("a", 39), "binding_id": "bind_1"}
	for _, token := range []string{e.reg.Key.Secret, ""} {
		for _, method := range []string{"GET", "PUT"} {
			path := "/v1/push-config"
			if method == "PUT" {
				path = "/v1/device/push-gateway"
			}
			status, _ := gatewayRequest(t, ts.URL, method, path, token, binding)
			if status != 401 && status != 403 {
				t.Fatalf("send-only/anonymous accessed %s: %d", path, status)
			}
		}
	}
	status, body := gatewayRequest(t, ts.URL, "GET", "/v1/push-config", e.reg.DeviceToken, nil)
	if status != 200 || !strings.Contains(string(body), "https://push.example.com") {
		t.Fatalf("config=%d %s", status, body)
	}
	status, body = gatewayRequest(t, ts.URL, "PUT", "/v1/device/push-gateway", e.reg.DeviceToken, binding)
	if status != 204 {
		t.Fatalf("binding=%d %s", status, body)
	}
	got, err := e.st.GatewayBinding(ctx, e.reg.Device.ID)
	if err != nil || got.DeviceID != "gwd_device" || got.GatewayURL != "https://push.example.com" || got.PushToken != binding["push_token"] {
		t.Fatalf("saved binding=%+v %v", got, err)
	}
	// Stored wake credentials are never returned to callers.
	for _, path := range []string{"/v1/device", "/v1/push-config"} {
		_, body = gatewayRequest(t, ts.URL, "GET", path, e.reg.DeviceToken, nil)
		if strings.Contains(string(body), binding["push_token"].(string)) {
			t.Fatalf("secret exposed at %s", path)
		}
	}
	binding["push_token"] = "gwm_owner"
	status, _ = gatewayRequest(t, ts.URL, "PUT", "/v1/device/push-gateway", e.reg.DeviceToken, binding)
	if status != 422 {
		t.Fatalf("owner credential accepted as wake secret=%d", status)
	}
	// Direct mode cannot silently acquire relay bindings.
	status, _ = gatewayRequest(t, e.url, "PUT", "/v1/device/push-gateway", e.reg.DeviceToken, binding)
	if status != 409 {
		t.Fatalf("binding accepted in direct mode=%d", status)
	}
}

func TestGatewayWhoamiDoesNotRequireDirectAPNsToken(t *testing.T) {
	e := setup(t)
	const gateway = "https://push.example.com"
	ts := httptest.NewServer(api.New(api.Config{Store: e.st, GatewayURL: gateway}))
	defer ts.Close()
	status, _ := gatewayRequest(t, ts.URL, "PUT", "/v1/device/push-gateway", e.reg.DeviceToken, map[string]string{
		"gateway_device_id": "gwd_ready", "push_token": "gwp_" + strings.Repeat("a", 39), "binding_id": "bind_ready",
	})
	if status != 204 {
		t.Fatalf("bind=%d", status)
	}
	status, body := gatewayRequest(t, ts.URL, "GET", "/v1/whoami", e.reg.Key.Secret, nil)
	var result struct {
		PushRegistered bool `json:"push_registered"`
		Device         struct {
			HasAPNsToken bool `json:"has_apns_token"`
		}
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if status != 200 || !result.PushRegistered || result.Device.HasAPNsToken {
		t.Fatalf("gateway registration misreported=%s", body)
	}
}
