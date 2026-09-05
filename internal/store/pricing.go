package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/lib/pq"
)

var ErrInvalidPricing = errors.New("invalid pricing")

const liteLLMPriceURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

var liteLLMProfiles = []struct {
	Name     string
	Provider string
	Prefix   string
}{
	{Name: "OpenAI", Provider: "openai"},
	{Name: "Anthropic", Provider: "anthropic"},
	{Name: "Google Gemini", Provider: "gemini", Prefix: "gemini/"},
}

type liteLLMPrice struct {
	Provider                    string   `json:"litellm_provider"`
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostAlt  *float64 `json:"input_cost_per_token_cache_hit"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
}

type pricingCacheKey struct {
	upstreamID int64
	model      string
}

type pricingCacheEntry struct {
	input, output, cacheRead, cacheWrite float64
	found                                bool
	expires                              time.Time
	prices                               []PricingModelPrice
	partial                              bool
	from, to                             *time.Time
}

func (entry pricingCacheEntry) covers(at time.Time) bool {
	return !entry.partial || (entry.from == nil || !at.Before(*entry.from)) && (entry.to == nil || at.Before(*entry.to))
}

type PricingModelPrice struct {
	ID                      int64      `json:"id,omitempty"`
	Model                   string     `json:"model"`
	InputUSDPerMillion      float64    `json:"input_usd_per_million"`
	OutputUSDPerMillion     float64    `json:"output_usd_per_million"`
	CacheReadUSDPerMillion  float64    `json:"cache_read_usd_per_million"`
	CacheWriteUSDPerMillion float64    `json:"cache_write_usd_per_million"`
	ValidFrom               time.Time  `json:"valid_from"`
	ValidTo                 *time.Time `json:"valid_to,omitempty"`
	Source                  string     `json:"source,omitempty"`
}

type PricingProfile struct {
	ID              int64               `json:"id"`
	Name            string              `json:"name"`
	Provider        string              `json:"provider"`
	SourceURL       string              `json:"source_url"`
	SourceVersion   string              `json:"source_version"`
	LastRefreshedAt *time.Time          `json:"last_refreshed_at,omitempty"`
	Prices          []PricingModelPrice `json:"prices"`
}

type PricingBackfillResult struct {
	LogsUpdated   int64     `json:"logs_updated"`
	DailyUpdated  int64     `json:"daily_updated"`
	HourlyUpdated int64     `json:"hourly_updated"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
}

func (s *Store) USDCNYRate(ctx context.Context) (float64, error) {
	var value float64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((value #>> '{}')::double precision,7.2) FROM settings WHERE key='usd_cny_rate'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 7.2, nil
	}
	if err != nil {
		return 0, err
	}
	if value < 0.1 || value > 100 {
		return 7.2, nil
	}
	return value, nil
}

func (s *Store) SetUSDCNYRate(ctx context.Context, value float64) error {
	if value < 0.1 || value > 100 {
		return errors.New("invalid exchange rate")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('usd_cny_rate',to_jsonb($1::double precision)) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=now()`, value)
	return err
}

func (s *Store) ListPricingProfiles(ctx context.Context) ([]PricingProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,provider,source_url,source_version,last_refreshed_at FROM pricing_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]PricingProfile, 0)
	for rows.Next() {
		var profile PricingProfile
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.Provider, &profile.SourceURL, &profile.SourceVersion, &profile.LastRefreshedAt); err != nil {
			return nil, err
		}
		profile.Prices = []PricingModelPrice{}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	priceRows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (profile_id,model) id,profile_id,model,input_usd_per_million,output_usd_per_million,cache_read_usd_per_million,cache_write_usd_per_million,valid_from,valid_to,source
		FROM pricing_model_prices
		WHERE valid_from <= now() AND (valid_to IS NULL OR valid_to > now())
		ORDER BY profile_id,model,valid_from DESC`)
	if err != nil {
		return nil, err
	}
	defer priceRows.Close()
	byID := make(map[int64]int, len(profiles))
	for i := range profiles {
		byID[profiles[i].ID] = i
	}
	for priceRows.Next() {
		var price PricingModelPrice
		var profileID int64
		if err := priceRows.Scan(&price.ID, &profileID, &price.Model, &price.InputUSDPerMillion, &price.OutputUSDPerMillion, &price.CacheReadUSDPerMillion, &price.CacheWriteUSDPerMillion, &price.ValidFrom, &price.ValidTo, &price.Source); err != nil {
			return nil, err
		}
		if index, ok := byID[profileID]; ok {
			profiles[index].Prices = append(profiles[index].Prices, price)
		}
	}
	return profiles, priceRows.Err()
}

func (s *Store) SavePricingProfile(ctx context.Context, profile PricingProfile) (int64, error) {
	name := strings.TrimSpace(profile.Name)
	if name == "" || len([]rune(name)) > 200 || len(profile.Prices) > 2000 {
		return 0, ErrInvalidPricing
	}
	now := time.Now().UTC()
	for _, price := range profile.Prices {
		model := strings.TrimSpace(price.Model)
		if model == "" || len([]rune(model)) > 200 || price.InputUSDPerMillion < 0 || price.OutputUSDPerMillion < 0 || price.CacheReadUSDPerMillion < 0 || price.CacheWriteUSDPerMillion < 0 {
			return 0, ErrInvalidPricing
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id, err := savePricingProfileTx(ctx, tx, profile, now)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.invalidatePricingCache()
	return id, nil
}

func savePricingProfileTx(ctx context.Context, tx *sql.Tx, profile PricingProfile, now time.Time) (int64, error) {
	name := strings.TrimSpace(profile.Name)
	models := make([]string, 0, len(profile.Prices))
	for _, price := range profile.Prices {
		models = append(models, strings.TrimSpace(price.Model))
	}
	var id int64
	var err error
	if profile.ID > 0 {
		err = tx.QueryRowContext(ctx, `UPDATE pricing_profiles SET name=$1,provider=$2,source_url=$3,source_version=$4,updated_at=now() WHERE id=$5 RETURNING id`, name, strings.TrimSpace(profile.Provider), strings.TrimSpace(profile.SourceURL), strings.TrimSpace(profile.SourceVersion), profile.ID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
	} else {
		err = tx.QueryRowContext(ctx, `INSERT INTO pricing_profiles(name,provider,source_url,source_version) VALUES($1,$2,$3,$4) RETURNING id`, name, strings.TrimSpace(profile.Provider), strings.TrimSpace(profile.SourceURL), strings.TrimSpace(profile.SourceVersion)).Scan(&id)
	}
	if err != nil {
		return 0, err
	}
	if profile.ID > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE pricing_model_prices SET valid_to=GREATEST(valid_from,$2)
			WHERE profile_id=$1 AND valid_to IS NULL AND NOT (model=ANY($3))`, id, now, pq.Array(models)); err != nil {
			return 0, err
		}
	}
	for _, price := range profile.Prices {
		model := strings.TrimSpace(price.Model)
		validFrom := price.ValidFrom
		if validFrom.IsZero() {
			validFrom = now
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE pricing_model_prices SET valid_to=$3
			WHERE profile_id=$1 AND model=$2 AND valid_from < $3 AND valid_to IS NULL`, id, model, validFrom); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO pricing_model_prices(profile_id,model,input_usd_per_million,output_usd_per_million,cache_read_usd_per_million,cache_write_usd_per_million,valid_from,source)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(profile_id,model,valid_from) DO UPDATE SET input_usd_per_million=EXCLUDED.input_usd_per_million,output_usd_per_million=EXCLUDED.output_usd_per_million,cache_read_usd_per_million=EXCLUDED.cache_read_usd_per_million,cache_write_usd_per_million=EXCLUDED.cache_write_usd_per_million,source=EXCLUDED.source`,
			id, model, price.InputUSDPerMillion, price.OutputUSDPerMillion, price.CacheReadUSDPerMillion, price.CacheWriteUSDPerMillion, validFrom, strings.TrimSpace(price.Source)); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (s *Store) SetUpstreamPricingProfile(ctx context.Context, upstreamID, profileID int64) error {
	if profileID == 0 {
		result, err := s.db.ExecContext(ctx, `UPDATE upstreams SET pricing_profile_id=NULL,updated_at=now() WHERE id=$1`, upstreamID)
		if err == nil {
			if count, _ := result.RowsAffected(); count == 0 {
				return ErrNotFound
			}
			s.invalidatePricingCache()
		}
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE upstreams SET pricing_profile_id=$1,updated_at=now() WHERE id=$2`, profileID, upstreamID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	s.invalidatePricingCache()
	return nil
}

func (s *Store) DeletePricingProfile(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM pricing_profiles WHERE id=$1`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	s.invalidatePricingCache()
	return nil
}

func (s *Store) RefreshPricingProfiles(ctx context.Context) error {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, liteLLMPriceURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "D-API pricing sync")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch LiteLLM pricing: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("fetch LiteLLM pricing: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("read LiteLLM pricing: %w", err)
	}
	prices, err := decodeLiteLLMPrices(body)
	if err != nil {
		return err
	}
	versionBytes := sha256.Sum256(body)
	version := "litellm-" + hex.EncodeToString(versionBytes[:6])
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, spec := range liteLLMProfiles {
		profilePrices := prices[spec.Name]
		var profileID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM pricing_profiles WHERE name=$1 AND source_url=$2`, spec.Name, liteLLMPriceURL).Scan(&profileID); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := savePricingProfileTx(ctx, tx, PricingProfile{
			ID: profileID, Name: spec.Name, Provider: spec.Provider, SourceURL: liteLLMPriceURL,
			SourceVersion: version, Prices: profilePrices,
		}, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE pricing_profiles SET last_refreshed_at=$2,updated_at=now() WHERE id=$1`, profileID, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidatePricingCache()
	return nil
}

func decodeLiteLLMPrices(data []byte) (map[string][]PricingModelPrice, error) {
	var entries map[string]liteLLMPrice
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode LiteLLM pricing: %w", err)
	}
	result := make(map[string][]PricingModelPrice, len(liteLLMProfiles))
	for _, spec := range liteLLMProfiles {
		models := make([]string, 0)
		byModel := make(map[string]PricingModelPrice)
		for name, entry := range entries {
			if entry.Provider != spec.Provider || (entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil) {
				continue
			}
			model := strings.TrimPrefix(name, spec.Prefix)
			if model == "" || len([]rune(model)) > 200 {
				continue
			}
			values := []*float64{entry.InputCostPerToken, entry.OutputCostPerToken, entry.CacheReadInputTokenCost, entry.CacheReadInputTokenCostAlt, entry.CacheCreationInputTokenCost}
			for _, value := range values {
				if value != nil && *value < 0 {
					return nil, fmt.Errorf("invalid LiteLLM price for %s", name)
				}
			}
			cacheRead := entry.CacheReadInputTokenCost
			if cacheRead == nil {
				cacheRead = entry.CacheReadInputTokenCostAlt
			}
			price := PricingModelPrice{Model: model, InputUSDPerMillion: perMillion(entry.InputCostPerToken), OutputUSDPerMillion: perMillion(entry.OutputCostPerToken), CacheReadUSDPerMillion: perMillion(cacheRead), CacheWriteUSDPerMillion: perMillion(entry.CacheCreationInputTokenCost), Source: "LiteLLM"}
			byModel[model] = price
		}
		for model := range byModel {
			models = append(models, model)
		}
		sort.Strings(models)
		for _, model := range models {
			result[spec.Name] = append(result[spec.Name], byModel[model])
		}
		if len(result[spec.Name]) == 0 {
			return nil, fmt.Errorf("LiteLLM pricing has no models for %s", spec.Provider)
		}
	}
	return result, nil
}

func perMillion(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value * 1_000_000
}

func (s *Store) BackfillPricingCosts(ctx context.Context, from, to time.Time) (PricingBackfillResult, error) {
	if to.IsZero() {
		now := time.Now().UTC()
		to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	} else {
		to = time.Date(to.UTC().Year(), to.UTC().Month(), to.UTC().Day(), 0, 0, 0, 0, time.UTC)
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -364)
	} else {
		from = time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC)
	}
	if from.After(to) || to.Sub(from) > 364*24*time.Hour {
		return PricingBackfillResult{}, ErrInvalidPricing
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PricingBackfillResult{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE pricing_backfill(
		id BIGINT PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL, api_key_id BIGINT NOT NULL,
		group_id BIGINT, upstream_id BIGINT NOT NULL, protocol TEXT NOT NULL, model TEXT NOT NULL,
		cost_usd NUMERIC(20,8) NOT NULL
	) ON COMMIT DROP`); err != nil {
		return PricingBackfillResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		WITH candidates AS (
			SELECT l.id, l.created_at, l.api_key_id, l.group_id, l.upstream_id, l.protocol, l.model,
				(
					COALESCE(l.uncached_input_tokens, GREATEST(COALESCE(l.input_tokens,0)-COALESCE(l.cached_input_tokens,0),0)) * p.input_usd_per_million
					+ COALESCE(l.cached_input_tokens,0) * p.cache_read_usd_per_million
					+ COALESCE(l.cache_creation_input_tokens,0) * p.cache_write_usd_per_million
					+ COALESCE(l.output_tokens,0) * p.output_usd_per_million
				) / 1000000.0 AS cost_usd
			FROM request_logs l
			JOIN upstreams u ON u.id=l.upstream_id
			JOIN LATERAL (
				SELECT m.input_usd_per_million,m.output_usd_per_million,m.cache_read_usd_per_million,m.cache_write_usd_per_million
				FROM pricing_model_prices m
				WHERE m.profile_id=u.pricing_profile_id
				  AND (m.model=l.model OR m.model=COALESCE(u.model_aliases ->> l.model,l.model))
				  AND m.valid_from <= l.created_at AND (m.valid_to IS NULL OR m.valid_to > l.created_at)
				ORDER BY CASE WHEN m.model=l.model THEN 0 ELSE 1 END,m.valid_from DESC LIMIT 1
			) p ON TRUE
			WHERE l.cost_usd IS NULL
			  AND l.created_at >= $1::date AT TIME ZONE 'UTC'
			  AND l.created_at < ($2::date + interval '1 day') AT TIME ZONE 'UTC'
			  AND l.upstream_id IS NOT NULL AND l.api_key_id IS NOT NULL
			  AND (l.input_tokens IS NOT NULL OR l.output_tokens IS NOT NULL OR l.cached_input_tokens IS NOT NULL OR l.cache_creation_input_tokens IS NOT NULL OR l.uncached_input_tokens IS NOT NULL)
		), updated AS (
			UPDATE request_logs l SET cost_usd=c.cost_usd FROM candidates c WHERE l.id=c.id AND l.cost_usd IS NULL
			RETURNING l.id,l.created_at,l.api_key_id,l.group_id,l.upstream_id,l.protocol,l.model,l.cost_usd
		)
		INSERT INTO pricing_backfill SELECT id,created_at,api_key_id,group_id,upstream_id,protocol,model,cost_usd FROM updated`, from, to); err != nil {
		return PricingBackfillResult{}, err
	}
	var result PricingBackfillResult
	result.From, result.To = from, to
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pricing_backfill`).Scan(&result.LogsUpdated); err != nil {
		return PricingBackfillResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `
		WITH aggregate AS (
			SELECT (created_at AT TIME ZONE 'UTC')::date AS day,api_key_id,COALESCE(group_id,0) AS group_id,upstream_id,protocol,model,
				sum(cost_usd) AS cost_usd,count(*) AS known
			FROM pricing_backfill GROUP BY 1,2,3,4,5,6
		), updated AS (
			UPDATE daily_usage d SET cost_usd=d.cost_usd+a.cost_usd,cost_known_requests=d.cost_known_requests+a.known
			FROM aggregate a WHERE d.day=a.day AND d.api_key_id=a.api_key_id AND d.group_id=a.group_id AND d.upstream_id=a.upstream_id AND d.protocol=a.protocol AND d.model=a.model
			RETURNING d.day
		) SELECT count(*) FROM updated`).Scan(&result.DailyUpdated); err != nil {
		return PricingBackfillResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `
		WITH aggregate AS (
			SELECT date_trunc('hour',created_at) AS hour,api_key_id,COALESCE(group_id,0) AS group_id,upstream_id,protocol,model,
				sum(cost_usd) AS cost_usd,count(*) AS known
			FROM pricing_backfill GROUP BY 1,2,3,4,5,6
		), updated AS (
			UPDATE hourly_usage h SET cost_usd=h.cost_usd+a.cost_usd,cost_known_requests=h.cost_known_requests+a.known
			FROM aggregate a WHERE h.hour=a.hour AND h.api_key_id=a.api_key_id AND h.group_id=a.group_id AND h.upstream_id=a.upstream_id AND h.protocol=a.protocol AND h.model=a.model
			RETURNING h.hour
		) SELECT count(*) FROM updated`).Scan(&result.HourlyUpdated); err != nil {
		return PricingBackfillResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO upstream_lifetime_usage(upstream_id,requests,cost_known_requests,cost_usd,updated_at)
		SELECT upstream_id,0,count(*),sum(cost_usd),now() FROM pricing_backfill GROUP BY upstream_id
		ON CONFLICT(upstream_id) DO UPDATE SET
			cost_known_requests=upstream_lifetime_usage.cost_known_requests+EXCLUDED.cost_known_requests,
			cost_usd=upstream_lifetime_usage.cost_usd+EXCLUDED.cost_usd,
			updated_at=now()`); err != nil {
		return PricingBackfillResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PricingBackfillResult{}, err
	}
	s.invalidatePricingCache()
	return result, nil
}

func (s *Store) RequestCost(ctx context.Context, upstreamID int64, model string, usage core.Usage, at time.Time) (*float64, error) {
	if upstreamID <= 0 || strings.TrimSpace(model) == "" {
		return nil, nil
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CachedInputTokens == nil && usage.CacheCreationInputTokens == nil && usage.UncachedInputTokens == nil {
		return nil, nil
	}
	rate, err := s.pricingRate(ctx, upstreamID, model, at)
	if err != nil || !rate.found {
		return nil, err
	}
	total := calculateRequestCost(PricingModelPrice{
		InputUSDPerMillion: rate.input, OutputUSDPerMillion: rate.output,
		CacheReadUSDPerMillion: rate.cacheRead, CacheWriteUSDPerMillion: rate.cacheWrite,
	}, usage)
	return &total, nil
}

func (s *Store) pricingRate(ctx context.Context, upstreamID int64, model string, at time.Time) (pricingCacheEntry, error) {
	at = at.Round(time.Microsecond)
	key := pricingCacheKey{upstreamID: upstreamID, model: strings.TrimSpace(model)}
	s.pricingMu.RLock()
	cached, ok := s.pricingCache[key]
	s.pricingMu.RUnlock()
	if ok && cached.expires.After(time.Now()) && cached.covers(at) {
		return selectPricingRate(cached, at), nil
	}
	release, err := s.pricingLoads.acquire(ctx, key)
	if err != nil {
		return pricingCacheEntry{}, err
	}
	defer release()
	now := time.Now()
	s.pricingMu.RLock()
	cached, ok = s.pricingCache[key]
	generation := s.pricingGen
	s.pricingMu.RUnlock()
	if ok && cached.expires.After(now) && cached.covers(at) {
		return selectPricingRate(cached, at), nil
	}
	entry := pricingCacheEntry{}
	if cached.partial {
		entry, err = s.loadPricingWindow(ctx, key, at)
	} else {
		entry, err = s.loadPricingHistory(ctx, key, at)
	}
	if err != nil {
		return pricingCacheEntry{}, err
	}
	entry.expires = now.Add(30 * time.Second)
	s.pricingMu.Lock()
	defer s.pricingMu.Unlock()
	if generation == s.pricingGen {
		if s.pricingCache == nil {
			s.pricingCache = make(map[pricingCacheKey]pricingCacheEntry)
		}
		if len(s.pricingCache) >= 1024 {
			for key, value := range s.pricingCache {
				if !value.expires.After(now) {
					delete(s.pricingCache, key)
				}
			}
			if len(s.pricingCache) >= 1024 {
				clear(s.pricingCache)
			}
		}
		s.pricingCache[key] = entry
	}
	return selectPricingRate(entry, at), nil
}

func (s *Store) loadPricingHistory(ctx context.Context, key pricingCacheKey, at time.Time) (pricingCacheEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.input_usd_per_million,m.output_usd_per_million,m.cache_read_usd_per_million,m.cache_write_usd_per_million,m.valid_from,m.valid_to
		FROM upstreams u JOIN pricing_model_prices m ON m.profile_id=u.pricing_profile_id
		WHERE u.id=$1 AND (m.model=$2 OR m.model=COALESCE(u.model_aliases ->> $2,$2))
		ORDER BY CASE WHEN m.model=$2 THEN 0 ELSE 1 END,m.valid_from DESC LIMIT 257`, key.upstreamID, key.model)
	if err != nil {
		return pricingCacheEntry{}, err
	}
	defer rows.Close()
	entry := pricingCacheEntry{}
	count := 0
	for rows.Next() {
		var price PricingModelPrice
		if err := rows.Scan(&price.InputUSDPerMillion, &price.OutputUSDPerMillion, &price.CacheReadUSDPerMillion, &price.CacheWriteUSDPerMillion, &price.ValidFrom, &price.ValidTo); err != nil {
			return pricingCacheEntry{}, err
		}
		count++
		if count <= 256 {
			entry.prices = append(entry.prices, price)
		}
	}
	if err := rows.Err(); err != nil {
		return pricingCacheEntry{}, err
	}
	if err := rows.Close(); err != nil {
		return pricingCacheEntry{}, err
	}
	if count > 256 {
		return s.loadPricingWindow(ctx, key, at)
	}
	return entry, nil
}

// Bound the returned history while retaining the interval in which the chosen
// exact-model or alias rate cannot change. Empty intervals are cached too.
func (s *Store) loadPricingWindow(ctx context.Context, key pricingCacheKey, at time.Time) (pricingCacheEntry, error) {
	entry := pricingCacheEntry{partial: true}
	var price PricingModelPrice
	var found bool
	err := s.db.QueryRowContext(ctx, `
		WITH candidates AS MATERIALIZED (
			SELECT m.* FROM upstreams u JOIN pricing_model_prices m ON m.profile_id=u.pricing_profile_id
			WHERE u.id=$1 AND (m.model=$2 OR m.model=COALESCE(u.model_aliases ->> $2,$2))
		), boundaries AS (
			SELECT valid_from AS at FROM candidates UNION SELECT valid_to FROM candidates WHERE valid_to IS NOT NULL
		), selected AS (
			SELECT * FROM candidates WHERE valid_from <= $3 AND (valid_to IS NULL OR valid_to > $3)
			ORDER BY CASE WHEN model=$2 THEN 0 ELSE 1 END,valid_from DESC LIMIT 1
		)
		SELECT p.id IS NOT NULL,COALESCE(p.input_usd_per_million,0),COALESCE(p.output_usd_per_million,0),
			COALESCE(p.cache_read_usd_per_million,0),COALESCE(p.cache_write_usd_per_million,0),
			(SELECT max(at) FROM boundaries WHERE at <= $3),(SELECT min(at) FROM boundaries WHERE at > $3)
		FROM (SELECT 1) singleton LEFT JOIN selected p ON TRUE`, key.upstreamID, key.model, at.UTC()).Scan(
		&found, &price.InputUSDPerMillion, &price.OutputUSDPerMillion, &price.CacheReadUSDPerMillion, &price.CacheWriteUSDPerMillion, &entry.from, &entry.to)
	if err != nil {
		return pricingCacheEntry{}, err
	}
	if found {
		if entry.from != nil {
			price.ValidFrom = *entry.from
		}
		price.ValidTo = entry.to
		entry.prices = []PricingModelPrice{price}
	}
	return entry, nil
}

func selectPricingRate(entry pricingCacheEntry, at time.Time) pricingCacheEntry {
	for _, price := range entry.prices {
		if !at.Before(price.ValidFrom) && (price.ValidTo == nil || at.Before(*price.ValidTo)) {
			return pricingCacheEntry{found: true, input: price.InputUSDPerMillion, output: price.OutputUSDPerMillion, cacheRead: price.CacheReadUSDPerMillion, cacheWrite: price.CacheWriteUSDPerMillion}
		}
	}
	return pricingCacheEntry{}
}

func (s *Store) invalidatePricingCache() {
	s.pricingMu.Lock()
	s.pricingCache = make(map[pricingCacheKey]pricingCacheEntry)
	s.pricingGen++
	s.pricingMu.Unlock()
}

func calculateRequestCost(price PricingModelPrice, usage core.Usage) float64 {
	uncached := usage.UncachedInputTokens
	if uncached == nil && usage.InputTokens != nil {
		value := *usage.InputTokens
		if usage.CachedInputTokens != nil {
			value -= *usage.CachedInputTokens
		}
		if value < 0 {
			value = 0
		}
		uncached = &value
	}
	var total float64
	if uncached != nil {
		total += float64(*uncached) * price.InputUSDPerMillion
	}
	if usage.CachedInputTokens != nil {
		total += float64(*usage.CachedInputTokens) * price.CacheReadUSDPerMillion
	}
	if usage.CacheCreationInputTokens != nil {
		total += float64(*usage.CacheCreationInputTokens) * price.CacheWriteUSDPerMillion
	}
	if usage.OutputTokens != nil {
		total += float64(*usage.OutputTokens) * price.OutputUSDPerMillion
	}
	total /= 1_000_000
	return total
}
