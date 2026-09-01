-- ================================================================
-- Version: 000007_video_rejected_at
-- Description: 视频拒绝时间戳，供处理状态查询与自动清扫判定保留期
-- ================================================================

ALTER TABLE videos
    ADD COLUMN rejected_at DATETIME(3) DEFAULT NULL AFTER rejected_reason;
