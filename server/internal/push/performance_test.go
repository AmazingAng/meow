package push

import (
	"container/heap"
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"meow.blue/meow/server/internal/store"
)

func TestRetryBackoffReleasesWorkersAndRecoversAfterRestart(t *testing.T) {
	st, reg := reviewStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pending []string
	for i := 0; i < 4; i++ {
		n, _, err := st.CreateNotification(ctx, store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "limited", Priority: "normal"}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, n.ID)
	}
	healthy, err := st.RegisterDevice(ctx, "other device")
	if err != nil {
		t.Fatal(err)
	}
	token := "other-device-token"
	if _, err := st.UpdateDevice(ctx, healthy.Device.ID, store.DeviceUpdate{APNsToken: &token}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	accepted := make(chan struct{}, 1)
	var calls atomic.Int32
	p := pushFunc(func(ctx context.Context, m Message) error {
		if m.Token == token {
			accepted <- struct{}{}
			return nil
		}
		calls.Add(1)
		started <- struct{}{}
		return &Error{Status: 429, RetryAfter: 10 * time.Second}
	})
	d := NewDispatcher(st, p, slog.New(slog.DiscardHandler))
	for _, id := range pending {
		d.Enqueue(id)
	}
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx, 4) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("initial sends did not start")
		}
	}
	n, _, err := st.CreateNotification(ctx, store.NewNotification{DeviceID: healthy.Device.ID, KeyID: healthy.Key.ID, Type: "info", Title: "healthy", Priority: "normal"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	d.Enqueue(n.ID)
	d.Enqueue(n.ID)
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("unrelated device blocked behind Retry-After")
	}
	if calls.Load() != 4 {
		t.Fatalf("retried before upstream minimum delay: %d", calls.Load())
	}
	waitForPushStatus(t, st, n.ID, store.PushAccepted)
	cancel()
	<-done
	remaining, err := st.QueuedNotifications(context.Background(), time.Now().Add(-time.Hour), time.Now().Add(time.Second))
	if err != nil || len(remaining) != 4 {
		t.Fatalf("shutdown lost queued work: %d %v", len(remaining), err)
	}
	recovered := make(chan struct{}, 4)
	next := NewDispatcher(st, pushFunc(func(context.Context, Message) error { recovered <- struct{}{}; return nil }), slog.New(slog.DiscardHandler))
	rctx, stop := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); next.Run(rctx, 4) }()
	defer func() { stop(); <-finished }()
	for i := 0; i < 4; i++ {
		select {
		case <-recovered:
		case <-time.After(3 * time.Second):
			t.Fatal("restart did not recover pending sends")
		}
	}
}

func TestRetryHeapOrdersDeadlineAndStableID(t *testing.T) {
	at := time.Now()
	var h pushJobs
	for _, job := range []pushJob{{id: "b", readyAt: at}, {id: "late", readyAt: at.Add(time.Second)}, {id: "a", readyAt: at}} {
		heap.Push(&h, job)
	}
	for _, want := range []string{"a", "b", "late"} {
		if got := heap.Pop(&h).(pushJob).id; got != want {
			t.Fatalf("got=%s want=%s", got, want)
		}
	}
}

func waitForPushStatus(t *testing.T, st *store.Store, id, status string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		n, err := st.Notification(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if n.PushStatus == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status=%s; want %s", n.PushStatus, status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestScheduledRetryHonorsRetryAfter(t *testing.T) {
	for _, delay := range []time.Duration{37 * time.Second, time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st, reg := reviewStore(t)
				n, _, err := st.CreateNotification(context.Background(), store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "retry", Priority: "normal"}, store.Idem{})
				if err != nil {
					t.Fatal(err)
				}
				var attempts []time.Time
				accepted := make(chan struct{})
				d := NewDispatcher(st, pushFunc(func(context.Context, Message) error {
					attempts = append(attempts, time.Now())
					if len(attempts) == 1 {
						return &Error{Status: 429, RetryAfter: delay}
					}
					close(accepted)
					return nil
				}), slog.New(slog.DiscardHandler))
				d.Enqueue(n.ID)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { defer close(done); d.Run(ctx, 1) }()
				<-accepted
				waitForPushStatus(t, st, n.ID, store.PushAccepted)
				cancel()
				<-done
				if len(attempts) != 2 || attempts[1].Sub(attempts[0]) != delay {
					t.Fatalf("retry timestamps=%v", attempts)
				}
			})
		})
	}
}

func TestDelayedJobsCountTowardSchedulingCapacity(t *testing.T) {
	st, reg := reviewStore(t)
	started := make(chan struct{}, 1)
	d := NewDispatcher(st, pushFunc(func(context.Context, Message) error {
		select {
		case started <- struct{}{}:
		default:
		}
		return &Error{Status: 429, RetryAfter: time.Hour}
	}), slog.New(slog.DiscardHandler))
	d.queue = make(chan string, 4)
	for i := 0; i < 4; i++ {
		n, _, err := st.CreateNotification(context.Background(), store.NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Type: "info", Title: "limited", Priority: "normal"}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		d.Enqueue(n.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx, 1) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	d.Enqueue("overflow")
	d.mu.Lock()
	count := len(d.inflight)
	d.mu.Unlock()
	if count != 4 {
		t.Fatalf("delayed/running/queued IDs exceeded capacity: %d", count)
	}
}

// Drive the same production attempt function synchronously for existing unit fixtures.
func (d *Dispatcher) deliver(ctx context.Context, id string) {
	for attempt := 0; ; attempt++ {
		delay, retry := d.attempt(ctx, id, attempt)
		if !retry {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
