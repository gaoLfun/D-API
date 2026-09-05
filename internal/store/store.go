package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"sync"
	"time"

	"github.com/gaoLfun/dapi/internal/cryptox"
	_ "github.com/lib/pq"
)

//go:embed schema.sql
var schema string

type Store struct {
	db               *sql.DB
	box              *cryptox.SecretBox
	pricingMu        sync.RWMutex
	pricingCache     map[pricingCacheKey]pricingCacheEntry
	cacheMu          sync.RWMutex
	authCache        map[string]authCacheEntry
	routeCache       map[routeCacheKey]routeCacheEntry
	maxAttempts      maxAttemptsCacheEntry
	authGen          uint64
	routeGen         uint64
	maxAttemptsGen   uint64
	routeLoads       loadGate[routeCacheKey]
	authLoads        loadGate[string]
	pricingLoads     loadGate[pricingCacheKey]
	pricingGen       uint64
	dashboardLoads   loadGate[int]
	dashboardValue   Dashboard
	dashboardExpires time.Time
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
	return &Store{
		db: db, box: box,
		pricingCache: make(map[pricingCacheKey]pricingCacheEntry),
		authCache:    make(map[string]authCacheEntry),
		routeCache:   make(map[routeCacheKey]routeCacheEntry),
	}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	// Serialize DDL across replicas while keeping the lock scoped to this
	// transaction and connection.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(739842106)`); err != nil {
		return fmt.Errorf("lock migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }
