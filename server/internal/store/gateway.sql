-- v2: gateway-only routes and self-hosted outbound bindings. Different
-- processes use separate database files; the two roles never share secrets.
CREATE TABLE gateway_routes (
  id TEXT PRIMARY KEY,
  management_hash TEXT NOT NULL UNIQUE,
  push_hash TEXT NOT NULL UNIQUE,
  apns_token TEXT NOT NULL,
  apns_env TEXT NOT NULL CHECK (apns_env IN ('sandbox', 'production')),
  binding_id TEXT NOT NULL,
  quota_at INTEGER NOT NULL DEFAULT 0,
  quota_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE device_gateway_bindings (
  device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
  gateway_url TEXT NOT NULL,
  gateway_device_id TEXT NOT NULL,
  push_token TEXT NOT NULL,
  binding_id TEXT NOT NULL
);
