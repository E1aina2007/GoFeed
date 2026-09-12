-- ================================================================
-- Version: 000009_outbox_publishing_lease
-- Description: outbox 事件增加 publishing 租约状态与重试观测字段
-- ================================================================

ALTER TABLE video_outbox_events
    ADD COLUMN next_attempt_at DATETIME(3) DEFAULT NULL AFTER attempt,
    ADD COLUMN locked_until    DATETIME(3) DEFAULT NULL AFTER next_attempt_at,
    ADD COLUMN last_attempt_at DATETIME(3) DEFAULT NULL AFTER locked_until,
    ADD COLUMN last_error      VARCHAR(255) NOT NULL DEFAULT '' AFTER last_attempt_at;

-- 历史 pending 事件没有租约字段，立即可被 claim
UPDATE video_outbox_events
SET next_attempt_at = created_at
WHERE status = 'pending'
  AND next_attempt_at IS NULL;

ALTER TABLE video_outbox_events
    ADD INDEX idx_video_outbox_events_claim (status, next_attempt_at, id);
