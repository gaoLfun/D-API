package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/gaoLfun/dapi/internal/cryptox"
	"github.com/gaoLfun/dapi/internal/store"
	"github.com/lib/pq"
)

type recordingRepository struct {
	*fakeRepository
	write func(context.Context, []core.RequestLog) error
}

func (r *recordingRepository) RecordRequests(ctx context.Context, entries []core.RequestLog) error {
	return r.write(ctx, entries)
}

func TestRecorderIsolatesInvalidRecord(t *testing.T) {
	for _, code := range []pq.ErrorCode{"22021", "23514"} {
		t.Run(string(code), func(t *testing.T) {
			base := &fakeRepository{}
			repo := &recordingRepository{fakeRepository: base, write: func(ctx context.Context, entries []core.RequestLog) error {
				for _, entry := range entries {
					if entry.RequestID == "bad" {
						return fmt.Errorf("write: %w", &pq.Error{Code: code})
					}
				}
				return base.RecordRequests(ctx, entries)
			}}
			checkIsolatedRecords(t, repo)
			if len(base.logs) != 2 || base.logs[0].RequestID != "before" || base.logs[1].RequestID != "after" {
				t.Fatalf("saved logs: %+v", base.logs)
			}
		})
	}
}

func checkIsolatedRecords(t *testing.T, repo Repository) {
	t.Helper()
	r := newRequestRecorder(repo)
	for _, entry := range []core.RequestLog{
		{RequestID: "before", Model: "valid", StatusCode: 200},
		{RequestID: "bad", Model: "x\x00", StatusCode: 400},
		{RequestID: "after", Model: "valid", StatusCode: 200},
	} {
		if !r.Submit(entry) {
			t.Fatal("queue rejected record")
		}
	}
	if err := r.Close(context.Background()); err == nil {
		t.Fatal("Close did not report the invalid record")
	}
	if r.Dropped() != 1 {
		t.Fatalf("dropped %d records, want 1", r.Dropped())
	}
}

func TestRecorderIsolatesPostgresInvalidRecord(t *testing.T) {
	dsn := os.Getenv("DAPI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DAPI_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("recorder_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	box, err := cryptox.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), u.String(), box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	checkIsolatedRecords(t, &recordingRepository{fakeRepository: &fakeRepository{}, write: db.RecordRequests})
	logs, err := db.ListRequestLogs(context.Background(), store.LogFilter{})
	if err != nil || len(logs) != 2 {
		t.Fatalf("saved logs=%v err=%v", logs, err)
	}
	for _, entry := range logs {
		if entry.RequestID != "before" && entry.RequestID != "after" {
			t.Fatalf("unexpected record: %s", entry.RequestID)
		}
	}
}

func TestFullQueueFallbackReportsConcurrentFailures(t *testing.T) {
	writeErr := errors.New("database unavailable")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	repo := &recordingRepository{fakeRepository: &fakeRepository{}, write: func(ctx context.Context, entries []core.RequestLog) error {
		if len(entries) == 1 && entries[0].RequestID == "fallback" {
			return writeErr
		}
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	r := newRequestRecorderWithRetryDelays(repo, nil)
	defer r.Close(context.Background())
	defer close(release)
	for range requestLogBatchSize {
		r.Submit(core.RequestLog{})
	}
	<-started
	for range requestLogQueueSize {
		if !r.Submit(core.RequestLog{}) {
			t.Fatal("queue filled prematurely")
		}
	}
	h := &Handler{repo: repo, recorder: r}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() { h.record(core.RequestLog{RequestID: "fallback"}) })
	}
	workers.Wait()
	if h.DroppedRequestLogs() != 8 || !errors.Is(r.flushError(), writeErr) {
		t.Fatalf("dropped=%d err=%v", h.DroppedRequestLogs(), r.flushError())
	}
	// The deferred close releases the worker before its deferred Close call.
	t.Cleanup(func() {
		if err := h.Close(context.Background()); !errors.Is(err, writeErr) {
			t.Fatalf("Close error=%v", err)
		}
	})
}

func TestProxyRejectsNullModelWithoutPoisoningLog(t *testing.T) {
	repo := &fakeRepository{key: core.APIKey{ID: 1, Enabled: true}}
	h := NewHandler(repo)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, requestWithAuth(http.MethodPost, "/v1/responses", `{"model":"x\u0000"}`))
	if w.Code != http.StatusBadRequest || len(repo.logs) != 1 || repo.logs[0].Model != "" || repo.logs[0].ErrorCode != "invalid_request" {
		t.Fatalf("status=%d logs=%+v", w.Code, repo.logs)
	}
}
