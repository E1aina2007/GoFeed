-- ================================================================
-- Version: 000004_draft_purge_lease
-- Description: 为草稿清扫增加租约围栏和逐媒体删除检查点
-- ================================================================

ALTER TABLE videos
    ADD COLUMN purge_token CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER status,
    ADD COLUMN purge_lease_until DATETIME(3) NULL AFTER purge_token,
    ADD COLUMN play_purged_at DATETIME(3) NULL AFTER play_original_name,
    ADD COLUMN cover_purged_at DATETIME(3) NULL AFTER cover_original_name,
    ADD INDEX idx_videos_draft_created (status, created_at, id),
    ADD INDEX idx_videos_purging_lease (status, purge_lease_until, id);

-- 旧版本的通用删除接口可能已经软删了非公开视频
-- 这些行不能恢复为可写草稿，也不能再交给 published 清扫，统一转入不可逆回收状态
UPDATE videos
SET status = 'purging',
    deleted_at = NULL
WHERE deleted_at IS NOT NULL
  AND status <> 'published';
