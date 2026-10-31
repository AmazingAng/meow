package store

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayQuotaSharedAcrossRoutesAndTokenAliases(t *testing.T) {
	st, path := openTest(t)
	now := time.Now().UTC()
	st.Now = func() time.Time { return now }
	first, err := st.RegisterGatewayRoute(ctx, strings.Repeat("a", 64), "production", "one")
	if err != nil {
		t.Fatal(err)
	}
	for range 60 {
		if err := st.TakeGatewayQuota(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
	}
	second, err := st.RegisterGatewayRoute(ctx, strings.Repeat("A", 64), "production", "two")
	if err != nil {
		t.Fatal(err)
	}
	wantQuota(t, st.TakeGatewayQuota(ctx, second.ID), "gateway_pushes")
	if err := st.RevokeGatewayRoute(ctx, first.ID, first.ManagementToken); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateGatewayRoute(ctx, second.ID, second.ManagementToken, strings.Repeat("b", 64), "production"); err != nil {
		t.Fatal(err)
	}
	if err := st.TakeGatewayQuota(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateGatewayRoute(ctx, second.ID, second.ManagementToken, strings.Repeat("a", 64), "production"); err != nil {
		t.Fatal(err)
	}
	wantQuota(t, st.TakeGatewayQuota(ctx, second.ID), "gateway_pushes")
	separate, err := st.RegisterGatewayRoute(ctx, strings.Repeat("a", 64), "sandbox", "three")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.TakeGatewayQuota(ctx, separate.ID); err != nil {
		t.Fatal(err)
	}
	peer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.Now = st.Now
	wantQuota(t, peer.TakeGatewayQuota(ctx, second.ID), "gateway_pushes")
	now = now.Add(time.Minute)
	if err := st.TakeGatewayQuota(ctx, second.ID); err != nil {
		t.Fatalf("window did not refill: %v", err)
	}
}

func TestGatewayQuotaConcurrentAcrossProcesses(t *testing.T) {
	st, path := openTest(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	routes := make([]GatewayRegistration, 2)
	for i := range routes {
		routes[i], err = st.RegisterGatewayRoute(ctx, "same-token", "production", "binding")
		if err != nil {
			t.Fatal(err)
		}
	}
	var accepted, limited atomic.Int32
	var wg sync.WaitGroup
	for i := range 80 {
		wg.Go(func() {
			target := []*Store{st, other}[i%2]
			err := target.TakeGatewayQuota(ctx, routes[i%2].ID)
			var quota *QuotaError
			if err == nil {
				accepted.Add(1)
			} else if errors.As(err, &quota) {
				limited.Add(1)
			} else {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 60 || limited.Load() != 20 {
		t.Fatalf("accepted=%d limited=%d", accepted.Load(), limited.Load())
	}
}

func TestGatewayQuotaMigrationPreservesActiveUsage(t *testing.T) {
	st, path := openTest(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	st.Now = func() time.Time { return now }
	routes := make([]GatewayRegistration, 3)
	for i, token := range []string{"aBc", "AbC", "abc"} {
		var err error
		routes[i], err = st.RegisterGatewayRoute(ctx, token, "production", "migration")
		if err != nil {
			t.Fatal(err)
		}
		at, count := now.Add(-10*time.Second).UnixMilli(), 30
		if i == 2 {
			at = now.Add(-2 * time.Minute).UnixMilli()
			count = 1000
		}
		if _, err = st.w.Exec(`UPDATE gateway_routes SET quota_at=?,quota_count=? WHERE id=?`, at, count, routes[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.w.Exec(`DROP TABLE gateway_push_quotas; PRAGMA user_version=6`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	migrated.Now = func() time.Time { return now }
	wantQuota(t, migrated.TakeGatewayQuota(ctx, routes[0].ID), "gateway_pushes")
	wantQuota(t, migrated.TakeGatewayQuota(ctx, routes[1].ID), "gateway_pushes")
	var count int
	if err = migrated.r.QueryRow(`SELECT quota_count FROM gateway_push_quotas WHERE apns_token='abc' AND apns_env='production'`).Scan(&count); err != nil || count != 60 {
		t.Fatalf("migration usage=%d err=%v", count, err)
	}
	now = now.Add(time.Minute)
	if err = migrated.TakeGatewayQuota(ctx, routes[2].ID); err != nil {
		t.Fatalf("migrated allowance never refilled: %v", err)
	}
	if err = migrated.r.QueryRow(`SELECT count(*) FROM gateway_push_quotas`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expired identities not pruned: %d %v", count, err)
	}
}

func TestGatewayQuotaRejectsStaleRouteWithoutSpendingNewToken(t *testing.T) {
	st, _ := openTest(t)
	route, err := st.RegisterGatewayRoute(ctx, "old-token", "sandbox", "rotation")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := st.GatewayRoute(ctx, route.ID, route.PushToken, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateGatewayRoute(ctx, route.ID, route.ManagementToken, "new-token", "production"); err != nil {
		t.Fatal(err)
	}
	if err = st.TakeGatewayPushQuota(ctx, stale); !errors.Is(err, ErrGatewayRouteChanged) {
		t.Fatalf("stale route accepted: %v", err)
	}
	for range 60 {
		if err = st.TakeGatewayQuota(ctx, route.ID); err != nil {
			t.Fatalf("new token quota spent: %v", err)
		}
	}
	wantQuota(t, st.TakeGatewayQuota(ctx, route.ID), "gateway_pushes")
	if err = st.RevokeGatewayRoute(ctx, route.ID, route.ManagementToken); err != nil {
		t.Fatal(err)
	}
	if err = st.TakeGatewayQuota(ctx, route.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked route accepted: %v", err)
	}
}
