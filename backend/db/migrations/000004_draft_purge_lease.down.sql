-- ================================================================
-- Version: 000004_draft_purge_lease
-- Description: 回滚草稿清扫租约字段
-- Preconditions: 仅可在维护窗口执行；先停止 API/sweeper、确认 sweeper 已退出且没有 status='purging' 草稿
-- ================================================================

ALTER TABLE videos
    DROP INDEX idx_videos_purging_lease,
    DROP INDEX idx_videos_draft_created,
    DROP COLUMN cover_purged_at,
    DROP COLUMN play_purged_at,
    DROP COLUMN purge_lease_until,
    DROP COLUMN purge_token;
