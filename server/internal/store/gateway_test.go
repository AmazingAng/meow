package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestGatewayMigrationBackupAndRevocation(t *testing.T) {
	st, path := openTest(t)
	reg := register(t, st)
	n, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "keep me", Priority: "normal"}, Idem{Key: "before-migration", RequestHash: "same"})
	if err != nil {
		t.Fatal(err)
	}
	q, _, err := st.CreateQuestion(ctx, NewQuestion{NewNotification: NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "question", Title: "preserve answer", Priority: "normal"}, Choices: []string{"approve", "reject"}, TTL: time.Hour}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.Respond(ctx, q.ID, reg.Device.ID, "approve", "before-upgrade-response"); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the exact v1 schema from a v2 installation with existing data.
	if _, err = st.w.Exec(`DROP TABLE gateway_routes; DROP TABLE device_gateway_bindings; PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.DeviceByToken(ctx, reg.DeviceToken); err != nil {
		t.Fatalf("lost device: %v", err)
	}
	got, err := st.Notification(ctx, n.ID)
	if err != nil || got.Title != "keep me" {
		t.Fatalf("lost notification=%+v %v", got, err)
	}
	_, replay, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "keep me", Priority: "normal"}, Idem{Key: "before-migration", RequestHash: "same"})
	if err != nil || !replay {
		t.Fatalf("lost idempotency: %v %v", replay, err)
	}
	answered, replayed, err := st.Respond(ctx, q.ID, reg.Device.ID, "approve", "before-upgrade-response")
	if err != nil || !replayed || answered.Status != StatusAnswered {
		t.Fatalf("lost question/response: %+v replay=%v err=%v", answered, replayed, err)
	}
	route, err := st.RegisterGatewayRoute(ctx, "apns", "sandbox", "bind_1")
	if err != nil {
		t.Fatal(err)
	}
	binding := GatewayBinding{GatewayURL: "https://push.example", DeviceID: route.ID, PushToken: route.PushToken, BindingID: route.BindingID}
	if err = st.SetGatewayBinding(ctx, reg.Device.ID, binding); err != nil {
		t.Fatal(err)
	}
	for range 60 {
		if err = st.TakeGatewayQuota(ctx, route.ID); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := filepath.Join(t.TempDir(), "backup.db")
	if err = st.Backup(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got, err := restored.GatewayBinding(ctx, reg.Device.ID); err != nil || got != binding {
		t.Fatalf("lost outbound binding=%+v %v", got, err)
	}
	if _, err = restored.GatewayRoute(ctx, route.ID, route.ManagementToken, true); err != nil {
		t.Fatal(err)
	}
	if err = restored.TakeGatewayQuota(ctx, route.ID); err == nil {
		t.Fatal("quota reset after restore")
	}
	restored.Now = func() time.Time { return time.Now().Add(61 * time.Second) }
	if err = restored.TakeGatewayQuota(ctx, route.ID); err != nil {
		t.Fatalf("quota never refilled: %v", err)
	}
	if err = restored.RevokeGatewayRoute(ctx, route.ID, route.ManagementToken); err != nil {
		t.Fatal(err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = reopened.GatewayRoute(ctx, route.ID, route.PushToken, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revocation lost: %v", err)
	}
}

func TestGatewayEnrollmentHasNoInstallationCapAndKeepsRouteIsolation(t *testing.T) {
	st, _ := openTest(t)
	first, err := st.RegisterGatewayRoute(ctx, "first", "sandbox", "bind_1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.RegisterGatewayRoute(ctx, "second", "sandbox", "bind_2")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateGatewayRoute(ctx, first.ID, second.ManagementToken, "attacker", "production"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-route management=%v", err)
	}
	// Crossing 1,000 devices must not impose a product admission limit.
	for range 1000 {
		if _, err = st.RegisterGatewayRoute(ctx, "fixture", "sandbox", "bind_capacity"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.RegisterGatewayRoute(ctx, "next", "sandbox", "bind_extra"); err != nil {
		t.Fatalf("arbitrary installation cap: %v", err)
	}
	if err = st.RevokeGatewayRoute(ctx, first.ID, first.ManagementToken); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RegisterGatewayRoute(ctx, "replacement", "production", "bind_new"); err != nil {
		t.Fatalf("registration after revocation failed: %v", err)
	}
}
