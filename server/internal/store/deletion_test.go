package store

import (
	"fmt"
	"testing"
	"time"
)

func TestDeleteTransactionRollsBackCascadesAndHistory(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	q := ask(t, s, r, nil)
	if _, _, err := s.Respond(ctx, q.ID, r.Device.ID, "answer", "reply"); err != nil {
		t.Fatal(err)
	}
	_, err := s.w.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON notifications BEGIN SELECT RAISE(ABORT, 'test rollback'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteNotifications(ctx, r.Device.ID, NotificationDeletion{ID: q.NotificationID}); err == nil {
		t.Fatal("trigger did not abort transaction")
	}
	if n, err := s.Notification(ctx, q.NotificationID); err != nil || n.Question.Answer != "answer" {
		t.Fatalf("partial cascade %+v %v", n, err)
	}
	var responses int
	if err = s.r.QueryRow(`SELECT count(*) FROM responses WHERE question_id=?`, q.ID).Scan(&responses); err != nil || responses != 1 {
		t.Fatalf("response lost: %d %v", responses, err)
	}
	if _, err = s.w.Exec(`DROP TRIGGER reject_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteNotifications(ctx, r.Device.ID, NotificationDeletion{ID: q.NotificationID}); err != nil {
		t.Fatal(err)
	}
	if err = s.r.QueryRow(`SELECT count(*) FROM responses WHERE question_id=?`, q.ID).Scan(&responses); err != nil || responses != 0 {
		t.Fatalf("response remains: %d %v", responses, err)
	}
}

func TestDeleteGeneratedHalfOpenIntervals(t *testing.T) {
	s, _ := openTest(t)
	a := register(t, s)
	b := register(t, s)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for sample := 0; sample < 20; sample++ {
		ids := map[string]int{}
		for i := 0; i < 9; i++ {
			s.Now = func() time.Time { return base.Add(time.Duration(i) * time.Millisecond) }
			for _, reg := range []Registration{a, b} {
				n, _, err := s.CreateNotification(ctx, NewNotification{DeviceID: reg.Device.ID, KeyID: reg.Key.ID, Title: fmt.Sprint(sample, i), Type: "info", Priority: "normal"}, Idem{})
				if err != nil {
					t.Fatal(err)
				}
				if reg.Device.ID == a.Device.ID {
					ids[n.ID] = i
				}
			}
		}
		lo, hi := sample%8, sample%8+1+sample%(9-sample%8)
		result, err := s.DeleteNotifications(ctx, a.Device.ID, NotificationDeletion{From: base.Add(time.Duration(lo) * time.Millisecond), Before: base.Add(time.Duration(hi) * time.Millisecond)})
		if err != nil || result.Deleted != hi-lo {
			t.Fatalf("range %d %d: %+v %v", lo, hi, result, err)
		}
		for id, i := range ids {
			_, err := s.Notification(ctx, id)
			if (err == nil) != (i < lo || i >= hi) {
				t.Fatalf("boundary %d in [%d,%d): %v", i, lo, hi, err)
			}
		}
		foreign, err := s.Inbox(ctx, b.Device.ID, InboxQuery{Limit: 1000})
		if err != nil || len(foreign) != (sample+1)*9 {
			t.Fatal("scope isolation", len(foreign), err)
		}
		// Keep each generated selection independent, including surviving A rows.
		if _, err = s.DeleteNotifications(ctx, a.Device.ID, NotificationDeletion{From: base, Before: base.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteRejectsInvalidRangeAndClosedDatabase(t *testing.T) {
	s, _ := openTest(t)
	r := register(t, s)
	if _, err := s.DeleteNotifications(ctx, r.Device.ID, NotificationDeletion{From: time.UnixMilli(-1), Before: time.UnixMilli(2)}); err == nil {
		t.Fatal("negative time accepted")
	}
	if _, err := s.DeleteNotifications(ctx, r.Device.ID, NotificationDeletion{}); err == nil {
		t.Fatal("unbounded selection accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteNotifications(ctx, r.Device.ID, NotificationDeletion{ID: "ntf_test"}); err == nil {
		t.Fatal("closed database ignored")
	}
}
