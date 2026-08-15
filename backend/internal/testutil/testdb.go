// Package testutil 提供依赖真实 MySQL 的集成测试基础设施
//
// 在测试包中加入：
//
//	func TestMain(m *testing.M) {
//		os.Exit(testutil.Main(m))
//	}
//
//	func TestXxx(t *testing.T) {
//		db := testutil.DB(t)
//		...
//	}
//
// 未设置 MYSQL_DATABASE 时每个集成测试自动 skip，单元测试不受影响
// 设置 MYSQL_* 后，每个测试进程创建独立数据库并执行迁移，测试结束时删除
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"gofeed/internal/config"

	sqldriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	testDB     *gorm.DB
	testDBName string
)

// Main 在测试包的 TestMain 中调用：准备独立测试库、执行迁移，结束后清理
func Main(m *testing.M) int {
	if os.Getenv("MYSQL_DATABASE") == "" {
		// 不创建数据库，让每个集成测试自行 skip，保留可见的测试结果
		return m.Run()
	}

	if os.Getenv("JWT_SECRET") == "" {
		// auth.Secret 有进程级缓存，必须在任何测试运行前固定密钥
		os.Setenv("JWT_SECRET", "integration-test-secret-not-for-production")
	}

	db, err := setupTestDatabase()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: 初始化测试数据库失败: %v\n", err)
		return 1
	}
	testDB = db

	code := m.Run()

	if err := teardownTestDatabase(db); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: 清理测试数据库失败: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// DB 返回当前测试进程共享的数据库连接，并在每次调用前清空业务表
// 保证同包内用例互不污染
func DB(t *testing.T) *gorm.DB {
	t.Helper()
	if testDB == nil {
		t.Skip("需要真实 MySQL：设置 MYSQL_DATABASE（以及 MYSQL_HOST、MYSQL_ROOT_PASSWORD 等）后重跑")
	}
	CleanDB(t, testDB)
	return testDB
}

// CleanDB 清空集成测试涉及的业务表并重置自增 ID
func CleanDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	// 表之间没有外键，直接逐表 TRUNCATE；自增 ID 一并重置，便于断言
	for _, table := range []string{"videos", "auth_sessions", "users"} {
		if err := db.Exec("TRUNCATE TABLE " + table).Error; err != nil {
			t.Fatalf("清空表 %s 失败: %v", table, err)
		}
	}
}

// setupTestDatabase 以 MYSQL_* 环境变量为基准创建进程级独立测试库并应用迁移
func setupTestDatabase() (*gorm.DB, error) {
	cfg := envConfig()
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}

	name := fmt.Sprintf("%s_test_%d_%d", cfg.DBName, os.Getpid(), time.Now().UnixNano())

	admin, err := openMySQL(cfg, "", true)
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}
	defer closeDB(admin)

	createSQL := fmt.Sprintf(
		"CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci",
		name,
	)
	if err := admin.Exec(createSQL).Error; err != nil {
		return nil, fmt.Errorf("创建测试库失败: %w", err)
	}

	db, err := openMySQL(cfg, name, true)
	if err != nil {
		return nil, fmt.Errorf("连接测试库失败: %w", err)
	}
	if err := applyMigrations(db); err != nil {
		_ = closeDB(db)
		return nil, err
	}

	testDBName = name
	return db, nil
}

// teardownTestDatabase 关闭连接后删除当前测试进程创建的独立数据库
func teardownTestDatabase(db *gorm.DB) error {
	name := testDBName
	if err := closeDB(db); err != nil {
		return fmt.Errorf("关闭测试库连接失败: %w", err)
	}
	if name == "" {
		return nil
	}

	cfg := envConfig()
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	admin, err := openMySQL(cfg, "", true)
	if err != nil {
		return fmt.Errorf("重连 MySQL 清理失败: %w", err)
	}
	defer closeDB(admin)

	if err := admin.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name)).Error; err != nil {
		return fmt.Errorf("删除测试库失败: %w", err)
	}
	return nil
}

// envConfig 复用应用的环境变量读取逻辑，保证测试与生产连接参数一致
func envConfig() config.DatabaseConfig {
	cfg := config.Config{}
	config.OverrideWithEnv(&cfg)
	if cfg.DB.Port == 0 {
		cfg.DB.Port = 3306
	}
	if cfg.DB.User == "" {
		cfg.DB.User = "root"
	}
	return cfg.DB
}

// openMySQL 建立到指定库的 GORM 连接；dbname 为空时只连接服务器不选库
func openMySQL(cfg config.DatabaseConfig, dbname string, multiStatements bool) (*gorm.DB, error) {
	mc := sqldriver.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	mc.DBName = dbname
	mc.ParseTime = true
	mc.Loc = time.Local
	// 与生产连接参数保持一致：clientFoundRows 影响 UPDATE 的 RowsAffected 语义。
	mc.Params = map[string]string{"charset": "utf8mb4", "clientFoundRows": "true"}
	if multiStatements {
		mc.Params["multiStatements"] = "true"
	}

	return gorm.Open(gormmysql.Open(mc.FormatDSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
}

// applyMigrations 按文件名顺序执行 db/migrations 下的全部 .up.sql
// 使用真实手写 DDL 而非 AutoMigrate，确保测试能捕获模型与迁移不一致
func applyMigrations(db *gorm.DB) error {
	dir := migrationsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读取迁移目录失败: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("读取迁移 %s 失败: %w", name, err)
		}
		if err := db.Exec(string(content)).Error; err != nil {
			return fmt.Errorf("执行迁移 %s 失败: %w", name, err)
		}
	}
	return nil
}

// migrationsDir 以 testutil 源文件位置定位迁移目录，不依赖测试进程工作目录
func migrationsDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "db/migrations"
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations")
}

func closeDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
