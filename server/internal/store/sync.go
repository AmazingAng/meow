package store

import (
	"context"
	"errors"
)

// SyncNotification adds an internal ordering value, never included in the record.
type SyncNotification struct {
	Notification
	Sequence int64
}

type syncScanner struct {
	scanner
	sequence *int64
}

func (s syncScanner) Scan(values ...any) error { return s.scanner.Scan(append(values, s.sequence)...) }

func (s *Store) SyncSequence(ctx context.Context, deviceID string) (int64, error) {
	var sequence int64
	err := s.r.QueryRowContext(ctx, `SELECT sequence FROM inbox_sync_clock WHERE device_id=?`, deviceID).Scan(&sequence)
	return sequence, noRows(err)
}

// SyncInbox enumerates a bounded change window. An updated row that moves past
// ceiling is deliberately picked up by the next window, never lost permanently.
func (s *Store) SyncInbox(ctx context.Context, deviceID string, after, ceiling int64, limit int) ([]SyncNotification, error) {
	if after < 0 || ceiling < after || limit < 1 || limit > 201 {
		return nil, errors.New("invalid sync window")
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+notificationColumns+`,n.sync_sequence `+notificationFrom+`
 WHERE n.device_id=? AND n.sync_sequence>? AND n.sync_sequence<=? ORDER BY n.sync_sequence LIMIT ?`, deviceID, after, ceiling, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SyncNotification{}
	for rows.Next() {
		var value SyncNotification
		value.Notification, err = scanNotification(syncScanner{rows, &value.Sequence})
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
