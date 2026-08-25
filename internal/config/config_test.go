package config

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestLoadBuildsEscapedDatabaseURL(t *testing.T) {
	password := "p@ss:word/?#% value"
	t.Setenv("DAPI_DATABASE_URL", "")
	t.Setenv("DAPI_DATABASE_HOST", "db.example")
	t.Setenv("DAPI_DATABASE_PORT", "5433")
	t.Setenv("DAPI_DATABASE_NAME", "gateway")
	t.Setenv("DAPI_DATABASE_USER", "dapi")
	t.Setenv("DAPI_DATABASE_PASSWORD", password)
	t.Setenv("DAPI_DATABASE_SSLMODE", "require")
	t.Setenv("DAPI_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("DAPI_TRUST_PROXY", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != password || parsed.Host != "db.example:5433" || parsed.Query().Get("sslmode") != "require" {
		t.Fatalf("database URL was not preserved: %s", cfg.DatabaseURL)
	}
	if !cfg.TrustProxy || strings.Contains(cfg.DatabaseURL, password) {
		t.Fatalf("configuration was not normalized: %#v", cfg)
	}
}
