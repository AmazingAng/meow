package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func requirePrivate(t *testing.T, path string, dir bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := os.FileMode(0600)
	if dir {
		want = 0700
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode=%04o want=%04o", filepath.Base(path), info.Mode().Perm(), want)
	}
}

func TestPrivatePathEncodingAndBackupFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private?mode=memory#fragment.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg := register(t, s)
	requirePrivate(t, path, false)
	for _, uri := range []string{"file:" + path, ":memory:"} {
		if opened, err := Open(uri); err == nil {
			opened.Close()
			t.Fatal("non-filesystem DB accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	failed := filepath.Join(root, "cancelled.db")
	if err = s.Backup(cancelled, failed); err == nil {
		t.Fatal("cancelled backup succeeded")
	}
	if _, err = os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial backup published: %v", err)
	}
	backup := filepath.Join(root, "snapshot.db")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() { results <- s.Backup(ctx, backup) })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent snapshots published %d times", successes)
	}
	restored, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.DeviceByToken(ctx, reg.DeviceToken); err != nil {
		t.Fatalf("snapshot inconsistent: %v", err)
	}
	staging, err := filepath.Glob(filepath.Join(root, ".meow-backup-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging snapshots leaked: %v %v", staging, err)
	}
}

func TestPrivateDatabaseAndBackupWithPermissiveUmask(t *testing.T) {
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)
	root := t.TempDir()
	path := filepath.Join(root, "db", "private.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	register(t, s)
	requirePrivate(t, filepath.Dir(path), true)
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		requirePrivate(t, file, false)
	}
	backup := filepath.Join(root, "backups", "snapshot.db")
	if err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	requirePrivate(t, filepath.Dir(backup), true)
	requirePrivate(t, backup, false)
	if err = s.Backup(ctx, backup); err == nil {
		t.Fatal("existing backup overwritten")
	}
}

func TestPrivateExistingFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "existing.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Chmod(path+suffix, 0666); err != nil {
			t.Fatal(err)
		}
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	defer s.Close()
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		requirePrivate(t, file, false)
	}
	link := filepath.Join(root, "link.db")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(link); err == nil {
		opened.Close()
		t.Fatal("symlink database accepted")
	}
	if err = s.Backup(ctx, link); err == nil {
		t.Fatal("symlink backup accepted")
	}
	parent := filepath.Join(root, "shared")
	if err = os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(filepath.Join(parent, "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	opened.Close()
	info, _ := os.Stat(parent)
	if info.Mode().Perm() != 0755 {
		t.Fatal("existing parent permissions changed")
	}
}
