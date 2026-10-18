package api

import "sync"

// broker wakes long-polling waiters when a question changes state.
// Wake-ups carry no data; waiters re-read the question from the store.
type broker struct {
	mu        sync.Mutex
	subs      map[string]map[chan struct{}]struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newBroker() *broker {
	return &broker{subs: map[string]map[chan struct{}]struct{}{}, done: make(chan struct{})}
}

func (b *broker) subscribe(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs[id] == nil {
		b.subs[id] = map[chan struct{}]struct{}{}
	}
	b.subs[id][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[id], ch)
		if len(b.subs[id]) == 0 {
			delete(b.subs, id)
		}
		b.mu.Unlock()
	}
}

func (b *broker) publish(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[id] {
		select {
		case ch <- struct{}{}:
		default: // a wake-up is already pending
		}
	}
}

func (b *broker) close() { b.closeOnce.Do(func() { close(b.done) }) }
