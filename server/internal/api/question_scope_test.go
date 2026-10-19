package api_test

import (
	"io"
	"meow.blue/meow/client"
	"net/http"
	"strings"
	"testing"
)

func TestQuestionReadRequiresAskScope(t *testing.T) {
	e := setup(t)
	notify, err := e.phone.CreateKey(ctx, "notify only", []string{"notify"})
	if err != nil {
		t.Fatal(err)
	}
	ask, err := e.phone.CreateKey(ctx, "ask only", []string{"ask"})
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.agent.Ask(ctx, client.AskRequest{Question: "Private question"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "answered"} {
		t.Run(state, func(t *testing.T) {
			if state == "answered" {
				if _, err := e.phone.Respond(ctx, q.ID, "private answer", "answer"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := client.New(e.url, notify.Secret).Question(ctx, q.ID)
			apiErr(t, err, 403, "forbidden")

			req, err := http.NewRequest("GET", e.url+"/v1/questions/"+q.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+notify.Secret)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != 403 || strings.Contains(string(body), "Private question") || strings.Contains(string(body), "private answer") || strings.Contains(string(body), q.ID) {
				t.Fatalf("denied response leaked question: %s", body)
			}

			for _, c := range []*client.Client{e.agent, e.phone, client.New(e.url, ask.Secret)} {
				got, err := c.Question(ctx, q.ID)
				if err != nil || got.Status != state {
					t.Fatalf("authorized read = %+v, %v", got, err)
				}
				if state == "answered" && (got.Answer == nil || *got.Answer != "private answer") {
					t.Fatalf("answer = %+v", got)
				}
			}
		})
	}
	for _, token := range []string{"", "meow_invalid"} {
		_, err := client.New(e.url, token).Question(ctx, q.ID)
		apiErr(t, err, 401, "unauthorized")
	}
	if err := e.phone.RevokeKey(ctx, ask.ID); err != nil {
		t.Fatal(err)
	}
	_, err = client.New(e.url, ask.Secret).Question(ctx, q.ID)
	apiErr(t, err, 401, "unauthorized")
}
