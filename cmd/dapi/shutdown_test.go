package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type testLogCloser func(context.Context) error

func (f testLogCloser) Close(ctx context.Context) error { return f(ctx) }

func TestShutdownCancelsThenWaitsBeforeFlushing(t *testing.T) {
	drain := newRequestDrain()
	started := make(chan struct{})
	var logged atomic.Bool
	server := httptest.NewServer(drain.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		// Request log submission happens in handler defers after cancellation.
		time.Sleep(20 * time.Millisecond)
		logged.Store(true)
	})))
	defer server.Close()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, err := server.Client().Get(server.URL)
		if err == nil {
			response.Body.Close()
		}
	}()
	<-started
	flushed := false
	err := shutdownServer(server.Config, drain, testLogCloser(func(ctx context.Context) error {
		if !logged.Load() {
			t.Error("flushed before request finished")
		}
		if ctx.Err() != nil {
			t.Error("flush context already expired")
		}
		flushed = true
		return nil
	}), 10*time.Millisecond, time.Second, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || !flushed {
		t.Fatalf("shutdown err=%v flushed=%v", err, flushed)
	}
	<-clientDone
	w := httptest.NewRecorder()
	drain.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("accepted request after drain") })).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestShutdownPropagatesFlushFailure(t *testing.T) {
	drain := newRequestDrain()
	failure := errors.New("logs unavailable")
	server := httptest.NewServer(drain.wrap(http.NotFoundHandler()))
	defer server.Close()
	err := shutdownServer(server.Config, drain, testLogCloser(func(context.Context) error { return failure }), time.Second, time.Second, time.Second)
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
