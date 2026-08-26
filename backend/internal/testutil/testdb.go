// 测试工具包提供真实数据库集成测试的初始化、隔离和清理能力
// 未配置数据库时，预期集成测试自行跳过而单元测试继续执行
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

// 测试目标：保存当前测试进程的数据库连接和临时库名称
// 预期效果：测试入口初始化该资源并在结束后销毁
var (
	testDB     *gorm.DB
	testDBName string
)

// 测试目标：初始化独立测试库并执行迁移
// 预期效果：测试结束后清理对应资源
func Main(m *testing.M) int {
	if os.Getenv("MYSQL_DATABASE") == "" {
		// 未配置数据库时不创建测试库，预期集成测试自行跳过并保留可见结果
		return m.Run()
	}

	if os.Getenv("JWT_SECRET") == "" {
		// 认证密钥有进程级缓存，预期在任何测试运行前固定该密钥
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

// 测试目标：返回当前测试进程共享的数据库连接并在每次调用前清空业务表
// 预期效果：同包内测试用例互不污染
func DB(t *testing.T) *gorm.DB {
	t.Helper()
	if testDB == nil {
		t.Skip("需要真实 MySQL：设置 MYSQL_DATABASE（以及 MYSQL_HOST、MYSQL_ROOT_PASSWORD 等）后重跑")
	}
	CleanDB(t, testDB)
	return testDB
}

// 测试目标：清空集成测试涉及的业务表并重置自增标识
// 预期效果：为每个用例提供干净数据
func CleanDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	// 社交关系表引用用户和视频，使用子表优先的物理删除避免外键下 TRUNCATE 父表失败
	tables := []struct {
		name           string
		resetIncrement bool
	}{
		{name: "video_comments", resetIncrement: true},
		{name: "video_likes", resetIncrement: true},
		{name: "user_follows", resetIncrement: true},
		{name: "videos", resetIncrement: true},
		{name: "auth_sessions"},
		{name: "users", resetIncrement: true},
	}
	for _, table := range tables {
		if err := db.Exec("DELETE FROM " + table.name).Error; err != nil {
			t.Fatalf("清空表 %s 失败: %v", table.name, err)
		}
	}
	for _, table := range tables {
		if !table.resetIncrement {
			continue
		}
		if err := db.Exec("ALTER TABLE " + table.name + " AUTO_INCREMENT = 1").Error; err != nil {
			t.Fatalf("重置表 %s 自增标识失败: %v", table.name, err)
		}
	}
}

// 测试目标：根据数据库环境变量创建进程级独立测试库并应用迁移
// 预期效果：返回可用的数据库连接
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

// 测试目标：关闭连接后删除当前测试进程创建的独立数据库
// 预期效果：释放全部测试资源
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

// 测试目标：复用应用的环境变量读取逻辑
// 预期效果：测试与生产使用一致的连接参数
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

// 测试目标：建立到指定数据库的对象关系映射连接
// 预期效果：空库名时仅连接数据库服务器
func openMySQL(cfg config.DatabaseConfig, dbname string, multiStatements bool) (*gorm.DB, error) {
	mc := sqldriver.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	mc.DBName = dbname
	mc.ParseTime = true
	mc.Loc = time.Local
	// 与生产连接参数保持一致，预期更新行数配置沿用生产语义
	mc.Params = map[string]string{"charset": "utf8mb4", "clientFoundRows": "true"}
	if multiStatements {
		mc.Params["multiStatements"] = "true"
	}

	return gorm.Open(gormmysql.Open(mc.FormatDSN()), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
}

// 测试目标：按文件名顺序执行迁移目录中的全部向上迁移脚本
// 预期效果：得到真实业务表结构并捕获模型与迁移之间的不一致
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

// 测试目标：根据测试工具源文件位置定位迁移目录
// 预期效果：不依赖测试进程工作目录
func migrationsDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "db/migrations"
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations")
}

// 测试目标：关闭对象关系映射连接
// 预期效果：释放测试数据库的底层连接资源
func closeDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
