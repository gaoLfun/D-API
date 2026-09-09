package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/lib/pq"
)

var ErrReadonlyForbidden = errors.New("readonly forbidden")

// Only a hash is stored. Scope is an explicit allowlist, never an implicit wildcard.
type UsageCredential struct {
	ID         int64      `json:"id,string"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	StationIDs []int64    `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func (s *Store) CreateUsageCredential(ctx context.Context, name string, hash []byte, ids []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO usage_credentials(name,credential_hash) VALUES($1,$2) RETURNING id`, name, hash).Scan(&id); err != nil {
		return 0, err
	}
	for _, station := range ids {
		if _, err = tx.ExecContext(ctx, `INSERT INTO usage_credential_stations(credential_id,upstream_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, station); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (s *Store) UpdateUsageCredential(ctx context.Context, id int64, enabled bool, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE usage_credentials SET enabled=$2 WHERE id=$1 AND revoked_at IS NULL`, id, enabled)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM usage_credential_stations WHERE credential_id=$1`, id); err != nil {
		return err
	}
	for _, station := range ids {
		if _, err = tx.ExecContext(ctx, `INSERT INTO usage_credential_stations(credential_id,upstream_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, station); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) RevokeUsageCredential(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE usage_credentials SET revoked_at=COALESCE(revoked_at,now()),enabled=false WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) ListUsageCredentials(ctx context.Context) ([]UsageCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.name,c.enabled,c.created_at,c.revoked_at,COALESCE(array_agg(s.upstream_id ORDER BY s.upstream_id) FILTER (WHERE s.upstream_id IS NOT NULL),'{}') FROM usage_credentials c LEFT JOIN usage_credential_stations s ON s.credential_id=c.id GROUP BY c.id ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []UsageCredential{}
	for rows.Next() {
		var c UsageCredential
		if err = rows.Scan(&c.ID, &c.Name, &c.Enabled, &c.CreatedAt, &c.RevokedAt, pq.Array(&c.StationIDs)); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

type ReadonlyBalance struct {
	Source    string     `json:"source"`
	Status    string     `json:"status"`
	Available *float64   `json:"available"`
	Used      *float64   `json:"used"`
	Currency  *string    `json:"currency"`
	Unlimited bool       `json:"unlimited"`
	UpdatedAt *time.Time `json:"updated_at"`
}
type ReadonlyToday struct {
	KnownInput     int64    `json:"known_input_tokens"`
	KnownOutput    int64    `json:"known_output_tokens"`
	KnownTotal     int64    `json:"known_total_tokens"`
	InputCoverage  *float64 `json:"input_coverage"`
	OutputCoverage *float64 `json:"output_coverage"`
	Requests       int64    `json:"requests"`
	Input          *int64   `json:"input_tokens"`
	Output         *int64   `json:"output_tokens"`
	Total          *int64   `json:"total_tokens"`
	Cost           *float64 `json:"estimated_cost_usd"`
	Coverage       *float64 `json:"cost_coverage"`
}
type ReadonlyGroup struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}
type ReadonlyStation struct {
	UpstreamToday *core.UpstreamToday `json:"upstream_today"`
	Enabled       bool                `json:"enabled"`
	Priority      int                 `json:"priority"`
	Groups        []ReadonlyGroup     `json:"groups"`
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Balance       ReadonlyBalance     `json:"balance"`
	Today         ReadonlyToday       `json:"today"`
}
type ReadonlyUsage struct {
	Version     int               `json:"version"`
	GeneratedAt time.Time         `json:"generated_at"`
	Timezone    string            `json:"timezone"`
	Stations    []ReadonlyStation `json:"stations"`
}

// Query deliberately has no response/auth cache. The shared credential lock makes
// scope edits and revocation atomic relative to authentication and data selection.
func (s *Store) ReadonlyUsage(ctx context.Context, hash []byte, now time.Time, staleAfter time.Duration) (ReadonlyUsage, int64, error) {
	result := ReadonlyUsage{Version: 1, GeneratedAt: now.UTC(), Timezone: "UTC", Stations: []ReadonlyStation{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, 0, err
	}
	defer tx.Rollback()
	var id int64
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT id,enabled FROM usage_credentials WHERE credential_hash=$1 AND revoked_at IS NULL FOR SHARE`, hash).Scan(&id, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return result, 0, ErrNotFound
	}
	if err != nil {
		return result, 0, err
	}
	if !enabled {
		return result, id, ErrReadonlyForbidden
	}
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.name,u.kind,u.balance,u.enabled,u.priority,
 COALESCE((SELECT json_agg(json_build_object('id',g.id::text,'name',g.name,'enabled',g.enabled) ORDER BY g.id) FROM group_upstreams gu JOIN groups g ON g.id=gu.group_id WHERE gu.upstream_id=u.id),'[]'::json),
 COALESCE(sum(d.requests),0),COALESCE(sum(d.input_tokens),0),COALESCE(sum(d.output_tokens),0),
 COALESCE(sum(d.usage_requests),0),COALESCE(sum(d.cost_usd),0),COALESCE(sum(d.cost_known_requests),0),COALESCE(sum(d.output_usage_requests),0)
 FROM usage_credential_stations a JOIN upstreams u ON u.id=a.upstream_id
 LEFT JOIN daily_usage d ON d.upstream_id=u.id AND d.day=$2::date
 WHERE a.credential_id=$1 AND u.enabled GROUP BY u.id ORDER BY u.priority,u.id`, id, now.UTC().Format("2006-01-02"))
	if err != nil {
		return result, id, err
	}
	for rows.Next() {
		var station ReadonlyStation
		var sid, input, output, knownUsage, knownCost, knownOutput int64
		var cost float64
		var raw, groups []byte
		var kind string
		if err = rows.Scan(&sid, &station.Name, &kind, &raw, &station.Enabled, &station.Priority, &groups, &station.Today.Requests, &input, &output, &knownUsage, &cost, &knownCost, &knownOutput); err != nil {
			rows.Close()
			return result, id, err
		}
		if err = json.Unmarshal(groups, &station.Groups); err != nil {
			rows.Close()
			return result, id, err
		}
		var balance core.Balance
		if err = json.Unmarshal(raw, &balance); err != nil {
			rows.Close()
			return result, id, err
		}
		// Old Sub2API USD may have been a parser fallback, not an actual unit.
		if kind == "sub2api" && balance.Currency == "USD" && !balance.CurrencyReported {
			balance.Currency = ""
		}
		station.ID = strconv.FormatInt(sid, 10)
		station.Balance = ProjectReadonlyBalance(balance, now, staleAfter)
		if (balance.Source == "sub2api_usage" || balance.Source == "newapi_account") && (station.Balance.Status == "ok" || station.Balance.Status == "stale") {
			station.UpstreamToday = balance.Today
		}
		station.Today.KnownInput = input
		station.Today.KnownOutput = output
		station.Today.KnownTotal = input + output
		if station.Today.Requests > 0 {
			inputCoverage := float64(knownUsage) / float64(station.Today.Requests)
			outputCoverage := float64(knownOutput) / float64(station.Today.Requests)
			if inputCoverage >= 0 && inputCoverage <= 1 {
				station.Today.InputCoverage = &inputCoverage
			}
			if outputCoverage >= 0 && outputCoverage <= 1 {
				station.Today.OutputCoverage = &outputCoverage
			}
		}
		if station.Today.Requests == 0 || knownUsage == station.Today.Requests {
			station.Today.Input = &input
		}
		if station.Today.Requests == 0 || knownOutput == station.Today.Requests {
			station.Today.Output = &output
		}
		if station.Today.Input != nil && station.Today.Output != nil {
			total := input + output
			station.Today.Total = &total
		}

		if knownCost > 0 {
			station.Today.Cost = &cost
		}
		if station.Today.Requests > 0 {
			coverage := float64(knownCost) / float64(station.Today.Requests)
			if coverage >= 0 && coverage <= 1 {
				station.Today.Coverage = &coverage
			}
		}
		result.Stations = append(result.Stations, station)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, id, err
	}
	return result, id, tx.Commit()
}

// Never serialize core.Balance directly: its Error may contain upstream secrets.
func ProjectReadonlyBalance(b core.Balance, now time.Time, staleAfter time.Duration) ReadonlyBalance {
	out := ReadonlyBalance{Status: "error"}
	switch b.Source {
	case "sub2api_usage", "newapi_account", "newapi_token", "billing_subscription":
		out.Source = b.Source
	}
	if b.Status == "unsupported" || (b.Status == "unknown" && b.Error == "balance API unsupported") {
		out.Status = "unsupported"
		return out
	}
	if b.Status != "ok" {
		return out
	}
	out.Status = "ok"
	out.Available = b.Available
	out.Used = b.Used
	out.Unlimited = b.Unlimited
	out.UpdatedAt = b.LastSuccess
	if out.UpdatedAt == nil {
		out.UpdatedAt = b.UpdatedAt
	}
	if b.Currency != "" {
		out.Currency = &b.Currency
	}
	if out.Unlimited {
		out.Available = nil
	}
	if out.UpdatedAt == nil || now.Sub(*out.UpdatedAt) > staleAfter {
		out.Status = "stale"
	}
	return out
}
