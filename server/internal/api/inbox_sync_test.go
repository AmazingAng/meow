package api_test

import (
	"encoding/base64"
	"encoding/json"
	"meow.blue/meow/client"
	"meow.blue/meow/server/internal/api"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type syncPage struct {
	Notifications []client.Notification `json:"notifications"`
	Cursor        string                `json:"sync_cursor"`
	More          bool                  `json:"has_more"`
}

func TestInboxSyncRejectsInvalidWindowsAndFutureCursor(t *testing.T) {
	e := setup(t)
	page, _ := syncRequest(t, e, "", 200)
	raw, err := base64.RawURLEncoding.DecodeString(page.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	var cursor map[string]any
	if err = json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	cursor["after"] = 9999
	raw, _ = json.Marshal(cursor)
	reset, status := syncRequest(t, e, base64.RawURLEncoding.EncodeToString(raw), 200)
	if status != 200 || len(reset.Notifications) != 0 {
		t.Fatalf("future cursor: %+v %d", reset, status)
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=bad", "filter=unread", "q=secret", "cursor=old", "sync_cursor=" + strings.Repeat("a", 2049)} {
		r, err := http.NewRequest("GET", e.url+"/v1/inbox?sync=1&"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+e.reg.DeviceToken)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("%s accepted: %d", query, response.StatusCode)
		}
	}
}

func syncRequest(t *testing.T, e *env, cursor string, limit int) (syncPage, int) {
	t.Helper()
	q := url.Values{"sync": {"1"}, "limit": {"1"}}
	if limit > 1 {
		q.Set("limit", "200")
	}
	if cursor != "" {
		q.Set("sync_cursor", cursor)
	}
	r, err := http.NewRequest("GET", e.url+"/v1/inbox?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+e.reg.DeviceToken)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var page syncPage
	if err = json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	return page, response.StatusCode
}
func sendSyncItem(t *testing.T, e *env, title string) string {
	t.Helper()
	n, err := e.agent.Notify(ctx, client.NotifyRequest{Title: title}, "")
	if err != nil {
		t.Fatal(err)
	}
	return n.ID
}
func TestInboxSyncPagesAndUpdates(t *testing.T) {
	e := setup(t)
	first := sendSyncItem(t, e, "one")
	second := sendSyncItem(t, e, "two")
	page, status := syncRequest(t, e, "", 1)
	if status != 200 || page.Cursor == "" || !page.More || len(page.Notifications) != 1 || page.Notifications[0].ID != first {
		t.Fatalf("first page=%+v status=%d", page, status)
	}
	page, status = syncRequest(t, e, page.Cursor, 1)
	if status != 200 || page.More || len(page.Notifications) != 1 || page.Notifications[0].ID != second {
		t.Fatalf("second=%+v %d", page, status)
	}
	cursor := page.Cursor
	page, status = syncRequest(t, e, cursor, 200)
	if status != 200 || len(page.Notifications) != 0 || page.More {
		t.Fatalf("unchanged=%+v %d", page, status)
	}
	if err := e.st.SetPushStatus(ctx, first, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	page, status = syncRequest(t, e, cursor, 200)
	if status != 200 || len(page.Notifications) != 1 || page.Notifications[0].ID != first || page.Notifications[0].PushStatus != "accepted" {
		t.Fatalf("update=%+v %d", page, status)
	}
}
func TestInboxSyncSnapshotAndDeviceFence(t *testing.T) {
	e := setup(t)
	first := sendSyncItem(t, e, "one")
	second := sendSyncItem(t, e, "two")
	page, _ := syncRequest(t, e, "", 1)
	cursor := page.Cursor
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	third := sendSyncItem(t, e, "during sync")
	if err := e.st.SetPushStatus(ctx, second, "accepted", ""); err != nil {
		t.Fatal(err)
	}
	page, status := syncRequest(t, e, cursor, 200)
	if status != 200 || page.More || len(page.Notifications) != 0 {
		t.Fatalf("ceiling advanced: %+v %d", page, status)
	}
	page, status = syncRequest(t, e, page.Cursor, 200)
	if status != 200 || len(page.Notifications) != 2 {
		t.Fatalf("missed moving/new rows: %+v %d", page, status)
	}
	ids := map[string]bool{}
	for _, n := range page.Notifications {
		ids[n.ID] = true
	}
	if !ids[second] || !ids[third] || ids[first] {
		t.Fatal(ids)
	}
	other := setup(t)
	_, status = syncRequest(t, other, page.Cursor, 200)
	if status != 400 {
		t.Fatalf("foreign device cursor accepted: %d", status)
	}
}
func TestInboxSyncRestartBackfillsWithoutReusingCheckpoint(t *testing.T) {
	e := setup(t)
	id := sendSyncItem(t, e, "keep")
	page, _ := syncRequest(t, e, "", 200)
	if page.Cursor == "" {
		t.Fatal("missing cursor")
	}
	restarted := httptest.NewServer(api.New(api.Config{Store: e.st, Push: e.push}))
	defer restarted.Close()
	e.url = restarted.URL
	next, status := syncRequest(t, e, page.Cursor, 200)
	if status != 200 || len(next.Notifications) != 1 || next.Notifications[0].ID != id || next.Cursor == page.Cursor {
		t.Fatalf("restart=%+v %d", next, status)
	}
}
func TestInboxSyncMalformedCursorAndLegacyPagination(t *testing.T) {
	e := setup(t)
	sendSyncItem(t, e, "legacy")
	for _, c := range []string{"!", "e30", "null", "eyJzZXEiOi0xfQ"} {
		_, status := syncRequest(t, e, c, 200)
		if status != 400 {
			t.Fatalf("malformed %q accepted: %d", c, status)
		}
	}
	items, _, err := e.phone.Inbox(ctx, "", 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("legacy inbox changed: %v %v", items, err)
	}
}
