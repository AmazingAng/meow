package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var ctx = context.Background()

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meow.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func register(t *testing.T, s *Store) Registration {
	t.Helper()
	reg, err := s.RegisterDevice(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func ask(t *testing.T, s *Store, reg Registration, choices []string) Question {
	t.Helper()
	q, _, err := s.CreateQuestion(ctx, NewQuestion{
		NewNotification: NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Title: "Deploy to production?", Source: "Claude Code", Priority: "high"},
		Choices:         choices,
		TTL:             15 * time.Minute,
	}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestRegisterAndAuthenticate(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)

	d, err := s.DeviceByToken(ctx, reg.DeviceToken)
	if err != nil || d.ID != reg.Device.ID {
		t.Fatalf("DeviceByToken = %v, %v", d.ID, err)
	}
	k, err := s.KeyBySecret(ctx, reg.KeySecret)
	if err != nil || k.DeviceID != reg.Device.ID || !k.Has(ScopeAsk) || !k.Has(ScopeNotify) {
		t.Fatalf("KeyBySecret = %+v, %v", k, err)
	}
	if _, err := s.KeyBySecret(ctx, reg.DeviceToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device token accepted as a Device Key: %v", err)
	}
	if err := s.RevokeKey(ctx, reg.Device.ID, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KeyBySecret(ctx, reg.KeySecret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked key still valid: %v", err)
	}
	if err := s.RevokeKey(ctx, reg.Device.ID, k.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v", err)
	}
}

func TestRespondExactlyOnce(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	q := ask(t, s, reg, []string{"approve", "reject"})

	got, replay, err := s.Respond(ctx, q.ID, reg.Device.ID, "Approve", "r1")
	if err != nil || replay || got.Status != StatusAnswered || got.Answer != "approve" {
		t.Fatalf("first response = %+v replay=%v err=%v", got, replay, err)
	}
	// A retry of the same request is a replay, not a second action.
	got, replay, err = s.Respond(ctx, q.ID, reg.Device.ID, "approve", "r1")
	if err != nil || !replay || got.Answer != "approve" {
		t.Fatalf("replay = %+v replay=%v err=%v", got, replay, err)
	}
	// Anything else is rejected and the outcome is unchanged.
	for _, tc := range []struct{ answer, key string }{{"approve", "r2"}, {"reject", "r1"}, {"reject", "r3"}} {
		got, _, err = s.Respond(ctx, q.ID, reg.Device.ID, tc.answer, tc.key)
		if !errors.Is(err, ErrResolved) || got.Answer != "approve" {
			t.Fatalf("Respond(%q, %q) = %+v, %v; want ErrResolved", tc.answer, tc.key, got, err)
		}
	}
}

func TestRespondConcurrent(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	q := ask(t, s, reg, []string{"approve", "reject"})

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 20 {
		wg.Go(func() {
			answer := "approve"
			if i%2 == 1 {
				answer = "reject"
			}
			_, _, err := s.Respond(ctx, q.ID, reg.Device.ID, answer, "key-"+string(rune('a'+i)))
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, ErrResolved) {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d responses accepted, want exactly 1", wins)
	}
}

func TestRejectAndInvalidAnswer(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	q := ask(t, s, reg, []string{"approve", "reject"})

	if _, _, err := s.Respond(ctx, q.ID, reg.Device.ID, "maybe", "r1"); !errors.Is(err, ErrInvalidAnswer) {
		t.Fatalf("invalid answer = %v", err)
	}
	got, _, err := s.Respond(ctx, q.ID, reg.Device.ID, "REJECT", "r2")
	if err != nil || got.Status != StatusRejected || got.Answer != "reject" {
		t.Fatalf("reject = %+v, %v", got, err)
	}
}

func TestFreeTextAnswerIsExact(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	q := ask(t, s, reg, nil)
	const answer = "Run the evaluation first — then reject"
	got, _, err := s.Respond(ctx, q.ID, reg.Device.ID, answer, "r1")
	if err != nil || got.Status != StatusAnswered || got.Answer != answer {
		t.Fatalf("free text = %+v, %v", got, err)
	}
}

func TestExpiredFailsClosed(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	q := ask(t, s, reg, []string{"approve", "reject"})

	s.Now = func() time.Time { return time.Now().Add(time.Hour) }
	got, _, err := s.Respond(ctx, q.ID, reg.Device.ID, "approve", "r1")
	if !errors.Is(err, ErrExpired) || got.Status != StatusExpired || got.Answer != "" {
		t.Fatalf("late response = %+v, %v; want expired with no answer", got, err)
	}
	if _, _, err := s.Respond(ctx, q.ID, reg.Device.ID, "approve", "r1"); !errors.Is(err, ErrExpired) {
		t.Fatalf("retry after expiry = %v", err)
	}
}

func TestExpireDueAndCancel(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	q1 := ask(t, s, reg, nil)
	q2 := ask(t, s, reg, nil)

	if _, err := s.CancelQuestion(ctx, q2.ID, reg.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelQuestion(ctx, q2.ID, reg.Device.ID); !errors.Is(err, ErrResolved) {
		t.Fatalf("second cancel = %v", err)
	}
	if _, _, err := s.Respond(ctx, q2.ID, reg.Device.ID, "hi", "r1"); !errors.Is(err, ErrResolved) {
		t.Fatalf("respond to cancelled = %v", err)
	}

	s.Now = func() time.Time { return time.Now().Add(time.Hour) }
	ids, err := s.ExpireDue(ctx)
	if err != nil || len(ids) != 1 || ids[0] != q1.ID {
		t.Fatalf("ExpireDue = %v, %v", ids, err)
	}
}

func TestOtherDeviceCannotRespond(t *testing.T) {
	s, _ := openTest(t)
	a, b := register(t, s), register(t, s)
	q := ask(t, s, a, nil)
	if _, _, err := s.Respond(ctx, q.ID, b.Device.ID, "hi", "r1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-device respond = %v", err)
	}
	if _, err := s.CancelQuestion(ctx, q.ID, b.Device.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-device cancel = %v", err)
	}
}

func TestIdempotentCreate(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	nn := NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "Build completed", Priority: "normal"}

	n1, replay, err := s.CreateNotification(ctx, nn, Idem{Key: "k", RequestHash: "h1"})
	if err != nil || replay {
		t.Fatal(err, replay)
	}
	n2, replay, err := s.CreateNotification(ctx, nn, Idem{Key: "k", RequestHash: "h1"})
	if err != nil || !replay || n2.ID != n1.ID {
		t.Fatalf("replay = %v %v %v", n2.ID, replay, err)
	}
	if _, _, err := s.CreateNotification(ctx, nn, Idem{Key: "k", RequestHash: "h2"}); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatch = %v", err)
	}
	// The same key for a different resource type is also a mismatch.
	_, _, err = s.CreateQuestion(ctx, NewQuestion{NewNotification: nn, TTL: time.Minute}, Idem{Key: "k", RequestHash: "h1"})
	if !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("cross-type reuse = %v", err)
	}
	ns, _ := s.Inbox(ctx, reg.Device.ID, InboxQuery{Limit: 10})
	if len(ns) != 1 {
		t.Fatalf("inbox has %d notifications, want 1", len(ns))
	}
}

func TestInboxPaginationWithEqualTimestamps(t *testing.T) {
	s, _ := openTest(t)
	fixed := time.Now()
	s.Now = func() time.Time { return fixed }
	reg := register(t, s)
	for range 5 {
		if _, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "x", Priority: "normal"}, Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cur := Cursor{}
	for {
		page, err := s.Inbox(ctx, reg.Device.ID, InboxQuery{After: cur, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range page {
			if seen[n.ID] {
				t.Fatalf("duplicate %s across pages", n.ID)
			}
			seen[n.ID] = true
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		cur = Cursor{At: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d notifications, want 5", len(seen))
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	s, path := openTest(t)
	reg := register(t, s)
	q := ask(t, s, reg, []string{"approve", "reject"})
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Question(ctx, q.ID)
	if err != nil || got.Status != StatusPending || got.Text != "Deploy to production?" {
		t.Fatalf("after restart = %+v, %v", got, err)
	}
	if _, _, err := s2.Respond(ctx, q.ID, reg.Device.ID, "approve", "r1"); err != nil {
		t.Fatal(err)
	}
	ns, _ := s2.Inbox(ctx, reg.Device.ID, InboxQuery{Limit: 10})
	if len(ns) != 1 || ns[0].Question == nil || ns[0].Question.Status != StatusAnswered || ns[0].ReadAt == nil {
		t.Fatalf("inbox after restart = %+v", ns)
	}
}

func TestPruneKeepsPending(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	pending := ask(t, s, reg, nil)
	done := ask(t, s, reg, nil)
	if _, _, err := s.Respond(ctx, done.ID, reg.Device.ID, "ok", "r1"); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	if _, err := s.Prune(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Question(ctx, pending.ID); err != nil {
		t.Fatalf("pending question pruned: %v", err)
	}
	if _, err := s.Question(ctx, done.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolved question kept: %v", err)
	}
}

func TestBackup(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	q := ask(t, s, reg, nil)
	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.Question(ctx, q.ID); err != nil {
		t.Fatalf("question missing from backup: %v", err)
	}
}

// A response that waits for the write lock past the expiry must still be refused.
func TestRespondExpiresWhileWaitingForLock(t *testing.T) {
	s, _ := openTest(t)
	var offset atomic.Int64
	s.Now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	reg := register(t, s)
	q := ask(t, s, reg, []string{"approve", "reject"})

	blocker, err := s.w.BeginTx(ctx, nil) // hold the only writer connection
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := s.Respond(ctx, q.ID, reg.Device.ID, "approve", "r1")
		result <- err
	}()
	time.Sleep(100 * time.Millisecond) // Respond is now queued behind the blocker
	offset.Store(int64(time.Hour))
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrExpired) {
		t.Fatalf("Respond = %v, want ErrExpired", err)
	}
}

// Retrying an idempotent create after its resource was pruned must not fail.
func TestIdempotencyAfterShortRetention(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	nn := NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "x", Priority: "normal"}
	idem := Idem{Key: "k", RequestHash: "h"}
	if _, _, err := s.CreateNotification(ctx, nn, idem); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := s.Prune(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateNotification(ctx, nn, idem); err != nil {
		t.Fatalf("retry after prune = %v", err)
	}
}

func TestInboxFiltersAndCounts(t *testing.T) {
	s, _ := openTest(t)
	reg := register(t, s)
	other := register(t, s)
	for _, title := range []string{"Build completed", "CI failed: 100% of tests_x"} {
		if _, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: title, Source: "GitHub Actions", Priority: "normal"}, Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	q := ask(t, s, reg, []string{"approve", "reject"})
	ask(t, s, other, nil)

	count := func(iq InboxQuery) int {
		t.Helper()
		iq.Limit = 50
		ns, err := s.Inbox(ctx, reg.Device.ID, iq)
		if err != nil {
			t.Fatal(err)
		}
		return len(ns)
	}
	if n := count(InboxQuery{Pending: true}); n != 1 {
		t.Fatalf("pending = %d", n)
	}
	if n := count(InboxQuery{Search: "github"}); n != 2 {
		t.Fatalf("search by source = %d", n)
	}
	if n := count(InboxQuery{Search: "100%"}); n != 1 {
		t.Fatalf("search with %% = %d", n)
	}
	if n := count(InboxQuery{Search: "s_x"}); n != 1 {
		t.Fatalf("search with _ = %d", n)
	}
	if n := count(InboxQuery{Search: "%"}); n != 1 {
		t.Fatalf("literal %% search = %d, want 1", n)
	}
	unread, pending, err := s.InboxCounts(ctx, reg.Device.ID)
	if err != nil || unread != 3 || pending != 1 {
		t.Fatalf("counts = %d, %d, %v", unread, pending, err)
	}
	if n, err := s.MarkAllRead(ctx, reg.Device.ID); err != nil || n != 3 {
		t.Fatalf("MarkAllRead = %d, %v", n, err)
	}
	if n := count(InboxQuery{Unread: true}); n != 0 {
		t.Fatalf("unread after mark all = %d", n)
	}
	if _, _, err := s.Respond(ctx, q.ID, reg.Device.ID, "approve", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, pending, _ = s.InboxCounts(ctx, reg.Device.ID); pending != 0 {
		t.Fatalf("pending after answer = %d", pending)
	}
	// The other device's data is untouched.
	if unread, pending, _ = s.InboxCounts(ctx, other.Device.ID); unread != 1 || pending != 1 {
		t.Fatalf("other device counts = %d, %d", unread, pending)
	}
}
