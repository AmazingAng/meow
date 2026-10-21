package store

// Receipts contain no notification content and deliberately do not reference the
// notification row: deleting/expiring its body must not reset the 24h quota.
const receiptSchema = `
CREATE TABLE IF NOT EXISTS notification_receipts (
  notification_id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS notification_receipts_device ON notification_receipts(device_id, created_at);
CREATE INDEX IF NOT EXISTS notification_receipts_expiry ON notification_receipts(created_at);
`
