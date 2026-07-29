package db

const (
	CreateDatabaseTpl = "CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"

	CreateTableUsers = `
CREATE TABLE IF NOT EXISTS users (
    id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    username      VARCHAR(191)    NOT NULL UNIQUE,
    password      VARCHAR(255)    NOT NULL,
    access_token  VARCHAR(512)    DEFAULT '',
    refresh_token VARCHAR(512)    DEFAULT '',
    avatar_url    VARCHAR(512)    DEFAULT '',
    bio           VARCHAR(255)    DEFAULT '',
    deleted_at    DATETIME(3)     DEFAULT NULL,
    INDEX idx_users_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
)

var allTables = []string{
	CreateTableUsers,
}
