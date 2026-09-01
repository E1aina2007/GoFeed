-- ================================================================
-- Version: 000008_rejected_purge_index (down)
-- Description: 回滚拒绝视频清扫候选索引
-- Preconditions: 仅可在维护窗口执行：先停止 API 与全部 sweeper/worker，
-- 确认没有依赖该索引的在线迁移；rejected_at 的历史回填不可逆。
-- ================================================================

ALTER TABLE videos
    DROP INDEX idx_videos_rejected_purge;
