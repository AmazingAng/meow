package store

import (
	"path/filepath"
	"testing"

	"meow.blue/meow/message"
)

func TestContextMigrationPersistenceAndBackup(t *testing.T) {
	st, path := openTest(t)
	reg := register(t, st)
	old, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "legacy", Priority: "normal"}, Idem{Key: "legacy", RequestHash: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.w.Exec(`ALTER TABLE notifications DROP COLUMN context; PRAGMA user_version=2`); err != nil {
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
	legacy, err := st.Notification(ctx, old.ID)
	if err != nil || legacy.Context != nil || legacy.Title != "legacy" {
		t.Fatalf("legacy=%v %v", legacy, err)
	}
	if _, err = st.DeviceByToken(ctx, reg.DeviceToken); err != nil {
		t.Fatal(err)
	}
	_, replay, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "legacy", Priority: "normal"}, Idem{Key: "legacy", RequestHash: "same"})
	if err != nil || !replay {
		t.Fatalf("replay=%v %v", replay, err)
	}
	c := &message.Context{Workspace: "工程", WorkspaceID: "w1", Session: "部署 😺", SessionID: "s1"}
	fresh, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "new", Priority: "normal", Context: c}, Idem{})
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
	defer restored.Close()
	got, err := restored.Notification(ctx, fresh.ID)
	if err != nil || got.Context == nil || *got.Context != *c {
		t.Fatalf("restore=%+v %v", got, err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.Notification(ctx, fresh.ID)
	if err != nil || got.Context == nil || *got.Context != *c {
		t.Fatalf("reopen=%+v %v", got, err)
	}
}

func TestContextCorruptionAndMigrationFailureAreReported(t *testing.T) {
	st, _ := openTest(t)
	reg := register(t, st)
	n, _, err := st.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "keep", Priority: "normal"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.w.Exec(`UPDATE notifications SET context='not-json' WHERE id=?`, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Notification(ctx, n.ID); err == nil {
		t.Fatal("corrupt context silently accepted")
	}
	if _, err = st.w.Exec(`UPDATE notifications SET context=NULL; PRAGMA user_version=2`); err != nil {
		t.Fatal(err)
	}
	// Deliberately inconsistent schema: duplicate-column migration must roll back,
	// preserve the old version, and leave the existing notification untouched.
	if err = st.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	var version int
	if err = st.w.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("version=%d %v", version, err)
	}
	if _, err = st.w.Exec(`PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	got, err := st.Notification(ctx, n.ID)
	if err != nil || got.Title != "keep" {
		t.Fatalf("lost original=%v %v", got, err)
	}
}
