package push

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"meow.blue/meow/server/internal/store"
)

// Dispatcher sends queued notifications in the background. The queue is only an
// optimization: every notification is persisted as "queued" first, and a periodic
// sweep re-enqueues anything left behind (full queue, crash, restart).
type Dispatcher struct {
	Retries int
	Backoff time.Duration

	store  *store.Store
	pusher Pusher
	log    *slog.Logger
	queue  chan string

	mu       sync.Mutex
	inflight map[string]bool
}

func NewDispatcher(st *store.Store, p Pusher, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		Retries: 3, Backoff: 2 * time.Second,
		store: st, pusher: p, log: log,
		queue: make(chan string, 1024), inflight: map[string]bool{},
	}
}

func (d *Dispatcher) Enqueue(id string) {
	d.mu.Lock()
	if d.inflight[id] {
		d.mu.Unlock()
		return
	}
	d.inflight[id] = true
	d.mu.Unlock()
	select {
	case d.queue <- id:
	default:
		d.done(id)
		d.log.Warn("push queue full; deferring to sweep", "notification", id)
	}
}

func (d *Dispatcher) done(id string) {
	d.mu.Lock()
	delete(d.inflight, id)
	d.mu.Unlock()
}

// Run processes the queue until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context, workers int) {
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-d.queue:
					d.deliver(ctx, id)
					d.done(id)
				}
			}
		})
	}
	d.sweep(ctx, 0)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-t.C:
			d.sweep(ctx, 30*time.Second)
		}
	}
}

// sweep re-enqueues notifications from the last day that are still queued and at least minAge old.
func (d *Dispatcher) sweep(ctx context.Context, minAge time.Duration) {
	now := d.store.Now()
	ids, err := d.store.QueuedNotifications(ctx, now.Add(-24*time.Hour), now.Add(-minAge))
	if err != nil {
		d.log.Error("push sweep failed", "err", err)
		return
	}
	for _, id := range ids {
		d.Enqueue(id)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, id string) {
	t, err := d.store.PushTarget(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.log.Error("load push target", "notification", id, "err", err)
		}
		return
	}
	n := t.Notification
	switch {
	case n.PushStatus != store.PushQueued:
		return
	case t.Device.APNsToken == "":
		d.set(ctx, id, store.PushSkipped, "device has no APNs token")
		return
	case n.Question != nil && n.Question.Status != store.StatusPending:
		d.set(ctx, id, store.PushSkipped, "question already "+n.Question.Status)
		return
	}
	msg, err := BuildMessage(t)
	if err != nil {
		d.set(ctx, id, store.PushFailed, err.Error())
		return
	}
	for attempt := 0; ; attempt++ {
		err = d.pusher.Push(ctx, msg)
		if err == nil {
			d.set(ctx, id, store.PushAccepted, "")
			return
		}
		if errors.Is(err, ErrNotConfigured) {
			d.set(ctx, id, store.PushSkipped, err.Error())
			return
		}
		var ae *Error
		if errors.As(err, &ae) {
			if ae.InvalidToken() {
				if cerr := d.store.ClearAPNsToken(ctx, t.Device.ID, msg.Token); cerr != nil {
					d.log.Error("clear apns token", "device", t.Device.ID, "err", cerr)
				}
				d.set(ctx, id, store.PushFailed, err.Error())
				return
			}
			if !ae.Retryable() {
				d.set(ctx, id, store.PushFailed, err.Error())
				return
			}
		}
		if ctx.Err() != nil {
			return // stays queued; retried after restart
		}
		if attempt >= d.Retries {
			d.log.Warn("push failed", "notification", id, "err", err)
			d.set(ctx, id, store.PushFailed, err.Error())
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.Backoff << attempt):
		}
	}
}

func (d *Dispatcher) set(ctx context.Context, id, status, msg string) {
	if err := d.store.SetPushStatus(ctx, id, status, msg); err != nil {
		d.log.Error("set push status", "notification", id, "err", err)
	}
}
