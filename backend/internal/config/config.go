package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	DB        DatabaseConfig  `yaml:"database"`
	Retention RetentionConfig `yaml:"retention"`
	Sweeper   SweeperConfig   `yaml:"sweeper"`

	// Dev is controlled by MODE and is intentionally not loaded from YAML.
	Dev bool `yaml:"-"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type DatabaseConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	// Password is supplied through MYSQL_ROOT_PASSWORD or MYSQL_PASSWORD.
	Password string `yaml:"-"`
	DBName   string `yaml:"dbname"`
}

type RetentionConfig struct {
	// UserDeletedDays 注销账号从软删除到硬删除的保留天数
	UserDeletedDays int `yaml:"user_deleted_days"`
	// VideoDeletedDays 视频从软删除到清扫媒体和硬删除记录的保留天数
	VideoDeletedDays int `yaml:"video_deleted_days"`
	// VideoDraftHours 未完成草稿从创建到清扫媒体和硬删除记录的保留小时数
	VideoDraftHours int `yaml:"video_draft_hours"`
}

type SweeperConfig struct {
	// IntervalMinutes 注销用户清扫任务执行间隔（分钟）
	IntervalMinutes int `yaml:"interval_minutes"`
	// DraftPurgeLeaseMinutes 草稿清扫 worker 持有单条草稿租约的时长（分钟）
	DraftPurgeLeaseMinutes int `yaml:"draft_purge_lease_minutes"`
}

func Load(filename string) (Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := Config{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config %s: %w", filename, err)
	}

	OverrideWithEnv(&cfg)
	return cfg, nil
}

func OverrideWithEnv(cfg *Config) {
	if cfg == nil {
		return
	}

	if os.Getenv("MODE") == "prod" {
		cfg.Dev = false
	} else {
		cfg.Dev = true
	}

	// 读取服务配置
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}

	// 读取数据库配置
	if v := os.Getenv("MYSQL_HOST"); v != "" {
		cfg.DB.Host = v
	}
	if v := os.Getenv("MYSQL_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.DB.Port = port
		}
	}
	if v := os.Getenv("MYSQL_USER"); v != "" {
		cfg.DB.User = v
	}
	// Password is environment-only, so discard any value supplied by a caller
	// before applying the supported environment variables.
	cfg.DB.Password = ""
	// MYSQL_PASSWORD 仅在没有设置 MYSQL_ROOT_PASSWORD 时生效，避免优先级歧义
	if v := os.Getenv("MYSQL_PASSWORD"); v != "" && os.Getenv("MYSQL_ROOT_PASSWORD") == "" {
		cfg.DB.Password = v
	}
	if v := os.Getenv("MYSQL_ROOT_PASSWORD"); v != "" {
		cfg.DB.Password = v
	}
	if v := os.Getenv("MYSQL_DATABASE"); v != "" {
		cfg.DB.DBName = v
	}
	// 读取保留期和清扫任务配置
	if v := os.Getenv("RETENTION_USER_DELETED_DAYS"); v != "" {
		if days, err := strconv.Atoi(v); err == nil {
			cfg.Retention.UserDeletedDays = days
		}
	}
	if v := os.Getenv("RETENTION_VIDEO_DELETED_DAYS"); v != "" {
		if days, err := strconv.Atoi(v); err == nil {
			cfg.Retention.VideoDeletedDays = days
		}
	}
	if v := os.Getenv("RETENTION_VIDEO_DRAFT_HOURS"); v != "" {
		if hours, err := strconv.Atoi(v); err == nil {
			cfg.Retention.VideoDraftHours = hours
		}
	}
	if v := os.Getenv("SWEEPER_INTERVAL_MINUTES"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil {
			cfg.Sweeper.IntervalMinutes = minutes
		}
	}
	if v := os.Getenv("SWEEPER_DRAFT_PURGE_LEASE_MINUTES"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil {
			cfg.Sweeper.DraftPurgeLeaseMinutes = minutes
		}
	}
}
