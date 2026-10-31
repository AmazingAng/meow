-- v8: durable per-device change tracking. Expiry never resets the clock.
ALTER TABLE notifications ADD COLUMN sync_sequence INTEGER NOT NULL DEFAULT 0;
CREATE TABLE inbox_sync_clock (
 device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
 sequence INTEGER NOT NULL
);
CREATE TEMP TABLE inbox_sync_backfill(id TEXT PRIMARY KEY, sequence INTEGER NOT NULL);
INSERT INTO inbox_sync_backfill
 SELECT id,ROW_NUMBER() OVER(PARTITION BY device_id ORDER BY created_at,id) FROM notifications;
UPDATE notifications SET sync_sequence=(SELECT sequence FROM inbox_sync_backfill WHERE id=notifications.id);
DROP TABLE inbox_sync_backfill;
CREATE INDEX notifications_sync ON notifications(device_id,sync_sequence);
INSERT INTO inbox_sync_clock
 SELECT d.id,COALESCE(MAX(n.sync_sequence),0) FROM devices d LEFT JOIN notifications n ON n.device_id=d.id GROUP BY d.id;
CREATE TRIGGER inbox_sync_device AFTER INSERT ON devices BEGIN
 INSERT INTO inbox_sync_clock(device_id,sequence) VALUES(NEW.id,0);
END;
CREATE TRIGGER inbox_sync_insert AFTER INSERT ON notifications BEGIN
 UPDATE inbox_sync_clock SET sequence=sequence+1 WHERE device_id=NEW.device_id;
 UPDATE notifications SET sync_sequence=(SELECT sequence FROM inbox_sync_clock WHERE device_id=NEW.device_id) WHERE id=NEW.id;
END;
CREATE TRIGGER inbox_sync_update AFTER UPDATE OF device_id,type,title,body,source,priority,question_id,push_status,push_error,delivered_at,read_at,context,event ON notifications
WHEN OLD.device_id IS NOT NEW.device_id OR OLD.type IS NOT NEW.type OR OLD.title IS NOT NEW.title
 OR OLD.body IS NOT NEW.body OR OLD.source IS NOT NEW.source OR OLD.priority IS NOT NEW.priority
 OR OLD.question_id IS NOT NEW.question_id OR OLD.push_status IS NOT NEW.push_status
 OR OLD.push_error IS NOT NEW.push_error OR OLD.delivered_at IS NOT NEW.delivered_at
 OR OLD.read_at IS NOT NEW.read_at OR OLD.context IS NOT NEW.context OR OLD.event IS NOT NEW.event
BEGIN
 UPDATE inbox_sync_clock SET sequence=sequence+1 WHERE device_id=NEW.device_id;
 UPDATE notifications SET sync_sequence=(SELECT sequence FROM inbox_sync_clock WHERE device_id=NEW.device_id) WHERE id=NEW.id;
END;
CREATE TRIGGER inbox_sync_question AFTER UPDATE OF status,answer,choices,expires_at ON questions
WHEN OLD.status IS NOT NEW.status OR OLD.answer IS NOT NEW.answer OR OLD.choices IS NOT NEW.choices OR OLD.expires_at IS NOT NEW.expires_at
BEGIN
 UPDATE inbox_sync_clock SET sequence=sequence+1 WHERE device_id=NEW.device_id;
 UPDATE notifications SET sync_sequence=(SELECT sequence FROM inbox_sync_clock WHERE device_id=NEW.device_id) WHERE question_id=NEW.id;
END;
