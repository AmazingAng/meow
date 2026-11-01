package store

import (
	"context"
	"time"
)

// GatewayRouteTTL bounds abandoned anonymous enrollments. Ordinary management
// updates and admitted wakes renew the route, including on a locked phone.
const GatewayRouteTTL = 90 * 24 * time.Hour

const lifecycleSchema = `
ALTER TABLE gateway_routes ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gateway_routes ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX gateway_routes_expiry ON gateway_routes(last_seen_at);
CREATE INDEX api_keys_active ON api_keys(device_id) WHERE revoked_at IS NULL;
CREATE INDEX api_keys_revoked_expiry ON api_keys(revoked_at) WHERE revoked_at IS NOT NULL;
CREATE INDEX notifications_key ON notifications(key_id) WHERE key_id IS NOT NULL;
CREATE INDEX questions_key ON questions(key_id) WHERE key_id IS NOT NULL;
`

// PruneGatewayRoutes runs independently of content retention. Each delete is
// a bounded autocommit transaction so renewals are evaluated under the same
// SQLite write lock as expiry. Token quotas live in a separate table.
func (s *Store) PruneGatewayRoutes(ctx context.Context) (int64, error) {
	var total int64
	cutoff := ms(s.now().Add(-GatewayRouteTTL))
	for {
		res, err := s.w.ExecContext(ctx, `DELETE FROM gateway_routes WHERE rowid IN
 (SELECT rowid FROM gateway_routes WHERE last_seen_at<=? ORDER BY last_seen_at,rowid LIMIT 1000)`, cutoff)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}
