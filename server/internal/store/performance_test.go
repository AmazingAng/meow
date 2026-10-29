package store

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/quick"
	"time"
)

func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.r.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(plan, "\n")
}

func BenchmarkServerPerformance(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	s.Now = func() time.Time { return now }
	reg, err := s.RegisterDevice(ctx, "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	seedPerformanceNotifications(b, s, reg.Device.ID, 30000, now)
	for i := 0; i < 10; i++ {
		other, err := s.RegisterDevice(ctx, "other")
		if err != nil {
			b.Fatal(err)
		}
		seedPerformanceNotifications(b, s, other.Device.ID, 27000, now)
	}
	cur := Cursor{At: now.Add(-29000 * time.Minute), ID: reg.Device.ID + "00029000"}
	b.Run("deep-page", func(b *testing.B) {
		for b.Loop() {
			if _, err := s.Inbox(ctx, reg.Device.ID, InboxQuery{Limit: 200, After: cur}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("legacy-prefix-page", func(b *testing.B) {
		for b.Loop() {
			rows, err := s.r.QueryContext(ctx, notificationSelect+` WHERE n.device_id = ? AND (n.created_at < ? OR (n.created_at = ? AND n.id < ?)) ORDER BY n.created_at DESC,n.id DESC LIMIT 200`, reg.Device.ID, ms(cur.At), ms(cur.At), cur.ID)
			if err != nil {
				b.Fatal(err)
			}
			for rows.Next() {
				if _, err := scanNotification(rows); err != nil {
					b.Fatal(err)
				}
			}
			if err := rows.Err(); err != nil {
				b.Fatal(err)
			}
			rows.Close()
		}
	})
	b.Run("empty-prune", func(b *testing.B) {
		for b.Loop() {
			if n, err := s.Prune(ctx, 720*time.Hour); err != nil || n != 0 {
				b.Fatalf("deleted=%d error=%v", n, err)
			}
		}
	})
	b.Run("legacy-unindexed-expiry", func(b *testing.B) {
		for b.Loop() {
			if _, err := s.w.ExecContext(ctx, `DELETE FROM notifications INDEXED BY sqlite_autoindex_notifications_2 WHERE question_id IS NULL AND created_at<=?`, ms(now.Add(-720*time.Hour))); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestInboxCursorUsesIndexRangeWithoutSort(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	q, args := s.inboxQuery(r.Device.ID, InboxQuery{Limit: 200, After: Cursor{At: time.Now(), ID: "ntf_cursor"}})
	plan := queryPlan(t, s, q, args...)
	if !strings.Contains(plan, "created_at") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("cursor must seek its index range without sorting: %s", plan)
	}
}

func TestPruneUsesExpiryIndexes(t *testing.T) {
	s, _ := openTest(t)
	for _, q := range []string{
		`SELECT rowid FROM notifications WHERE question_id IS NULL AND created_at <= ? ORDER BY created_at,rowid LIMIT 1000`,
		`SELECT rowid FROM questions WHERE status != 'pending' AND created_at <= ? ORDER BY created_at,rowid LIMIT 1000`,
		`SELECT rowid FROM idempotency WHERE created_at <= ? ORDER BY created_at,rowid LIMIT 1000`,
		`SELECT rowid FROM notification_receipts WHERE created_at <= ? ORDER BY created_at,rowid LIMIT 1000`,
	} {
		plan := queryPlan(t, s, q, time.Now().UnixMilli())
		if !strings.Contains(plan, "created_at<?") || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("expiry must seek time range without sorting: %s: %s", q, plan)
		}
	}
}

func seedPerformanceNotifications(t testing.TB, s *Store, device string, count int, at time.Time) {
	t.Helper()
	_, err := s.w.ExecContext(ctx, `WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM seq WHERE i < ?)
	INSERT INTO notifications(id,device_id,type,title,body,priority,created_at,push_status)
	SELECT ? || printf('%08d',i), ?, 'info', 'fixture', printf('%0512d',i), 'normal', ?-i*60000, 'accepted' FROM seq`, count, device, device, ms(at))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPruneCommitsBoundedBatchesAndRollsBackFailingBatch(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	seedPerformanceNotifications(t, s, r.Device.ID, 1500, time.Now().Add(-48*time.Hour))
	_, err := s.w.ExecContext(ctx, `CREATE TRIGGER fail_second_batch BEFORE DELETE ON notifications
	WHEN (SELECT COUNT(*) FROM notifications) <= 500 BEGIN SELECT RAISE(ABORT,'second batch failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(ctx, 24*time.Hour)
	if err == nil || n != 1000 {
		t.Fatalf("committed=%d error=%v; want 1000 and failing second batch", n, err)
	}
	var remaining int
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications`).Scan(&remaining); err != nil || remaining != 500 {
		t.Fatalf("failed batch was not atomic: remaining=%d error=%v", remaining, err)
	}
	if _, err := s.w.ExecContext(ctx, `DROP TRIGGER fail_second_batch`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Prune(ctx, 24*time.Hour); err != nil || n != 500 {
		t.Fatalf("resume count=%d error=%v", n, err)
	}
}

func downgradePerformanceSchema(t *testing.T, s *Store) {
	t.Helper()
	for _, q := range []string{
		`DROP INDEX notifications_inbox`, `CREATE INDEX notifications_inbox ON notifications(device_id,created_at DESC)`,
		`DROP INDEX IF EXISTS notifications_expiry`, `DROP INDEX IF EXISTS questions_expiry`,
		`DROP INDEX IF EXISTS idempotency_expiry`, `PRAGMA user_version=5`,
	} {
		if _, err := s.w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPerformanceMigrationPreservesRecords(t *testing.T) {
	s, path := openTest(t)
	r := register(t, s)
	n := cachedNotice(t, s, r, "migration")
	downgradePerformanceSchema(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var v int
	if err := migrated.r.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 6 {
		t.Fatalf("version=%d error=%v", v, err)
	}
	if _, err := migrated.DeviceByToken(ctx, r.DeviceToken); err != nil {
		t.Fatal(err)
	}
	if got, err := migrated.Notification(ctx, n.ID); err != nil || got.Body != n.Body {
		t.Fatalf("data lost: %v", err)
	}
	for _, table := range []string{"idempotency", "notification_receipts"} {
		var count int
		if err := migrated.r.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s=%d error=%v", table, count, err)
		}
	}
	if err := migrated.migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPerformanceMigrationFailureIsAtomic(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	n := cachedNotice(t, s, r, "retained")
	downgradePerformanceSchema(t, s)
	if _, err := s.w.Exec(`CREATE TABLE questions_expiry (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err == nil {
		t.Fatal("index migration failure swallowed")
	}
	var v, columns int
	if err := s.r.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 5 {
		t.Fatalf("failed migration version=%d error=%v", v, err)
	}
	if err := s.r.QueryRow(`SELECT count(*) FROM pragma_index_info('notifications_inbox')`).Scan(&columns); err != nil || columns != 2 {
		t.Fatalf("index replacement not rolled back: %d %v", columns, err)
	}
	if _, err := s.Notification(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
}

func TestInboxPaginationGenerated(t *testing.T) {
	err := quick.Check(func(seed uint64) bool {
		s, _ := openTest(t)
		defer s.Close()
		a, b := register(t, s), register(t, s)
		rng := rand.New(rand.NewSource(int64(seed)))
		var all []Notification
		base := time.Now().UTC().Truncate(time.Millisecond)
		for i := 0; i < 45; i++ {
			at := base.Add(-time.Duration(rng.Intn(4)) * time.Millisecond)
			s.Now = func() time.Time { return at }
			device := a
			if i%3 == 0 {
				device = b
			}
			n, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: device.Device.ID, KeyID: device.Key.ID, Type: "info", Title: fmt.Sprintf("row-%d", i), Priority: "normal"}, Idem{})
			if err != nil {
				t.Fatal(err)
			}
			if i%2 == 0 {
				if err := s.MarkRead(ctx, device.Device.ID, n.ID); err != nil {
					t.Fatal(err)
				}
				n.ReadAt = &at
			}
			if device.Device.ID == a.Device.ID {
				all = append(all, n)
			}
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].CreatedAt.Equal(all[j].CreatedAt) {
				return all[i].ID > all[j].ID
			}
			return all[i].CreatedAt.After(all[j].CreatedAt)
		})
		for _, unread := range []bool{false, true} {
			want := []string{}
			for _, n := range all {
				if !unread || n.ReadAt == nil {
					want = append(want, n.ID)
				}
			}
			got := []string{}
			cur := Cursor{}
			limit := 1 + rng.Intn(9)
			for pageNo := 0; ; pageNo++ {
				if pageNo > 45 {
					return false
				}
				page, err := s.Inbox(context.Background(), a.Device.ID, InboxQuery{After: cur, Limit: limit, Unread: unread})
				if err != nil {
					t.Fatal(err)
				}
				for _, n := range page {
					got = append(got, n.ID)
				}
				if len(page) < limit {
					break
				}
				last := page[len(page)-1]
				cur = Cursor{At: last.CreatedAt, ID: last.ID}
			}
			if !reflect.DeepEqual(got, want) {
				return false
			}
		}
		return true
	}, &quick.Config{MaxCount: 20, Rand: rand.New(rand.NewSource(20261029))})
	if err != nil {
		t.Fatal(err)
	}
}
