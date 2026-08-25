package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr          string
	WebDir        string
	DatabaseURL   string
	MasterKey     []byte
	AdminUsername string
	AdminPassword string
	SessionTTL    time.Duration
	LogRetention  time.Duration
	BalanceEvery  time.Duration
	HealthEvery   time.Duration
	TrustProxy    bool
}

func Load() (Config, error) {
	cfg := Config{
		Addr:          env("DAPI_ADDR", ":8080"),
		WebDir:        env("DAPI_WEB_DIR", "web/dist"),
		DatabaseURL:   databaseURL(),
		AdminUsername: strings.TrimSpace(os.Getenv("DAPI_ADMIN_USERNAME")),
		AdminPassword: os.Getenv("DAPI_ADMIN_PASSWORD"),
		SessionTTL:    duration("DAPI_SESSION_TTL", 24*time.Hour),
		LogRetention:  duration("DAPI_LOG_RETENTION", 30*24*time.Hour),
		BalanceEvery:  duration("DAPI_BALANCE_INTERVAL", 10*time.Minute),
		HealthEvery:   duration("DAPI_HEALTH_INTERVAL", 30*time.Second),
		TrustProxy:    boolean("DAPI_TRUST_PROXY", false),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DAPI_DATABASE_URL is required")
	}
	key, err := decodeKey(os.Getenv("DAPI_MASTER_KEY"))
	if err != nil {
		return Config{}, err
	}
	cfg.MasterKey = key
	return cfg, nil
}

func databaseURL() string {
	if value := strings.TrimSpace(os.Getenv("DAPI_DATABASE_URL")); value != "" {
		return value
	}
	password := os.Getenv("DAPI_DATABASE_PASSWORD")
	if password == "" {
		return ""
	}
	database := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(env("DAPI_DATABASE_USER", "dapi"), password),
		Host:   net.JoinHostPort(env("DAPI_DATABASE_HOST", "postgres"), env("DAPI_DATABASE_PORT", "5432")),
		Path:   "/" + env("DAPI_DATABASE_NAME", "dapi"),
	}
	query := database.Query()
	query.Set("sslmode", env("DAPI_DATABASE_SSLMODE", "disable"))
	database.RawQuery = query.Encode()
	return database.String()
}

func decodeKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("DAPI_MASTER_KEY is required (base64-encoded 32 bytes)")
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("DAPI_MASTER_KEY must be base64-encoded 32 bytes")
	}
	return key, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func duration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func Int(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func boolean(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (c Config) String() string {
	return fmt.Sprintf("addr=%s balance_interval=%s health_interval=%s trust_proxy=%t", c.Addr, c.BalanceEvery, c.HealthEvery, c.TrustProxy)
}
