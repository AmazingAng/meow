package store

import (
	"fmt"
	"testing"
)

func TestSelectedReadGeneratedSelectionAndReplay(t *testing.T) {
	s, _ := openTest(t)
	owner, other := register(t, s), register(t, s)
	var ids []string
	read := map[string]bool{}
	for i := 0; i < 80; i++ {
		n, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: owner.Device.ID, KeyID: owner.Key.ID,
			Type: "info", Title: fmt.Sprint(i), Priority: "normal"}, Idem{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, n.ID)
	}
	foreign, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: other.Device.ID, KeyID: other.Key.ID,
		Type: "info", Title: "foreign", Priority: "normal"}, Idem{})
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 20; round++ {
		batch := []string{foreign.ID, "ntf_missing"}
		var want int64
		for i, id := range ids {
			if (i+round)%7 != 0 {
				continue
			}
			batch = append(batch, id, id)
			if !read[id] {
				want++
				read[id] = true
			}
		}
		got, err := s.MarkSelectedRead(ctx, owner.Device.ID, batch)
		if err != nil || got != want {
			t.Fatalf("round %d: got %d want %d: %v", round, got, want, err)
		}
		replay, err := s.MarkSelectedRead(ctx, owner.Device.ID, batch)
		if err != nil || replay != 0 {
			t.Fatalf("replay round %d: %d %v", round, replay, err)
		}
		for _, id := range ids {
			n, err := s.Notification(ctx, id)
			if err != nil || (n.ReadAt != nil) != read[id] {
				t.Fatalf("unselected row changed: %s %v", id, err)
			}
		}
		n, err := s.Notification(ctx, foreign.ID)
		if err != nil || n.ReadAt != nil {
			t.Fatalf("foreign row changed: %v", err)
		}
	}
}

func TestSelectedReadRejectsInvalidSizeAndClosedDatabase(t *testing.T) {
	s, _ := openTest(t)
	for _, ids := range [][]string{nil, make([]string, MaxReadBatch+1)} {
		if _, err := s.MarkSelectedRead(ctx, "device", ids); err == nil {
			t.Fatal("invalid batch accepted")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSelectedRead(ctx, "device", []string{"ntf_missing"}); err == nil {
		t.Fatal("closed database accepted")
	}
}
