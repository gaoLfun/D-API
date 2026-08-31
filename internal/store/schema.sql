CREATE TABLE IF NOT EXISTS admins (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash BYTEA NOT NULL,
    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash BYTEA PRIMARY KEY,
    admin_id BIGINT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS upstreams (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK (kind IN ('newapi', 'sub2api')),
    base_url TEXT NOT NULL,
    user_agent TEXT NOT NULL DEFAULT '',
    api_key_encrypted BYTEA NOT NULL,
    access_token_encrypted BYTEA,
    user_id_encrypted BYTEA,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    balance_protection_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    balance_suspended BOOLEAN NOT NULL DEFAULT FALSE,
    zero_balance_checks INTEGER NOT NULL DEFAULT 0,
    priority INTEGER NOT NULL DEFAULT 100,
    protocols TEXT[] NOT NULL DEFAULT '{}',
    models TEXT[] NOT NULL DEFAULT '{}',
    models_locked BOOLEAN NOT NULL DEFAULT FALSE,
    model_aliases JSONB NOT NULL DEFAULT '{}',
    connect_timeout_ms INTEGER NOT NULL DEFAULT 5000,
    first_byte_timeout_ms INTEGER NOT NULL DEFAULT 60000,
    idle_timeout_ms INTEGER NOT NULL DEFAULT 300000,
    failure_threshold INTEGER NOT NULL DEFAULT 3,
    cooldown_seconds INTEGER NOT NULL DEFAULT 60,
    health_status TEXT NOT NULL DEFAULT 'unknown',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    circuit_open_until TIMESTAMPTZ,
    last_check_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    balance JSONB NOT NULL DEFAULT '{"status":"unknown"}',
    balance_updated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS upstreams_route_idx ON upstreams(enabled, priority, id);
CREATE INDEX IF NOT EXISTS upstreams_protocols_idx ON upstreams USING GIN(protocols);
CREATE INDEX IF NOT EXISTS upstreams_models_idx ON upstreams USING GIN(models);
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS models_locked BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS balance_protection_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS balance_suspended BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS zero_balance_checks INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS pricing_profiles (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    source_version TEXT NOT NULL DEFAULT '',
    last_refreshed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS pricing_model_prices (
    id BIGSERIAL PRIMARY KEY,
    profile_id BIGINT NOT NULL REFERENCES pricing_profiles(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    input_usd_per_million NUMERIC(20,8) NOT NULL DEFAULT 0,
    output_usd_per_million NUMERIC(20,8) NOT NULL DEFAULT 0,
    cache_read_usd_per_million NUMERIC(20,8) NOT NULL DEFAULT 0,
    cache_write_usd_per_million NUMERIC(20,8) NOT NULL DEFAULT 0,
    valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to TIMESTAMPTZ,
    source TEXT NOT NULL DEFAULT '',
    UNIQUE(profile_id, model, valid_from)
);
CREATE INDEX IF NOT EXISTS pricing_model_prices_lookup_idx ON pricing_model_prices(profile_id, model, valid_from DESC);

ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS pricing_profile_id BIGINT REFERENCES pricing_profiles(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS groups (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS group_upstreams (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    upstream_id BIGINT NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    PRIMARY KEY(group_id, upstream_id)
);
CREATE INDEX IF NOT EXISTS group_upstreams_upstream_idx ON group_upstreams(upstream_id);

CREATE TABLE IF NOT EXISTS api_keys (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    key_hash BYTEA NOT NULL UNIQUE,
    key_encrypted BYTEA,
    group_id BIGINT REFERENCES groups(id) ON DELETE RESTRICT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    protocols TEXT[] NOT NULL DEFAULT '{}',
    models TEXT[] NOT NULL DEFAULT '{}',
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_encrypted BYTEA;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS group_id BIGINT REFERENCES groups(id) ON DELETE RESTRICT;

CREATE TABLE IF NOT EXISTS request_logs (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    group_id BIGINT,
    upstream_id BIGINT REFERENCES upstreams(id) ON DELETE SET NULL,
    protocol TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL,
    duration_ms BIGINT NOT NULL,
    ttfb_ms BIGINT,
    ttft_ms BIGINT,
    attempts JSONB NOT NULL DEFAULT '[]',
    input_tokens BIGINT,
    output_tokens BIGINT,
    cached_input_tokens BIGINT,
    cache_creation_input_tokens BIGINT,
    uncached_input_tokens BIGINT,
    cost_usd NUMERIC(20,8),
    error_code TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS ttfb_ms BIGINT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS ttft_ms BIGINT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS cache_creation_input_tokens BIGINT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS uncached_input_tokens BIGINT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS cost_usd NUMERIC(20,8);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS group_id BIGINT;
CREATE INDEX IF NOT EXISTS request_logs_created_idx ON request_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS request_logs_upstream_idx ON request_logs(upstream_id, created_at DESC);
CREATE INDEX IF NOT EXISTS request_logs_key_idx ON request_logs(api_key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS request_logs_group_idx ON request_logs(group_id, created_at DESC);

CREATE TABLE IF NOT EXISTS daily_usage (
    day DATE NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL DEFAULT 0,
    upstream_id BIGINT NOT NULL,
    protocol TEXT NOT NULL,
    model TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    successes BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cached_input_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
    cost_known_requests BIGINT NOT NULL DEFAULT 0,
    cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_usage_requests BIGINT NOT NULL DEFAULT 0,
    uncached_input_tokens BIGINT NOT NULL DEFAULT 0,
    usage_requests BIGINT NOT NULL DEFAULT 0,
    cache_hit_requests BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(day, api_key_id, group_id, upstream_id, protocol, model)
);
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS group_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daily_usage DROP CONSTRAINT IF EXISTS daily_usage_pkey;
ALTER TABLE daily_usage ADD PRIMARY KEY(day, api_key_id, group_id, upstream_id, protocol, model);
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS cache_creation_usage_requests BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS uncached_input_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS usage_requests BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS cache_hit_requests BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0;
ALTER TABLE daily_usage ADD COLUMN IF NOT EXISTS cost_known_requests BIGINT NOT NULL DEFAULT 0;
ALTER TABLE daily_usage DROP CONSTRAINT IF EXISTS daily_usage_api_key_id_fkey;
ALTER TABLE daily_usage DROP CONSTRAINT IF EXISTS daily_usage_upstream_id_fkey;

CREATE TABLE IF NOT EXISTS upstream_lifetime_usage (
    upstream_id BIGINT PRIMARY KEY,
    requests BIGINT NOT NULL DEFAULT 0,
    cost_known_requests BIGINT NOT NULL DEFAULT 0,
    cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS hourly_usage (
    hour TIMESTAMPTZ NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL DEFAULT 0,
    upstream_id BIGINT NOT NULL,
    protocol TEXT NOT NULL,
    model TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    successes BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cached_input_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_usage_requests BIGINT NOT NULL DEFAULT 0,
    uncached_input_tokens BIGINT NOT NULL DEFAULT 0,
    usage_requests BIGINT NOT NULL DEFAULT 0,
    cache_hit_requests BIGINT NOT NULL DEFAULT 0,
    cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
    cost_known_requests BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(hour, api_key_id, group_id, upstream_id, protocol, model)
);
CREATE INDEX IF NOT EXISTS hourly_usage_hour_idx ON hourly_usage(hour);

CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGSERIAL PRIMARY KEY,
    admin_id BIGINT REFERENCES admins(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    detail JSONB NOT NULL DEFAULT '{}',
    ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_logs_created_idx ON audit_logs(created_at DESC);

CREATE TABLE IF NOT EXISTS notification_channels (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK (kind IN ('email', 'webhook')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    config_encrypted BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS alert_rules (
    id BIGSERIAL PRIMARY KEY,
    event TEXT NOT NULL,
    upstream_id BIGINT REFERENCES upstreams(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    threshold DOUBLE PRECISION,
    window_seconds INTEGER NOT NULL DEFAULT 300,
    cooldown_seconds INTEGER NOT NULL DEFAULT 1800,
    max_notifications INTEGER NOT NULL DEFAULT 3,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(event, upstream_id)
);

CREATE TABLE IF NOT EXISTS alert_events (
    id BIGSERIAL PRIMARY KEY,
    rule_id BIGINT REFERENCES alert_rules(id) ON DELETE SET NULL,
    upstream_id BIGINT REFERENCES upstreams(id) ON DELETE SET NULL,
    event TEXT NOT NULL,
    state TEXT NOT NULL,
    message TEXT NOT NULL,
    notified_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS alert_events_created_idx ON alert_events(created_at DESC);

CREATE TABLE IF NOT EXISTS notification_outbox (
    id BIGSERIAL PRIMARY KEY,
    channel_id BIGINT REFERENCES notification_channels(id) ON DELETE CASCADE,
    payload JSONB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL DEFAULT '',
    dead_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE notification_outbox ADD COLUMN IF NOT EXISTS channel_id BIGINT REFERENCES notification_channels(id) ON DELETE CASCADE;
ALTER TABLE notification_outbox ADD COLUMN IF NOT EXISTS dead_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS notification_outbox_pending_idx ON notification_outbox(next_attempt_at, id);

CREATE TABLE IF NOT EXISTS alert_states (
    rule_id BIGINT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    observation_key TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT FALSE,
    value DOUBLE PRECISION NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    last_observed_at TIMESTAMPTZ NOT NULL,
    last_notified_at TIMESTAMPTZ,
    notification_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(rule_id, observation_key)
);

ALTER TABLE alert_rules ADD COLUMN IF NOT EXISTS max_notifications INTEGER NOT NULL DEFAULT 3;
ALTER TABLE alert_states ADD COLUMN IF NOT EXISTS notification_count INTEGER NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX IF NOT EXISTS alert_rules_global_event_idx ON alert_rules(event) WHERE upstream_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS alert_rules_upstream_event_idx ON alert_rules(event, upstream_id) WHERE upstream_id IS NOT NULL;

INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'low_balance',5,300,1800 ON CONFLICT DO NOTHING;
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'balance_unavailable',1,900,1800 ON CONFLICT DO NOTHING;
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'error_rate',20,300,1800 ON CONFLICT DO NOTHING;
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'latency',30000,300,1800 ON CONFLICT DO NOTHING;
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'client_error_rate',50,300,1800 ON CONFLICT DO NOTHING;
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'login_failure',5,900,1800 ON CONFLICT DO NOTHING;
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'new_login_ip',1,900,86400 ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO settings(key,value) VALUES('usd_cny_rate','7.2'::jsonb) ON CONFLICT(key) DO NOTHING;
INSERT INTO upstream_lifetime_usage(upstream_id, requests, cost_known_requests, cost_usd, updated_at)
SELECT upstream_id, sum(requests), sum(cost_known_requests), sum(cost_usd), now()
FROM daily_usage
WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key='upstream_lifetime_usage_migrated_v1')
GROUP BY upstream_id
ON CONFLICT(upstream_id) DO NOTHING;
INSERT INTO settings(key,value) VALUES('upstream_lifetime_usage_migrated_v1','true') ON CONFLICT(key) DO NOTHING;

INSERT INTO pricing_profiles(name,provider,source_url,source_version)
VALUES
    ('OpenAI','openai','https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json','litellm'),
    ('Anthropic','anthropic','https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json','litellm'),
    ('Google Gemini','gemini','https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json','litellm')
ON CONFLICT(name) DO NOTHING;
UPDATE pricing_profiles
SET provider=CASE name WHEN 'OpenAI' THEN 'openai' WHEN 'Anthropic' THEN 'anthropic' WHEN 'Google Gemini' THEN 'gemini' ELSE provider END,
    source_url='https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json',
    source_version='litellm', updated_at=now()
WHERE name IN ('OpenAI','Anthropic','Google Gemini')
  AND source_url IN ('https://openai.com/api/pricing/','https://www.anthropic.com/pricing#api','https://ai.google.dev/gemini-api/docs/pricing');
INSERT INTO pricing_model_prices(profile_id,model,input_usd_per_million,output_usd_per_million,cache_read_usd_per_million,cache_write_usd_per_million,source)
SELECT p.id,v.model,v.input,v.output,v.cache_read,v.cache_write,'built-in snapshot'
FROM pricing_profiles p
JOIN (VALUES
    ('OpenAI','gpt-4o',2.5,10,1.25,0),
    ('OpenAI','gpt-4o-mini',0.15,0.6,0.075,0),
    ('Anthropic','claude-3-5-sonnet-20241022',3,15,0.3,3.75),
    ('Anthropic','claude-3-7-sonnet-20250219',3,15,0.3,3.75),
    ('Google Gemini','gemini-2.0-flash',0.1,0.4,0.025,0),
    ('Google Gemini','gemini-2.5-pro',1.25,10,0.3125,0)
) AS v(provider,model,input,output,cache_read,cache_write) ON v.provider=p.name
WHERE NOT EXISTS (SELECT 1 FROM pricing_model_prices m WHERE m.profile_id=p.id AND m.model=v.model);

-- The first migration keeps existing clients on the current global route pool.
INSERT INTO groups(name, enabled) VALUES('默认分组', TRUE) ON CONFLICT(name) DO NOTHING;
INSERT INTO group_upstreams(group_id, upstream_id)
SELECT g.id, u.id FROM groups g CROSS JOIN upstreams u
WHERE g.name='默认分组'
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key='groups_migrated_v1')
ON CONFLICT DO NOTHING;
UPDATE groups SET enabled=false,updated_at=now()
WHERE name='默认分组' AND NOT EXISTS (SELECT 1 FROM group_upstreams gu JOIN groups g ON g.id=gu.group_id WHERE g.name='默认分组');
UPDATE api_keys SET group_id=(SELECT id FROM groups WHERE name='默认分组')
WHERE group_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key='groups_migrated_v1');
INSERT INTO settings(key,value) VALUES('groups_migrated_v1','true') ON CONFLICT(key) DO NOTHING;
