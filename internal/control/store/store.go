// Package store implements the control-plane persistence contracts on PostgreSQL.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidDatabase    = errors.New("store requires a database")
	ErrEventEpochMismatch = errors.New("observation event epoch does not match node")
	ErrStaleObservation   = errors.New("observation sequence is not newer")
	ErrArtifactBinding    = errors.New("artifact digest is bound to different metadata")
)

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Store is the composition root for every PostgreSQL repository adapter. New
// returns one value that can be passed directly to the auth, PKI, media,
// release, and assignment services.
type Store struct {
	db     *sql.DB
	bound  queryer
	random io.Reader
}

func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, ErrInvalidDatabase
	}
	return &Store{db: db, random: rand.Reader}, nil
}

// EnsureActor creates the persistence identity used by the domain adapters.
// It is deliberately idempotent and never overwrites an existing display name.
func (s *Store) EnsureActor(ctx context.Context, id, name string) error {
	if id == "" || name == "" {
		return errors.New("actor ID and name are required")
	}
	_, err := s.q(ctx).ExecContext(ctx, `
		INSERT INTO actors (id, name) VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING`, id, name)
	return wrap("ensure actor", err)
}

type transactionContextKey struct{}

func (s *Store) q(ctx context.Context) queryer {
	if s.bound != nil {
		return s.bound
	}
	if tx, ok := ctx.Value(transactionContextKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

func (s *Store) inTransaction(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if s.bound != nil {
		tx, ok := s.bound.(*sql.Tx)
		if !ok {
			return errors.New("bound store is not transactional")
		}
		return fn(ctx, tx)
	}
	if tx, ok := ctx.Value(transactionContextKey{}).(*sql.Tx); ok {
		return fn(ctx, tx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin transaction", err)
	}
	txCtx := context.WithValue(ctx, transactionContextKey{}, tx)
	if err := fn(txCtx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrap("commit transaction", err)
	}
	return nil
}

func (s *Store) transactionStore(tx *sql.Tx) *Store {
	return &Store{db: s.db, bound: tx, random: s.random}
}

func (s *Store) newUUID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(s.random, value[:]); err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(value[0:4]),
		hex.EncodeToString(value[4:6]),
		hex.EncodeToString(value[6:8]),
		hex.EncodeToString(value[8:10]),
		hex.EncodeToString(value[10:16])), nil
}

func postgresCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("PostgreSQL %s: %w", operation, err)
}
