-- ================================================================
-- Version: 000005_social_interactions
-- Description: 创建点赞、评论和关注关系表
-- ================================================================

CREATE TABLE video_likes (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    video_id   BIGINT UNSIGNED NOT NULL,
    user_id    BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(3)     NOT NULL,
    UNIQUE KEY uq_video_likes_video_user (video_id, user_id),
    KEY idx_video_likes_video_created (video_id, created_at DESC, id DESC),
    KEY idx_video_likes_user_created (user_id, created_at DESC, id DESC),
    CONSTRAINT fk_video_likes_video
        FOREIGN KEY (video_id) REFERENCES videos (id) ON DELETE CASCADE,
    CONSTRAINT fk_video_likes_user
        FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE user_follows (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    follower_id BIGINT UNSIGNED NOT NULL,
    followee_id BIGINT UNSIGNED NOT NULL,
    created_at  DATETIME(3)     NOT NULL,
    UNIQUE KEY uq_user_follows_follower_followee (follower_id, followee_id),
    KEY idx_user_follows_followee_created (followee_id, created_at DESC, id DESC),
    KEY idx_user_follows_follower_created (follower_id, created_at DESC, id DESC),
    CONSTRAINT fk_user_follows_follower
        FOREIGN KEY (follower_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT fk_user_follows_followee
        FOREIGN KEY (followee_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE video_comments (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    video_id   BIGINT UNSIGNED NOT NULL,
    author_id  BIGINT UNSIGNED NOT NULL,
    content    VARCHAR(1000)    NOT NULL,
    created_at DATETIME(3)     NOT NULL,
    updated_at DATETIME(3)     NOT NULL,
    deleted_at DATETIME(3)     DEFAULT NULL,
    KEY idx_video_comments_video_visible (video_id, deleted_at, created_at DESC, id DESC),
    KEY idx_video_comments_author_visible (author_id, deleted_at, id DESC),
    CONSTRAINT fk_video_comments_video
        FOREIGN KEY (video_id) REFERENCES videos (id) ON DELETE CASCADE,
    CONSTRAINT fk_video_comments_author
        FOREIGN KEY (author_id) REFERENCES users (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
