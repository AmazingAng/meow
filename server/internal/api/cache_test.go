package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"meow.blue/meow/client"
)

func TestCacheExpiryHTTPKeepsQuotaAndNeverRequeuesReplay(t *testing.T) {
	e := setup(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	e.st.Now = func() time.Time { return base }
	e.st.Limits.DailyMessages = 1
	body := client.NotifyRequest{Title: "cached content", Body: "**完整正文** 😺"}
	n, err := e.agent.Notify(ctx, body, "cache-event")
	if err != nil {
		t.Fatal(err)
	}
	e.st.Now = func() time.Time { return base.Add(13 * time.Hour) }
	if _, err = e.st.Prune(ctx, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	page, _, err := e.phone.Inbox(ctx, "", 50)
	if err != nil || len(page) != 0 {
		t.Fatalf("expired inbox: %v %v", page, err)
	}
	req, _ := http.NewRequest("GET", e.url+"/v1/notifications/"+n.ID, nil)
	req.Header.Set("Authorization", "Bearer "+e.reg.DeviceToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("expired detail status=%d", res.StatusCode)
	}
	_, err = e.agent.Notify(ctx, body, "cache-event")
	apiErr(t, err, 404, "not_found")
	_, err = e.agent.Notify(ctx, client.NotifyRequest{Title: "different"}, "cache-event")
	apiErr(t, err, 422, "idempotency_mismatch")
	_, err = e.agent.Notify(ctx, client.NotifyRequest{Title: "new event"}, "other-event")
	apiErr(t, err, 429, "quota_exceeded")
	e.push.mu.Lock()
	count := len(e.push.ids)
	e.push.mu.Unlock()
	if count != 1 {
		t.Fatalf("expiry/replay queued %d pushes want 1", count)
	}
}

func TestCacheExpiredReadAndDeleteReturnResourceNotFound(t *testing.T) {
	e := setup(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	e.st.Now = func() time.Time { return base }
	n, err := e.agent.Notify(ctx, client.NotifyRequest{Title: "will expire"}, "")
	if err != nil {
		t.Fatal(err)
	}
	e.st.Now = func() time.Time { return base.Add(13 * time.Hour) }
	if _, err = e.st.Prune(ctx, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "DELETE"} {
		path := "/v1/notifications/" + n.ID
		if method == "POST" {
			path += "/read"
		}
		req, _ := http.NewRequest(method, e.url+path, nil)
		req.Header.Set("Authorization", "Bearer "+e.reg.DeviceToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		decodeErr := json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if decodeErr != nil || res.StatusCode != 404 || body.Error.Code != "not_found" {
			t.Fatalf("%s status=%d body=%+v err=%v", method, res.StatusCode, body, decodeErr)
		}
	}
}
