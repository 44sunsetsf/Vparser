// Package repo holds the GORM data access layer. The schema is owned by versioned golang-migrate
// migrations applied at startup; AutoMigrate is never used, so schema changes are reviewable SQL.
package repo

import (
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"

	"dovideo/server/internal/config"
	"dovideo/server/internal/migrations"
)

func driverConfig(c *config.Config) (*mysql.Config, error) {
	loc, err := time.LoadLocation(c.DBTimezone)
	if err != nil {
		return nil, fmt.Errorf("invalid DB_TIMEZONE %q: %w", c.DBTimezone, err)
	}
	cfg := mysql.NewConfig()
	cfg.User = c.DBUser
	cfg.Passwd = c.DBPassword
	cfg.Net = "tcp"
	cfg.Addr = c.DBHost + ":" + c.DBPort
	cfg.DBName = c.DBName
	cfg.ParseTime = true
	cfg.Loc = loc
	cfg.Timeout = 3 * time.Second
	cfg.MultiStatements = true           // idempotent migrations use SET/PREPARE/EXECUTE
	cfg.Collation = "utf8mb4_unicode_ci" // utf8mb4 connection charset (a "charset" param would be sent as SET charset=…, which MySQL rejects)
	cfg.AllowNativePasswords = true
	return cfg, nil
}

// Migrate applies the embedded migrations before the runtime pool opens.
func Migrate(c *config.Config) error {
	cfg, err := driverConfig(c)
	if err != nil {
		return err
	}
	db, err := openSQL(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	drv, err := migratemysql.WithInstance(db, &migratemysql.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrate source: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "mysql", drv)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Open connects GORM with a bounded pool and registers the OpenTelemetry plugin so every query
// becomes a child span of the request or task that issued it.
func Open(c *config.Config) (*gorm.DB, error) {
	cfg, err := driverConfig(c)
	if err != nil {
		return nil, err
	}
	// Runtime pool does not need multi-statements.
	cfg.MultiStatements = false
	db, err := gorm.Open(gormmysql.Open(cfg.FormatDSN()), &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	if err := db.Use(tracing.NewPlugin(tracing.WithoutMetrics(), tracing.WithoutQueryVariables())); err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(c.DBPoolMax)
	sqlDB.SetMaxIdleConns(c.DBPoolMinIdle)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}
