-- ================================================================
-- Version: 000002_video_file_names
-- Description: 回滚迁移：删除媒体文件名列。
-- ================================================================

ALTER TABLE videos
    DROP COLUMN cover_original_name,
    DROP COLUMN cover_file_name,
    DROP COLUMN play_original_name,
    DROP COLUMN play_file_name;
