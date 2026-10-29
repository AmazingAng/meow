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
	if len(d.inflight) >= cap(d.queue) {
		d.mu.Unlock()
		d.log.Warn("push scheduling capacity full; deferring to sweep", "notification", id)
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
	d.runScheduled(ctx, max(1, workers))
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

// attempt always reloads current state and returns without sleeping.
func (d *Dispatcher) attempt(ctx context.Context, id string, attempt int) (time.Duration, bool) {
	if ctx.Err() != nil {
		return 0, false // leave queued for restart
	}
	t, err := d.store.PushTarget(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.log.Error("load push target", "notification", id, "err", err)
		}
		return 0, false
	}
	n := t.Notification
	switch {
	case n.PushStatus != store.PushQueued:
		return 0, false
	case t.Device.APNsToken == "" && !d.isGateway():
		d.set(ctx, id, store.PushSkipped, "device has no APNs token")
		return 0, false
	case n.Question != nil && n.Question.Status != store.StatusPending:
		d.set(ctx, id, store.PushSkipped, "question already "+n.Question.Status)
		return 0, false
	case n.Question != nil && !d.store.Now().Before(n.Question.ExpiresAt):
		d.set(ctx, id, store.PushSkipped, "question already expired")
		return 0, false
	}
	msg, err := d.message(ctx, t)
	if err != nil {
		if d.isGateway() {
			if errors.Is(err, store.ErrNotFound) {
				d.set(ctx, id, store.PushSkipped, "device has no gateway binding")
			} else {
				d.log.Error("load gateway binding", "notification", id, "err", err)
			}
		} else {
			d.set(ctx, id, store.PushFailed, err.Error())
		}
		return 0, false
	}
	err = d.pusher.Push(ctx, msg)
	if err == nil {
		d.set(ctx, id, store.PushAccepted, "")
		return 0, false
	}
	if errors.Is(err, ErrNotConfigured) {
		d.set(ctx, id, store.PushSkipped, err.Error())
		return 0, false
	}
	var ae *Error
	if errors.As(err, &ae) {
		if ae.InvalidToken() && !d.isGateway() {
			if cerr := d.store.ClearAPNsToken(ctx, t.Device.ID, msg.Token); cerr != nil {
				d.log.Error("clear apns token", "device", t.Device.ID, "err", cerr)
			}
			d.set(ctx, id, store.PushFailed, err.Error())
			return 0, false
		}
		if !ae.Retryable() {
			d.set(ctx, id, store.PushFailed, err.Error())
			return 0, false
		}
	}
	if ctx.Err() != nil {
		return 0, false // stays queued; retried after restart
	}
	if attempt >= d.Retries {
		d.log.Warn("push failed", "notification", id, "err", err)
		d.set(ctx, id, store.PushFailed, err.Error())
		return 0, false
	}
	delay := d.Backoff << attempt
	if ae != nil {
		delay = max(delay, ae.RetryAfter)
	}
	return delay, true
}

func (d *Dispatcher) set(ctx context.Context, id, status, msg string) {
	if err := d.store.SetPushStatus(ctx, id, status, msg); err != nil {
		d.log.Error("set push status", "notification", id, "err", err)
	}
}

func (d *Dispatcher) isGateway() bool { _, ok := d.pusher.(*GatewayClient); return ok }

func (d *Dispatcher) message(ctx context.Context, t store.PushTarget) (Message, error) {
	if !d.isGateway() {
		return BuildMessage(t)
	}
	b, err := d.store.GatewayBinding(ctx, t.Device.ID)
	if err != nil {
		return Message{}, err
	}
	return relayMessage(t, b), nil
}
