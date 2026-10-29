package store

// Applied transactionally to both fresh and existing databases. Match the
// tuple cursor's complete ordering and each cleanup predicate's time range.
const performanceIndexes = `
DROP INDEX notifications_inbox;
CREATE INDEX notifications_inbox ON notifications(device_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS notifications_expiry ON notifications(question_id, created_at);
CREATE INDEX IF NOT EXISTS questions_expiry ON questions(created_at) WHERE status != 'pending';
CREATE INDEX IF NOT EXISTS idempotency_expiry ON idempotency(created_at);
`
