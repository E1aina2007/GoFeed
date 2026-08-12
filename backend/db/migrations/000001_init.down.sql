-- ================================================================
-- Version: 000001_init
-- Description: 回滚迁移：按依赖顺序删除核心业务表（videos → auth_sessions → users）
-- ================================================================

DROP TABLE IF EXISTS videos;
DROP TABLE IF EXISTS auth_sessions;
DROP TABLE IF EXISTS users;
