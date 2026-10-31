package store

// Old-format writers are fixtures for historical-state/backup/retention tests.
// They are not compiled into the server and have no HTTP entry points.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"meow.blue/meow/server/internal/ids"
	"strings"
	"time"
)

const ScopeAsk = "ask"

type NewQuestion struct {
	NewNotification // Title holds the question text
	Choices         []string
	TTL             time.Duration
}

func (s *Store) CreateQuestion(ctx context.Context, nq NewQuestion, idem Idem) (q Question, replay bool, err error) {
	now := s.now()
	var existing string
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if existing, err = lookupIdem(ctx, tx, nq.KeyID, idem, "question"); err != nil || existing != "" {
			return err
		}
		if err := s.checkDaily(ctx, tx, nq.DeviceID, now); err != nil {
			return err
		}
		if err := s.checkQuota(ctx, tx, "pending_questions", s.Limits.PendingQuestions,
			`SELECT COUNT(*) FROM questions WHERE device_id = ? AND status = 'pending' AND expires_at > ?`,
			nq.DeviceID, ms(now)); err != nil {
			return err
		}
		var choices any
		if nq.Choices != nil {
			b, _ := json.Marshal(nq.Choices)
			choices = string(b)
		}
		qid := ids.New("qst")
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO questions (id, device_id, key_id, choices, status, created_at, expires_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)`,
			qid, nq.DeviceID, nq.KeyID, choices, ms(now), ms(now.Add(nq.TTL))); err != nil {
			return err
		}
		n := newNotification(nq.NewNotification, now)
		n.Type, n.QuestionID = "question", qid
		if err := insertNotification(ctx, tx, n); err != nil {
			return err
		}
		q.ID = qid
		return saveIdem(ctx, tx, nq.KeyID, idem, "question", qid, now)
	})
	if err != nil {
		return Question{}, false, err
	}
	if existing != "" {
		q, err = s.Question(ctx, existing)
		return q, true, err
	}
	q, err = s.Question(ctx, q.ID)
	return q, false, err
}

// matchChoice returns the canonical answer, or false if it is not allowed.
func matchChoice(choices []string, answer string) (string, bool) {
	if choices == nil {
		return answer, true
	}
	for _, c := range choices {
		if strings.EqualFold(c, answer) {
			return c, true
		}
	}
	return "", false
}

// Respond records the single accepted answer for a pending question.
//
// Retrying with the same idempotency key and answer returns the stored result
// with replay=true. Any other second response fails with ErrResolved. A
// response arriving after expiry marks the question expired and fails with
// ErrExpired; nothing is ever approved implicitly. On ErrResolved and
// ErrExpired the current question is still returned.
func (s *Store) Respond(ctx context.Context, questionID, deviceID, answer, idemKey string) (Question, bool, error) {
	var replay, expired bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// Read the clock only once the write lock is held, so a response that
		// waited behind other writers is judged at the time it is applied.
		now := s.now()
		q, err := scanQuestion(tx.QueryRowContext(ctx, questionSelect+` WHERE q.id = ?`, questionID))
		if err != nil {
			return err
		}
		if q.DeviceID != deviceID {
			return ErrNotFound
		}
		canonical, valid := matchChoice(q.Choices, answer)

		var prevKey, prevAnswer string
		err = tx.QueryRowContext(ctx, `SELECT idempotency_key, answer FROM responses WHERE question_id = ?`, questionID).Scan(&prevKey, &prevAnswer)
		switch {
		case err == nil:
			if prevKey == idemKey && valid && prevAnswer == canonical {
				replay = true
				return nil
			}
			return ErrResolved
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		switch q.Status {
		case StatusPending:
		case StatusExpired:
			return ErrExpired
		default:
			return ErrResolved
		}
		if !now.Before(q.ExpiresAt) {
			expired = true
			_, err := tx.ExecContext(ctx, `UPDATE questions SET status = 'expired', resolved_at = ? WHERE id = ? AND status = 'pending'`, ms(now), questionID)
			return err
		}
		if !valid {
			return ErrInvalidAnswer
		}

		status := StatusAnswered
		if q.Choices != nil && strings.EqualFold(canonical, "reject") {
			status = StatusRejected
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE questions SET status = ?, answer = ?, resolved_at = ? WHERE id = ? AND status = 'pending'`,
			status, canonical, ms(now), questionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrResolved
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO responses (id, question_id, device_id, answer, idempotency_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			ids.New("rsp"), questionID, deviceID, canonical, idemKey, ms(now)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE notifications SET read_at = COALESCE(read_at, ?) WHERE question_id = ?`, ms(now), questionID)
		return err
	})
	if err != nil && !errors.Is(err, ErrResolved) && !errors.Is(err, ErrExpired) {
		return Question{}, false, err
	}
	if expired {
		err = ErrExpired
	}
	q, qerr := s.Question(ctx, questionID)
	if qerr != nil {
		return Question{}, false, qerr
	}
	return q, replay, err
}

// CancelQuestion cancels a pending question owned by deviceID.
func (s *Store) CancelQuestion(ctx context.Context, id, deviceID string) (Question, error) {
	res, err := s.w.ExecContext(ctx,
		`UPDATE questions SET status = 'cancelled', resolved_at = ? WHERE id = ? AND device_id = ? AND status = 'pending'`,
		ms(s.now()), id, deviceID)
	if err != nil {
		return Question{}, err
	}
	q, err := s.Question(ctx, id)
	if err != nil {
		return q, err
	}
	if q.DeviceID != deviceID {
		return Question{}, ErrNotFound
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return q, ErrResolved
	}
	return q, nil
}
