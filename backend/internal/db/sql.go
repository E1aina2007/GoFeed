package db

const (
	CreateDatabaseTpl = "CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"

	CreateTableUsers = `
CREATE TABLE IF NOT EXISTS users (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    username      VARCHAR(191)    NOT NULL UNIQUE,
    password      VARCHAR(255)    NOT NULL,
    avatar_url    VARCHAR(512)    DEFAULT '',
    bio           VARCHAR(255)    DEFAULT '',
    deleted_at    DATETIME(3)     DEFAULT NULL,
    INDEX idx_users_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`

	CreateTableAuthSessions = `
CREATE TABLE IF NOT EXISTS auth_sessions (
    id                 VARCHAR(64)     PRIMARY KEY,
    user_id            BIGINT UNSIGNED NOT NULL,
    refresh_token_hash CHAR(64)        NOT NULL UNIQUE,
    expires_at         DATETIME(3)     NOT NULL,
    revoked_at         DATETIME(3)     DEFAULT NULL,
    created_at         DATETIME(3)     NOT NULL,
    updated_at         DATETIME(3)     NOT NULL,
    INDEX idx_auth_sessions_user_active (user_id, revoked_at),
    INDEX idx_auth_sessions_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
)

var allTables = []string{
	CreateTableUsers,
	CreateTableAuthSessions,
}
