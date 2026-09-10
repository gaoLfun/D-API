ALTER TABLE notification_outbox ADD COLUMN IF NOT EXISTS lease_version BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS request_logs_cursor_idx ON request_logs(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS notification_outbox_dead_cursor_idx ON notification_outbox(dead_at DESC, id DESC) WHERE dead_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS notification_outbox_due_idx ON notification_outbox(next_attempt_at, id) WHERE dead_at IS NULL;
