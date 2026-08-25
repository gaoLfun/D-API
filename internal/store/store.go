package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"github.com/gaoLfun/dapi/internal/cryptox"
	_ "github.com/lib/pq"
)

//go:embed schema.sql
var schema string

type Store struct {
	db  *sql.DB
	box *cryptox.SecretBox
}

func Open(ctx context.Context, databaseURL string, box *cryptox.SecretBox) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	return &Store{db: db, box: box}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }
