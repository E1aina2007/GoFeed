-- ================================================================
-- Version: 000006_video_outbox
-- Description: 视频处理状态机与 outbox 事件表；删除冗余计数值列
-- ================================================================

CREATE TABLE video_outbox_events (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    event_id      CHAR(36)        NOT NULL,
    video_id      BIGINT UNSIGNED NOT NULL,
    event_type    VARCHAR(64)     NOT NULL,
    status        VARCHAR(16)     NOT NULL DEFAULT 'pending',
    attempt       INT             NOT NULL DEFAULT 0,
    created_at    DATETIME(3)     NOT NULL,
    dispatched_at DATETIME(3)     DEFAULT NULL,
    UNIQUE KEY uq_video_outbox_events_event_id (event_id),
    KEY idx_video_outbox_events_video (video_id),
    KEY idx_video_outbox_events_status_id (status, id),
    CONSTRAINT fk_video_outbox_events_video
        FOREIGN KEY (video_id) REFERENCES videos (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

ALTER TABLE videos
    ADD COLUMN rejected_reason VARCHAR(255) NOT NULL DEFAULT '' AFTER status;

-- 计数值是互动关系表的派生字段，读路径已按聚合实时计算，删除以消除双事实源
ALTER TABLE videos
    DROP COLUMN likes_count,
    DROP COLUMN comments_count;
