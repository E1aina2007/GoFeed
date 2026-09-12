-- ================================================================
-- Version: 000009_outbox_publishing_lease (down)
-- Description: 回滚 outbox 租约字段
-- Preconditions: 仅可在维护窗口执行：先停止 worker 的 relay 与全部 sweeper，
-- 确认 video_outbox_events 不存在 status='publishing' 行后再执行；
-- publishing 行必须先复位为 pending，否则事件会失去可投递状态。
-- 复位后的 pending 事件可能被重复投递，消费端 CAS 幂等是必要前提。
-- ================================================================

UPDATE video_outbox_events
SET status = 'pending',
    locked_until = NULL
WHERE status = 'publishing';

ALTER TABLE video_outbox_events
    DROP INDEX idx_video_outbox_events_claim;

ALTER TABLE video_outbox_events
    DROP COLUMN next_attempt_at,
    DROP COLUMN locked_until,
    DROP COLUMN last_attempt_at,
    DROP COLUMN last_error;
