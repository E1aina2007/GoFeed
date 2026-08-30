package db

import (
	"fmt"
	"log"
	"os"
	"time"

	"gofeed/internal/config"

	sqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm"
)

// slowQueryThreshold 是慢查询日志阈值，超过该耗时的语句按 Warn 级别记录
const slowQueryThreshold = 200 * time.Millisecond

func NewDB(cfg config.DatabaseConfig) (*gorm.DB, error) {
	mc := sqldriver.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	mc.DBName = cfg.DBName
	mc.ParseTime = true
	mc.Loc = time.Local
	mc.Params = map[string]string{"charset": "utf8mb4", "clientFoundRows": "true"}

	db, err := gorm.Open(mysql.Open(mc.FormatDSN()), &gorm.Config{
		// 显式声明慢查询阈值与日志级别，不依赖 GORM 默认值的隐式行为
		Logger: gormlogger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gormlogger.Config{
			SlowThreshold: slowQueryThreshold,
			LogLevel:      gormlogger.Warn,
			// 预期内的记录不存在（详情 404、登录查无此人等）不按错误级别刷日志
			IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		return nil, err
	}
	if err := RegisterQueryCounter(db); err != nil {
		_ = Close(db)
		return nil, fmt.Errorf("注册查询计数回调失败: %w", err)
	}
	return db, nil
}

func Close(db *gorm.DB) error {
	DB, err := db.DB()
	if err != nil {
		return err
	}
	return DB.Close()
}
