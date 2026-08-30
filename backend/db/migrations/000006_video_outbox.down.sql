-- ================================================================
-- Version: 000006_video_outbox (down)
-- Description: 回滚视频处理状态机迁移
-- Preconditions: 仅可在维护窗口执行：先停止 API 与全部 sweeper/worker，
-- 确认 videos 不存在 status='processing' 或 'rejected' 行、
-- video_outbox_events 无未派发事件后再执行。
-- likes_count/comments_count 重建后为 0：计数值是互动关系表的派生字段，不做数据回填。
-- ================================================================

ALTER TABLE videos
    ADD COLUMN likes_count    BIGINT NOT NULL DEFAULT 0 AFTER published_at,
    ADD COLUMN comments_count BIGINT NOT NULL DEFAULT 0 AFTER likes_count;

ALTER TABLE videos DROP COLUMN rejected_reason;

DROP TABLE video_outbox_events;
