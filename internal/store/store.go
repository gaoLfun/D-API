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

//go:embed reliability_migration.sql
var reliabilityMigration string

type Store struct {
	cleanupMu        sync.RWMutex
	cleanupReport    CleanupReport
	usageReports     usageReportCache
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
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create migrations: %w", err)
	}
	migrations := []string{schema, `ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS pricing_version INTEGER NOT NULL DEFAULT 1; ALTER TABLE request_logs ALTER COLUMN pricing_version SET DEFAULT 2;`, reliabilityMigration, `ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS config_version BIGINT NOT NULL DEFAULT 1;`}
	for index, migration := range migrations {
		var applied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, index+1).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			return fmt.Errorf("migration %d: %w", index+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, index+1); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }
