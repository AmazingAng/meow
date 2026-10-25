package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"meow.blue/meow/adapters"
	"meow.blue/meow/cli/internal/config"
)

func cursorTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("MEOW_CONFIG", filepath.Join(dir, "config.json"))
	return config.Config{Server: "https://disposable.example", Key: "meow_disposable_snapshot"}
}

func TestCursorSnapshotFilesPrivateBoundedAndExpired(t *testing.T) {
	cfg := cursorTestConfig(t)
	dir, err := cursorSnapshotDir(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := cursorSnapshotPath(dir, cfg, "chat", "g", "response")
	if strings.Contains(path, cfg.Key) || strings.Contains(filepath.Base(path), "chat") {
		t.Fatal("private identity in path")
	}
	if err := writeCursorSnapshot(path, cursorReplySnapshot{Body: "saved"}, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, path} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if p == dir {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o", p, info.Mode().Perm())
		}
	}
	var record cursorReplySnapshot
	if ok, err := readCursorSnapshot(path, &record); err != nil || !ok || record.Body != "saved" {
		t.Fatalf("round trip: %+v %v", record, err)
	}
	for _, name := range []string{"old.json", "old.tmp", "keep.txt"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-25 * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cursorSnapshotDir(cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old.json", "old.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("expired record kept", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatal("foreign file removed", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("fresh record removed", err)
	}
}

func TestCursorSnapshotRejectsInvalidAndUnreadableFiles(t *testing.T) {
	cfg := cursorTestConfig(t)
	dir, err := cursorSnapshotDir(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var result cursorReplySnapshot
	if ok, err := readCursorSnapshot(filepath.Join(dir, "missing"), &result); err != nil || ok {
		t.Fatalf("missing %v %v", ok, err)
	}
	cases := map[string][]byte{"bad": []byte("{"), "large": []byte(strings.Repeat("x", cursorSnapshotLimit+1)), "unreadable": []byte(`{"body":"private"}`)}
	for name, data := range cases {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
		if name == "unreadable" {
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(p, 0600)
		}
		if _, err := readCursorSnapshot(p, &result); err == nil {
			t.Fatal("accepted", name)
		}
	}
	if _, err := readCursorSnapshot(dir, &result); err == nil {
		t.Fatal("directory accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "bad"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := readCursorSnapshot(link, &result); err == nil {
		t.Fatal("symlink accepted")
	}
	parent := filepath.Join(dir, "blocked")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0700)
	if _, err := readCursorSnapshot(filepath.Join(parent, "entry"), &result); err == nil {
		t.Fatal("stat failure ignored")
	}
}

type cursorFaultFile struct {
	cursorSnapshotFile
	stage string
}

func (f cursorFaultFile) Write(b []byte) (int, error) {
	if f.stage == "write" {
		return 0, errors.New("injected write")
	}
	return f.cursorSnapshotFile.Write(b)
}
func (f cursorFaultFile) Sync() error {
	if f.stage == "sync" {
		return errors.New("injected sync")
	}
	return f.cursorSnapshotFile.Sync()
}
func (f cursorFaultFile) Close() error {
	err := f.cursorSnapshotFile.Close()
	if f.stage == "close" {
		return errors.New("injected close")
	}
	return err
}

func TestCursorSnapshotAtomicFailuresLeaveNoPartialRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	create, publish, replace := cursorCreateSnapshot, cursorPublishSnapshot, cursorReplaceSnapshot
	defer func() {
		cursorCreateSnapshot = create
		cursorPublishSnapshot = publish
		cursorReplaceSnapshot = replace
	}()
	if err := writeCursorSnapshot(path, make(chan int), false); err == nil {
		t.Fatal("marshal failure ignored")
	}
	if err := writeCursorSnapshot(filepath.Join(dir, "missing", "record"), "x", false); err == nil {
		t.Fatal("create failure ignored")
	}
	for _, stage := range []string{"write", "sync", "close"} {
		cursorCreateSnapshot = func(dir string) (cursorSnapshotFile, error) {
			f, err := create(dir)
			if err != nil {
				return nil, err
			}
			return cursorFaultFile{f, stage}, nil
		}
		if err := writeCursorSnapshot(path, "value", false); err == nil {
			t.Fatal("fault ignored", stage)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial record published for %s: %v", stage, err)
		}
	}
	cursorCreateSnapshot = create
	cursorPublishSnapshot = func(string, string) error { return errors.New("injected publish") }
	if err := writeCursorSnapshot(path, "value", false); err == nil {
		t.Fatal("publish failure ignored")
	}
	cursorPublishSnapshot = publish
	cursorReplaceSnapshot = func(string, string) error { return errors.New("injected replace") }
	if err := writeCursorSnapshot(path, "value", true); err == nil {
		t.Fatal("replace failure ignored")
	}
	cursorReplaceSnapshot = replace
	if err := writeCursorSnapshot(path, "first", false); err != nil {
		t.Fatal(err)
	}
	if err := writeCursorSnapshot(path, "second", false); err != nil {
		t.Fatal(err)
	}
	var got string
	if ok, err := readCursorSnapshot(path, &got); err != nil || !ok || got != "first" {
		t.Fatalf("first writer changed: %q %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files retained: %v %v", entries, err)
	}
}

func TestCursorSnapshotMissingRegistrationAndBadDirectory(t *testing.T) {
	cfg := cursorTestConfig(t)
	if _, err := cursorSnapshotDir(config.Config{}); err == nil {
		t.Fatal("empty registration accepted")
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEOW_CONFIG", filepath.Join(file, "config.json"))
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("file parent accepted")
	}
	t.Setenv("MEOW_CONFIG", filepath.Join(parent, "config.json"))
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(parent, "cursor-snapshots")); err != nil {
		t.Fatal(err)
	}
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("symlink directory accepted")
	}
	t.Setenv("MEOW_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("unavailable config path accepted")
	}
}

func TestCursorCaptureAndStopFailureModes(t *testing.T) {
	cfg := cursorTestConfig(t)
	for _, input := range []string{"null", "{", `{"hook_event_name":"stop","conversation_id":"c","generation_id":"g","text":"wrong event"}`, `{"hook_event_name":"afterAgentResponse","conversation_id":"c","generation_id":"","text":"missing identity"}`, `{"hook_event_name":"afterAgentResponse","conversation_id":"c","generation_id":"g","text":null}`} {
		if err := captureCursorResponse([]byte(input), true, cfg); err == nil {
			t.Fatalf("invalid capture accepted: %s", input)
		}
	}
	if _, _, send, err := cursorStopNotification([]byte(`{"conversation_id":"c","status":"completed"}`), true, cfg); err == nil || send {
		t.Fatal("missing generation accepted")
	}
	if err := captureCursorResponse([]byte(cursorInput("cursor-response", "c", "g", strings.Repeat("猫", 5000), "")), true, cfg); err != nil {
		t.Fatal(err)
	}
	n, _, send, err := cursorStopNotification([]byte(cursorInput("cursor-stop", "c", "g", "", "")), true, cfg)
	if err != nil || !send || len([]rune(n.Body)) != 4000 {
		t.Fatalf("bounded body: %d %v", len([]rune(n.Body)), err)
	}
	dir, err := cursorSnapshotDir(cfg)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := cursorSnapshotPath(dir, cfg, "c", "corrupt", "response")
	if err := os.WriteFile(corrupt, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	n, _, send, err = cursorStopNotification([]byte(cursorInput("cursor-stop", "c", "corrupt", "", "")), true, cfg)
	if err != nil || !send || n.Body != "" {
		t.Fatalf("corrupt reply fallback: %+v %v", n, err)
	}
	frozen := cursorSnapshotPath(dir, cfg, "c", "bad-delivery", "cursor-stop:true:"+os.Getenv("MEOW_LANGUAGE"))
	if err := os.WriteFile(frozen, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, send, err := cursorStopNotification([]byte(cursorInput("cursor-stop", "c", "bad-delivery", "", "")), true, cfg); err == nil || send {
		t.Fatal("corrupt delivery sent")
	}
	if _, _, _, err := cursorFrozenResult(adapters.Result{}); err == nil {
		t.Fatal("invalid frozen result accepted")
	}
	n.Title = ""
	if _, _, _, err := cursorFrozenResult(adapters.Result{Send: true, IdempotencyKey: "key", Notification: n}); err == nil {
		t.Fatal("invalid notification accepted")
	}
}

func TestCursorConcurrentStopsFreezeOnePayload(t *testing.T) {
	cfg := cursorTestConfig(t)
	raw := []byte(cursorInput("cursor-stop", "chat", "concurrent", "", ""))
	if err := captureCursorResponse([]byte(cursorInput("cursor-response", "chat", "concurrent", "First reply", "")), true, cfg); err != nil {
		t.Fatal(err)
	}
	const count = 16
	records := make([]string, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, key, send, err := cursorStopNotification(raw, true, cfg)
			if err != nil {
				errs[i] = err
				return
			}
			if !send {
				errs[i] = errors.New("stop not sent")
				return
			}
			b, _ := json.Marshal(n)
			records[i] = key + string(b)
		}(i)
	}
	wg.Wait()
	for i := range count {
		if errs[i] != nil || records[i] != records[0] {
			t.Fatalf("concurrent payload diverged at %d: %v", i, errs[i])
		}
	}
}

func FuzzCursorSnapshotIdentity(f *testing.F) {
	f.Add("chat", "generation", "response")
	f.Add("同名会话", "回合😺", "stop")
	f.Fuzz(func(t *testing.T, chat, generation, kind string) {
		if chat == "" || generation == "" || kind == "" {
			t.Skip()
		}
		cfg := config.Config{Server: "https://origin.example", Key: "meow_disposable_fuzz"}
		first := cursorSnapshotPath("/cache", cfg, chat, generation, kind)
		if first != cursorSnapshotPath("/cache", cfg, chat, generation, kind) {
			t.Fatal("non-deterministic identity")
		}
		other := cursorSnapshotPath("/cache", cfg, chat, generation+"x", kind)
		if reflect.DeepEqual(first, other) {
			t.Fatal("generations share a record")
		}
		rotated := cfg
		rotated.Key += "x"
		if first == cursorSnapshotPath("/cache", rotated, chat, generation, kind) {
			t.Fatal("registrations share a record")
		}
	})
}

// Deterministic filesystem faults cover failures that depend on an external
// process removing a directory/file between syscalls.
type cursorBrokenEntry struct {
	os.DirEntry
	err error
}

func (e cursorBrokenEntry) Info() (os.FileInfo, error) { return nil, e.err }

func TestCursorSnapshotDirectoryFaults(t *testing.T) {
	cfg := cursorTestConfig(t)
	dir, err := cursorSnapshotDir(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stat, chmod, readDir, remove := cursorSnapshotStat, cursorSnapshotChmod, cursorSnapshotReadDir, cursorSnapshotRemove
	defer func() {
		cursorSnapshotStat = stat
		cursorSnapshotChmod = chmod
		cursorSnapshotReadDir = readDir
		cursorSnapshotRemove = remove
	}()
	cursorSnapshotStat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("stat failure ignored")
	}
	cursorSnapshotStat = stat
	cursorSnapshotChmod = func(string, os.FileMode) error { return os.ErrPermission }
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("chmod failure ignored")
	}
	cursorSnapshotChmod = chmod
	cursorSnapshotReadDir = func(string) ([]os.DirEntry, error) { return nil, os.ErrPermission }
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("read-directory failure ignored")
	}
	p := filepath.Join(dir, "old.json")
	if err := os.WriteFile(p, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	entries, err := readDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	cursorSnapshotReadDir = func(string) ([]os.DirEntry, error) {
		return []os.DirEntry{cursorBrokenEntry{entries[0], os.ErrNotExist}}, nil
	}
	if _, err := cursorSnapshotDir(cfg); err != nil {
		t.Fatal("concurrent unlink not tolerated", err)
	}
	cursorSnapshotReadDir = func(string) ([]os.DirEntry, error) {
		return []os.DirEntry{cursorBrokenEntry{entries[0], os.ErrPermission}}, nil
	}
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("entry stat failure ignored")
	}
	cursorSnapshotReadDir = readDir
	cursorSnapshotRemove = func(string) error { return os.ErrPermission }
	if _, err := cursorSnapshotDir(cfg); err == nil {
		t.Fatal("prune failure ignored")
	}
}

func TestCursorSnapshotPreparationErrorsDoNotSend(t *testing.T) {
	cfg := cursorTestConfig(t)
	raw := []byte(cursorInput("cursor-stop", "chat", "g", "", ""))
	parent := t.TempDir()
	p := filepath.Join(parent, "block")
	if err := os.WriteFile(p, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	originalConfig := os.Getenv("MEOW_CONFIG")
	t.Setenv("MEOW_CONFIG", filepath.Join(p, "config.json"))
	if err := captureCursorResponse([]byte(cursorInput("cursor-response", "chat", "g", "reply", "")), true, cfg); err == nil {
		t.Fatal("capture failed to report unavailable state")
	}
	if _, _, send, err := cursorStopNotification(raw, true, cfg); err == nil || send {
		t.Fatal("stop sent without durable state")
	}
	t.Setenv("MEOW_CONFIG", originalConfig)
	var oversized map[string]any
	_ = json.Unmarshal(raw, &oversized)
	oversized["padding"] = strings.Repeat("x", adapters.MaxInputBytes)
	big, _ := json.Marshal(oversized)
	if _, _, send, err := cursorStopNotification(big, true, cfg); err == nil || send {
		t.Fatal("oversized adapter input accepted")
	}
	publish := cursorPublishSnapshot
	defer func() { cursorPublishSnapshot = publish }()
	cursorPublishSnapshot = func(string, string) error { return os.ErrPermission }
	if _, _, send, err := cursorStopNotification(raw, true, cfg); err == nil || send {
		t.Fatal("stop sent before snapshot publication")
	}
	cursorPublishSnapshot = func(source, dest string) error {
		if err := publish(source, dest); err != nil {
			return err
		}
		return os.WriteFile(dest, []byte("{"), 0600)
	}
	if _, _, send, err := cursorStopNotification(raw, true, cfg); err == nil || send {
		t.Fatal("stop sent a corrupt published record")
	}
}

func TestCursorSnapshotFirstWriterWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frozen.json")
	if err := writeCursorSnapshot(path, cursorReplySnapshot{Body: "first"}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeCursorSnapshot(path, cursorReplySnapshot{Body: "replacement"}, false); err != nil {
		t.Fatal(err)
	}
	var record cursorReplySnapshot
	if ok, err := readCursorSnapshot(path, &record); err != nil || !ok || record.Body != "first" {
		t.Fatalf("first snapshot overwritten: %+v %v", record, err)
	}
}

func TestCursorSnapshotScopeIncludesServer(t *testing.T) {
	cfg := cursorTestConfig(t)
	if err := captureCursorResponse([]byte(cursorInput("cursor-response", "chat", "g", "Server A private reply", "")), true, cfg); err != nil {
		t.Fatal(err)
	}
	other := cfg
	other.Server = "https://another-origin.example"
	n, _, send, err := cursorStopNotification([]byte(cursorInput("cursor-stop", "chat", "g", "", "")), true, other)
	if err != nil || !send || n.Body != "" {
		t.Fatalf("server scope leaked: %+v %v", n, err)
	}
}

func TestCursorSnapshotIdentityUnambiguous(t *testing.T) {
	cfg := config.Config{Server: "https://origin.example", Key: "meow_disposable"}
	a := cursorSnapshotPath("/cache", cfg, "chat\x00part", "generation", "response")
	b := cursorSnapshotPath("/cache", cfg, "chat", "part\x00generation", "response")
	if a == b {
		t.Fatal("different identity tuples share a snapshot")
	}
}
