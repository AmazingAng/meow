package store

import (
	"path/filepath"
	"testing"

	"meow.blue/meow/message"
)

func TestEventMigrationPersistenceAndBackup(t *testing.T) {
	st, path := openTest(t)
	reg := register(t, st)
	legacy, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "New result", Priority: "normal"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the actual v4 schema, retaining content and credentials.
	if _, err = st.w.Exec(`ALTER TABLE notifications DROP COLUMN event; PRAGMA user_version=4`); err != nil {
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
	got, err := st.Notification(ctx, legacy.ID)
	if err != nil || got.Event != "" || got.Title != "New result" {
		t.Fatalf("legacy=%+v %v", got, err)
	}
	for _, event := range []message.EventKind{message.EventResult, message.EventAttention, message.EventFailure} {
		n, _, err := st.CreateNotification(ctx, NewNotification{Event: event, DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "自定义标题", Priority: "low"}, Idem{})
		if err != nil {
			t.Fatal(err)
		}
		snapshot := filepath.Join(t.TempDir(), "backup.db")
		if err = st.Backup(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		restored, err := Open(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := restored.Notification(ctx, n.ID)
		restored.Close()
		if err != nil || saved.Event != event || saved.Type != "info" || saved.Priority != "low" {
			t.Fatalf("saved=%+v %v", saved, err)
		}
	}
	if _, err = st.w.Exec(`UPDATE notifications SET event='unknown' WHERE id=?`, legacy.ID); err == nil {
		t.Fatal("database accepted invalid event")
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	page, err := reopened.Inbox(ctx, reg.Device.ID, InboxQuery{Limit: 10})
	if err != nil || len(page) != 4 {
		t.Fatalf("reopened=%v %v", page, err)
	}
	for _, n := range page {
		if n.ID != legacy.ID && n.Event == "" {
			t.Fatal("event lost on reopen")
		}
	}
}

func TestEventMigrationFailureRollsBack(t *testing.T) {
	st, _ := openTest(t)
	reg := register(t, st)
	n, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "keep", Priority: "normal"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	// An inconsistent schema must fail instead of claiming to have upgraded.
	if _, err = st.w.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	if err = st.migrate(ctx); err == nil {
		t.Fatal("duplicate-column migration ignored")
	}
	var version int
	if err = st.w.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 4 {
		t.Fatalf("version=%d %v", version, err)
	}
	got, err := st.Notification(ctx, n.ID)
	if err != nil || got.Title != "keep" {
		t.Fatalf("got=%+v %v", got, err)
	}
}
