package api_test

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	"meow.blue/meow/client"
)

// Lose only the first successful response, after the real API committed it.
// The API, SQLite store and HTTP client remain real.
type loseAcknowledgment struct {
	mu   sync.Mutex
	lost bool
}

func (l *loseAcknowledgment) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return res, err
	}
	l.mu.Lock()
	lose := !l.lost && res.StatusCode >= 200 && res.StatusCode < 300
	if lose {
		l.lost = true
	}
	l.mu.Unlock()
	if !lose {
		return res, nil
	}
	_, err = io.Copy(io.Discard, res.Body)
	closeErr := res.Body.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return nil, io.ErrUnexpectedEOF
}

func TestReplyCommittedWithLostAcknowledgmentIsReplayedExactlyOnce(t *testing.T) {
	e := setup(t)
	q, err := e.agent.Ask(ctx, client.AskRequest{Question: "Fault injection only", Choices: []string{"approve", "reject"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	e.phone.HTTP = &http.Client{Transport: &loseAcknowledgment{}}
	if _, err = e.phone.Respond(ctx, q.ID, "approve", "lost-ack-fixed-key"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("first attempt must lose the acknowledgment: %v", err)
	}
	committed, err := e.agent.Question(ctx, q.ID)
	if err != nil || committed.Status != "answered" || committed.Answer == nil || *committed.Answer != "approve" {
		t.Fatalf("the first response must already be committed: %+v, %v", committed, err)
	}
	replayed, err := e.phone.Respond(ctx, q.ID, "approve", "lost-ack-fixed-key")
	if err != nil || replayed.ResolvedAt == nil || committed.ResolvedAt == nil || !replayed.ResolvedAt.Equal(*committed.ResolvedAt) {
		t.Fatalf("retry must return the original committed result: %+v, %v", replayed, err)
	}
	_, err = e.phone.Respond(ctx, q.ID, "reject", "different-key")
	apiErr(t, err, 409, "already_resolved")
	db, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM responses WHERE question_id = ?", q.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("persisted response count=%d, error=%v", count, err)
	}
}
