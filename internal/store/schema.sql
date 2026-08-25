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
    api_key_encrypted BYTEA NOT NULL,
    access_token_encrypted BYTEA,
    user_id_encrypted BYTEA,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
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
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS models_locked BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS api_keys (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    key_hash BYTEA NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    protocols TEXT[] NOT NULL DEFAULT '{}',
    models TEXT[] NOT NULL DEFAULT '{}',
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS request_logs (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    upstream_id BIGINT REFERENCES upstreams(id) ON DELETE SET NULL,
    protocol TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL,
    duration_ms BIGINT NOT NULL,
    attempts JSONB NOT NULL DEFAULT '[]',
    input_tokens BIGINT,
    output_tokens BIGINT,
    cached_input_tokens BIGINT,
    error_code TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS request_logs_created_idx ON request_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS request_logs_upstream_idx ON request_logs(upstream_id, created_at DESC);
CREATE INDEX IF NOT EXISTS request_logs_key_idx ON request_logs(api_key_id, created_at DESC);

CREATE TABLE IF NOT EXISTS daily_usage (
    day DATE NOT NULL,
    api_key_id BIGINT NOT NULL,
    upstream_id BIGINT NOT NULL,
    protocol TEXT NOT NULL,
    model TEXT NOT NULL,
    requests BIGINT NOT NULL DEFAULT 0,
    successes BIGINT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cached_input_tokens BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(day, api_key_id, upstream_id, protocol, model)
);
ALTER TABLE daily_usage DROP CONSTRAINT IF EXISTS daily_usage_api_key_id_fkey;
ALTER TABLE daily_usage DROP CONSTRAINT IF EXISTS daily_usage_upstream_id_fkey;

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

CREATE TABLE IF NOT EXISTS alert_states (
    rule_id BIGINT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    observation_key TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT FALSE,
    value DOUBLE PRECISION NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    last_observed_at TIMESTAMPTZ NOT NULL,
    last_notified_at TIMESTAMPTZ,
    PRIMARY KEY(rule_id, observation_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS alert_rules_global_event_idx ON alert_rules(event) WHERE upstream_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS alert_rules_upstream_event_idx ON alert_rules(event, upstream_id) WHERE upstream_id IS NOT NULL;

INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'low_balance',5,300,1800 WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE event='low_balance' AND upstream_id IS NULL);
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'balance_unavailable',1,900,1800 WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE event='balance_unavailable' AND upstream_id IS NULL);
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'error_rate',20,300,1800 WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE event='error_rate' AND upstream_id IS NULL);
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'latency',30000,300,1800 WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE event='latency' AND upstream_id IS NULL);
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'client_error_rate',50,300,1800 WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE event='client_error_rate' AND upstream_id IS NULL);
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'login_failure',5,900,1800 WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE event='login_failure' AND upstream_id IS NULL);
INSERT INTO alert_rules(event,threshold,window_seconds,cooldown_seconds)
SELECT 'new_login_ip',1,900,86400 WHERE NOT EXISTS (SELECT 1 FROM alert_rules WHERE event='new_login_ip' AND upstream_id IS NULL);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
