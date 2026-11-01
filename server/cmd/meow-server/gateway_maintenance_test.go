package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type gatewayPruner struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (p *gatewayPruner) PruneGatewayRoutes(context.Context) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return 0, errors.New("gateway cleanup unavailable")
	}
	return 1, nil
}

func TestGatewayMaintenanceStartsRetriesLogsAndStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st := &gatewayPruner{fail: true}
		var output lockedLog
		done := make(chan struct{})
		go func() { maintainGateway(ctx, st, slog.New(slog.NewTextHandler(&output, nil))); close(done) }()
		synctest.Wait()
		st.mu.Lock()
		calls := st.calls
		st.fail = false
		st.mu.Unlock()
		if calls != 1 || !strings.Contains(output.String(), "gateway cleanup unavailable") {
			t.Fatalf("startup cleanup missing: %d %s", calls, output.String())
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		st.mu.Lock()
		calls = st.calls
		st.mu.Unlock()
		if calls != 2 || !strings.Contains(output.String(), "pruned inactive gateway routes") {
			t.Fatalf("scheduled cleanup missing: %d %s", calls, output.String())
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("gateway maintenance did not stop")
		}
	})
}
