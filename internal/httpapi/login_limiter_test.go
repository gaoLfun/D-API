package httpapi

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginLimiterFirstCleanupAndBound(t *testing.T) {
	limiter := loginLimiter{attempts: map[string]loginAttempt{"expired": {windowStart: time.Now().Add(-time.Hour)}}}
	limiter.wait("new")
	if len(limiter.attempts) != 0 || limiter.lastCleanup.IsZero() {
		t.Fatal("first cleanup did not run")
	}
	for i := 0; i < 5000; i++ {
		limiter.fail(fmt.Sprint(i))
	}
	if len(limiter.attempts) != 4096 || limiter.wait("overflow") <= 0 {
		t.Fatalf("entries=%d", len(limiter.attempts))
	}
	limiter.cleanupLocked(time.Now().Add(time.Hour))
	if len(limiter.attempts) != 0 {
		t.Fatal("expired capacity not reclaimed")
	}
}
