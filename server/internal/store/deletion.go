package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// NotificationDeletion selects a single ID or a half-open creation-time range.
type NotificationDeletion struct {
	ID           string
	From, Before time.Time
}

type DeletionResult struct {
	Deleted int `json:"deleted"`
}

// DeleteNotifications removes only this device's records, in one transaction.
// Linked legacy questions/responses are removed too. Creation idempotency rows
// remain until their normal expiry so a sender retry cannot recreate a deletion.
func (s *Store) DeleteNotifications(ctx context.Context, deviceID string, selection NotificationDeletion) (DeletionResult, error) {
	where := `device_id = ? AND id = ?`
	args := []any{deviceID, selection.ID}
	if selection.ID == "" {
		if selection.From.UnixMilli() < 0 || !selection.Before.After(selection.From) {
			return DeletionResult{}, errors.New("invalid deletion interval")
		}
		where = `device_id = ? AND created_at >= ? AND created_at < ?`
		args = []any{deviceID, ms(selection.From), ms(selection.Before)}
	}
	var result DeletionResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE `+where, args...).Scan(&result.Deleted); err != nil {
			return err
		}
		if selection.ID != "" && result.Deleted == 0 {
			return ErrNotFound
		}
		// questions -> notifications/responses use ON DELETE CASCADE.
		if _, err := tx.ExecContext(ctx, `DELETE FROM questions WHERE device_id = ? AND id IN (SELECT question_id FROM notifications WHERE `+where+`)`, append([]any{deviceID}, args...)...); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM notifications WHERE `+where, args...)
		return err
	})
	if err != nil {
		return DeletionResult{}, err
	}
	return result, nil
}
