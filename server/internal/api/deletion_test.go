package api_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"meow.blue/meow/client"
	"meow.blue/meow/server/internal/store"
)

func deleteRequest(t *testing.T, e *env, token, path string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest("DELETE", e.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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
		t.Fatalf("DELETE %s: %d %s; want %d", path, res.StatusCode, data, want)
	}
	return data
}
func deletionMessage(t *testing.T, e *env, title, idem string) *client.Notification {
	t.Helper()
	n, err := e.agent.Notify(ctx, client.NotifyRequest{Title: title}, idem)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func TestDeleteNotificationAuthorizationAndPersistence(t *testing.T) {
	e := setup(t)
	n := deletionMessage(t, e, "Delete me", "create-1")
	path := "/v1/notifications/" + n.ID
	deleteRequest(t, e, "", path, 401)
	deleteRequest(t, e, e.reg.Key.Secret, path, 403)
	other, err := client.New(e.url, "").Register(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest(t, e, other.DeviceToken, path, 404)
	if _, err = e.st.Notification(ctx, n.ID); err != nil {
		t.Fatal("foreign delete erased record", err)
	}
	deleteRequest(t, e, e.reg.DeviceToken, path, 204)
	deleteRequest(t, e, e.reg.DeviceToken, path, 404)
	if _, err = e.st.Notification(ctx, n.ID); err == nil {
		t.Fatal("deleted detail still accessible")
	}
	page, _, err := e.phone.Inbox(ctx, "", 50)
	if err != nil || len(page) != 0 {
		t.Fatalf("inbox %+v %v", page, err)
	}
	if _, err = e.st.PushTarget(ctx, n.ID); err == nil {
		t.Fatal("deleted notification still pushable")
	}
	if _, err = e.agent.Notify(ctx, client.NotifyRequest{Title: "Delete me"}, "create-1"); err == nil {
		t.Fatal("creation replay resurrected deletion")
	}
	page, _, err = e.phone.Inbox(ctx, "", 50)
	if err != nil || len(page) != 0 {
		t.Fatal("replay restored history", err)
	}
}
func TestDeleteInboxBoundsAndIsolation(t *testing.T) {
	e := setup(t)
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	own := []*client.Notification{}
	for i := 0; i < 5; i++ {
		e.st.Now = func() time.Time { return base.Add(time.Duration(i) * time.Millisecond) }
		own = append(own, deletionMessage(t, e, fmt.Sprint(i), ""))
	}
	other, err := e.st.RegisterDevice(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := e.st.CreateNotification(ctx, store.NewNotification{DeviceID: other.Device.ID, KeyID: other.Key.ID, Title: "foreign", Type: "info", Priority: "normal"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/v1/inbox?from=%d&before=%d", base.UnixMilli()+1, base.UnixMilli()+4)
	deleteRequest(t, e, e.reg.Key.Secret, path, 403)
	data := deleteRequest(t, e, e.reg.DeviceToken, path, 200)
	var result struct{ Deleted int }
	if err := json.Unmarshal(data, &result); err != nil || result.Deleted != 3 {
		t.Fatalf("result %s %v", data, err)
	}
	page, _, err := e.phone.Inbox(ctx, "", 50)
	if err != nil || len(page) != 2 {
		t.Fatalf("remaining %+v %v", page, err)
	}
	if page[0].ID != own[4].ID || page[1].ID != own[0].ID {
		t.Fatalf("wrong endpoints %+v", page)
	}
	if _, err = e.st.Notification(ctx, foreign.ID); err != nil {
		t.Fatal("foreign record erased", err)
	}
	data = deleteRequest(t, e, e.reg.DeviceToken, path, 200)
	if !strings.Contains(string(data), `"deleted":0`) {
		t.Fatal(string(data))
	}
}
func TestDeleteInboxRejectsUnsafeIntervals(t *testing.T) {
	e := setup(t)
	n := deletionMessage(t, e, "keep", "")
	for _, q := range []string{"", "from=0", "before=1", "from=&before=1", "from=abc&before=2", "from=-1&before=2", "from=2&before=2", "from=3&before=2", "from=0&before=9223372036854775808", "from=0&before=253402300800000", "from=0&before=1&filter=all", "from=0&from=1&before=2", "from=0&before=1.5", "from=0&before=1&before=2", "from=0;before=1", "from=0%00&before=1", "from=0&before=1%ZZ"} {
		t.Run(url.QueryEscape(q), func(t *testing.T) { deleteRequest(t, e, e.reg.DeviceToken, "/v1/inbox?"+q, 400) })
	}
	if _, err := e.st.Notification(ctx, n.ID); err != nil {
		t.Fatal("invalid request deleted data", err)
	}
}
func TestDeleteQuestionWakesWaiterAndRemovesResponse(t *testing.T) {
	e := setup(t)
	q, err := e.agent.Ask(ctx, client.AskRequest{Question: "Approval?", Choices: []string{"approve", "reject"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.agent.Wait(ctx, q.ID, nil); done <- err }()
	time.Sleep(100 * time.Millisecond)
	deleteRequest(t, e, e.reg.DeviceToken, "/v1/notifications/"+q.NotificationID, 204)
	select {
	case err := <-done:
		apiErr(t, err, 404, "not_found")
	case <-time.After(2 * time.Second):
		t.Fatal("delete did not wake waiter")
	}
	if _, err = e.phone.Respond(ctx, q.ID, "approve", "late"); err == nil {
		t.Fatal("deleted question approved")
	}
}

func TestDeleteDatabaseFailureRollsBackAndReturns500(t *testing.T) {
	e := setup(t)
	n := deletionMessage(t, e, "keep", "")
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON notifications BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	deleteRequest(t, e, e.reg.DeviceToken, "/v1/notifications/"+n.ID, 500)
	if _, err = e.st.Notification(ctx, n.ID); err != nil {
		t.Fatal("failed delete removed message", err)
	}
	if _, err = db.Exec(`DROP TRIGGER fail_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE notifications RENAME TO unavailable_notifications`); err != nil {
		t.Fatal(err)
	}
	deleteRequest(t, e, e.reg.DeviceToken, "/v1/notifications/"+n.ID, 500)
	if _, err = db.Exec(`ALTER TABLE unavailable_notifications RENAME TO notifications`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.st.Notification(ctx, n.ID); err != nil {
		t.Fatal("failed query removed message", err)
	}
}
