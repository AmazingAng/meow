package api_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"meow.blue/meow/client"
)

func seedLegacyHistory(t *testing.T, e *env) string {
	t.Helper()
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UnixMilli()
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO questions(id,device_id,key_id,status,answer,created_at,expires_at,resolved_at) VALUES('qst_legacy',?,?,'answered','old answer',?,?,?)`, []any{e.reg.Device.ID, e.reg.Key.ID, now, now + 60000, now}},
		{`INSERT INTO notifications(id,device_id,key_id,type,title,body,source,priority,question_id,created_at) VALUES('ntf_legacy',?,?,'question','Legacy question','History stays','Codex','normal','qst_legacy',?)`, []any{e.reg.Device.ID, e.reg.Key.ID, now}},
		{`UPDATE api_keys SET scopes='notify,ask' WHERE id=?`, []any{e.reg.Key.ID}},
	} {
		if _, err = db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	return "qst_legacy"
}

func TestLaunchSenderKeysAreNotifyOnly(t *testing.T) {
	e := setup(t)
	if len(e.reg.Key.Scopes) != 1 || e.reg.Key.Scopes[0] != "notify" {
		t.Fatalf("registration grants legacy scopes: %v", e.reg.Key.Scopes)
	}
	key, err := e.phone.CreateKey(ctx, "new", nil)
	if err != nil || len(key.Scopes) != 1 || key.Scopes[0] != "notify" {
		t.Fatalf("default key: %+v %v", key, err)
	}
	for _, scopes := range [][]string{{"ask"}, {"notify", "ask"}} {
		_, err = e.phone.CreateKey(ctx, "retired", scopes)
		apiErr(t, err, 422, "invalid_request")
	}
}

func TestLaunchLegacyHistoryRequiresOwningPhone(t *testing.T) {
	e := setup(t)
	id := seedLegacyHistory(t, e)
	who, err := e.agent.WhoAmI(ctx)
	if err != nil || len(who.Key.Scopes) != 1 || who.Key.Scopes[0] != "notify" {
		t.Fatalf("old ask grant still advertised: %+v %v", who, err)
	}
	_, err = e.agent.Question(ctx, id)
	apiErr(t, err, 403, "forbidden")
	q, err := e.phone.Question(ctx, id)
	if err != nil || q.Answer == nil || *q.Answer != "old answer" {
		t.Fatalf("legacy history lost: %+v %v", q, err)
	}
	other, err := client.New(e.url, "").Register(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.New(e.url, other.DeviceToken).Question(ctx, id)
	apiErr(t, err, 404, "not_found")
	if _, err = e.agent.Notify(ctx, client.NotifyRequest{Title: "old key still sends"}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchQuestionExecutionRetiredWithoutSideEffects(t *testing.T) {
	e := setup(t)
	id := seedLegacyHistory(t, e)
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/v1/questions"}, {"GET", "/v1/questions/" + id + "/wait"},
		{"POST", "/v1/questions/" + id + "/cancel"}, {"POST", "/v1/questions/" + id + "/responses"},
	} {
		for _, token := range []string{e.reg.Key.Secret, e.reg.DeviceToken, ""} {
			r, err := http.NewRequest(endpoint.method, e.url+endpoint.path, strings.NewReader(`{"question":"obsolete","answer":"approve"}`))
			if err != nil {
				t.Fatal(err)
			}
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			var body struct{ Error struct{ Code string } }
			err = json.NewDecoder(response.Body).Decode(&body)
			response.Body.Close()
			if err != nil || response.StatusCode != 410 || body.Error.Code != "feature_retired" {
				t.Fatalf("%s %s: status=%d code=%s err=%v", endpoint.method, endpoint.path, response.StatusCode, body.Error.Code, err)
			}
		}
	}
	items, _, err := e.phone.Inbox(ctx, "", 50)
	if err != nil || len(items) != 1 || items[0].Question == nil || items[0].Question.Answer == nil || *items[0].Question.Answer != "old answer" || len(e.push.ids) != 0 {
		t.Fatalf("retired request changed history/queued push: %+v %v", items, err)
	}
}

func TestLaunchAskOnlyLegacyKeyDoesNotGainNotify(t *testing.T) {
	e := setup(t)
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE api_keys SET scopes='ask' WHERE id=?`, e.reg.Key.ID); err != nil {
		t.Fatal(err)
	}
	who, err := e.agent.WhoAmI(ctx)
	if err != nil || len(who.Key.Scopes) != 0 {
		t.Fatalf("retired grant still effective: %+v %v", who, err)
	}
	_, err = e.agent.Notify(ctx, client.NotifyRequest{Title: "must not send"}, "")
	apiErr(t, err, 403, "forbidden")
}

func seedPendingHistory(t *testing.T, e *env) {
	t.Helper()
	seedLegacyHistory(t, e)
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE questions SET status='pending',answer=NULL,resolved_at=NULL WHERE id='qst_legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE notifications SET source='' WHERE id='ntf_legacy'`); err != nil {
		t.Fatal(err)
	}
}
