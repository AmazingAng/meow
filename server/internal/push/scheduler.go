package push

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

type pushJob struct {
	id      string
	attempt int
	readyAt time.Time
}

type pushResult struct {
	job   pushJob
	retry bool
}

// One scheduler owns delayed jobs and one timer. Worker slots cover network
// attempts only; backoff never holds a worker or creates a sleeping goroutine.
func (d *Dispatcher) runScheduled(ctx context.Context, workers int) {
	jobs := make(chan pushJob)
	results := make(chan pushResult, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-jobs:
					delay, retry := d.attempt(ctx, job.id, job.attempt)
					job.attempt++
					job.readyAt = time.Now().Add(delay)
					select {
					case results <- pushResult{job: job, retry: retry}:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	defer wg.Wait()
	d.sweep(ctx, 0)
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	var pending pushJobs
	for {
		timer.Stop()
		var send chan pushJob
		var wake <-chan time.Time
		var next pushJob
		if len(pending) > 0 {
			next = pending[0]
			if delay := time.Until(next.readyAt); delay > 0 {
				timer.Reset(delay)
				wake = timer.C
			} else {
				send = jobs
			}
		}
		select {
		case <-ctx.Done():
			return
		case id := <-d.queue:
			heap.Push(&pending, pushJob{id: id, readyAt: time.Now()})
		case send <- next:
			heap.Pop(&pending)
		case result := <-results:
			if result.retry {
				heap.Push(&pending, result.job)
			} else {
				d.done(result.job.id)
			}
		case <-wake:
		case <-sweep.C:
			d.sweep(ctx, 30*time.Second)
		}
	}
}

type pushJobs []pushJob

func (h pushJobs) Len() int { return len(h) }
func (h pushJobs) Less(i, j int) bool {
	if h[i].readyAt.Equal(h[j].readyAt) {
		return h[i].id < h[j].id
	}
	return h[i].readyAt.Before(h[j].readyAt)
}
func (h pushJobs) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *pushJobs) Push(x any)   { *h = append(*h, x.(pushJob)) }
func (h *pushJobs) Pop() any {
	old := *h
	job := old[len(old)-1]
	old[len(old)-1] = pushJob{}
	*h = old[:len(old)-1]
	return job
}
