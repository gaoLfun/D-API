package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticServesSPAWithoutEscapingRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("D-API"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := Static(dir)
	for _, path := range []string{"/", "/upstreams"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.String() != "D-API" {
			t.Fatalf("path %s: code=%d body=%q", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing.js", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing asset code=%d", response.Code)
	}
}
