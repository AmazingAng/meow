package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestCacheConfiguration(t *testing.T) {
	for _, tt := range []struct {
		input   string
		want    time.Duration
		invalid bool
	}{{"", 720 * time.Hour, false}, {"12h", 12 * time.Hour, false}, {"720h", 720 * time.Hour, false}, {"24h", 24 * time.Hour, false}, {"1h", time.Hour, false}, {"0", 0, true}, {"-1h", 0, true}, {"30m", 0, true}, {"bad", 0, true}} {
		t.Run(tt.input, func(t *testing.T) {
			t.Setenv("MEOW_RETENTION", tt.input)
			got, err := retentionFromEnv()
			if tt.invalid {
				if err == nil {
					t.Fatal("invalid retention accepted")
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("got %v, %v want %v", got, err, tt.want)
			}
		})
	}
}

type maintenanceState struct {
	prunes, devices, expires     int
	fail, deviceFail, expiryFail bool
	retention, idle              time.Duration
}

type fakeCacheMaintenance struct {
	mu    sync.Mutex
	state maintenanceState
}

func (s *fakeCacheMaintenance) snapshot() maintenanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}
func (s *fakeCacheMaintenance) setFail(value bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.fail = value
}
func (s *fakeCacheMaintenance) Prune(_ context.Context, d time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.prunes++
	s.state.retention = d
	if s.state.fail {
		return 0, errors.New("injected cleanup failure")
	}
	return 1, nil
}
func (s *fakeCacheMaintenance) PruneInactiveDevices(_ context.Context, d time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.devices++
	s.state.idle = d
	if s.state.deviceFail {
		return 0, errors.New("device cleanup unavailable")
	}
	return 1, nil
}
func (s *fakeCacheMaintenance) ExpireDue(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.expires++
	if s.state.expiryFail {
		return errors.New("expiry unavailable")
	}
	return nil
}

type lockedLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.Buffer.Write(p)
}
func (l *lockedLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.Buffer.String() }

func TestCacheMaintenanceStartsImmediatelyRetriesAndCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st := &fakeCacheMaintenance{state: maintenanceState{fail: true}}
		var output lockedLog
		log := slog.New(slog.NewTextHandler(&output, nil))
		done := make(chan struct{})
		go func() { maintain(ctx, st, st, 12*time.Hour, 0, log); close(done) }()
		synctest.Wait()
		if st.snapshot().prunes != 1 {
			t.Fatalf("startup cleanup count=%d", st.snapshot().prunes)
		}
		if !strings.Contains(output.String(), "injected cleanup failure") {
			t.Fatal("cleanup failure not logged")
		}
		st.setFail(false)
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if state := st.snapshot(); state.prunes != 2 || state.devices != 0 || state.expires == 0 || state.retention != 12*time.Hour {
			t.Fatalf("maintenance %+v", st.snapshot())
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("maintenance did not stop")
		}
	})
}

func TestCacheMaintenanceOptionalDeviceCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st := &fakeCacheMaintenance{}
		go maintain(ctx, st, st, 12*time.Hour, 90*24*time.Hour, slog.New(slog.NewTextHandler(&lockedLog{}, nil)))
		synctest.Wait()
		if state := st.snapshot(); state.devices != 1 || state.idle != 90*24*time.Hour {
			t.Fatalf("device cleanup %+v", st.snapshot())
		}
		cancel()
		synctest.Wait()
	})
}

func TestCacheMaintenanceLogsExpiryAndDeviceFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st := &fakeCacheMaintenance{state: maintenanceState{deviceFail: true, expiryFail: true}}
		var output lockedLog
		go maintain(ctx, st, st, 12*time.Hour, 24*time.Hour, slog.New(slog.NewTextHandler(&output, nil)))
		synctest.Wait()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if !strings.Contains(output.String(), "device cleanup unavailable") || !strings.Contains(output.String(), "expiry unavailable") {
			t.Fatal(output.String())
		}
		cancel()
		synctest.Wait()
	})
}
