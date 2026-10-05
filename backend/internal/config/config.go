package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig      `yaml:"server"`
	DB          DatabaseConfig    `yaml:"database"`
	Redis       RedisConfig       `yaml:"redis"`
	Feed        FeedConfig        `yaml:"feed"`
	Interaction InteractionConfig `yaml:"interaction"`
	RabbitMQ    RabbitMQConfig    `yaml:"rabbitmq"`
	Retention   RetentionConfig   `yaml:"retention"`
	Sweeper     SweeperConfig     `yaml:"sweeper"`
	Observe     ObserveConfig     `yaml:"observe"`

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

type RedisConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	DB   int    `yaml:"db"`
	// Password is supplied through REDIS_PASSWORD.
	Password string `yaml:"-"`
}

type RabbitMQConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	// Password is supplied through RABBITMQ_DEFAULT_PASS.
	Password string `yaml:"-"`
}

type FeedConfig struct {
	PageCacheEnabled      bool       `yaml:"page_cache_enabled"`
	CardCacheEnabled      bool       `yaml:"card_cache_enabled"` // 仅页缓存开启时装配
	PublishedEventEnabled bool       `yaml:"published_event_enabled"`
	CardWarmupEnabled     bool       `yaml:"card_warmup_enabled"`
	HeatConsumerEnabled   bool       `yaml:"heat_consumer_enabled"` // 互动 Relay 开启时必须同时开启
	Heat                  HeatConfig `yaml:"heat"`
}

type HeatConfig struct {
	Generation            string `yaml:"generation"` // 修改规则或重建时使用新代际
	WindowMinutes         int    `yaml:"window_minutes"`
	RetentionGraceMinutes int    `yaml:"retention_grace_minutes"` // 热窗口结束后的分钟桶保留时长
	DedupeTTLHours        int    `yaml:"dedupe_ttl_hours"`        // 须覆盖窗口、宽限及一整分钟桶
	LikeWeight            int    `yaml:"like_weight"`
	CommentWeight         int    `yaml:"comment_weight"`
	MaxVideosPerMinute    int    `yaml:"max_videos_per_minute"`
	MaxEventsPerMinute    int    `yaml:"max_events_per_minute"`
}

func DefaultHeatConfig() HeatConfig {
	return HeatConfig{
		Generation:            "initial",
		WindowMinutes:         60,
		RetentionGraceMinutes: 10,
		DedupeTTLHours:        24,
		LikeWeight:            3,
		CommentWeight:         5,
		MaxVideosPerMinute:    10000,
		MaxEventsPerMinute:    100000,
	}
}

type InteractionConfig struct {
	// EventsEnabled 开启四种互动写入的同事务事实记录；默认关闭
	EventsEnabled bool `yaml:"events_enabled"`
	RelayEnabled  bool `yaml:"relay_enabled"`
}

type ObserveConfig struct {
	// Metrics 控制指标出口；默认关闭，启用时只接受字面量回环地址
	Metrics MetricsConfig `yaml:"metrics"`
}

type MetricsConfig struct {
	// Enabled 默认关闭；开启后指标出口使用独立回环监听
	Enabled bool `yaml:"enabled"`
	// Addr 只接受 127.0.0.1 或 ::1 加端口，留空时使用默认回环地址
	Addr string `yaml:"addr"`
}

// ErrPublishedWithoutCardWarmup 表示开启了发布事件却没有让本进程承担预热消费
var ErrPublishedWithoutCardWarmup = errors.New("feed published events require card warmup consumption in this worker")

// ValidateFeedRuntime 校验发布事件与本进程预热消费的开关组合
func (c Config) ValidateFeedRuntime() error {
	if c.Feed.PublishedEventEnabled && !c.Feed.CardWarmupEnabled {
		return ErrPublishedWithoutCardWarmup
	}
	return nil
}

// ValidateHeatRuntime 防止本进程只派发互动事件而没有运行热度消费
func (c Config) ValidateHeatRuntime() error {
	if c.Interaction.RelayEnabled && !c.Feed.HeatConsumerEnabled {
		return errors.New("interaction relay requires heat consumption in this worker")
	}
	if c.Feed.HeatConsumerEnabled {
		h := c.Feed.Heat
		if h.WindowMinutes < 1 || h.WindowMinutes > 1440 || h.RetentionGraceMinutes < 0 || h.RetentionGraceMinutes > 1440 ||
			h.DedupeTTLHours < 1 || h.DedupeTTLHours > 168 || h.DedupeTTLHours*60 < h.WindowMinutes+h.RetentionGraceMinutes+1 ||
			h.LikeWeight < 1 || h.LikeWeight > 1000 || h.CommentWeight < 1 || h.CommentWeight > 1000 ||
			h.MaxVideosPerMinute < 1 || h.MaxVideosPerMinute > 100000 ||
			h.MaxEventsPerMinute < h.MaxVideosPerMinute || h.MaxEventsPerMinute > 1000000 {
			return errors.New("invalid feed heat configuration")
		}
	}
	return nil
}

type RetentionConfig struct {
	// UserDeletedDays 注销账号从软删除到硬删除的保留天数
	UserDeletedDays int `yaml:"user_deleted_days"`
	// VideoDeletedDays 视频从软删除到清扫媒体和硬删除记录的保留天数
	VideoDeletedDays int `yaml:"video_deleted_days"`
	// VideoDraftHours 未完成草稿从创建到清扫媒体和硬删除记录的保留小时数
	VideoDraftHours int `yaml:"video_draft_hours"`
	// MediaOrphanHours 媒体落盘后未被任一记录引用时的最小保留时长
	MediaOrphanHours int `yaml:"media_orphan_hours"`
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

	cfg := Config{Feed: FeedConfig{Heat: DefaultHeatConfig()}}
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

	// 读取 Redis 配置
	if v := os.Getenv("REDIS_HOST"); v != "" {
		cfg.Redis.Host = v
	}
	if v := os.Getenv("REDIS_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Redis.Port = port
		}
	}
	if v := os.Getenv("REDIS_DB"); v != "" {
		if db, err := strconv.Atoi(v); err == nil {
			cfg.Redis.DB = db
		}
	}
	// Password is environment-only, so discard any value supplied by a caller
	// before applying the supported environment variable.
	cfg.Redis.Password = ""
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("FEED_PAGE_CACHE_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Feed.PageCacheEnabled = err == nil && enabled
	}
	if v := os.Getenv("FEED_CARD_CACHE_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Feed.CardCacheEnabled = err == nil && enabled
	}
	if v := os.Getenv("FEED_PUBLISHED_EVENT_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Feed.PublishedEventEnabled = err == nil && enabled
	}
	if v := os.Getenv("FEED_CARD_WARMUP_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Feed.CardWarmupEnabled = err == nil && enabled
	}
	if v := os.Getenv("FEED_HEAT_CONSUMER_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Feed.HeatConsumerEnabled = err == nil && enabled
	}
	if v := os.Getenv("FEED_HEAT_GENERATION"); v != "" {
		cfg.Feed.Heat.Generation = v
	}
	for name, target := range map[string]*int{
		"FEED_HEAT_WINDOW_MINUTES":          &cfg.Feed.Heat.WindowMinutes,
		"FEED_HEAT_RETENTION_GRACE_MINUTES": &cfg.Feed.Heat.RetentionGraceMinutes,
		"FEED_HEAT_DEDUPE_TTL_HOURS":        &cfg.Feed.Heat.DedupeTTLHours,
		"FEED_HEAT_LIKE_WEIGHT":             &cfg.Feed.Heat.LikeWeight,
		"FEED_HEAT_COMMENT_WEIGHT":          &cfg.Feed.Heat.CommentWeight,
		"FEED_HEAT_MAX_VIDEOS_PER_MINUTE":   &cfg.Feed.Heat.MaxVideosPerMinute,
		"FEED_HEAT_MAX_EVENTS_PER_MINUTE":   &cfg.Feed.Heat.MaxEventsPerMinute,
	} {
		if value := os.Getenv(name); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				parsed = -1
			}
			*target = parsed
		}
	}
	if v := os.Getenv("INTERACTION_EVENTS_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Interaction.EventsEnabled = err == nil && enabled
	}
	if v := os.Getenv("INTERACTION_RELAY_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Interaction.RelayEnabled = err == nil && enabled
	}

	// 读取观测出口配置；非法布尔值按关闭处理
	if v := os.Getenv("OBSERVE_METRICS_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		cfg.Observe.Metrics.Enabled = err == nil && enabled
	}
	if v := os.Getenv("OBSERVE_METRICS_ADDR"); v != "" {
		cfg.Observe.Metrics.Addr = v
	}

	// 读取 RabbitMQ 配置
	if v := os.Getenv("RABBITMQ_HOST"); v != "" {
		cfg.RabbitMQ.Host = v
	}
	if v := os.Getenv("RABBITMQ_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.RabbitMQ.Port = port
		}
	}
	// Reuse the image bootstrap variable so Compose and future AMQP clients
	// receive one credential source from backend/.env.
	if v := os.Getenv("RABBITMQ_DEFAULT_USER"); v != "" {
		cfg.RabbitMQ.Username = v
	}
	cfg.RabbitMQ.Password = ""
	if v := os.Getenv("RABBITMQ_DEFAULT_PASS"); v != "" {
		cfg.RabbitMQ.Password = v
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
	if v := os.Getenv("RETENTION_MEDIA_ORPHAN_HOURS"); v != "" {
		if hours, err := strconv.Atoi(v); err == nil {
			cfg.Retention.MediaOrphanHours = hours
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
