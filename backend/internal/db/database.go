package db

import (
	"fmt"
	"gofeed/internal/config"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func NewDB(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.DBName)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// check the db 
// if db is not exists, this method will create it automatically.
func EnsureDatabase(cfg config.DatabaseConfig) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Port)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect to MySQL server: %w", err)
	}

	DB, err := db.DB()
	if err != nil {
		return err
	}
	defer DB.Close()

	if err := db.Exec(fmt.Sprintf(CreateDatabaseTpl, cfg.DBName)).Error; err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}
	return nil
}

// Migrate runs GORM AutoMigrate on the given models.
func Migrate(db *gorm.DB, models ...any) error {
	return db.AutoMigrate(models...)
}

// MigrateAll creates all tables defined in sql.go if they don't exist yet.
func MigrateAll(db *gorm.DB) error {
	for _, ddl := range allTables {
		if err := db.Exec(ddl).Error; err != nil {
			return fmt.Errorf("migrate failed: %w", err)
		}
	}
	return nil
}

func Close(db *gorm.DB) error {
	DB, err := db.DB()
	if err != nil {
		return err
	}
	return DB.Close()
}
