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
		_, err := s.db.ExecContext(ctx, `UPDATE upstreams SET pricing_profile_id=NULL,updated_at=now() WHERE id=$1`, upstreamID)
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
	return tx.Commit()
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

func (s *Store) RequestCost(ctx context.Context, upstreamID int64, model string, usage core.Usage, at time.Time) (*float64, error) {
	if upstreamID <= 0 || strings.TrimSpace(model) == "" {
		return nil, nil
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil && usage.CachedInputTokens == nil && usage.CacheCreationInputTokens == nil && usage.UncachedInputTokens == nil {
		return nil, nil
	}
	var input, output, cacheRead, cacheWrite float64
	err := s.db.QueryRowContext(ctx, `
		SELECT m.input_usd_per_million,m.output_usd_per_million,m.cache_read_usd_per_million,m.cache_write_usd_per_million
		FROM upstreams u JOIN pricing_model_prices m ON m.profile_id=u.pricing_profile_id
		WHERE u.id=$1 AND (m.model=$2 OR m.model=COALESCE(u.model_aliases ->> $2,$2))
			AND m.valid_from <= $3 AND (m.valid_to IS NULL OR m.valid_to > $3)
		ORDER BY CASE WHEN m.model=$2 THEN 0 ELSE 1 END,m.valid_from DESC LIMIT 1`,
		upstreamID, model, at.UTC()).Scan(&input, &output, &cacheRead, &cacheWrite)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	total := calculateRequestCost(PricingModelPrice{
		InputUSDPerMillion: input, OutputUSDPerMillion: output,
		CacheReadUSDPerMillion: cacheRead, CacheWriteUSDPerMillion: cacheWrite,
	}, usage)
	return &total, nil
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
