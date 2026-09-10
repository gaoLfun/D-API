package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const cleanupBatchSize = 1000

type CleanupTableResult struct {
	Table           string `json:"table"`
	Deleted         int64  `json:"deleted"`
	DurationMS      int64  `json:"duration_ms"`
	BudgetExhausted bool   `json:"budget_exhausted"`
	Failed          bool   `json:"failed"`
}
type CleanupReport struct {
	StartedAt  time.Time            `json:"started_at"`
	DurationMS int64                `json:"duration_ms"`
	Skipped    bool                 `json:"skipped"`
	Tables     []CleanupTableResult `json:"tables"`
}

func (s *Store) CleanupStatus() CleanupReport {
	s.cleanupMu.RLock()
	defer s.cleanupMu.RUnlock()
	report := s.cleanupReport
	report.Tables = append([]CleanupTableResult(nil), report.Tables...)
	return report
}

func (s *Store) CleanupRetention(ctx context.Context, logsBefore, dailyBefore, hourlyBefore time.Time) (CleanupReport, error) {
	return s.cleanupRetention(ctx, logsBefore, dailyBefore, hourlyBefore, 15*time.Second)
}

func (s *Store) cleanupRetention(ctx context.Context, logsBefore, dailyBefore, hourlyBefore time.Time, tableBudget time.Duration) (report CleanupReport, resultErr error) {
	report.StartedAt = time.Now().UTC()
	defer func() {
		report.DurationMS = time.Since(report.StartedAt).Milliseconds()
		s.cleanupMu.Lock()
		s.cleanupReport = report
		s.cleanupMu.Unlock()
	}()
	roundCtx, cancel := context.WithTimeout(ctx, 5*tableBudget+5*time.Second)
	defer cancel()
	// Hold an otherwise empty transaction on one connection; deletions commit
	// independently in small batches on the pool. Rollback always releases the lock.
	lock, err := s.db.BeginTx(roundCtx, nil)
	if err != nil {
		return report, err
	}
	defer lock.Rollback()
	var acquired bool
	if err := lock.QueryRowContext(roundCtx, `SELECT pg_try_advisory_xact_lock(hashtext(current_schema()),739842107)`).Scan(&acquired); err != nil {
		return report, err
	}
	if !acquired {
		report.Skipped = true
		return report, nil
	}
	for _, plan := range []struct {
		table  string
		before time.Time
	}{
		{"request_logs", logsBefore}, {"audit_logs", logsBefore}, {"alert_events", logsBefore}, {"daily_usage", dailyBefore}, {"hourly_usage", hourlyBefore},
	} {
		if err := roundCtx.Err(); err != nil {
			return report, errors.Join(resultErr, err)
		}
		tableCtx, stop := context.WithTimeout(roundCtx, tableBudget)
		started := time.Now()
		deleted, err := s.cleanupBatches(tableCtx, plan.table, plan.before)
		exhausted := errors.Is(tableCtx.Err(), context.DeadlineExceeded)
		stop()
		report.Tables = append(report.Tables, CleanupTableResult{Table: plan.table, Deleted: deleted, DurationMS: time.Since(started).Milliseconds(), BudgetExhausted: exhausted, Failed: err != nil && !exhausted})
		if err != nil && !exhausted {
			resultErr = errors.Join(resultErr, fmt.Errorf("cleanup %s: %w", plan.table, err))
		}
	}
	return report, resultErr
}

func (s *Store) cleanupBatches(ctx context.Context, table string, before time.Time) (int64, error) {
	var query string
	switch table {
	case "daily_usage":
		query = `DELETE FROM daily_usage WHERE ctid IN (SELECT ctid FROM daily_usage WHERE day < ($1 AT TIME ZONE 'UTC')::date ORDER BY day LIMIT $2)`
	case "hourly_usage":
		query = `DELETE FROM hourly_usage WHERE ctid IN (SELECT ctid FROM hourly_usage WHERE hour < $1 ORDER BY hour LIMIT $2)`
	case "request_logs", "audit_logs", "alert_events":
		query = `DELETE FROM ` + table + ` WHERE id IN (SELECT id FROM ` + table + ` WHERE created_at < $1 ORDER BY created_at,id LIMIT $2)`
	default:
		return 0, errors.New("unsupported cleanup table")
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		result, err := s.db.ExecContext(ctx, query, before, cleanupBatchSize)
		if err != nil {
			return total, err
		}
		deleted, err := result.RowsAffected()
		total += deleted
		if err != nil || deleted < cleanupBatchSize {
			return total, err
		}
		// Yield I/O between committed batches and make the pause cancellable.
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return total, ctx.Err()
		case <-timer.C:
		}
	}
}
