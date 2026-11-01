package store

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
	"time"
)

// Reconstruct v8 rather than relabeling a database that already has v9 fields.
func stripLifecycleSchema(t *testing.T, s *Store) {
	t.Helper()
	for _, q := range []string{
		`DROP INDEX gateway_routes_expiry`, `DROP INDEX api_keys_active`, `DROP INDEX api_keys_revoked_expiry`,
		`DROP INDEX notifications_key`, `DROP INDEX questions_key`,
		`ALTER TABLE gateway_routes DROP COLUMN created_at`, `ALTER TABLE gateway_routes DROP COLUMN last_seen_at`,
	} {
		if _, err := s.w.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGatewayLifecycleMigrationPreservesRoutesAndRollsBack(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback"}[fail], func(t *testing.T) {
			s, path := openTest(t)
			reg := register(t, s)
			r, err := s.RegisterGatewayRoute(ctx, "old-apns", "production", "same-binding")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.TakeGatewayQuota(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			stripLifecycleSchema(t, s)
			if _, err = s.w.Exec(`PRAGMA user_version=8`); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err = s.w.Exec(`CREATE TABLE api_keys_active(conflict TEXT)`); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			before := time.Now().UnixMilli()
			upgraded, err := Open(path)
			if fail {
				if err == nil {
					upgraded.Close()
					t.Fatal("migration conflict accepted")
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var version, columns int
				if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 8 {
					t.Fatalf("version=%d %v", version, err)
				}
				if err = db.QueryRow(`SELECT count(*) FROM pragma_table_info('gateway_routes') WHERE name IN ('created_at','last_seen_at')`).Scan(&columns); err != nil || columns != 0 {
					t.Fatalf("partial migration=%d %v", columns, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Close()
			if _, err = upgraded.DeviceByToken(ctx, reg.DeviceToken); err != nil {
				t.Fatal(err)
			}
			if got, err := upgraded.GatewayRoute(ctx, r.ID, r.ManagementToken, true); err != nil || got.BindingID != r.BindingID {
				t.Fatalf("route changed=%+v %v", got, err)
			}
			var created, seen int64
			if err = upgraded.r.QueryRow(`SELECT created_at,last_seen_at FROM gateway_routes WHERE id=?`, r.ID).Scan(&created, &seen); err != nil || created < before || seen != created {
				t.Fatalf("legacy route expiry not reset safely: %d %d %v", created, seen, err)
			}
			var count int
			if err = upgraded.r.QueryRow(`SELECT quota_count FROM gateway_push_quotas`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("quota lost=%d %v", count, err)
			}
		})
	}
}

func TestGatewayPruneBatchesPreserveRenewedRoutesAndQuota(t *testing.T) {
	s, _ := openTest(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	s.Now = func() time.Time { return now }
	r, err := s.RegisterGatewayRoute(ctx, "same-token", "production", "keep")
	if err != nil {
		t.Fatal(err)
	}
	for range 1001 {
		if _, err = s.RegisterGatewayRoute(ctx, "unused", "production", "old"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(89 * 24 * time.Hour)
	if err = s.TakeGatewayQuota(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	// Quota metadata must be independent even if cleanup is called in the same window.
	if _, err = s.w.Exec(`UPDATE gateway_push_quotas SET quota_at=?,quota_count=60`, ms(now)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.w.Exec(`CREATE TRIGGER fail_late_route_batch BEFORE DELETE ON gateway_routes WHEN (SELECT count(*) FROM gateway_routes)<=2 BEGIN SELECT RAISE(ABORT,'second batch failed'); END`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneGatewayRoutes(ctx); err == nil || n != 1000 {
		t.Fatalf("partial cleanup=%d %v", n, err)
	}
	if _, err = s.w.Exec(`DROP TRIGGER fail_late_route_batch`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneGatewayRoutes(ctx); err != nil || n != 1 {
		t.Fatalf("cleanup resume=%d %v", n, err)
	}
	if _, err = s.GatewayRoute(ctx, r.ID, r.PushToken, false); err != nil {
		t.Fatalf("renewed route deleted: %v", err)
	}
	wantQuota(t, s.TakeGatewayQuota(ctx, r.ID), "gateway_pushes")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.PruneGatewayRoutes(cancelled); err == nil {
		t.Fatal("cancelled prune accepted")
	}
}

func TestGatewayLifetimeGenerated(t *testing.T) {
	err := quick.Check(func(age uint16) bool {
		s, _ := openTest(t)
		defer s.Close()
		now := time.Now().UTC().Truncate(time.Millisecond)
		s.Now = func() time.Time { return now }
		r, err := s.RegisterGatewayRoute(ctx, "apns", "production", "generated")
		if err != nil {
			t.Fatal(err)
		}
		days := int(age) % 181
		now = now.Add(time.Duration(days) * 24 * time.Hour)
		_, err = s.GatewayRoute(ctx, r.ID, r.PushToken, false)
		return days < 90 && err == nil || days >= 90 && errors.Is(err, ErrNotFound)
	}, &quick.Config{MaxCount: 30, Rand: rand.New(rand.NewSource(20261102))})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRevokedKeysAreRemovedWithoutLosingHistoryOrQuota(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	n, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Priority: "normal", Title: "keep", Body: "history"}, Idem{Key: "old", RequestHash: "same"})
	if err != nil {
		t.Fatal(err)
	}
	q := ask(t, s, reg, []string{"approve", "reject"})
	if _, _, err = s.Respond(ctx, q.ID, reg.Device.ID, "approve", "history"); err != nil {
		t.Fatal(err)
	}
	other := register(t, s)
	if err = s.RevokeKey(ctx, other.Device.ID, reg.Key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-device revoke=%v", err)
	}
	if err = s.RevokeKey(ctx, reg.Device.ID, reg.Key.ID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = s.r.QueryRow(`SELECT count(*) FROM api_keys WHERE device_id=?`, reg.Device.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("revoked credential rows=%d %v", rows, err)
	}
	if err = s.r.QueryRow(`SELECT count(*) FROM idempotency WHERE key_id=?`, reg.Key.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("revoked idempotency=%d %v", rows, err)
	}
	if got, err := s.Notification(ctx, n.ID); err != nil || got.Body != "history" {
		t.Fatalf("notification lost=%+v %v", got, err)
	}
	if got, err := s.Question(ctx, q.ID); err != nil || got.Answer != "approve" {
		t.Fatalf("answer lost=%+v %v", got, err)
	}
	if _, err = s.KeyBySecret(ctx, reg.KeySecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked auth=%v", err)
	}
	if err = s.RevokeKey(ctx, reg.Device.ID, reg.Key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat revoke=%v", err)
	}
	s.Limits.DailyMessages = 2
	for range 50 {
		k, _, err := s.CreateKey(ctx, reg.Device.ID, "rotation", DefaultScopes)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RevokeKey(ctx, reg.Device.ID, k.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.r.QueryRow(`SELECT count(*) FROM api_keys WHERE device_id=?`, reg.Device.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rotation accumulated rows=%d %v", rows, err)
	}
	k, _, err := s.CreateKey(ctx, reg.Device.ID, "new", DefaultScopes)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: k.ID, Type: "info", Priority: "normal", Title: "over budget"}, Idem{})
	wantQuota(t, err, "daily_messages")
}

func TestPruneRemovesLegacyRevokedKeysAndUsesActiveIndex(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	for range 1001 {
		k, _, err := s.CreateKey(ctx, reg.Device.ID, "legacy", DefaultScopes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.w.Exec(`UPDATE api_keys SET revoked_at=? WHERE id=?`, ms(s.now()), k.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Prune(ctx, 720*time.Hour); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.r.QueryRow(`SELECT count(*) FROM api_keys`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("legacy revoked rows remain=%d %v", rows, err)
	}
	if _, err := s.KeyBySecret(ctx, reg.KeySecret); err != nil {
		t.Fatalf("active key lost: %v", err)
	}
	plan := queryPlan(t, s, `SELECT count(*) FROM api_keys WHERE device_id=? AND revoked_at IS NULL`, reg.Device.ID)
	if !strings.Contains(plan, "api_keys_active") {
		t.Fatalf("active lookup scans historical rows: %s", plan)
	}
}

func TestCredentialDeletionDoesNotScanOtherDevicesHistory(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	for _, table := range []string{"notifications", "questions"} {
		plan := queryPlan(t, s, `SELECT rowid FROM `+table+` WHERE key_id=?`, reg.Key.ID)
		if !strings.Contains(plan, table+"_key") {
			t.Fatalf("credential deletion scans all %s: %s", table, plan)
		}
	}
}

func TestGatewayRouteExpiryBoundaryAndRenewal(t *testing.T) {
	for _, renew := range []string{"none", "management", "wake"} {
		t.Run(renew, func(t *testing.T) {
			s, _ := openTest(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			s.Now = func() time.Time { return now }
			r, err := s.RegisterGatewayRoute(ctx, "apns", "production", "binding")
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(89 * 24 * time.Hour)
			if renew == "management" {
				err = s.UpdateGatewayRoute(ctx, r.ID, r.ManagementToken, "apns", "production")
			}
			if renew == "wake" {
				err = s.TakeGatewayQuota(ctx, r.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if renew != "none" {
				now = now.Add(89 * 24 * time.Hour)
			}
			for _, management := range []bool{true, false} {
				secret := r.PushToken
				if management {
					secret = r.ManagementToken
				}
				if _, err = s.GatewayRoute(ctx, r.ID, secret, management); err != nil {
					t.Fatalf("live route rejected: %v", err)
				}
			}
			now = now.Add(24 * time.Hour)
			for _, management := range []bool{true, false} {
				secret := r.PushToken
				if management {
					secret = r.ManagementToken
				}
				if _, err = s.GatewayRoute(ctx, r.ID, secret, management); !errors.Is(err, ErrNotFound) {
					t.Fatalf("expired route authenticated: %v", err)
				}
			}
			if err = s.UpdateGatewayRoute(ctx, r.ID, r.ManagementToken, "new", "sandbox"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired route revived: %v", err)
			}
			if err = s.TakeGatewayQuota(ctx, r.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired route woke: %v", err)
			}
		})
	}
}
