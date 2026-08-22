-- ================================================================
-- Version: 000003_video_drafts
-- Description: 允许 videos 的 draft 状态在上传媒体前保持空媒体字段
-- ================================================================

ALTER TABLE videos
    MODIFY COLUMN play_url VARCHAR(512) NOT NULL DEFAULT '',
    MODIFY COLUMN cover_url VARCHAR(512) NOT NULL DEFAULT '',
    MODIFY COLUMN published_at DATETIME(3) DEFAULT NULL;
