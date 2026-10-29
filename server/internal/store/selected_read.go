package store

import (
	"context"
	"errors"
	"strings"
)

const MaxReadBatch = 200

// MarkSelectedRead touches only the authenticated device's selected rows.
// Unknown, foreign, already-read and expired IDs are indistinguishable no-ops.
func (s *Store) MarkSelectedRead(ctx context.Context, deviceID string, ids []string) (int64, error) {
	if len(ids) == 0 || len(ids) > MaxReadBatch {
		return 0, errors.New("invalid read batch size")
	}
	args := []any{ms(s.now()), deviceID}
	for _, id := range ids {
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	res, err := s.w.ExecContext(ctx, `UPDATE notifications SET read_at=? WHERE device_id=? AND read_at IS NULL AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
