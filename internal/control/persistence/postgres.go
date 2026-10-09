package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const migrationAdvisoryLockKey int64 = 0x6361626c656d6967

var ErrSchemaDrift = errors.New("database migration history does not match available migrations")

type PoolConfig struct {
	MaxOpenConnections int
	MaxIdleConnections int
	ConnectionLifetime time.Duration
}

func OpenPostgres(ctx context.Context, databaseURL string, config PoolConfig) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(config.MaxOpenConnections)
	db.SetMaxIdleConns(config.MaxIdleConnections)
	db.SetConnMaxLifetime(config.ConnectionLifetime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return db, nil
}

type MigrationSession interface {
	AppliedMigrations(context.Context) ([]string, error)
	ApplyMigration(context.Context, Migration) error
}

type MigrationLocker interface {
	WithMigrationLock(context.Context, func(MigrationSession) error) error
}

func Migrate(ctx context.Context, locker MigrationLocker, migrations []Migration) error {
	if err := validateMigrationOrder(migrations); err != nil {
		return err
	}
	return locker.WithMigrationLock(ctx, func(session MigrationSession) error {
		applied, err := session.AppliedMigrations(ctx)
		if err != nil {
			return fmt.Errorf("read migration history: %w", err)
		}
		if len(applied) > len(migrations) {
			return fmt.Errorf("%w: database has %d migrations, binary has %d", ErrSchemaDrift, len(applied), len(migrations))
		}
		for index, name := range applied {
			if migrations[index].Name != name {
				return fmt.Errorf("%w: database migration %d is %q, expected %q", ErrSchemaDrift, index+1, name, migrations[index].Name)
			}
		}
		for _, migration := range migrations[len(applied):] {
			if err := session.ApplyMigration(ctx, migration); err != nil {
				return fmt.Errorf("apply migration %s: %w", migration.Name, err)
			}
		}
		return nil
	})
}

func ApplyMigrations(ctx context.Context, db *sql.DB, filesystem fs.FS) error {
	migrations, err := LoadMigrations(filesystem)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	if err := Migrate(ctx, postgresMigrationLocker{db: db}, migrations); err != nil {
		return fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	return nil
}

func validateMigrationOrder(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("no migrations supplied")
	}
	for index, migration := range migrations {
		if migration.Name == "" || migration.Up == "" {
			return fmt.Errorf("invalid migration at position %d", index+1)
		}
		if index > 0 && migrations[index-1].Name >= migration.Name {
			return fmt.Errorf("migrations are not strictly ordered at %q", migration.Name)
		}
	}
	return nil
}

type postgresMigrationLocker struct {
	db *sql.DB
}

func (locker postgresMigrationLocker) WithMigrationLock(ctx context.Context, apply func(MigrationSession) error) error {
	tx, err := locker.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS cuelight_schema_migrations (
    name text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("initialize migration history: %w", err)
	}
	if err := apply(postgresMigrationSession{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

type postgresMigrationSession struct {
	tx *sql.Tx
}

func (session postgresMigrationSession) AppliedMigrations(ctx context.Context) ([]string, error) {
	rows, err := session.tx.QueryContext(ctx, "SELECT name FROM cuelight_schema_migrations ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

func (session postgresMigrationSession) ApplyMigration(ctx context.Context, migration Migration) error {
	if _, err := session.tx.ExecContext(ctx, migration.Up); err != nil {
		return err
	}
	if _, err := session.tx.ExecContext(ctx, "INSERT INTO cuelight_schema_migrations (name) VALUES ($1)", migration.Name); err != nil {
		return err
	}
	return nil
}
