-- ================================================================
-- Version: 000007_video_rejected_at (down)
-- Description: 回滚拒绝时间戳列
-- Preconditions: 仅可在维护窗口执行：先停止 API 与全部 sweeper/worker，
-- 确认 videos 不存在 status='rejected' 行后再执行。
-- ================================================================

ALTER TABLE videos DROP COLUMN rejected_at;
