package api_test

import (
	"testing"
	"time"
)

func TestGatewayExpiredBindingCanRegisterAgain(t *testing.T) {
	st, p, base := gatewayFixture(t)
	now := time.Now().UTC()
	st.Now = func() time.Time { return now }
	old := enroll(t, base, "old_binding")
	now = now.Add(90 * 24 * time.Hour)
	status, _ := gatewayRequest(t, base, "PATCH", "/v1/gateway/devices/"+old.ID, old.ManagementToken, map[string]string{"apns_token": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "apns_env": "sandbox"})
	if status != 401 {
		t.Fatalf("expired management=%d", status)
	}
	status, _ = gatewayRequest(t, base, "POST", "/v1/gateway/push", old.PushToken, wake(old))
	if status != 401 {
		t.Fatalf("expired wake=%d", status)
	}
	// Same 401 recovery used by the shipped iOS configurePush path.
	fresh := enroll(t, base, "new_binding")
	in := wake(fresh)
	in["expires_at"] = now.Add(time.Hour)
	status, _ = gatewayRequest(t, base, "POST", "/v1/gateway/push", fresh.PushToken, in)
	if status != 200 {
		t.Fatalf("re-registered wake=%d", status)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.messages) != 1 {
		t.Fatalf("expired route sent push: %d", len(p.messages))
	}
	if fresh.ID == old.ID || fresh.BindingID == old.BindingID {
		t.Fatal("old binding reused")
	}
}
