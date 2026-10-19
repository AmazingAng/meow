package store

import (
	"context"
	"database/sql"
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
	_, err := s.w.ExecContext(ctx, `INSERT INTO gateway_routes(id,management_hash,push_hash,apns_token,apns_env,binding_id) VALUES(?,?,?,?,?,?)`, r.ID, Hash(r.ManagementToken), Hash(r.PushToken), token, environment, binding)
	return r, err
}

func (s *Store) GatewayRoute(ctx context.Context, id, secret string, management bool) (GatewayRoute, error) {
	column := "push_hash"
	if management {
		column = "management_hash"
	}
	var r GatewayRoute
	err := s.r.QueryRowContext(ctx, `SELECT id,apns_token,apns_env,binding_id FROM gateway_routes WHERE id=? AND `+column+`=?`, id, Hash(secret)).Scan(&r.ID, &r.APNsToken, &r.APNsEnv, &r.BindingID)
	return r, noRows(err)
}

func (s *Store) UpdateGatewayRoute(ctx context.Context, id, secret, token, environment string) error {
	res, err := s.w.ExecContext(ctx, `UPDATE gateway_routes SET apns_token=?,apns_env=? WHERE id=? AND management_hash=?`, token, environment, id, Hash(secret))
	return affected(res, err)
}

func (s *Store) RevokeGatewayRoute(ctx context.Context, id, secret string) error {
	res, err := s.w.ExecContext(ctx, `DELETE FROM gateway_routes WHERE id=? AND management_hash=?`, id, Hash(secret))
	return affected(res, err)
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

// TakeGatewayQuota atomically consumes a slot in a durable fixed window.
// Failed APNs attempts also count, so a bad token cannot create a retry flood.
func (s *Store) TakeGatewayQuota(ctx context.Context, id string) error {
	now := ms(s.now())
	res, err := s.w.ExecContext(ctx, `UPDATE gateway_routes SET
 quota_count=CASE WHEN quota_at<=? THEN 1 ELSE quota_count+1 END,
 quota_at=CASE WHEN quota_at<=? THEN ? ELSE quota_at END
 WHERE id=? AND (quota_at<=? OR quota_count<60)`, now-int64(time.Minute/time.Millisecond), now-int64(time.Minute/time.Millisecond), now, id, now-int64(time.Minute/time.Millisecond))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return &QuotaError{Limit: "gateway_pushes", Max: 60}
	}
	return err
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
