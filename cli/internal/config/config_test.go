package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSaveIgnoresPredictableTempAndReplacesDestinationSymlink(t *testing.T) {
	for _, destinationLink := range []bool{false, true} {
		t.Run(fmt.Sprint(destinationLink), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			t.Setenv("MEOW_CONFIG", path)
			victim := filepath.Join(dir, "unrelated")
			if err := os.WriteFile(victim, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, path+".tmp"); err != nil {
				t.Fatal(err)
			}
			if destinationLink {
				if err := os.Symlink(victim, path); err != nil {
					t.Fatal(err)
				}
			}
			want := Config{Server: "https://example.invalid", Key: "meow_disposable"}
			if err := Save(want); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "preserve" {
				t.Fatalf("symlink target overwritten: %q %v", data, err)
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatalf("unsafe published config: %v %v", info, err)
			}
			got, err := LoadFile()
			if err != nil || got.Key != want.Key || got.Server != want.Server {
				t.Fatalf("saved config=%+v %v", got, err)
			}
		})
	}
}

func TestSaveDoesNotInheritExistingTempPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MEOW_CONFIG", path)
	if err := os.WriteFile(path+".tmp", []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".tmp", 0644); err != nil {
		t.Fatal(err)
	}
	if err := Save(Config{Key: "meow_disposable"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode is not 0600: %v %v", info, err)
	}
	data, err := os.ReadFile(path + ".tmp")
	if err != nil || string(data) != "keep" {
		t.Fatalf("unrelated predictable temporary file changed: %q %v", data, err)
	}
}

func TestSaveConcurrentAndFailedRenameLeaveNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("MEOW_CONFIG", path)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := Save(Config{Server: fmt.Sprint(i), Key: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := LoadFile()
	if err != nil || got.Key == "" || got.Key != got.Server {
		t.Fatalf("mixed or corrupt config: %+v %v", got, err)
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEOW_CONFIG", blocked)
	if err := Save(Config{Key: "do-not-publish"}); err == nil {
		t.Fatal("rename over directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary files leaked: %v %v", entries, err)
	}
	info, err := os.Stat(blocked)
	if err != nil || !info.IsDir() {
		t.Fatal("failed write changed existing destination")
	}
}
