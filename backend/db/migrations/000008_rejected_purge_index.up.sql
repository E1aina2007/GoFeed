-- ================================================================
-- Version: 000008_rejected_purge_index
-- Description: 为拒绝视频补齐清扫时间基准和候选查询索引
-- ================================================================

-- 000007 之后才有 rejected_at；旧拒绝记录以最后更新时间作为保留期起点，
-- 避免历史数据因时间戳为空而永久占用媒体存储
UPDATE videos
SET rejected_at = updated_at
WHERE status = 'rejected'
  AND rejected_at IS NULL;

ALTER TABLE videos
    ADD INDEX idx_videos_rejected_purge (status, rejected_at, id);
