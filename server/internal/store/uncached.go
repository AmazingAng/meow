package store

import (
	"context"
	"database/sql"
	"errors"
)

var ErrNoPushToken = errors.New("device has no APNs token")

const (
	uncachedPending  = "notification_uncached_pending"
	uncachedAccepted = "notification_uncached_accepted"
	uncachedFailed   = "notification_uncached_failed"
)

// ReserveUncachedNotification commits only quota and retry metadata. The
// returned preview target is request-local and never enters the inbox or queue.
func (s *Store) ReserveUncachedNotification(ctx context.Context, input NewNotification, idem Idem) (target PushTarget, replay bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		n := newNotification(input, now)
		if idem.Key != "" {
			var hash, kind, id string
			var createdAt int64
			err := tx.QueryRowContext(ctx, `SELECT request_hash, resource_type, resource_id, created_at
				FROM idempotency WHERE key_id=? AND idem_key=?`, input.KeyID, idem.Key).Scan(&hash, &kind, &id, &createdAt)
			switch {
			case err == nil:
				if hash != idem.RequestHash {
					return ErrIdempotencyMismatch
				}
				switch kind {
				case uncachedPending:
				case uncachedAccepted:
					n.PushStatus = PushAccepted
				case uncachedFailed:
					n.PushStatus = PushFailed
				default:
					return ErrIdempotencyMismatch
				}
				n.ID, n.CreatedAt = id, fromMS(createdAt)
				target.Notification, replay = n, true
				return nil
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}
		device, err := scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices
			WHERE id=? AND EXISTS (SELECT 1 FROM api_keys WHERE id=? AND device_id=devices.id AND revoked_at IS NULL)`,
			input.DeviceID, input.KeyID))
		if err != nil {
			return err
		}
		if device.APNsToken == "" {
			return ErrNoPushToken
		}
		if err := s.checkDaily(ctx, tx, input.DeviceID, now); err != nil {
			return err
		}
		if err := insertReceipt(ctx, tx, n); err != nil {
			return err
		}
		target = PushTarget{Notification: n, Device: device}
		return saveIdem(ctx, tx, input.KeyID, idem, uncachedPending, n.ID, now)
	})
	return target, replay, err
}

// FinishUncachedNotification records the provider outcome without persisting
// payload or provider error text.
func (s *Store) FinishUncachedNotification(ctx context.Context, n Notification, idem Idem, accepted bool) error {
	if idem.Key == "" {
		return nil
	}
	kind := uncachedFailed
	if accepted {
		kind = uncachedAccepted
	}
	_, err := s.w.ExecContext(ctx, `UPDATE idempotency SET resource_type=? WHERE key_id=? AND idem_key=? AND resource_id=? AND resource_type=?`,
		kind, n.KeyID, idem.Key, n.ID, uncachedPending)
	return err
}
