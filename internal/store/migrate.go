package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/yourorg/guanjin-next/migrations"
)

func MigrateUp(databaseURL string) error {
	m, closeFn, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = closeFn() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func MigrateDown(databaseURL string, steps int) error {
	m, closeFn, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = closeFn() }()

	if err := m.Steps(-steps); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func MigrateVersion(databaseURL string) (version uint, dirty bool, err error) {
	m, closeFn, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = closeFn() }()

	version, dirty, err = m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}

func newMigrator(databaseURL string) (*migrate.Migrate, func() error, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("open embedded migrations: %w", err)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open migration database: %w", err)
	}

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("init pgx migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("init migrator: %w", err)
	}

	closeFn := func() error {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			return srcErr
		}
		return dbErr
	}
	return m, closeFn, nil
}
