package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig   `yaml:"server"`
	DB     DatabaseConfig `yaml:"database"`

	Dev bool
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
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

	// devMode or not
	if v := os.Getenv("MODE"); v != "" {
		cfg.Dev = v == "dev"
	} else {
		cfg.Dev = true
	}

	// server
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}

	// mysql
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
	if v := os.Getenv("MYSQL_ROOT_PASSWORD"); v != "" {
		cfg.DB.Password = v
	}
	if v := os.Getenv("MYSQL_PASSWORD"); v != "" {
		cfg.DB.Password = v
	}
	if v := os.Getenv("MYSQL_DATABASE"); v != "" {
		cfg.DB.DBName = v
	}
}
