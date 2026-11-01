package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// Historical migration fixtures must actually remove the v8 schema, rather than
// merely relabel the latest database as an older version.
func stripSyncSchema(t *testing.T, s *Store) {
	t.Helper()
	stripLifecycleSchema(t, s)
	for _, q := range []string{`DROP TRIGGER inbox_sync_device`, `DROP TRIGGER inbox_sync_insert`, `DROP TRIGGER inbox_sync_update`, `DROP TRIGGER inbox_sync_question`, `DROP INDEX notifications_sync`, `ALTER TABLE notifications DROP COLUMN sync_sequence`, `DROP TABLE inbox_sync_clock`} {
		if _, err := s.w.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncMigrationFailureIsAtomic(t *testing.T) {
	st, path := openTest(t)
	reg := register(t, st)
	item, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Priority: "normal", Title: "preserved"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	stripSyncSchema(t, st)
	if _, err = st.w.Exec(`PRAGMA user_version=7; CREATE TABLE inbox_sync_clock(conflict TEXT)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if broken, err := Open(path); err == nil {
		broken.Close()
		t.Fatal("migration failure swallowed")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version=%d %v", version, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM pragma_table_info('notifications') WHERE name='sync_sequence'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial column persisted: %d %v", count, err)
	}
	if _, err = db.Exec(`DROP TABLE inbox_sync_clock`); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if got, err := recovered.Notification(ctx, item.ID); err != nil || got.Title != "preserved" {
		t.Fatalf("history=%+v %v", got, err)
	}
}

func TestSyncRejectsInvalidWindowsAndCancelledReads(t *testing.T) {
	st, _ := openTest(t)
	reg := register(t, st)
	for _, window := range [][3]int64{{-1, 0, 1}, {2, 1, 1}, {0, 1, 0}, {0, 1, 202}} {
		if _, err := st.SyncInbox(ctx, reg.Device.ID, window[0], window[1], int(window[2])); err == nil {
			t.Fatalf("invalid window accepted: %v", window)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.SyncInbox(cancelled, reg.Device.ID, 0, 1, 200); err == nil {
		t.Fatal("cancelled query succeeded")
	}
}
func TestSyncMigrationAndDeletionNeverResetSequence(t *testing.T) {
	st, path := openTest(t)
	reg := register(t, st)
	before, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Priority: "normal", Title: "before"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	stripSyncSchema(t, st)
	if _, err = st.w.Exec(`PRAGMA user_version=7`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sequence, err := st.SyncSequence(ctx, reg.Device.ID)
	if err != nil || sequence != 1 {
		t.Fatalf("backfill=%d %v", sequence, err)
	}
	if _, err = st.DeleteNotifications(ctx, reg.Device.ID, NotificationDeletion{ID: before.ID}); err != nil {
		t.Fatal(err)
	}
	next, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Priority: "normal", Title: "after"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := st.SyncSequence(ctx, reg.Device.ID)
	items, err := st.SyncInbox(ctx, reg.Device.ID, sequence, current, 200)
	if err != nil || len(items) != 1 || items[0].ID != next.ID || items[0].Sequence <= sequence {
		t.Fatalf("reused sequence: %+v %v", items, err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err = st.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got, err := restored.SyncSequence(ctx, reg.Device.ID); err != nil || got != current {
		t.Fatalf("backup clock=%d %v", got, err)
	}
}
func TestSyncClocksAreDeviceLocalAndNoopWritesStayEmpty(t *testing.T) {
	st, _ := openTest(t)
	a := register(t, st)
	b := register(t, st)
	n, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: a.Device.ID, KeyID: a.Key.ID, Type: "info", Priority: "normal", Title: "a"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if seq, _ := st.SyncSequence(ctx, b.Device.ID); seq != 0 {
		t.Fatalf("other device activity leaked: %d", seq)
	}
	seq, _ := st.SyncSequence(ctx, a.Device.ID)
	if err = st.SetPushStatus(ctx, n.ID, "queued", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.SyncSequence(ctx, a.Device.ID); got != seq {
		t.Fatalf("noop write advanced cursor: %d -> %d", seq, got)
	}
	if err = st.MarkDelivered(ctx, a.Device.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	changed, _ := st.SyncSequence(ctx, a.Device.ID)
	if changed <= seq {
		t.Fatal("delivery was not tracked")
	}
	if err = st.MarkDelivered(ctx, a.Device.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.SyncSequence(ctx, a.Device.ID); got != changed {
		t.Fatal("duplicate delivery churned sync")
	}
	plan := queryPlan(t, st, `SELECT id FROM notifications WHERE device_id=? AND sync_sequence>? AND sync_sequence<=? ORDER BY sync_sequence LIMIT 200`, a.Device.ID, seq, changed)
	if !strings.Contains(plan, "notifications_sync") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatal(plan)
	}
}
