package store

import (
	"context"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

type Options struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func Open(opts Options) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  opts.DSN,
		PreferSimpleProtocol: false,
	}), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: false},
		NowFunc:        func() time.Time { return time.Now().UTC() },
		Logger:         logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if opts.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(opts.MaxIdleConns)
	}
	if opts.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}
	idle := opts.ConnMaxIdleTime
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	sqlDB.SetConnMaxIdleTime(idle)
	return db, nil
}

func Ping(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
