package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"meow.blue/meow/server/internal/ids"
)

// GatewayRegistration is returned once. The gateway persists only secret hashes.
type GatewayRegistration struct {
	ID              string `json:"id"`
	ManagementToken string `json:"management_token"`
	PushToken       string `json:"push_token"`
	BindingID       string `json:"binding_id"`
}

type GatewayRoute struct {
	ID, APNsToken, APNsEnv, BindingID string
}

// RegisterGatewayRoute creates an independent device route and secret hashes.
func (s *Store) RegisterGatewayRoute(ctx context.Context, token, environment, binding string) (GatewayRegistration, error) {
	r := GatewayRegistration{ID: ids.New("gwd"), ManagementToken: ids.Secret("gwm"), PushToken: ids.Secret("gwp"), BindingID: binding}
	now := ms(s.now())
	_, err := s.w.ExecContext(ctx, `INSERT INTO gateway_routes(id,management_hash,push_hash,apns_token,apns_env,binding_id,created_at,last_seen_at) VALUES(?,?,?,?,?,?,?,?)`, r.ID, Hash(r.ManagementToken), Hash(r.PushToken), token, environment, binding, now, now)
	return r, err
}

func (s *Store) GatewayRoute(ctx context.Context, id, secret string, management bool) (GatewayRoute, error) {
	column := "push_hash"
	if management {
		column = "management_hash"
	}
	var r GatewayRoute
	err := s.r.QueryRowContext(ctx, `SELECT id,apns_token,apns_env,binding_id FROM gateway_routes WHERE id=? AND `+column+`=? AND last_seen_at>?`, id, Hash(secret), ms(s.now().Add(-GatewayRouteTTL))).Scan(&r.ID, &r.APNsToken, &r.APNsEnv, &r.BindingID)
	return r, noRows(err)
}

func (s *Store) UpdateGatewayRoute(ctx context.Context, id, secret, token, environment string) error {
	now := s.now()
	res, err := s.w.ExecContext(ctx, `UPDATE gateway_routes SET apns_token=?,apns_env=?,last_seen_at=MAX(last_seen_at,?) WHERE id=? AND management_hash=? AND last_seen_at>?`, token, environment, ms(now), id, Hash(secret), ms(now.Add(-GatewayRouteTTL)))
	return affected(res, err)
}

func (s *Store) RevokeGatewayRoute(ctx context.Context, id, secret string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM gateway_routes WHERE id=? AND management_hash=?`, id, Hash(secret))
	return affected(res, err)
}

// A late invalid-token result must not revoke a token replaced in flight.
func (s *Store) InvalidateGatewayRoute(ctx context.Context, route GatewayRoute) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM gateway_routes WHERE id=? AND apns_token=? AND apns_env=?`, route.ID, route.APNsToken, route.APNsEnv)
	return err
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

const gatewayQuotaSchema = `CREATE TABLE IF NOT EXISTS gateway_push_quotas (
 apns_token TEXT NOT NULL,
 apns_env TEXT NOT NULL,
 quota_at INTEGER NOT NULL,
 quota_count INTEGER NOT NULL,
 PRIMARY KEY(apns_token,apns_env)
);
CREATE INDEX IF NOT EXISTS gateway_push_quota_expiry ON gateway_push_quotas(quota_at);`

// TakeGatewayQuota shares the existing 60/minute allowance across routes for
// the same token/environment. Re-enrollment, revocation and token-case aliases
// cannot reset it. Failed provider attempts also count. The independent table
// expires after its fixed window, so unused token identities do not accumulate.
func (s *Store) TakeGatewayQuota(ctx context.Context, id string) error {
	var route GatewayRoute
	route.ID = id
	if err := s.r.QueryRowContext(ctx, `SELECT apns_token,apns_env FROM gateway_routes WHERE id=?`, id).Scan(&route.APNsToken, &route.APNsEnv); err != nil {
		return noRows(err)
	}
	return s.TakeGatewayPushQuota(ctx, route)
}

// Bind admission to the exact token that will be sent to APNs. A concurrent
// route update/revocation cannot spend a different token's quota or revive a
// deleted route. The caller retries after reloading the route when this fails.
func (s *Store) TakeGatewayPushQuota(ctx context.Context, route GatewayRoute) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		now := ms(s.now())
		cutoff := now - int64(time.Minute/time.Millisecond)
		var token, environment string
		if err := tx.QueryRowContext(ctx, `SELECT apns_token,apns_env FROM gateway_routes WHERE id=? AND last_seen_at>?`, route.ID, now-int64(GatewayRouteTTL/time.Millisecond)).Scan(&token, &environment); err != nil {
			return noRows(err)
		}
		if token != route.APNsToken || environment != route.APNsEnv {
			return ErrGatewayRouteChanged
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_push_quotas WHERE quota_at<=?`, cutoff); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO gateway_push_quotas(apns_token,apns_env,quota_at,quota_count) VALUES(?,?,?,1)
 ON CONFLICT(apns_token,apns_env) DO UPDATE SET quota_count=quota_count+1 WHERE quota_count<60`, strings.ToLower(route.APNsToken), route.APNsEnv, now)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return &QuotaError{Limit: "gateway_pushes", Max: 60}
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE gateway_routes SET last_seen_at=MAX(last_seen_at,?) WHERE id=?`, now, route.ID)
		return err
	})
}

// GatewayBinding is an outbound capability. Unlike inbound credentials, its
// wake secret has to be recoverable to authenticate requests to the gateway.
// Protect the self-hosted database and its backups accordingly.
type GatewayBinding struct {
	GatewayURL string `json:"-"`
	DeviceID   string `json:"gateway_device_id"`
	PushToken  string `json:"push_token"`
	BindingID  string `json:"binding_id"`
}

func (s *Store) SetGatewayBinding(ctx context.Context, deviceID string, b GatewayBinding) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO device_gateway_bindings(device_id,gateway_url,gateway_device_id,push_token,binding_id)
 VALUES(?,?,?,?,?) ON CONFLICT(device_id) DO UPDATE SET gateway_url=excluded.gateway_url,gateway_device_id=excluded.gateway_device_id,push_token=excluded.push_token,binding_id=excluded.binding_id`, deviceID, b.GatewayURL, b.DeviceID, b.PushToken, b.BindingID)
	return err
}

func (s *Store) GatewayBinding(ctx context.Context, deviceID string) (GatewayBinding, error) {
	var b GatewayBinding
	err := s.r.QueryRowContext(ctx, `SELECT gateway_url,gateway_device_id,push_token,binding_id FROM device_gateway_bindings WHERE device_id=?`, deviceID).Scan(&b.GatewayURL, &b.DeviceID, &b.PushToken, &b.BindingID)
	return b, noRows(err)
}
