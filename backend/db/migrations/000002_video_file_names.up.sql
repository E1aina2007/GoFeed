-- ================================================================
-- Version: 000002_video_file_names
-- Description: videos 表新增媒体文件名列：实际存储文件名与用户指定文件名分离。
-- ================================================================

ALTER TABLE videos
    ADD COLUMN play_file_name      VARCHAR(255) NOT NULL DEFAULT '' AFTER play_url,
    ADD COLUMN play_original_name  VARCHAR(255) NOT NULL DEFAULT '' AFTER play_file_name,
    ADD COLUMN cover_file_name     VARCHAR(255) NOT NULL DEFAULT '' AFTER cover_url,
    ADD COLUMN cover_original_name VARCHAR(255) NOT NULL DEFAULT '' AFTER cover_file_name;
