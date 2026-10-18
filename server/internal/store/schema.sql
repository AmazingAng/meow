-- Meow schema v1. All timestamps are Unix milliseconds (UTC).

CREATE TABLE devices (
  id           TEXT PRIMARY KEY,
  token_hash   TEXT NOT NULL UNIQUE,          -- sha256 of the mobile device token (mdt_…)
  name         TEXT NOT NULL DEFAULT '',
  apns_token   TEXT,
  apns_env     TEXT NOT NULL DEFAULT 'production' CHECK (apns_env IN ('sandbox', 'production')),
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);

-- Send-only Device Keys (meow_…). Only the hash is stored.
CREATE TABLE api_keys (
  id           TEXT PRIMARY KEY,
  device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  key_hash     TEXT NOT NULL UNIQUE,
  hint         TEXT NOT NULL,
  name         TEXT NOT NULL DEFAULT '',
  scopes       TEXT NOT NULL,                 -- comma separated: notify,ask
  created_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at   INTEGER
);
CREATE INDEX api_keys_device ON api_keys(device_id);

CREATE TABLE questions (
  id          TEXT PRIMARY KEY,
  device_id   TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  key_id      TEXT REFERENCES api_keys(id) ON DELETE SET NULL,
  choices     TEXT,                           -- JSON array; NULL means free-text reply
  status      TEXT NOT NULL CHECK (status IN ('pending', 'answered', 'rejected', 'expired', 'cancelled')),
  answer      TEXT,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  resolved_at INTEGER
);
CREATE INDEX questions_pending ON questions(status, expires_at);

-- Every push, including questions, is a notification row (the inbox).
CREATE TABLE notifications (
  id          TEXT PRIMARY KEY,
  device_id   TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  key_id      TEXT REFERENCES api_keys(id) ON DELETE SET NULL,
  type        TEXT NOT NULL CHECK (type IN ('info', 'alert', 'question')),
  title       TEXT NOT NULL,
  body        TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL DEFAULT '',
  priority    TEXT NOT NULL CHECK (priority IN ('low', 'normal', 'high')),
  question_id TEXT UNIQUE REFERENCES questions(id) ON DELETE CASCADE,
  push_status TEXT NOT NULL DEFAULT 'queued' CHECK (push_status IN ('queued', 'accepted', 'failed', 'skipped')),
  push_error  TEXT,
  created_at  INTEGER NOT NULL,
  delivered_at INTEGER,                      -- reported by the iOS notification service extension
  read_at     INTEGER
);
CREATE INDEX notifications_inbox ON notifications(device_id, created_at DESC);
CREATE INDEX notifications_push ON notifications(push_status, created_at);

-- At most one accepted response per question.
CREATE TABLE responses (
  id              TEXT PRIMARY KEY,
  question_id     TEXT NOT NULL UNIQUE REFERENCES questions(id) ON DELETE CASCADE,
  device_id       TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  answer          TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  created_at      INTEGER NOT NULL
);

CREATE TABLE idempotency (
  key_id        TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  idem_key      TEXT NOT NULL,
  request_hash  TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id   TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (key_id, idem_key)
);
