package api

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"testing/quick"
	"time"
)

func TestLimiterHardCapacityAndRefill(t *testing.T) {
	now := time.Now()
	l := newLimiter(1, 2)
	l.now = func() time.Time { return now }
	for i := 0; i < maxBuckets; i++ {
		if ok, _ := l.allow(fmt.Sprint(i)); !ok {
			t.Fatal("initial capacity rejected")
		}
	}
	if ok, retry := l.allow("overflow"); ok || retry <= 0 {
		t.Fatalf("capacity must reject unknown key: allowed=%v retry=%s", ok, retry)
	}
	if len(l.buckets) != maxBuckets {
		t.Fatalf("capacity exceeded: %d", len(l.buckets))
	}
	if ok, _ := l.allow("0"); !ok {
		t.Fatal("capacity blocked an existing key with a token")
	}
	if ok, _ := l.allow("0"); ok {
		t.Fatal("capacity refilled depleted key")
	}
	now = now.Add(2 * time.Second)
	if ok, _ := l.allow("after-refill"); !ok {
		t.Fatal("fully refilled buckets were not reclaimed")
	}
	if len(l.buckets) > maxBuckets || len(l.buckets) < maxBuckets-63 {
		t.Fatalf("unbounded cleanup or map growth: %d", len(l.buckets))
	}
}

func TestLimiterGeneratedTokenAccounting(t *testing.T) {
	err := quick.Check(func(seed uint64) bool {
		rng := rand.New(rand.NewSource(int64(seed)))
		now := time.Now()
		l := newLimiter(2, 3)
		l.now = func() time.Time { return now }
		tokens := 3.0
		for i := 0; i < 200; i++ {
			advance := time.Duration(rng.Intn(1200)) * time.Millisecond
			now = now.Add(advance)
			tokens = min(3, tokens+advance.Seconds()*2)
			want := tokens >= 1
			if want {
				tokens--
			}
			got, retry := l.allow("same-key")
			if got != want || (!got && retry <= 0) {
				return false
			}
			if len(l.buckets) != 1 || len(l.expiry) != 1 {
				return false
			}
		}
		return true
	}, &quick.Config{MaxCount: 100, Rand: rand.New(rand.NewSource(20261029))})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLimiterCapacityUnderConcurrentNewKeys(t *testing.T) {
	l := newLimiter(1, 2)
	fixed := time.Now()
	l.now = func() time.Time { return fixed }
	for i := 0; i < maxBuckets-8; i++ {
		l.allow(fmt.Sprint(i))
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Go(func() { l.allow(fmt.Sprintf("new-%d", i)) })
	}
	wg.Wait()
	if len(l.buckets) != maxBuckets {
		t.Fatalf("concurrent admission exceeded capacity: %d", len(l.buckets))
	}
}
