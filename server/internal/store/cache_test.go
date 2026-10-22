package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func cachedNotice(t *testing.T, s *Store, r Registration, key string) Notification {
	t.Helper()
	n, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "info", Title: "private title", Body: "private body", Priority: "normal"}, Idem{Key: key, RequestHash: key})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCacheExpiryKeepsIdempotencyAndQuota(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	s.Limits.DailyMessages = 1
	base := time.Now().UTC().Truncate(time.Millisecond)
	s.Now = func() time.Time { return base }
	n := cachedNotice(t, s, r, "one")
	s.Now = func() time.Time { return base.Add(13 * time.Hour) }
	if _, err := s.Prune(ctx, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Notification(ctx, n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired content: %v", err)
	}
	_, replay, err := s.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "info", Title: "private title", Body: "private body", Priority: "normal"}, Idem{Key: "one", RequestHash: "one"})
	if !replay || !errors.Is(err, ErrNotFound) {
		t.Errorf("expired replay recreated a notification: replay=%v err=%v", replay, err)
	}
	_, _, err = s.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "info", Title: "new", Priority: "normal"}, Idem{})
	var quota *QuotaError
	if !errors.As(err, &quota) || quota.Limit != "daily_messages" {
		t.Errorf("cache expiry reset 24h quota: %v", err)
	}
	if _, err = s.DeviceByToken(ctx, r.DeviceToken); err != nil {
		t.Errorf("cache expiry deleted registration: %v", err)
	}
	s.Now = func() time.Time { return base.Add(25 * time.Hour) }
	if _, err = s.Prune(ctx, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	cachedNotice(t, s, r, "one")
}

func TestCacheExplicitDeletionDoesNotResetQuota(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	s.Limits.DailyMessages = 1
	n := cachedNotice(t, s, r, "")
	if _, err := s.DeleteNotifications(ctx, r.Device.ID, NotificationDeletion{ID: n.ID}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "info", Title: "second", Priority: "normal"}, Idem{})
	var q *QuotaError
	if !errors.As(err, &q) {
		t.Fatalf("delete reset quota: %v", err)
	}
}

func TestCacheCutoffAndAtomicCleanup(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	base := time.Now().UTC().Truncate(time.Millisecond)
	s.Now = func() time.Time { return base }
	old := cachedNotice(t, s, r, "old")
	s.Now = func() time.Time { return base.Add(time.Millisecond) }
	fresh := cachedNotice(t, s, r, "fresh")
	s.Now = func() time.Time { return base.Add(12 * time.Hour) }
	if _, err := s.Prune(ctx, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	// At the exact cutoff, content has reached its lifetime.
	if _, err := s.Notification(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("exact boundary survived: %v", err)
	}
	if _, err := s.Notification(ctx, fresh.ID); err != nil {
		t.Fatalf("recent message removed: %v", err)
	}
	if _, err := s.w.Exec(`CREATE TRIGGER cache_fail BEFORE DELETE ON idempotency BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return base.Add(25 * time.Hour) }
	if _, err := s.Prune(ctx, 12*time.Hour); err == nil {
		t.Fatal("cleanup failure swallowed")
	}
	if _, err := s.Notification(ctx, fresh.ID); err != nil {
		t.Fatalf("partial cleanup committed despite failure: %v", err)
	}
}

func TestCacheMigrationBackfillsQuotaWithoutContent(t *testing.T) {
	s, path := openTest(t)
	r := register(t, s)
	cachedNotice(t, s, r, "one")
	// This is a real pre-cache-ledger database, not a schema-version label alone.
	if _, err := s.w.Exec(`DROP TABLE IF EXISTS notification_receipts; ALTER TABLE notifications DROP COLUMN event; PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	migrated.Limits.DailyMessages = 1
	if _, err = migrated.DeleteNotifications(ctx, r.Device.ID, NotificationDeletion{From: time.UnixMilli(0), Before: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	_, _, err = migrated.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "info", Title: "new", Priority: "normal"}, Idem{})
	var quota *QuotaError
	if !errors.As(err, &quota) {
		t.Errorf("upgrade lost quota history: %v", err)
	}
	var id, device string
	var created int64
	if err = migrated.r.QueryRow(`SELECT * FROM notification_receipts`).Scan(&id, &device, &created); err != nil {
		t.Fatalf("receipt must contain only ID, device and creation time: %v", err)
	}
	if device != r.Device.ID {
		t.Fatal("receipt has wrong owner")
	}
	if _, err = migrated.w.Exec(`DELETE FROM devices WHERE id=?`, r.Device.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = migrated.r.QueryRow(`SELECT count(*) FROM notification_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("device deletion left receipts: %d %v", count, err)
	}
}

func TestCachePrunedQuotaIsSerialized(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	base := time.Now().UTC().Truncate(time.Millisecond)
	s.Now = func() time.Time { return base }
	cachedNotice(t, s, r, "seed")
	s.Now = func() time.Time { return base.Add(13 * time.Hour) }
	if _, err := s.Prune(ctx, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	s.Limits.DailyMessages = 3
	var mu sync.Mutex
	success := 0
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			_, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "info", Title: fmt.Sprint(i), Priority: "normal"}, Idem{})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else {
				var q *QuotaError
				if !errors.As(err, &q) {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if success != 2 {
		t.Fatalf("new messages=%d want 2 after already-pruned seed", success)
	}
}

func FuzzCacheRetentionBoundary(f *testing.F) {
	for _, n := range []int64{0, 1, 43_199_999, 43_200_000, 43_200_001, 90_000_000} {
		f.Add(n)
	}
	f.Fuzz(func(t *testing.T, age int64) {
		if age < 0 || age > 172_800_000 {
			t.Skip()
		}
		s, _ := openTest(t)
		r := register(t, s)
		base := time.Now().UTC().Truncate(time.Millisecond)
		s.Now = func() time.Time { return base }
		n := cachedNotice(t, s, r, "")
		s.Now = func() time.Time { return base.Add(time.Duration(age) * time.Millisecond) }
		if _, err := s.Prune(ctx, 12*time.Hour); err != nil {
			t.Fatal(err)
		}
		_, err := s.Notification(ctx, n.ID)
		if age >= 43_200_000 {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired age=%d err=%v", age, err)
			}
		} else if err != nil {
			t.Fatalf("recent age=%d err=%v", age, err)
		}
	})
}

func TestCacheReceiptFailureRollsBackNotification(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	if _, err := s.w.Exec(`CREATE TRIGGER reject_receipt BEFORE INSERT ON notification_receipts BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "info", Title: "must roll back", Priority: "normal"}, Idem{Key: "rollback", RequestHash: "same"})
	if err == nil {
		t.Fatal("receipt failure ignored")
	}
	for _, table := range []string{"notifications", "notification_receipts", "idempotency"} {
		var n int
		if err := s.r.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial %s writes=%d err=%v", table, n, err)
		}
	}
}

func TestCacheBackfillFailureRollsBackMigration(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	n := cachedNotice(t, s, r, "")
	if _, err := s.w.Exec(`DROP TABLE notification_receipts;
 CREATE TABLE notification_receipts(notification_id TEXT PRIMARY KEY,device_id TEXT NOT NULL,created_at INTEGER CHECK(created_at < 0));
 PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err == nil {
		t.Fatal("invalid backfill silently accepted")
	}
	var v int
	if err := s.r.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 3 {
		t.Fatalf("version advanced despite failure: %d %v", v, err)
	}
	if _, err := s.Notification(ctx, n.ID); err != nil {
		t.Fatalf("migration failure lost existing content: %v", err)
	}
	var indexes int
	if err := s.r.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='notification_receipts_device'`).Scan(&indexes); err != nil || indexes != 0 {
		t.Fatalf("partial migration index remained: %d %v", indexes, err)
	}
}

func TestCacheQuotaDeviceIsolationAndLegacyQuestionReceipts(t *testing.T) {
	s, _ := openTest(t)
	a, b := register(t, s), register(t, s)
	s.Limits.DailyMessages = 1
	ask(t, s, a, nil)
	if _, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: a.Device.ID, KeyID: a.Key.ID, Type: "info", Title: "blocked", Priority: "normal"}, Idem{}); err == nil {
		t.Fatal("legacy question not counted")
	}
	cachedNotice(t, s, b, "")
}

func TestCacheSchemaCreationFailurePreservesVersion(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.w.Exec(`DROP TABLE notification_receipts; CREATE VIEW notification_receipts AS SELECT id AS notification_id,id AS device_id,created_at FROM devices; PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err == nil {
		t.Fatal("invalid receipt schema silently accepted")
	}
	var v int
	if err := s.r.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 3 {
		t.Fatalf("schema failure advanced version: %d %v", v, err)
	}
}

func TestCacheInvalidNotificationDoesNotConsumeQuota(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	s.Limits.DailyMessages = 1
	_, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: r.Device.ID, KeyID: r.Key.ID, Type: "invalid", Title: "invalid", Priority: "normal"}, Idem{})
	if err == nil {
		t.Fatal("invalid notification accepted")
	}
	cachedNotice(t, s, r, "")
}
