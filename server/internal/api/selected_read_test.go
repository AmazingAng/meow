package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"meow.blue/meow/server/internal/store"
)

func selectedReadRequest(t *testing.T, e *env, token string, body string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest("POST", e.url+"/v1/inbox/read-selected", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("selected read status=%d want=%d body=%s", res.StatusCode, want, data)
	}
	return data
}

func TestSelectedReadAuthorizationIsolationAndReplay(t *testing.T) {
	e := setup(t)
	one := deletionMessage(t, e, "selected", "")
	two := deletionMessage(t, e, "not selected", "")
	other, err := e.st.RegisterDevice(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := e.st.CreateNotification(ctx, store.NewNotification{DeviceID: other.Device.ID, KeyID: other.Key.ID, Type: "info", Title: "foreign", Priority: "normal"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"ids": []string{one.ID, one.ID, foreign.ID, "ntf_missing"}})
	selectedReadRequest(t, e, "", string(body), 401)
	selectedReadRequest(t, e, e.reg.Key.Secret, string(body), 403)
	got := selectedReadRequest(t, e, e.reg.DeviceToken, string(body), 200)
	if !bytes.Contains(got, []byte(`"marked":1`)) {
		t.Fatalf("unexpected count: %s", got)
	}
	first, err := e.st.Notification(ctx, one.ID)
	if err != nil || first.ReadAt == nil {
		t.Fatalf("selected not read: %v", err)
	}
	for _, id := range []string{two.ID, foreign.ID} {
		n, err := e.st.Notification(ctx, id)
		if err != nil || n.ReadAt != nil {
			t.Fatalf("unselected/foreign changed: %s %v", id, err)
		}
	}
	got = selectedReadRequest(t, e, e.reg.DeviceToken, string(body), 200)
	if !bytes.Contains(got, []byte(`"marked":0`)) {
		t.Fatalf("replay not idempotent: %s", got)
	}
	again, _ := e.st.Notification(ctx, one.ID)
	if !again.ReadAt.Equal(*first.ReadAt) {
		t.Fatal("replay changed read timestamp")
	}
}

func TestSelectedReadRejectsInvalidBatch(t *testing.T) {
	for _, body := range []string{`{}`, `{"ids":[]}`, `{"ids":["invalid"]}`, `{"ids":[123]}`, `not json`, `{"ids":["ntf_ok"],"unexpected":true}`} {
		t.Run(body, func(t *testing.T) {
			e := setup(t)
			want := 400
			if body == `{}` || body == `{"ids":[]}` || body == `{"ids":["invalid"]}` {
				want = 422
			}
			selectedReadRequest(t, e, e.reg.DeviceToken, body, want)
		})
	}
	e := setup(t)
	ids := make([]string, 201)
	for i := range ids {
		ids[i] = "ntf_same"
	}
	body, _ := json.Marshal(map[string]any{"ids": ids})
	selectedReadRequest(t, e, e.reg.DeviceToken, string(body), 422)
}

func TestSelectedReadDatabaseFailureDoesNotPartiallyMark(t *testing.T) {
	e := setup(t)
	one := deletionMessage(t, e, "one", "")
	two := deletionMessage(t, e, "two", "")
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER fail_selected BEFORE UPDATE OF read_at ON notifications BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"ids": []string{one.ID, two.ID}})
	selectedReadRequest(t, e, e.reg.DeviceToken, string(body), 500)
	for _, id := range []string{one.ID, two.ID} {
		n, err := e.st.Notification(ctx, id)
		if err != nil || n.ReadAt != nil {
			t.Fatalf("failed batch changed %s: %v", id, err)
		}
	}
}
